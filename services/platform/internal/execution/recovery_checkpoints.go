package execution

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
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
	UpgradeStatus   jsontext.Value    `json:"upgradeStatus,omitempty"`
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
	descriptor, _ := stablejson.Marshal(d)
	return d, db.New(tx).InsertRecoveryBaseCheckpoint(ctx, db.InsertRecoveryBaseCheckpointParams{ID: id, WorkspaceID: ws, ParentID: root.Id, Descriptor: descriptor, Envelope: envelope})
}

func reserveRecoveryGeneration(ctx context.Context, tx pgx.Tx, ws uuid.UUID, parent api.ExecutionCommand, phase string) (int64, error) {
	if parent.RecoveryCheckpoint == nil || phase == "checkpoint" || phase == "maintenance-checkpoint" {
		return 0, nil
	}
	var d deployment.RecoveryDescriptor
	if json.Unmarshal(*parent.RecoveryCheckpoint, &d) != nil || d.Validate() != nil {
		return 0, domain.Invalid("recovery.checkpoint")
	}
	q := db.New(tx)
	saved, err := q.RecoveryCheckpointPhase(ctx, db.RecoveryCheckpointPhaseParams{WorkspaceID: ws, ID: d.CheckpointID, ParentID: parent.Id})
	if err != nil {
		return 0, err
	}
	if saved == phase {
		return 0, nil
	}
	return q.AdvanceEnvironmentExecutionGeneration(ctx, db.AdvanceEnvironmentExecutionGenerationParams{WorkspaceID: ws, ID: parent.EnvironmentId})
}

func (s *Service) stageRecoveryPhase(ctx context.Context, tx pgx.Tx, ws uuid.UUID, parent api.ExecutionCommand, command coordinationCommand, phase string, ids []uuid.UUID, generation int64) error {
	if parent.RecoveryCheckpoint == nil || phase == "checkpoint" || phase == "maintenance-checkpoint" || command.Action == "resolve" || command.Action == "observe" {
		return nil
	}
	var previous deployment.RecoveryDescriptor
	if json.Unmarshal(*parent.RecoveryCheckpoint, &previous) != nil || previous.Validate() != nil {
		return domain.Invalid("recovery.checkpoint")
	}
	q := db.New(tx)
	existingPhase, err := q.RecoveryCheckpointPhase(ctx, db.RecoveryCheckpointPhaseParams{WorkspaceID: ws, ID: previous.CheckpointID, ParentID: parent.Id})
	if err != nil {
		return err
	}
	if existingPhase == phase {
		return nil
	}
	segment := recoveryContinuation{Phase: phase, Children: []recoveryChild{}, Approvals: []portability.Row{}}
	segment.CancelRequested, err = q.OperationCancelRequested(ctx, db.OperationCancelRequestedParams{WorkspaceID: ws, ID: parent.Id})
	if err != nil {
		return err
	}
	for _, id := range ids {
		var c api.ExecutionCommand
		raw, err := q.RecoveryChildCommand(ctx, db.RecoveryChildCommandParams{WorkspaceID: ws, ID: id, ParentID: parent.Id})
		if err != nil {
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
	approvals, err := q.RecoveryPhaseApprovals(ctx, db.RecoveryPhaseApprovalsParams{WorkspaceID: ws, ParentID: parent.Id, Phase: phase})
	if err != nil {
		return err
	}
	for _, raw := range approvals {
		var row portability.Row
		if err = json.Unmarshal(raw, &row); err != nil {
			return err
		}
		segment.Approvals = append(segment.Approvals, row)
	}
	if command.Maintenance != nil && command.Maintenance.Effect.Upgrade != nil {
		segment.UpgradeStatus, err = q.SupportedUpgradeStatus(ctx, db.SupportedUpgradeStatusParams{WorkspaceID: ws, ID: command.Maintenance.Effect.Upgrade.Plan.ID})
		if err != nil {
			return err
		}
	}
	raw, err := stablejson.Marshal(segment)
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
	descriptor, _ := stablejson.Marshal(d)
	if err = q.InsertRecoveryCheckpoint(ctx, db.InsertRecoveryCheckpointParams{ID: d.CheckpointID, WorkspaceID: ws, ParentID: parent.Id, Sequence: d.Sequence, Descriptor: descriptor, Envelope: envelope, Phase: phase}); err != nil {
		return err
	}
	if err = q.AttachRecoveryCheckpoint(ctx, db.AttachRecoveryCheckpointParams{Descriptor: descriptor, WorkspaceID: ws, ParentID: parent.Id, ChildIds: ids}); err != nil {
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
	payload, _ := stablejson.Marshal(body)
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
	body, err := s.Store.Queries.AgentRecoveryCheckpointEnvelope(ctx, db.AgentRecoveryCheckpointEnvelopeParams{WorkspaceID: a.Workspace, ParentID: parent, ID: checkpoint, ServerID: a.ID, CredentialHash: hash(a.Token)})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.NotFound
	}
	return body, err
}
