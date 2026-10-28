package execution

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var errRecoverySize = errors.New("recovery checkpoint exceeds its envelope limit")

// Payloads are derived from the frozen root, while the compact original headers
// preserve IDs, deadlines and generation counters that cannot be regenerated.
type recoveryChild struct {
	Header api.ExecutionCommand `json:"header"`
	UnitID uuid.UUID            `json:"unitId"`
}

type recoveryContinuation struct {
	Phase           string            `json:"phase"`
	CancelRequested bool              `json:"cancelRequested"`
	Children        []recoveryChild   `json:"children"`
	Approvals       []portability.Row `json:"approvals"`
	UpgradeStatus   json.RawMessage   `json:"upgradeStatus,omitempty"`
}

func recoveryIdentity(d deployment.RecoveryDescriptor) string {
	return fmt.Sprintf("cross-host-recovery:v1:%s:%s:%s:%d:%s", d.WorkspaceID, d.ParentID, d.CheckpointID, d.Sequence, d.RootHash)
}

func (s *Service) createRecoveryBase(ctx context.Context, tx pgx.Tx, ws uuid.UUID, root api.ExecutionCommand, p deployment.Plan, units []deployment.DistributedUnit) (deployment.RecoveryDescriptor, error) {
	id := uuid.New()
	d := deployment.RecoveryDescriptor{Version: 1, WorkspaceID: ws, ParentID: root.Id, RootHash: root.PlanHash, GroupID: p.ID, GroupHash: p.Hash, BaseID: id, CheckpointID: id, Participants: []uuid.UUID{}}
	for _, unit := range units {
		d.Participants = append(d.Participants, unit.Plan.ServerID)
	}
	raw, err := portability.CaptureRecovery(ctx, tx, ws)
	if err != nil {
		return d, err
	}
	defer clear(raw)
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(raw); err != nil {
		return d, err
	}
	if err := writer.Close(); err != nil {
		return d, err
	}
	envelope, err := s.Store.Keys.Seal(compressed.Bytes(), recoveryIdentity(d))
	if err != nil {
		return d, err
	}
	if len(envelope) > deployment.RecoveryEnvelopeLimit {
		return d, errRecoverySize
	}
	// Seal accepts more data than Open. Admission checks the final serialized
	// envelope, including base64 overhead, against the actual recovery reader.
	verified, err := s.Store.Keys.Open(envelope, recoveryIdentity(d))
	if err != nil {
		return d, err
	}
	defer clear(verified)
	if !bytes.Equal(verified, compressed.Bytes()) {
		return d, errors.New("recovery envelope round trip differs")
	}
	d.Digest, d.Bytes = deployment.RecoveryDigest(envelope), len(envelope)
	if err := d.Validate(); err != nil {
		return d, err
	}
	descriptor, _ := json.Marshal(d)
	_, err = tx.Exec(ctx, `INSERT INTO cross_host_checkpoints(id,workspace_id,parent_id,sequence,descriptor,envelope) VALUES($1,$2,$3,0,$4,$5)`, id, ws, root.Id, descriptor, envelope)
	return d, err
}

func reserveRecoveryGeneration(ctx context.Context, tx pgx.Tx, ws uuid.UUID, parent api.ExecutionCommand, phase string) (int64, error) {
	if parent.RecoveryCheckpoint == nil || phase == "checkpoint" || phase == "maintenance-checkpoint" {
		return 0, nil
	}
	var d deployment.RecoveryDescriptor
	if json.Unmarshal(*parent.RecoveryCheckpoint, &d) != nil || d.Validate() != nil {
		return 0, domain.Invalid("recovery.checkpoint")
	}
	var saved string
	if err := tx.QueryRow(ctx, `SELECT phase FROM cross_host_checkpoints WHERE id=$1 AND parent_id=$2`, d.CheckpointID, parent.Id).Scan(&saved); err != nil {
		return 0, err
	}
	if saved == phase {
		return 0, nil
	}
	var generation int64
	err := tx.QueryRow(ctx, `UPDATE environments SET execution_generation=execution_generation+1 WHERE workspace_id=$1 AND id=$2 RETURNING execution_generation`, ws, parent.EnvironmentId).Scan(&generation)
	return generation, err
}

