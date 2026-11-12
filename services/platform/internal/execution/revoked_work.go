package execution

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

// settledReportSequence marks work a revocation ended. Any later report is answered without changing
// it, so a renewed host can acknowledge its journal. Recovery treats it the same way.
const settledReportSequence = portability.MaxCounter - 1

const (
	revokedServersNote       = "A revoked server took part, and what it did is unknown. It may still be running what it was given."
	revokedDeploymentNote    = "A server was revoked before this deployment was published, so the remaining hosts removed their candidates and kept their previous pools. What the revoked server did is unknown."
	revokedBeforeChangesNote = "A server was revoked, so this stopped before any host changed."
)

// settleRevokedWork runs in the revocation's transaction. Nothing a revoked server could say would
// be accepted, so waiting for it can't produce an answer, and the healthy hosts waiting with it stay
// held until it stops. Only the owner's revocation decides this: nothing is read from the host.
func (s *Service) settleRevokedWork(ctx context.Context, tx pgx.Tx, ws, server uuid.UUID) error {
	q := db.New(tx)
	if err := q.SettleRevokedServerOperations(ctx, db.SettleRevokedServerOperationsParams{ReportSequence: settledReportSequence, WorkspaceID: ws, ServerID: server}); err != nil {
		return err
	}
	if err := q.RejectRevokedServerUpgrades(ctx, db.RejectRevokedServerUpgradesParams{WorkspaceID: ws, ServerID: server}); err != nil {
		return err
	}
	if err := q.CancelUnstartedReleaseCommands(ctx, db.CancelUnstartedReleaseCommandsParams{WorkspaceID: ws, ServerID: server.String()}); err != nil {
		return err
	}
	if err := releaseRevokedPreviews(ctx, tx, ws, server); err != nil {
		return err
	}
	if err := s.withdrawRevokedDomain(ctx, tx, ws, server); err != nil {
		return err
	}
	parked, err := q.ParkedMaintenanceOnPrimary(ctx, db.ParkedMaintenanceOnPrimaryParams{WorkspaceID: ws, ServerID: server})
	if err != nil {
		return err
	}
	for _, id := range parked {
		if err = q.ResumeMaintenanceSource(ctx, db.ResumeMaintenanceSourceParams{WorkspaceID: ws, ID: id}); err != nil {
			return err
		}
		job, err := s.Queue.InsertTx(ctx, tx, jobArgs{ID: id}, &river.InsertOpts{MaxAttempts: 100})
		if err != nil {
			return err
		}
		if err = q.AttachOperationJob(ctx, db.AttachOperationJobParams{JobID: job.Job.ID, WorkspaceID: ws, ID: id}); err != nil {
			return err
		}
	}
	return nil
}

func serverRevoked(ctx context.Context, tx pgx.Tx, ws, server uuid.UUID) (bool, error) {
	rows, err := db.New(tx).RevokedServersAmong(ctx, db.RevokedServersAmongParams{WorkspaceID: ws, Ids: []uuid.UUID{server}})
	return len(rows) > 0, err
}

func revokedParticipants(ctx context.Context, tx pgx.Tx, ws uuid.UUID, units []deployment.DistributedUnit) (map[uuid.UUID]bool, error) {
	ids := make([]uuid.UUID, 0, len(units))
	for _, u := range units {
		ids = append(ids, u.Plan.ServerID)
	}
	rows, err := db.New(tx).RevokedServersAmong(ctx, db.RevokedServersAmongParams{WorkspaceID: ws, Ids: ids})
	out := map[uuid.UUID]bool{}
	for _, row := range rows {
		out[row.ID] = true
	}
	return out, err
}

func withoutRevoked(units []deployment.DistributedUnit, revoked map[uuid.UUID]bool) []deployment.DistributedUnit {
	return slices.DeleteFunc(slices.Clone(units), func(u deployment.DistributedUnit) bool { return revoked[u.Plan.ServerID] })
}

// crossedBoundary says whether the command that can't be taken back has gone out: a publication or
// a data effect. Issued to a healthy host, it's seen through, because its outcome decides what the
// other hosts do next. On a revoked host, only a success it already reported counts.
func crossedBoundary(ctx context.Context, tx pgx.Tx, ws, command uuid.UUID, hostLost bool) (bool, error) {
	known, err := db.New(tx).OperationReportState(ctx, db.OperationReportStateParams{WorkspaceID: ws, ID: command})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return !hostLost || known.State == "succeeded", nil
}

// revokedStopsDeployment says whether a lost participant ends a deployment rather than leaving it
// to finish on the others. Before publication it does, because a complete backend vector can't
// include that host.
func revokedStopsDeployment(ctx context.Context, tx pgx.Tx, ws, parent uuid.UUID, p deployment.Plan, phase string, revoked map[uuid.UUID]bool) (bool, error) {
	if len(revoked) == 0 {
		return false, nil
	}
	switch phase {
	case "checkpoint", "resolve", "reserve", "links", "prepare", "hook", "probe":
		return true, nil
	case "publish":
		crossed, err := crossedBoundary(ctx, tx, ws, phaseKey(parent, phase, p.UnitID), revoked[p.ServerID])
		return !crossed, err
	}
	return false, nil
}

