package execution

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// RecoverySetAside is a base recovery authenticated and doesn't use: one the newest base lists as
// superseded, or one that agrees with it but is older.
type RecoverySetAside struct {
	Checkpoint uuid.UUID `json:"checkpoint"`
	Parent     uuid.UUID `json:"parent"`
	CapturedAt time.Time `json:"capturedAt"`
	Reason     string    `json:"reason"`
}

// recoveryWindowError refuses a copy of the workspace older than the newest base's window. The
// newest base holds the applied history since its window opened, and nothing older, so it can't
// show that the copy is its ancestor rather than a diverged one.
type recoveryWindowError struct {
	Environment string
	Since       time.Time
	// Recorded is when the copy's runtime for Environment was recorded. Retired says the copy
	// shows Environment live where the newest base no longer carries it.
	Recorded time.Time
	Retired  bool
}

func (e recoveryWindowError) Error() string {
	day := func(t time.Time) string { return t.UTC().Format("2 January 2006") }
	if e.Retired {
		return fmt.Sprintf("this copy shows environment %s live, but it was retired before the newest recovery checkpoint's window, which covers changes since %s; use a management backup taken after %s", e.Environment, day(e.Since), day(e.Since))
	}
	return fmt.Sprintf("this copy's runtime for environment %s is from %s, and the newest recovery checkpoint covers changes since %s; use a management backup taken after %s", e.Environment, day(e.Recorded), day(e.Since), day(e.Since))
}

// stagedBase decodes a base the loader authenticated and staged in this transaction.
func stagedBase(ctx context.Context, tx pgx.Tx, parent uuid.UUID) (portability.Archive, error) {
	var raw []byte
	// inline-sql: recovery session temporary table
	if err := tx.QueryRow(ctx, `SELECT decoded FROM recovery_input_checkpoints WHERE parent_id=$1 AND sequence=0`, parent).Scan(&raw); err != nil {
		return portability.Archive{}, err
	}
	defer clear(raw)
	return portability.DecodeRecovery(raw)
}

// selectRecoveryBases chooses what recovery reads from the bases it loaded. The newest by capture
// time, N, is the one it merges: N carries every unfinished root with its children and everything
// they name, so an older base adds nothing to merge, and an older copy of a row N dropped would
// overwrite the restored database's newer one. The base of a root that still needs recovery is
// kept for its environment's applied runtime. Every other base is set aside, after a comparison
// with N unless N lists it as superseded: N's management captured or recovered every base it
// lists, so they are its ancestors, while an unlisted one may come from a diverged management.
// Bases are compared only with N, whose applied history holds any older head of its own lineage.
func selectRecoveryBases(ctx context.Context, tx pgx.Tx, ws uuid.UUID, out *recoverySource) error {
	return chooseRecoveryBases(out, func(parent uuid.UUID) (portability.Archive, error) { return stagedBase(ctx, tx, parent) })
}

