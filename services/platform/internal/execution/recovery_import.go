package execution

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type RecoveryImport struct {
	WorkspaceID   uuid.UUID
	OwnerID       uuid.UUID
	ExpectedState string
	HostExports   []string
}

type RecoveryPreview struct {
	StateHash      string                                    `json:"stateHash"`
	WorkspaceID    uuid.UUID                                 `json:"workspaceId"`
	Parents        []uuid.UUID                               `json:"parents"`
	Handovers      map[uuid.UUID]deployment.RecoveryHandover `json:"handovers"`
	Applied        bool                                      `json:"applied"`
	AlreadyApplied bool                                      `json:"alreadyApplied"`
}

type recoveringRoot struct {
	Command       api.ExecutionCommand
	Body          coordinationCommand
	Descriptor    deployment.RecoveryDescriptor
	KnownSequence int64
}

// RecoverCrossHost is an offline operator action. An empty ExpectedState returns
// a concrete preview; a write must name the exact current workspace state from
// that preview. Accounts, sessions and installation keys are never imported.
func (s *Service) RecoverCrossHost(ctx context.Context, in RecoveryImport) (RecoveryPreview, error) {
	out := RecoveryPreview{WorkspaceID: in.WorkspaceID, Parents: []uuid.UUID{}, Handovers: map[uuid.UUID]deployment.RecoveryHandover{}}
	if in.WorkspaceID == uuid.Nil || in.OwnerID == uuid.Nil {
		return out, domain.Invalid("recovery.owner")
	}
	tx, err := s.Store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	if err = store.RequireOffline(ctx, tx); err != nil {
		return out, err
	}
	var owner bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM memberships WHERE workspace_id=$1 AND account_id=$2 AND role='owner')`, in.WorkspaceID, in.OwnerID).Scan(&owner); err != nil {
		return out, err
	}
	if !owner {
		return out, domain.Forbidden
	}
	if _, err = tx.Exec(ctx, `SELECT id FROM workspaces WHERE id=$1 FOR UPDATE`, in.WorkspaceID); err != nil {
		return out, err
	}
	before, stateHash, err := portability.InspectRecovery(ctx, tx, in.WorkspaceID)
	if err != nil {
		return out, err
	}
	out.StateHash = stateHash
	source, err := loadRecoverySource(ctx, tx, s.Store.Keys, in.WorkspaceID, in.HostExports)
	if err != nil {
		return out, err
	}
	if err = s.recoverSavedCheckpoints(ctx, tx, before, &source); err != nil {
		return out, err
	}
	roots, err := requiredRecoveryRoots(source)
	if err != nil {
		return out, err
	}
	if len(roots) == 0 {
		return out, errors.New("no interrupted root needs checkpoint recovery; use ordinary controller transfer")
	}
	if _, err = tx.Exec(ctx, `CREATE TEMP TABLE recovery_selected_roots(id uuid PRIMARY KEY) ON COMMIT DROP; CREATE TEMP TABLE recovery_incoming_grants(endpoint_id uuid NOT NULL,server_id uuid NOT NULL,unit_id uuid NOT NULL,PRIMARY KEY(endpoint_id,server_id,unit_id)) ON COMMIT DROP; CREATE TEMP TABLE recovery_standalone_restrictions(id uuid PRIMARY KEY,source jsonb) ON COMMIT DROP; INSERT INTO recovery_standalone_restrictions(id) SELECT id FROM recovery_input_proofs WHERE parent_id='00000000-0000-0000-0000-000000000000'`); err != nil {
		return out, err
	}
	inputHash := Hash(struct {
		Hosts map[uuid.UUID]deployment.RecoveryEvidence
		Roots map[uuid.UUID]deployment.RecoveryDescriptor
	}{source.Hosts, source.Roots})
	if in.ExpectedState != "" && in.ExpectedState != out.StateHash {
		if repeated, err := s.repeatedRecovery(ctx, tx, in, inputHash, roots, &out); err != nil {
			return out, err
		} else if repeated {
			return out, nil
		}
		return out, errors.New("workspace state changed; inspect recovery again")
	}
	if err = s.prepareRecoveryKnowledge(ctx, tx, before, &source, roots); err != nil {
		return out, err
	}
	if err = collectRecoveryRestrictions(ctx, tx, before, source.Archive); err != nil {
		return out, err
	}
	for id := range roots {
		if _, err = tx.Exec(ctx, `INSERT INTO recovery_selected_roots VALUES($1)`, id); err != nil {
			return out, err
		}
	}
	for _, row := range source.Archive.Tables["network_endpoint_sources"] {
		raw, _ := json.Marshal(row)
		if _, err = tx.Exec(ctx, `INSERT INTO recovery_incoming_grants SELECT endpoint_id,server_id,unit_id FROM jsonb_populate_record(NULL::network_endpoint_sources,$1)`, raw); err != nil {
			return out, err
		}
	}
	for id := range roots {
		out.Parents = append(out.Parents, id)
	}
	slices.SortFunc(out.Parents, func(a, b uuid.UUID) int { return bytesCompareUUID(a, b) })
	if err = validateRecoveryCompatibility(before, source, roots); err != nil {
		return out, err
	}
	controllers, err := recoveryControllers(before, source)
	if err != nil {
		return out, err
	}
	for parent, root := range roots {
		units := root.Body.Plan.Distributed.Units
		if root.Body.Maintenance != nil {
			units = root.Body.Plan.ActiveUnits()
		}
		for _, unit := range units {
			host, exists := source.Hosts[unit.Plan.ServerID]
			if !exists {
				return out, errors.New("a required participant export is missing")
			}
			if _, duplicate := out.Handovers[host.ServerID]; duplicate {
				return out, errors.New("two interrupted parents claim the same host")
			}
			out.Handovers[host.ServerID] = deployment.RecoveryHandover{Authority: deployment.RecoveryAuthority{ParentID: parent, RootHash: root.Descriptor.RootHash, EvidenceHash: deployment.Hash(host), PriorController: host.Controller, Controller: controllers[host.ServerID]}, Descriptor: root.Descriptor, UnitID: unit.Plan.UnitID, EnvironmentID: unit.Plan.EnvironmentID, UnitHash: unit.Plan.Hash, Primary: unit.Plan.Local.Primary, Network: unit.Plan.Local.Network}
		}
	}
	if err = validateRecoveryRestrictionEvidence(ctx, tx, out.Handovers); err != nil {
		return out, err
	}
	if err = portability.CheckRecoveryKeys(s.Store.Keys, source.Archive); err != nil {
		return out, err
	}
	if in.ExpectedState == "" {
		return out, nil
	}
	// Domain history remains intact. Old mutable delivery and authority are
	// fenced before copying the newer checkpoint; no host is contacted here.
	if err = fenceRecoveryWorkspace(ctx, tx, in.WorkspaceID); err != nil {
		return out, err
	}
	if err = holdRecoveryRestrictions(ctx, tx, in.WorkspaceID); err != nil {
		return out, err
	}
	if err = stageRecoveryProjections(ctx, tx, in.WorkspaceID, source.Projections); err != nil {
		return out, err
	}
	if err = portability.MergeRecovery(ctx, tx, source.Archive, in.OwnerID); err != nil {
		return out, err
	}
	if err = holdRecoveryRestrictions(ctx, tx, in.WorkspaceID); err != nil {
		return out, err
	}
	if err = s.restoreRecoveryRoots(ctx, tx, in.WorkspaceID, roots, in.ExpectedState, inputHash, in.OwnerID); err != nil {
		return out, err
	}
	if err = s.restoreRecoveryProofs(ctx, tx, in.WorkspaceID, roots, source.Hosts); err != nil {
		return out, err
	}
	if err = restoreRecoveryGrants(ctx, tx, in.WorkspaceID, source.Projections, roots); err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `UPDATE execution_operations SET state='cancelled',report_sequence=report_sequence+1,note='Prior controller replay grant superseded by guarded recovery.' WHERE workspace_id=$1 AND parent_id IN (SELECT id FROM recovery_selected_roots) AND command->'crosshost'->>'phase'='recovery-open' AND state NOT IN ('succeeded','failed','cancelled')`, in.WorkspaceID); err != nil {
		return out, err
	}
	if err = fenceRecoveryWorkspace(ctx, tx, in.WorkspaceID); err != nil {
		return out, err
	}
	if err = validateRecoveryUpgradeKnowledge(ctx, tx, before, source.Archive); err != nil {
		return out, err
	}
	if err = s.restoreStandaloneRestrictions(ctx, tx, in.WorkspaceID, controllers); err != nil {
		return out, err
	}
	for server, controller := range controllers {
		host := source.Hosts[server]
		if _, err = tx.Exec(ctx, `UPDATE servers SET controller_generation=$3,awaiting_transfer=true,credential_hash=NULL,previous_hash=NULL,previous_until=NULL,enrollment_hash=NULL,enrollment_expires=NULL,poll_id=NULL,poll_until=NULL,rotation_requested=false WHERE workspace_id=$1 AND id=$2`, in.WorkspaceID, server, controller); err != nil {
			return out, err
		}
		if handover, required := out.Handovers[server]; required {
			inventory := api.ControllerInventory{Units: host.Units, Schedules: host.Schedules}
			raw, _ := json.Marshal(handover.Authority)
			inventory.Recovery = rawMessage(raw)
			encoded, _ := json.Marshal(inventory)
			handoverRaw, _ := json.Marshal(handover)
			if _, err = tx.Exec(ctx, `INSERT INTO cross_host_recovery_servers(parent_id,workspace_id,server_id,machine_id,controller_generation,inventory,handover) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(parent_id,server_id) DO UPDATE SET machine_id=excluded.machine_id,controller_generation=excluded.controller_generation,inventory=excluded.inventory,handover=excluded.handover,verified=false`, handover.Authority.ParentID, in.WorkspaceID, server, host.MachineID, controller, encoded, handoverRaw); err != nil {
				return out, err
			}
		}
	}
	if err = s.finishRecoveryStaging(ctx, tx, in, source, roots); err != nil {
		return out, err
	}
	if err = tx.Commit(ctx); err != nil {
		return out, err
	}
	out.Applied = true
	return out, nil
}

func bytesCompareUUID(a, b uuid.UUID) int {
	for i := range a {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	return 0
}

func requiredRecoveryRoots(source recoverySource) (map[uuid.UUID]recoveringRoot, error) {
	out := map[uuid.UUID]recoveringRoot{}
	active := map[uuid.UUID]bool{}
	for _, host := range source.Hosts {
		if host.ActiveParent != uuid.Nil {
			active[host.ActiveParent] = true
		}
	}
	for _, row := range source.Archive.Tables["execution_operations"] {
		var c api.ExecutionCommand
		var state api.ExecutionState
		if json.Unmarshal(row["command"], &c) != nil || json.Unmarshal(row["state"], &state) != nil {
			return out, portability.ErrArchive
		}
		if c.Kind != "crosshost" || c.ParentId != nil || Terminal(state) && !active[c.Id] {
			continue
		}
		var body coordinationCommand
		if c.Crosshost == nil || json.Unmarshal(*c.Crosshost, &body) != nil || Hash(body) != c.PlanHash || body.Plan.Distributed == nil || body.Plan.Validate(true) != nil {
			return out, errors.New("invalid frozen recovery root")
		}
		if body.Action == "resolve" || body.Action == "observe" {
			continue
		}
		d, exists := source.Roots[c.Id]
		if !exists || d.RootHash != c.PlanHash || d.GroupID != body.Plan.ID || d.GroupHash != body.Plan.Hash {
			return out, errors.New("an interrupted root requires its complete primary capsule chain")
		}
		raw, _ := json.Marshal(d)
		c.RecoveryCheckpoint = rawMessage(raw)
		out[c.Id] = recoveringRoot{Command: c, Body: body, Descriptor: d, KnownSequence: source.KnownSequences[c.Id]}
	}
	for parent := range active {
		if _, ok := out[parent]; !ok {
			return out, errors.New("active host parent is absent from the selected checkpoint")
		}
	}
	return out, nil
}

func recoveryControllers(before portability.Archive, source recoverySource) (map[uuid.UUID]int64, error) {
	out := map[uuid.UUID]int64{}
	for _, archive := range []portability.Archive{before, source.Archive} {
		for _, row := range archive.Tables["servers"] {
			var id uuid.UUID
			var epoch int64
			var machine *uuid.UUID
			if json.Unmarshal(row["id"], &id) != nil || json.Unmarshal(row["controller_generation"], &epoch) != nil || json.Unmarshal(row["machine_id"], &machine) != nil {
				return out, portability.ErrArchive
			}
			host, exists := source.Hosts[id]
			if machine != nil && (!exists || *machine != host.MachineID) {
				return out, errors.New("every captured enrolled server needs matching machine evidence")
			}
			if prior, exists := out[id]; !exists || epoch > prior {
				out[id] = epoch
			}
			if host.Controller > out[id] {
				out[id] = host.Controller
			}
		}
	}
	for id, epoch := range out {
		if epoch == math.MaxInt64 {
			return out, domain.Invalid("controllerGeneration")
		}
		out[id] = epoch + 1
	}
	return out, nil
}

func validateRecoveryCompatibility(before portability.Archive, source recoverySource, roots map[uuid.UUID]recoveringRoot) error {
	created := map[uuid.UUID]time.Time{}
	for _, archive := range []portability.Archive{before, source.Archive} {
		for _, row := range archive.Tables["specification_revisions"] {
			var id uuid.UUID
			var at time.Time
			if json.Unmarshal(row["id"], &id) != nil || json.Unmarshal(row["created_at"], &at) != nil {
				return portability.ErrArchive
			}
			created[id] = at
		}
	}
	incoming := map[uuid.UUID]*uuid.UUID{}
	for _, row := range source.Archive.Tables["environments"] {
		var id uuid.UUID
		var applied *uuid.UUID
		if json.Unmarshal(row["id"], &id) != nil || json.Unmarshal(row["applied_revision_id"], &applied) != nil {
			return portability.ErrArchive
		}
		incoming[id] = applied
	}
	for _, row := range before.Tables["environments"] {
		var id uuid.UUID
		var old *uuid.UUID
		if json.Unmarshal(row["id"], &id) != nil || json.Unmarshal(row["applied_revision_id"], &old) != nil {
			return portability.ErrArchive
		}
		if old == nil {
			continue
		}
		next, exists := incoming[id]
		if !exists || next == nil || *old != *next && !created[*old].Before(created[*next]) {
			return errors.New("current applied history is newer or incompatible; supply a compatible newer checkpoint")
		}
	}
	for _, row := range before.Tables["execution_operations"] {
		var id uuid.UUID
		var state api.ExecutionState
		if json.Unmarshal(row["id"], &id) != nil || json.Unmarshal(row["state"], &state) != nil {
			return portability.ErrArchive
		}
		if _, required := roots[id]; required && Terminal(state) {
			return errors.New("current management already completed this root; use ordinary transfer or a compatible newer recovery point")
		}
	}
	return nil
}
