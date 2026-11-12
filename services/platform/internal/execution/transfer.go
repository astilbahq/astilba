package execution

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"reflect"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/agentidentity"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func generation(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}
func transferInventory(ctx context.Context, tx pgx.Tx, ws, server uuid.UUID) (api.ControllerInventory, error) {
	out := api.ControllerInventory{Units: map[string]string{}, Schedules: map[string]int64{}}
	q := db.New(tx)
	units, e := q.ServerControllerInventory(ctx, db.ServerControllerInventoryParams{WorkspaceID: ws, ServerID: server})
	if e != nil {
		return out, e
	}
	for _, unit := range units {
		var p deployment.Plan
		if e = json.Unmarshal(unit.Plan, &p); e != nil {
			return out, e
		}
		out.Units[unit.UnitID.String()] = p.PrimaryPlan().Hash
	}
	statuses, e := q.ListServerBackupStatuses(ctx, db.ListServerBackupStatusesParams{WorkspaceID: ws, ServerID: server})
	if e != nil {
		return out, e
	}
	for _, row := range statuses {
		var s backup.Status
		if e = json.Unmarshal(row.Status, &s); e != nil {
			return out, e
		}
		if s.PolicyID != uuid.Nil {
			out.Schedules[row.ID.String()] = s.Generation
		}
	}
	return out, nil
}

// enroll exchanges a one-time code for the server's identity: a key the agent holds, or a legacy
// bearer. Consuming the code starts a new epoch, the only thing besides a bind that does, and
// clears any identity conflict. A retry through the identity the code already registered changes
// nothing, so nobody holding that identity can reset what management knows about forks.
func (s *Service) enroll(ctx context.Context, in api.AgentEnrollment, source string) (api.AgentStatus, error) {
	var out api.AgentStatus
	var candidate, keyDER []byte
	if in.PublicKey != nil {
		public, der, err := agentidentity.ParsePublicKey(*in.PublicKey)
		if err != nil || !agentidentity.Verify(public, agentidentity.EnrollMessage(in.ServerId, in.Token, in.MachineId), *in.Proof) {
			return out, codeNotAccepted
		}
		keyDER, candidate = der, agentidentity.KeyHash(der)
	} else {
		candidate = hash(*in.Credential)
	}
	tx, e := s.Store.Pool.Begin(ctx)
	if e != nil {
		return out, e
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)
	locked, e := q.LockAgentEnrollment(ctx, db.LockAgentEnrollmentParams{ServerID: in.ServerId, EnrollmentHash: hash(in.Token), CredentialHash: candidate, MachineID: &in.MachineId})
	if errors.Is(e, pgx.ErrNoRows) {
		if keyDER != nil {
			// Release this request's connection before checking, so refused requests can't hold one
			// each while they wait for another.
			if e = tx.Rollback(ctx); e != nil {
				return out, e
			}
			if e = s.codeTaken(ctx, in, candidate, source); e != nil {
				return out, e
			}
		}
		return out, codeNotAccepted
	}
	if e != nil {
		return out, e
	}
	if !locked.Consuming {
		out.Role, out.RotationRequested = api.ServerRole(locked.Role), locked.RotationRequested
		if locked.PreviewOnly {
			out.PreviewOnly = &locked.PreviewOnly
		}
		return out, nil
	}
	ws, expected, awaiting := locked.WorkspaceID, locked.ControllerGeneration, locked.AwaitingTransfer
	// Enrolling grants a server an identity, so a hosted workspace needs active management. A
	// transfer is part of recovery, which its own admission governed when the code was issued.
	if !awaiting {
		if e = s.Store.ManagementAdmission(ctx, tx, ws); e != nil {
			return out, e
		}
	}
	if locked.PreviewOnly && locked.Fresh && in.Preflight.ExistingContainers > 0 {
		return out, domain.Invalid("preview.cleanServerRequired")
	}
	if generation(in.ControllerGeneration) != expected {
		return out, codeNotAccepted
	}
	if awaiting {
		if in.Inventory == nil {
			return out, domain.Invalid("inventory")
		}
		if in.Inventory != nil {
			if e = s.acceptPreviewCleanupProofs(ctx, tx, ws, in.ServerId, in.Inventory.PreviewCleanups); e != nil {
				return out, e
			}
			in.Inventory.PreviewCleanups = nil
		}
		inventory, e := transferInventory(ctx, tx, ws, in.ServerId)
		if e != nil {
			return out, e
		}
		stagedServer := db.StagedRecoveryInventoryParams{WorkspaceID: ws, ServerID: in.ServerId, MachineID: in.MachineId, ControllerGeneration: expected}
		staged, err := q.StagedRecoveryInventory(ctx, stagedServer)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return out, err
		}
		if err == nil {
			var stagedInventory api.ControllerInventory
			if json.Unmarshal(staged, &stagedInventory) != nil {
				return out, domain.Invalid("recovery.inventory")
			}
			inventory = stagedInventory
		}
		actual := *in.Inventory
		if inventory.Recovery != nil || actual.Recovery != nil {
			var expectedAuthority, actualAuthority deployment.RecoveryAuthority
			if inventory.Recovery == nil || actual.Recovery == nil || hostnetwork.Decode(*inventory.Recovery, &expectedAuthority) != nil || hostnetwork.Decode(*actual.Recovery, &actualAuthority) != nil || expectedAuthority != actualAuthority {
				return out, domain.Conflict
			}
			inventory.Recovery, actual.Recovery = nil, nil
		}
		if !reflect.DeepEqual(inventory, actual) {
			return out, domain.Conflict
		}
		if err == nil {
			if err = q.VerifyStagedRecoveryServer(ctx, db.VerifyStagedRecoveryServerParams(stagedServer)); err != nil {
				return out, err
			}
		}
	}
	caps, _ := stablejson.Marshal(in.Capabilities)
	pre, _ := stablejson.Marshal(in.Preflight)
	enrolled, e := q.CompleteAgentEnrollment(ctx, db.CompleteAgentEnrollmentParams{WorkspaceID: ws, ServerID: in.ServerId, CredentialHash: candidate, MachineID: &in.MachineId, Capabilities: caps, Preflight: pre})
	if e != nil {
		return out, e
	}
	epoch, e := q.StartNewEpoch(ctx, db.StartNewEpochParams{CodeHash: hash(in.Token), KeyBound: keyDER != nil, WorkspaceID: ws, ServerID: in.ServerId})
	if e != nil {
		return out, e
	}
	if keyDER != nil {
		if e = q.InsertAgentKey(ctx, db.InsertAgentKeyParams{WorkspaceID: ws, ServerID: in.ServerId, KeyHash: candidate, PublicKey: keyDER, Epoch: epoch}); e != nil {
			return out, e
		}
		if e = identityEvent(ctx, q, ws, in.ServerId, epoch, "enrolled", "", candidate, source); e != nil {
			return out, e
		}
	} else if e = identityEvent(ctx, q, ws, in.ServerId, epoch, "enrolled", "", nil, source); e != nil {
		return out, e
	}
	out.Role, out.RotationRequested = api.ServerRole(enrolled.Role), enrolled.RotationRequested
	if enrolled.PreviewOnly {
		out.PreviewOnly = &enrolled.PreviewOnly
	}
	return out, tx.Commit(ctx)
}