// chooseRecoveryBases is selectRecoveryBases over a way to decode each loaded base by its root.
func chooseRecoveryBases(out *recoverySource, decode func(uuid.UUID) (portability.Archive, error)) error {
	if len(out.Bases) == 0 {
		return errors.New("no compatible base recovery capsule was supplied")
	}
	newest := newestRecoveryBase(out.Bases)
	a, err := decode(newest.Parent)
	if err != nil {
		return err
	}
	origin, err := buildAppliedOrigin(a, newest.Parent)
	if err != nil {
		return err
	}
	out.Origins = appliedOrigins{}
	if err = out.Origins.add(origin); err != nil {
		return err
	}
	out.Origins.Newest = origin
	out.Archive, out.History = a, a.Recovery
	if err = refuseUnknownActiveRoots(a, newest, out.Hosts); err != nil {
		return err
	}
	needed := map[uuid.UUID]bool{}
	for _, host := range out.Hosts {
		if host.ActiveParent != uuid.Nil {
			needed[host.ActiveParent] = true
		}
	}
	for _, row := range a.Tables["execution_operations"] {
		c, err := recoveryValue[api.ExecutionCommand](row, "command")
		if err != nil {
			return err
		}
		state, err := recoveryValue[api.ExecutionState](row, "state")
		if err != nil {
			return err
		}
		if c.Kind == "crosshost" && c.ParentId == nil && !Terminal(state) {
			needed[c.Id] = true
		}
	}
	for _, b := range out.Bases {
		if b.ID == newest.ID {
			continue
		}
		parent, listed := newest.Supersedes[b.ID]
		vouched := listed && parent == b.Parent
		if vouched && !needed[b.Parent] {
			out.SetAside = append(out.SetAside, RecoverySetAside{Checkpoint: b.ID, Parent: b.Parent, CapturedAt: b.CapturedAt, Reason: "superseded by the newest checkpoint"})
			continue
		}
		older, err := decode(b.Parent)
		if err != nil {
			return err
		}
		o, err := buildAppliedOrigin(older, b.Parent)
		if err != nil {
			return err
		}
		if !vouched {
			if err = compareWithNewest(origin, o); err != nil {
				// A newer management backup can't help here: the checkpoint is a host's.
				var window recoveryWindowError
				if errors.As(err, &window) {
					err = fmt.Errorf("its runtime for environment %s is from %s, before the newest checkpoint's window opened on %s, so the newest checkpoint can't show it is an ancestor; it may come from a diverged management", window.Environment, window.Recorded.UTC().Format(time.DateOnly), window.Since.UTC().Format(time.DateOnly))
				}
				return fmt.Errorf("checkpoint %s of root %s, captured %s, isn't one the newest checkpoint lists: %w", b.ID, b.Parent, b.CapturedAt.UTC().Format(time.RFC3339), err)
			}
		}
		if needed[b.Parent] {
			if err = out.Origins.add(o); err != nil {
				return err
			}
			continue
		}
		out.SetAside = append(out.SetAside, RecoverySetAside{Checkpoint: b.ID, Parent: b.Parent, CapturedAt: b.CapturedAt, Reason: "older than the newest checkpoint, which agrees with it"})
	}
	return nil
}

// refuseUnknownActiveRoots refuses hosts that report running a cross-host root the newest base
// neither carries nor lists. A root of N's own management is unfinished, and so carried, or its
// base is listed while a host may still hold it. So such a root was started by a management N's
// doesn't descend from, as when a management restored from a backup ran other work on other hosts,
// or N records that it ended and the host's report is stale. A base captured when the root started
// always shows it unfinished, and only the host's unsigned report says it still is, so recovery
// takes neither over. The hosts are named, so the operator can revoke them, leave their exports
// out, and enrol them again afterwards.
func refuseUnknownActiveRoots(a portability.Archive, newest loadedBase, hosts map[uuid.UUID]deployment.RecoveryEvidence) error {
	known := map[uuid.UUID]bool{}
	for _, parent := range newest.Supersedes {
		known[parent] = true
	}
	for _, row := range a.Tables["execution_operations"] {
		id, err := recoveryValue[uuid.UUID](row, "id")
		if err != nil {
			return err
		}
		known[id] = true
	}
	ended := map[uuid.UUID]bool{}
	if a.Recovery != nil {
		for _, e := range a.Recovery.Applied {
			ended[e.Operation] = true
		}
		for _, e := range a.Recovery.Settled {
			ended[e.Operation] = true
		}
	}
	names := map[uuid.UUID]string{}
	for _, row := range a.Tables["servers"] {
		id, err := recoveryValue[uuid.UUID](row, "id")
		if err != nil {
			return err
		}
		names[id], _ = recoveryValue[string](row, "name")
	}
	unknown := map[uuid.UUID][]string{}
	for _, host := range hosts {
		if host.ActiveParent == uuid.Nil || known[host.ActiveParent] {
			continue
		}
		name := names[host.ServerID]
		if name == "" {
			name = host.ServerID.String()
		}
		unknown[host.ActiveParent] = append(unknown[host.ActiveParent], fmt.Sprintf("%s (%s)", name, host.ServerID))
	}
	if len(unknown) == 0 {
		return nil
	}
	root := slices.MinFunc(slices.Collect(maps.Keys(unknown)), func(x, y uuid.UUID) int { return bytes.Compare(x[:], y[:]) })
	servers := unknown[root]
	slices.Sort(servers)
	reason := "which the newest recovery checkpoint neither carries nor lists, so a management this one doesn't descend from started it"
	if ended[root] {
		reason = "which the newest recovery checkpoint records as ended, so their report is stale"
	}
	return fmt.Errorf("servers %s report running cross-host deployment %s, %s. Recovery can't take that work over: revoke those servers in this management and leave their exports out, then after recovery enrol them again and redeploy what they ran", strings.Join(servers, ", "), root, reason)
}