// abandonedServers finds the hosts a new plan can't include. Only a revocation qualifies: a member
// merely removed from the network could still answer, so it keeps blocking until it does.
func abandonedServers(ctx context.Context, tx pgx.Tx, ws uuid.UUID, prior map[uuid.UUID]deployment.Plan) (map[uuid.UUID]string, error) {
	ids := make([]uuid.UUID, 0, len(prior))
	for id := range prior {
		ids = append(ids, id)
	}
	rows, err := db.New(tx).RevokedServersAmong(ctx, db.RevokedServersAmongParams{WorkspaceID: ws, Ids: ids})
	out := map[uuid.UUID]string{}
	for _, row := range rows {
		out[row.ID] = row.Name
	}
	return out, err
}

func abandonedIDs(abandoned map[uuid.UUID]string) []uuid.UUID {
	ids := slices.Collect(maps.Keys(abandoned))
	slices.SortFunc(ids, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
	return ids
}

// requireStillRevoked stops a reviewed plan once a server it left behind can answer again.
func requireStillRevoked(ctx context.Context, tx pgx.Tx, ws uuid.UUID, servers []uuid.UUID) error {
	rows, err := db.New(tx).RevokedServersAmong(ctx, db.RevokedServersAmongParams{WorkspaceID: ws, Ids: servers})
	if err != nil {
		return err
	}
	if len(rows) != len(servers) {
		return domain.Conflict
	}
	return nil
}

func abandonedEffects(abandoned map[uuid.UUID]string) []string {
	names := make([]string, 0, len(abandoned))
	for _, name := range abandoned {
		names = append(names, name)
	}
	slices.Sort(names)
	out := make([]string, 0, len(names))
	for _, name := range names {
		out = append(out, fmt.Sprintf("Leave revoked server %s out of this environment. It can't be told to stop, so it may keep running what it was given. Rotate the secrets and registry access it held.", name))
	}
	return out
}

// revokedUnitsAsUnknown adds what the plan placed on hosts that can't report, so one host's state
// isn't presented as the whole group's. Applying also gives each its runtime row under this plan.
func revokedUnitsAsUnknown(ctx context.Context, tx pgx.Tx, ws uuid.UUID, p deployment.Plan, reported []deployment.DistributedUnit, resources map[uuid.UUID]deployment.Resource, apply bool) ([]deployment.HostObservation, error) {
	hosts := []deployment.HostObservation{}
	now := time.Now().UTC()
	for _, u := range p.ActiveUnits() {
		if slices.ContainsFunc(reported, func(r deployment.DistributedUnit) bool { return r.Plan.UnitID == u.Plan.UnitID }) {
			continue
		}
		hosts = append(hosts, deployment.HostObservation{ServerID: u.Plan.ServerID, UnitID: u.Plan.UnitID, State: "unknown", ObservedAt: now})
		for _, app := range u.Plan.LocalApplications() {
			res, ok := resources[app.ID]
			if !ok {
				res = deployment.Resource{ID: app.ID, Kind: "service", Image: u.Plan.Images[app.ID.String()]}
			}
			for _, id := range u.Plan.InstanceIDs(app.ID) {
				res.Instances = append(res.Instances, deployment.InstanceState{InstanceID: id, ServerID: &u.Plan.ServerID, UnitID: &u.Plan.UnitID, State: "unknown", Image: u.Plan.Images[app.ID.String()]})
			}
			resources[app.ID] = res
		}
		if !apply {
			continue
		}
		inventory, err := stablejson.Marshal(struct {
			Plan        deployment.Plan        `json:"plan"`
			Observation deployment.Observation `json:"observation"`
		}{u.Plan, deployment.Observation{ObservedAt: now, Resources: []deployment.Resource{}}})
		if err != nil {
			return nil, err
		}
		if err = db.New(tx).RecordRevokedUnitRuntime(ctx, db.RecordRevokedUnitRuntimeParams{WorkspaceID: ws, EnvironmentID: p.EnvironmentID, UnitID: u.Plan.UnitID, ServerID: u.Plan.ServerID, PlanID: p.ID, Inventory: inventory, CpuMillis: u.CPUMillis, MemoryMib: u.MemoryMiB}); err != nil {
			return nil, err
		}
	}
	return hosts, nil
}

func deleteAbandonedRuntime(ctx context.Context, tx pgx.Tx, ws uuid.UUID, p deployment.Plan) error {
	units := []uuid.UUID{}
	// A unit with nothing left to run removes its own row on reporting, and a revoked one can't.
	for _, u := range p.ActiveUnits() {
		units = append(units, u.Plan.UnitID)
	}
	return db.New(tx).DeleteAbandonedCrossHostRuntime(ctx, db.DeleteAbandonedCrossHostRuntimeParams{WorkspaceID: ws, EnvironmentID: p.EnvironmentID, Units: units})
}
