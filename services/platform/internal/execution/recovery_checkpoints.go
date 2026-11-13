package execution

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"log/slog"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
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

// sealCheckpoint seals a recovery checkpoint's envelope with the coordinator's keys, which a
// pass asks for only when it reaches a checkpoint.
func sealCheckpoint(keys *passKeys, d deployment.RecoveryDescriptor, raw []byte) ([]byte, error) {
	session, err := keys.get()
	if err != nil {
		return nil, err
	}
	sealed, err := session.Seal(secretbroker.Checkpoint(d), raw)
	if err == nil {
		keys.use()
	}
	return sealed, err
}

// openCheckpoint reads an envelope back, to prove it opens, with the keys that sealed it.
func openCheckpoint(keys *passKeys, d deployment.RecoveryDescriptor, envelope []byte) ([]byte, error) {
	session, err := keys.get()
	if err != nil {
		return nil, err
	}
	return session.Open(secretbroker.Checkpoint(d), envelope)
}

func (s *Service) createRecoveryBase(ctx context.Context, tx pgx.Tx, keys *passKeys, ws uuid.UUID, root api.ExecutionCommand, p deployment.Plan, units []deployment.DistributedUnit) (deployment.RecoveryDescriptor, error) {
	// Asked for before the capture, so a pass without keys rolls back after a few reads rather
	// than after capturing and compressing the whole workspace.
	if _, err := keys.get(); err != nil {
		return deployment.RecoveryDescriptor{}, err
	}
	id := uuid.New()
	d := deployment.RecoveryDescriptor{Version: 1, WorkspaceID: ws, ParentID: root.Id, RootHash: root.PlanHash, GroupID: p.ID, GroupHash: p.Hash, BaseID: id, CheckpointID: id, Participants: []uuid.UUID{}}
	for _, unit := range units {
		d.Participants = append(d.Participants, unit.Plan.ServerID)
	}
	capture, envelope, compressed, err := sealRecoveryBase(ctx, tx, keys, ws, d)
	if err != nil {
		return d, err
	}
	defer clear(compressed)
	if short := capture.CapturedAt.Sub(capture.WindowStart); short < portability.RecoveryShortWindow {
		slog.WarnContext(ctx, "recovery checkpoint window is shorter than the suggested backup retention", "workspace", ws, "parent", root.Id, "days", int(short.Hours()/24))
	}
	// Admission checks the final serialized envelope, including base64 overhead,
	// against the actual recovery reader.
	verified, err := openCheckpoint(keys, d, envelope)
	if err != nil {
		return d, err
	}
	defer clear(verified)
	if !bytes.Equal(verified, compressed) {
		return d, errors.New("recovery envelope round trip differs")
	}
	d.Digest, d.Bytes = deployment.RecoveryDigest(envelope), len(envelope)
	if err := d.Validate(); err != nil {
		return d, err
	}
	descriptor, _ := stablejson.Marshal(d)
	q := db.New(tx)
	if err := q.InsertRecoveryBaseCheckpoint(ctx, db.InsertRecoveryBaseCheckpointParams{ID: id, WorkspaceID: ws, ParentID: root.Id, Descriptor: descriptor, Envelope: envelope}); err != nil {
		return d, err
	}
	// The root's primary holds the base until it closes the root (recovery_bases).
	return d, q.RegisterRecoveryBase(ctx, db.RegisterRecoveryBaseParams{WorkspaceID: ws, ID: id, ParentID: root.Id, EnvironmentID: root.EnvironmentId, PrimaryServerID: root.ServerId, CapturedAt: capture.CapturedAt, WindowStart: &capture.WindowStart})
}

// recoverySeals is how many times a base is sealed before it is refused. A seal that doesn't fit
// is followed by one with fewer applied-history entries, so a workspace whose history compresses
// worse than the capture's estimate still gets a base inside the coordinate job's time.
const recoverySeals = 3