// codeTaken checks a refused key enrollment: a valid proof for the code this epoch already
// consumed, with a key other than the one it registered, is a second holder of that code.
func (s *Service) codeTaken(ctx context.Context, in api.AgentEnrollment, candidate []byte, source string) error {
	code := hash(in.Token)
	// Any other refused code takes no lock and records nothing.
	taken, err := s.Store.Queries.CodeTakenByAnotherKey(ctx, db.CodeTakenByAnotherKeyParams{CodeHash: code, Candidate: candidate, ServerID: in.ServerId})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !taken) {
		return nil
	}
	if err != nil {
		return err
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)
	row, err := q.LockServerIdentity(ctx, in.ServerId)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil || !bytes.Equal(row.ConsumedCodeHash, code) || row.CredentialHash == nil || bytes.Equal(row.CredentialHash, candidate) {
		return err
	}
	if err = s.conflict(ctx, q, row.WorkspaceID, in.ServerId, row.EnrollmentEpoch, signalCode, candidate, source); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) PrepareTransfer(ctx context.Context, p store.Principal, ws, id uuid.UUID) (api.TransferTicket, error) {
	out := api.TransferTicket{ServerId: id, ExpiresAt: time.Now().Add(10 * time.Minute)}
	token, e := cryptobox.Token()
	if e != nil {
		return out, e
	}
	out.Token = token
	e = s.scope(ctx, p, ws, workspaceAccess{Write: true, Owner: true, Credential: true, Recovery: true}, "workspace.transfer", id, func(tx pgx.Tx) error {
		if e := s.Store.ManagedServerAdmission(ctx, tx, ws, id); e != nil {
			return e
		}
		// A transfer lifts a revocation, so, as with renewal, not while a root still running decided
		// to carry on without the server: it would wait for a host it never handed anything.
		if held, e := db.New(tx).RevokedServerInActiveRoot(ctx, db.RevokedServerInActiveRootParams{WorkspaceID: ws, ServerID: id}); e != nil {
			return e
		} else if held {
			return domain.Conflict
		}
		generation, e := db.New(tx).PrepareServerTransfer(ctx, db.PrepareServerTransferParams{WorkspaceID: ws, ID: id, EnrollmentHash: hash(token), EnrollmentExpires: &out.ExpiresAt})
		out.Generation = generation
		return e
	})
	return out, e
}