// newestRecoveryBase is N: the base captured last among those no other loaded base lists as
// superseded. Capture times come from the database clock, and a base a later one lists is older
// whatever its time says, as when a recovered management's clock runs behind the failed one's.
func newestRecoveryBase(bases []loadedBase) loadedBase {
	listed := map[uuid.UUID]bool{}
	for _, b := range bases {
		for id := range b.Supersedes {
			listed[id] = true
		}
	}
	candidates := []loadedBase{}
	for _, b := range bases {
		if !listed[b.ID] {
			candidates = append(candidates, b)
		}
	}
	// Bases that list each other in a cycle come from no single lineage; capture time decides,
	// and the comparison with the newest refuses them if they diverge.
	if len(candidates) == 0 {
		candidates = bases
	}
	newest := candidates[0]
	for _, b := range candidates[1:] {
		if b.CapturedAt.After(newest.CapturedAt) || b.CapturedAt.Equal(newest.CapturedAt) && bytes.Compare(b.ID[:], newest.ID[:]) > 0 {
			newest = b
		}
	}
	return newest
}

// compareWithNewest refuses an older base whose applied history N doesn't continue.
func compareWithNewest(newest, older *appliedOrigin) error {
	for env, tuple := range older.Environments {
		head, carried := newest.Environments[env]
		if !carried {
			continue
		}
		order, err := compareApplied(newest, head, older, tuple)
		if err != nil {
			return err
		}
		if order < 0 {
			return errors.New("an older recovery checkpoint holds a newer applied runtime than the newest one, so they come from diverged managements")
		}
	}
	return nil
}

// refuseRetiredBeyondWindow refuses a restored database that shows an environment live that the
// newest base doesn't carry: a standard environment, or a preview with an applied runtime. A base
// carries every standard environment and every preview whose runtime is applied or changed inside
// its window, so such an environment was retired before the window opened, and the database is
// older than the window. A preview without a runtime brings nothing back to life, and an
// environment created after the base is the database's own.
func refuseRetiredBeyondWindow(before portability.Archive, source recoverySource) error {
	if source.History == nil {
		return nil
	}
	carried := map[uuid.UUID]bool{}
	for _, e := range source.History.Environments {
		carried[e.Environment] = true
	}
	operations := map[uuid.UUID]portability.Row{}
	for _, row := range before.Tables["execution_operations"] {
		id, err := recoveryValue[uuid.UUID](row, "id")
		if err != nil {
			return err
		}
		operations[id] = row
	}
	for _, row := range before.Tables["environments"] {
		id, err := recoveryValue[uuid.UUID](row, "id")
		if err != nil {
			return err
		}
		created, err := recoveryValue[time.Time](row, "created_at")
		if err != nil {
			return err
		}
		if carried[id] || !created.Before(source.Archive.CreatedAt) {
			continue
		}
		purpose, err := recoveryValue[string](row, "purpose")
		if err != nil {
			return err
		}
		head, err := recoveryValue[*uuid.UUID](row, "runtime_operation_id")
		if err != nil {
			return err
		}
		live := purpose != "preview"
		if head != nil {
			outcome, err := portability.ReadRuntimeOutcome(operations[*head])
			if err != nil {
				return err
			}
			live = live || outcome.Kind == "applied"
		}
		if live {
			name, _ := recoveryValue[string](row, "name")
			return recoveryWindowError{Environment: name, Since: source.History.WindowStart, Retired: true}
		}
	}
	return nil
}