// sealRecoveryBase captures and seals a base whose envelope fits, shortening the window when it
// doesn't. A base that doesn't compress and read back exactly is refused like one too large to
// seal, so the root fails before any host effect rather than retrying forever.
//
// The envelope grows about linearly with the number of entries. The first overflow scales the
// entries down by it; later ones fit a line through the sizes measured so far, which also finds
// the part of the base that doesn't shrink with the window.
func sealRecoveryBase(ctx context.Context, tx pgx.Tx, keys *passKeys, ws uuid.UUID, d deployment.RecoveryDescriptor) (portability.RecoveryCapture, []byte, []byte, error) {
	limit := 0
	budget := float64(deployment.RecoveryEnvelopeLimit) * 0.95
	var entries, sizes []float64
	for range recoverySeals {
		capture, err := portability.CaptureRecoveryBase(ctx, tx, ws, portability.RecoveryWindowTarget, limit)
		if err != nil {
			return capture, nil, nil, err
		}
		compressed, err := portability.CompressRecovery(capture.Raw)
		clear(capture.Raw)
		capture.Raw = nil
		if err != nil {
			return capture, nil, nil, errors.Join(errRecoverySize, err)
		}
		envelope, err := sealCheckpoint(keys, d, compressed)
		size := len(envelope)
		if errors.Is(err, secretbroker.ErrTooLarge) {
			// The sealer refuses before writing anything: estimate its envelope from the input.
			size, err = len(compressed)*4/3+1024, nil
		}
		if err != nil {
			clear(compressed)
			return capture, nil, nil, err
		}
		if size <= deployment.RecoveryEnvelopeLimit {
			return capture, envelope, compressed, nil
		}
		clear(compressed)
		if capture.Entries == 0 {
			break
		}
		entries, sizes = append(entries, float64(capture.Entries)), append(sizes, float64(size))
		next := entries[len(entries)-1] * budget / float64(size)
		if n := len(entries); n > 1 && entries[n-2] != entries[n-1] {
			per := (sizes[n-2] - sizes[n-1]) / (entries[n-2] - entries[n-1])
			if per > 0 {
				next = (budget - (sizes[n-1] - per*entries[n-1])) / per
			}
		}
		limit = min(int(next), capture.Entries-1)
		if limit < 1 {
			break
		}
	}
	return portability.RecoveryCapture{}, nil, nil, errRecoverySize
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

func (s *Service) stageRecoveryPhase(ctx context.Context, tx pgx.Tx, keys *passKeys, ws uuid.UUID, parent api.ExecutionCommand, command coordinationCommand, phase string, ids []uuid.UUID, generation int64) error {
	if parent.RecoveryCheckpoint == nil || phase == "checkpoint" || phase == "maintenance-checkpoint" || command.Action == "resolve" || command.Action == "observe" || len(ids) == 0 {
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
	envelope, err := sealCheckpoint(keys, d, raw)
	clear(raw)
	if errors.Is(err, secretbroker.ErrTooLarge) {
		return errRecoverySize
	}
	if err != nil {
		return err
	}
	if len(envelope) > deployment.RecoveryEnvelopeLimit {
		return errRecoverySize
	}
	opened, err := openCheckpoint(keys, d, envelope)
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
	// Management keeps the segment either way. A revoked primary isn't given any more of it.
	if gone, err := serverRevoked(ctx, tx, ws, primary.Plan.ServerID); err != nil || gone {
		return err
	}
	c := api.ExecutionCommand{Id: uuid.NewSHA1(d.CheckpointID, []byte("recovery-delivery")), ParentId: &parent.Id, ServerId: primary.Plan.ServerID, EnvironmentId: parent.EnvironmentId, ResourceId: primary.Plan.UnitID, Kind: "crosshost", Action: "inspect", Generation: generation, Deadline: time.Now().Add(30 * time.Minute), PlanHash: body.Digest(), Crosshost: rawMessage(payload), RecoveryCheckpoint: rawMessage(descriptor)}
	_, err = s.insert(ctx, tx, ws, uuid.New(), nil, c)
	return err
}

// RecoveryCheckpoint returns opaque metadata only to the root's assigned
// primary. Authentication and controller-transfer fencing remain mandatory.
func (s *Service) RecoveryCheckpoint(ctx context.Context, a Identity, parent, checkpoint uuid.UUID) ([]byte, error) {
	body, err := s.Store.Queries.AgentRecoveryCheckpointEnvelope(ctx, db.AgentRecoveryCheckpointEnvelopeParams{WorkspaceID: a.Workspace, ParentID: parent, ID: checkpoint, ServerID: a.ID, CredentialHash: a.Credential, Lineage: a.lineage()})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.NotFound
	}
	return body, err
}