func (s *Service) stageRecoveryPhase(ctx context.Context, tx pgx.Tx, ws uuid.UUID, parent api.ExecutionCommand, command coordinationCommand, phase string, ids []uuid.UUID, generation int64) error {
	if parent.RecoveryCheckpoint == nil || phase == "checkpoint" || phase == "maintenance-checkpoint" || command.Action == "resolve" || command.Action == "observe" {
		return nil
	}
	var previous deployment.RecoveryDescriptor
	if json.Unmarshal(*parent.RecoveryCheckpoint, &previous) != nil || previous.Validate() != nil {
		return domain.Invalid("recovery.checkpoint")
	}
	var existingPhase string
	if err := tx.QueryRow(ctx, `SELECT phase FROM cross_host_checkpoints WHERE id=$1 AND parent_id=$2`, previous.CheckpointID, parent.Id).Scan(&existingPhase); err != nil {
		return err
	}
	if existingPhase == phase {
		return nil
	}
	segment := recoveryContinuation{Phase: phase, Children: []recoveryChild{}, Approvals: []portability.Row{}}
	if err := tx.QueryRow(ctx, `SELECT cancel_requested FROM execution_operations WHERE workspace_id=$1 AND id=$2`, ws, parent.Id).Scan(&segment.CancelRequested); err != nil {
		return err
	}
	for _, id := range ids {
		var raw []byte
		var c api.ExecutionCommand
		if err := tx.QueryRow(ctx, `SELECT command FROM execution_operations WHERE id=$1 AND parent_id=$2`, id, parent.Id).Scan(&raw); err != nil {
			return err
		}
		if json.Unmarshal(raw, &c) != nil {
			return domain.Invalid("recovery.child")
		}
		unit := c.ResourceId
		if c.Crosshost != nil {
			var body deployment.CrossHostCommand
			if json.Unmarshal(*c.Crosshost, &body) != nil {
				return domain.Invalid("recovery.child")
			}
			unit = body.Unit.Plan.UnitID
		}
		// Network restrictions are not derivable from the base; retain their
		// frozen payload. All other bodies are reconstructed from root+phase.
		c.Crosshost, c.Backup, c.Upgrade, c.RecoveryCheckpoint = nil, nil, nil, nil
		segment.Children = append(segment.Children, recoveryChild{Header: c, UnitID: unit})
	}
	rows, err := tx.Query(ctx, `SELECT to_jsonb(a) FROM cross_host_approvals a WHERE workspace_id=$1 AND parent_id=$2 AND $3 LIKE '%:'||request_key::text`, ws, parent.Id, phase)
	if err != nil {
		return err
	}
	for rows.Next() {
		var raw []byte
		var row portability.Row
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			return err
		}
		if err = json.Unmarshal(raw, &row); err != nil {
			rows.Close()
			return err
		}
		segment.Approvals = append(segment.Approvals, row)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if command.Maintenance != nil && command.Maintenance.Effect.Upgrade != nil {
		if err = tx.QueryRow(ctx, `SELECT status FROM supported_upgrades WHERE workspace_id=$1 AND id=$2`, ws, command.Maintenance.Effect.Upgrade.Plan.ID).Scan(&segment.UpgradeStatus); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(segment)
	if err != nil {
		return err
	}
	d := previous
	d.Sequence++
	d.CheckpointID, d.PreviousHash = uuid.New(), previous.Digest
	envelope, err := s.Store.Keys.Seal(raw, recoveryIdentity(d))
	clear(raw)
	if err != nil {
		return err
	}
	if len(envelope) > deployment.RecoveryEnvelopeLimit {
		return errRecoverySize
	}
	opened, err := s.Store.Keys.Open(envelope, recoveryIdentity(d))
	clear(opened)
	if err != nil {
		return err
	}
	d.Digest, d.Bytes = deployment.RecoveryDigest(envelope), len(envelope)
	descriptor, _ := json.Marshal(d)
	if _, err = tx.Exec(ctx, `INSERT INTO cross_host_checkpoints(id,workspace_id,parent_id,sequence,descriptor,envelope,phase) VALUES($1,$2,$3,$4,$5,$6,$7)`, d.CheckpointID, ws, parent.Id, d.Sequence, descriptor, envelope, phase); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE execution_operations SET command=jsonb_set(command,'{recoveryCheckpoint}',$2::jsonb,true) WHERE id=$1 OR id=ANY($3::uuid[])`, parent.Id, descriptor, ids); err != nil {
		return err
	}
	var primary deployment.DistributedUnit
	var intent *deployment.Maintenance
	if command.Maintenance != nil {
		for _, u := range command.Maintenance.Units {
			if u.Unit.Plan.Local.Primary {
				primary, intent = u.Unit, &u.Intent
			}
		}
	} else {
		for _, u := range command.Plan.Distributed.Units {
			if u.Plan.Local.Primary {
				primary = u
			}
		}
	}
	body := deployment.CrossHostCommand{ParentID: parent.Id, GroupHash: command.Plan.Hash, Phase: "checkpoint", Unit: primary, Maintenance: intent}
	if intent != nil {
		body.GroupHash = parent.PlanHash
	}
	payload, _ := json.Marshal(body)
	if generation < 1 {
		return domain.Invalid("recovery.generation")
	}
	c := api.ExecutionCommand{Id: uuid.NewSHA1(d.CheckpointID, []byte("recovery-delivery")), ParentId: &parent.Id, ServerId: primary.Plan.ServerID, EnvironmentId: parent.EnvironmentId, ResourceId: primary.Plan.UnitID, Kind: "crosshost", Action: "inspect", Generation: generation, Deadline: time.Now().Add(30 * time.Minute), PlanHash: body.Digest(), Crosshost: rawMessage(payload), RecoveryCheckpoint: rawMessage(descriptor)}
	_, err = s.insert(ctx, tx, ws, uuid.New(), nil, c)
	return err
}

// RecoveryCheckpoint returns opaque metadata only to the root's assigned
// primary. Authentication and controller-transfer fencing remain mandatory.
func (s *Service) RecoveryCheckpoint(ctx context.Context, a Identity, parent, checkpoint uuid.UUID) ([]byte, error) {
	var body []byte
	err := s.Store.Pool.QueryRow(ctx, `SELECT c.envelope FROM cross_host_checkpoints c JOIN execution_operations o ON o.workspace_id=c.workspace_id AND o.id=c.parent_id JOIN servers s ON s.id=o.server_id AND s.workspace_id=o.workspace_id WHERE c.workspace_id=$1 AND c.parent_id=$2 AND c.id=$3 AND o.parent_id IS NULL AND o.server_id=$4 AND NOT s.revoked AND NOT s.awaiting_transfer AND (s.credential_hash=$5 OR (s.previous_hash=$5 AND s.previous_until>now()))`, a.Workspace, parent, checkpoint, a.ID, hash(a.Token)).Scan(&body)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.NotFound
	}
	return body, err
}