// endRecoveryGapWork gives an operation the restored database holds unfinished the final state the
// newest base records for it, from its applied history or its settled list. The base carries every
// unfinished operation as a row, so one it records only there ended before it was captured. Only
// recorded states are applied, never inferred ones: the database may be newer than the base, so
// one that progressed further conflicts, and one the base records nothing for is left to fencing.
//
// A runtime plan that neither the database nor the base holds can't be named, as for an upgrade
// whose target plan is stored only when it completes. The operation then ends without the
// outcome, and recovery keeps the entry with the applied history it holds without a row.
func endRecoveryGapWork(row portability.Row, history *portability.RecoveryHistory, planKnown func(uuid.UUID) bool) (portability.Row, error) {
	if history == nil {
		return row, nil
	}
	state, err := recoveryValue[api.ExecutionState](row, "state")
	if err != nil || Terminal(state) {
		return row, err
	}
	id, err := recoveryValue[uuid.UUID](row, "id")
	if err != nil {
		return nil, err
	}
	final := portability.Row{}
	found := false
	for _, e := range history.Applied {
		if e.Operation != id {
			continue
		}
		identity, err := portability.CommandIdentity(row["command"], portability.CommandIdentityVersion)
		if err != nil {
			return nil, portability.ErrArchive
		}
		if identity != e.Identity {
			return nil, errors.New("execution identity conflicts with the newest recovery checkpoint")
		}
		recoverySet(final, "state", e.State)
		recoverySet(final, "report_sequence", e.Sequence)
		e.Runtime().Set(final)
		found = true
	}
	for _, s := range history.Settled {
		if s.Operation == id {
			recoverySet(final, "state", s.State)
			recoverySet(final, "report_sequence", s.Sequence)
			recoverySet(final, "runtime_outcome", s.Outcome)
			recoverySet(final, "runtime_plan_id", s.RuntimePlan)
			found = true
		}
	}
	if !found {
		return row, nil
	}
	ended, err := recoveryValue[api.ExecutionState](final, "state")
	if err != nil || !Terminal(ended) {
		return row, err
	}
	sequence, err := recoveryValue[int64](row, "report_sequence")
	if err != nil {
		return nil, err
	}
	last, err := recoveryValue[int64](final, "report_sequence")
	if err != nil {
		return nil, err
	}
	if sequence > last {
		return nil, errors.New("execution outcome conflicts with the newest recovery checkpoint")
	}
	outcome, _, err := portability.MergeRuntimeOutcomes(row, final)
	if err != nil {
		return nil, err
	}
	out := maps.Clone(row)
	out["state"], out["report_sequence"] = final["state"], final["report_sequence"]
	if outcome.Plan == uuid.Nil || planKnown(outcome.Plan) {
		outcome.Set(out)
	}
	return out, nil
}

// endRecoveryGapRow is a restored row as recovery merges it when the newest base doesn't carry it:
// an operation gets the final state the base records, naming only a plan one of the two holds.
func endRecoveryGapRow(table string, row portability.Row, history *portability.RecoveryHistory, known, incoming recoveryRows) (portability.Row, error) {
	row = maps.Clone(row)
	if table != "execution_operations" {
		return row, nil
	}
	return endRecoveryGapWork(row, history, func(plan uuid.UUID) bool {
		key, _ := stablejson.Marshal([]string{plan.String()})
		_, kept := known["visual_plans"][string(key)]
		_, carried := incoming["visual_plans"][string(key)]
		return kept || carried
	})
}

// registerLoadedBases records every base recovery loaded that this database doesn't know, since a
// host holds it and a later base must list it.
func registerLoadedBases(ctx context.Context, q *db.Queries, ws uuid.UUID, source recoverySource) error {
	for _, b := range source.Bases {
		if err := q.RegisterRecoveredBase(ctx, db.RegisterRecoveredBaseParams{WorkspaceID: ws, ID: b.ID, ParentID: b.Parent, EnvironmentID: b.Environment, PrimaryServerID: b.Primary, CapturedAt: b.CapturedAt, WindowStart: b.WindowStart}); err != nil {
			return err
		}
	}
	return nil
}
