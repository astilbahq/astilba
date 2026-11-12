package integration

import (
	"context"
	"encoding/json/v2"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/agent"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/astilbahq/astilba/services/platform/internal/upgrade"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// revokedGroup runs protocol peers that answer every coordinated phase through the real management
// paths, so plans are resolved, admitted, applied and finalized as they are in production. A held
// phase is one the host received and will never answer, which is what a lost host looks like.
type revokedGroup struct {
	*aheadNetwork
	cancel context.CancelFunc
	wg     sync.WaitGroup
	mu     sync.Mutex
	held   map[string]bool
	seen   map[string]bool
	// attention names phases a host reports as needing attention, until they're cancelled. It's set
	// before the phase starts and read by the host alone afterwards.
	attention map[string]bool
	// failing names visual actions and cross-host phases ("crosshost-" and the phase) a host reports
	// as failed. Set while the host isn't polling for one.
	failing map[string]bool
	// heldCheckpoint is a parent phase whose recovery metadata the primary won't acknowledge.
	heldCheckpoint string
	// journals holds the real agent journal of a host that takes its commands through one, and
	// refused counts the commands that journal refused.
	journals    map[uuid.UUID]*agent.Journal
	journalDirs map[uuid.UUID]string
	refused     map[uuid.UUID]int
	journalMu   sync.Mutex
}

func newRevokedGroup(t *testing.T, count int) *revokedGroup {
	t.Helper()
	g := &revokedGroup{aheadNetwork: newAheadNetwork(t, count), held: map[string]bool{}, seen: map[string]bool{}, attention: map[string]bool{}, failing: map[string]bool{}, journals: map[uuid.UUID]*agent.Journal{}, journalDirs: map[uuid.UUID]string{}, refused: map[uuid.UUID]int{}}
	g.save(t, g.hosts...)
	g.converge(t)
	yes := true
	for _, h := range g.hosts {
		h.input.Capabilities.Backups, h.input.Capabilities.Upgrades = &yes, &yes
	}
	ctx, cancel := context.WithCancel(g.ctx)
	g.cancel = cancel
	for _, h := range g.hosts {
		g.wg.Go(func() { g.answer(ctx, t, h) })
	}
	g.wg.Go(func() { g.hurryCoordinators(ctx, t) })
	t.Cleanup(func() { cancel(); g.wg.Wait() })
	return g
}

// hurryCoordinators brings snoozed execution jobs forward. A coordinator only looks again 2 seconds
// after each pass, and a deployment takes dozens of passes, which is nearly all of these tests'
// time. Every pass is still the real job. Should River ignore the notification, its 1 second poll
// picks the job up anyway.
func (g *revokedGroup) hurryCoordinators(ctx context.Context, t *testing.T) {
	for ctx.Err() == nil {
		_, err := g.f.st.Pool.Exec(ctx, `WITH due AS (UPDATE river_job SET scheduled_at=now() WHERE kind='platform_execution_v1' AND state='available' AND scheduled_at>now() RETURNING id) SELECT pg_notify(current_schema()||'.river_insert','{"queue":"default"}') FROM (SELECT 1 FROM due LIMIT 1) woken`)
		if err != nil && ctx.Err() == nil {
			t.Error(err)
			return
		}
		select {
		case <-ctx.Done():
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// reports changes what a host tells management about itself, and tells it at once. change
// replaces any pointer it alters, because the host's own heartbeats read them.
func (g *revokedGroup) reports(t *testing.T, h *aheadHost, change func(*api.AgentCapabilities, *api.HostPreflight)) {
	t.Helper()
	g.mu.Lock()
	change(&h.input.Capabilities, &h.input.Preflight)
	heartbeat := api.AgentHeartbeat{Capabilities: h.input.Capabilities, Preflight: h.input.Preflight}
	g.mu.Unlock()
	if _, err := g.f.execution.Heartbeat(g.ctx, h.identity, heartbeat); err != nil {
		t.Fatal(err)
	}
}

// hold makes the host stop answering a phase, and forgets any earlier plan's use of it.
func (g *revokedGroup) hold(h *aheadHost, phase string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.held[phase+"/"+h.id.String()] = true
	delete(g.seen, phase+"/"+h.id.String())
}

func (g *revokedGroup) release(h *aheadHost, phase string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.held, phase+"/"+h.id.String())
}

func (g *revokedGroup) received(h *aheadHost, phase string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.seen[phase+"/"+h.id.String()]
}

// answer is the host. It stops as soon as its credential is refused, as a revoked agent must.
func (g *revokedGroup) answer(ctx context.Context, t *testing.T, h *aheadHost) {
	var beat time.Time
	scopes := map[uuid.UUID]deployment.Plan{}
	for ctx.Err() == nil {
		if time.Since(beat) >= 10*time.Second {
			g.mu.Lock()
			heartbeat := api.AgentHeartbeat{Capabilities: h.input.Capabilities, Preflight: h.input.Preflight}
			g.mu.Unlock()
			if _, err := g.f.execution.Heartbeat(ctx, h.identity, heartbeat); err != nil {
				if !errors.Is(err, domain.Unauthenticated) && ctx.Err() == nil {
					t.Error(err)
				}
				return
			}
			beat = time.Now()
		}
		poll, done := context.WithTimeout(ctx, 200*time.Millisecond)
		c, err := g.f.execution.Poll(poll, h.identity)
		expired := poll.Err()
		done()
		if revokedHost(ctx, g.f, h.identity, err) {
			return
		}
		if err != nil {
			if !waitForFixturePollLease(ctx, err) && expired == nil && ctx.Err() == nil {
				t.Error(err)
				return
			}
			continue
		}
		if c == nil {
			continue
		}
		g.mu.Lock()
		_, journaled := g.journals[h.id]
		g.mu.Unlock()
		if journaled {
			if !g.answerThroughJournal(ctx, t, h, *c, scopes) {
				return
			}
			continue
		}
		report, answered := g.respond(t, h, *c, scopes)
		if !answered {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		if err = g.f.execution.Report(ctx, h.identity, report); err != nil && !errors.Is(err, domain.Unauthenticated) && ctx.Err() == nil {
			t.Error("report", c.Kind, err)
			return
		}
	}
}

// answerThroughJournal is the agent's accept, report and acknowledge for one polled command. It
// holds the journal for all of them, so the test can stop and restart the agent in between.
func (g *revokedGroup) answerThroughJournal(ctx context.Context, t *testing.T, h *aheadHost, c api.ExecutionCommand, scopes map[uuid.UUID]deployment.Plan) bool {
	g.journalMu.Lock()
	defer g.journalMu.Unlock()
	g.mu.Lock()
	journal := g.journals[h.id]
	g.mu.Unlock()
	report, answered := g.respondThroughJournal(t, h, journal, c, scopes)
	if !answered {
		time.Sleep(50 * time.Millisecond)
		return true
	}
	err := g.f.execution.Report(ctx, h.identity, report)
	if err != nil {
		if !errors.Is(err, domain.Unauthenticated) && ctx.Err() == nil {
			t.Error("report", c.Kind, err)
			return false
		}
		return true
	}
	if err = journal.Acknowledge(report.Id, report.Sequence); err != nil {
		t.Error(err)
		return false
	}
	return true
}

// restartJournal stops the host's agent cleanly, runs between on its state directory while it is
// down, and starts it again on the same journal.
func (g *revokedGroup) restartJournal(t *testing.T, h *aheadHost, between func(dir string)) *agent.Journal {
	t.Helper()
	g.journalMu.Lock()
	defer g.journalMu.Unlock()
	g.mu.Lock()
	journal, dir := g.journals[h.id], g.journalDirs[h.id]
	g.mu.Unlock()
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	between(dir)
	restarted, err := agent.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	g.mu.Lock()
	g.journals[h.id] = restarted
	g.mu.Unlock()
	return restarted
}

// journaled makes the host take every command through the real agent's journal, which decides
// what it accepts, as the agent's client does, and holds what it reports.
func (g *revokedGroup) journaled(t *testing.T, h *aheadHost) *agent.Journal {
	t.Helper()
	dir := t.TempDir() + "/state"
	journal, err := agent.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	if err = journal.SetConfig(agent.Config{Origin: g.f.server.URL, ControllerGeneration: h.controller, ServerID: h.id, Credential: strings.SplitN(h.token, ".", 2)[1], Enrolled: true, Role: "deployment"}); err != nil {
		t.Fatal(err)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.journals[h.id], g.journalDirs[h.id] = journal, dir
	return journal
}

// respondThroughJournal answers as the agent does. A command its journal refuses gets no report,
// and the agent polls again. One it accepts but has already ended reports that ending. Anything
// else runs as respond answers it, and the outcome is recorded in the journal first.
func (g *revokedGroup) respondThroughJournal(t *testing.T, h *aheadHost, journal *agent.Journal, c api.ExecutionCommand, scopes map[uuid.UUID]deployment.Plan) (api.AgentReport, bool) {
	// A coordinated links phase is the only network command with a parent that isn't a restriction.
	if c.Kind == "network" && c.ParentId != nil && !fixtureRestriction(t, c) && !g.note(h, "links") {
		return api.AgentReport{}, false
	}
	record, err := journal.Accept(c)
	if err != nil {
		g.mu.Lock()
		g.refused[h.id]++
		g.mu.Unlock()
		return api.AgentReport{}, false
	}
	if !execution.Terminal(record.Report.State) {
		answer, answered := g.respond(t, h, c, scopes)
		if !answered {
			return answer, false
		}
		if err = journal.Mutate(c.Id, func(r *agent.Record) {
			r.Report.State, r.Report.Note, r.Report.Network, r.Report.Crosshost, r.Report.Observation, r.Report.Upgrade = answer.State, answer.Note, answer.Network, answer.Crosshost, answer.Observation, answer.Upgrade
		}); err != nil {
			t.Error(err)
			return answer, false
		}
		if record, err = journal.Record(c.Id); err != nil {
			t.Error(err)
			return answer, false
		}
	}
	return record.Report, true
}

func fixtureRestriction(t *testing.T, c api.ExecutionCommand) bool {
	var command hostnetwork.Command
	if c.Network == nil || hostnetwork.Decode(*c.Network, &command) != nil {
		t.Error("invalid network command")
		return false
	}
	return command.Action == "restrict"
}

// revokedHost says whether management refused a host's poll because it was revoked. A poll checks
// the credential before it reads the host's work, and hydrating a cross-host command checks the
// server again, so a revocation that commits in between is refused as forbidden rather than
// unauthenticated. An agent backs off from either alike.
func revokedHost(ctx context.Context, f *fixture, identity execution.Identity, err error) bool {
	if errors.Is(err, domain.Unauthenticated) {
		return true
	}
	if !errors.Is(err, domain.Forbidden) {
		return false
	}
	_, err = f.execution.Authenticate(ctx, identity.ID, identity.Token)
	return errors.Is(err, domain.Unauthenticated)
}

func (g *revokedGroup) respond(t *testing.T, h *aheadHost, c api.ExecutionCommand, scopes map[uuid.UUID]deployment.Plan) (api.AgentReport, bool) {
	report := api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 2, State: "succeeded", Steps: []api.ExecutionStep{}}
	if c.CancelRequested {
		report.State, report.Sequence = "cancelled", 3
		return report, true
	}
	switch c.Kind {
	case "network":
		receipt, err := fixtureNetworkReceipt(h.store, c, &h.held, scopes, h.controller)
		if err != nil {
			t.Error(err)
			return report, false
		}
		report.Network = claimMessage(t, receipt)
	case "visual":
		var payload deployment.Payload
		if json.Unmarshal(*c.Visual, &payload) != nil {
			t.Error("invalid visual command")
			return report, false
		}
		if !g.note(h, "visual-"+payload.Action) {
			return report, false
		}
		if g.failing["visual-"+payload.Action+"/"+h.id.String()] {
			report.State = "failed"
			return report, true
		}
		observation := deployment.Observation{ObservedAt: time.Now().UTC(), Resources: []deployment.Resource{}, StateHash: strings.Repeat("b", 64)}
		if payload.Action == "resolve" {
			observation.Images = fixtureImages(payload.Plan.Specification.Applications)
		} else {
			observation.Applied = true
			observation.Resources = fixtureResources(payload.Plan)
		}
		report.Observation = claimMessage(t, observation)
	case "crosshost":
		var command deployment.CrossHostCommand
		if hostnetwork.Decode(*c.Crosshost, &command) != nil {
			t.Error("invalid host command")
			return report, false
		}
		scopes[command.ParentID] = command.Unit.Plan
		if g.attention[command.Phase+"/"+h.id.String()] {
			g.note(h, command.Phase)
			report.State = "needs_attention"
			return report, true
		}
		if !g.note(h, command.Phase) || command.Phase == "checkpoint" && g.checkpointHeld(t, command.ParentID) {
			return report, false
		}
		g.mu.Lock()
		failing := g.failing["crosshost-"+command.Phase+"/"+h.id.String()]
		g.mu.Unlock()
		if failing {
			report.State = "failed"
			return report, true
		}
		checkpoint := ""
		if command.Phase == "checkpoint" {
			var descriptor deployment.RecoveryDescriptor
			if c.RecoveryCheckpoint == nil || json.Unmarshal(*c.RecoveryCheckpoint, &descriptor) != nil {
				t.Error("checkpoint without its descriptor")
				return report, false
			}
			checkpoint = descriptor.Digest
		}
		observation := deployment.Observation{ObservedAt: time.Now().UTC(), Resources: []deployment.Resource{}, StateHash: strings.Repeat("a", 64)}
		switch command.Phase {
		case "resolve":
			observation.Images = fixtureImages(command.Unit.Plan.LocalApplications())
		case "complete", "observe":
			observation.Applied = command.Phase == "complete"
			observation.Resources = fixtureResources(command.Unit.Plan)
		}
		report.Crosshost = claimMessage(t, deployment.CrossHostReceipt{ParentID: command.ParentID, GroupHash: command.GroupHash, UnitID: c.ResourceId, Phase: command.Phase, PlanHash: command.Unit.Plan.Hash, Observation: observation, CheckpointHash: checkpoint})
	case "backup", "schedule":
		if !g.note(h, "backup") {
			return report, false
		}
	case "upgrade":
		var command upgrade.Command
		if c.Upgrade == nil || json.Unmarshal(*c.Upgrade, &command) != nil {
			t.Error("invalid upgrade command")
			return report, false
		}
		if !g.note(h, "upgrade-"+command.Action) {
			return report, false
		}
		report.Upgrade = claimMessage(t, upgrade.Status{Phase: "ready", Step: "verified", Serving: "none", Snapshot: strings.Repeat("c", 64), KeepUntil: time.Now().Add(time.Hour), Note: "Fixture database outcome"})
	default:
		t.Error("unexpected host operation", c.Kind)
		return report, false
	}
	return report, true
}

// note records that the host received a phase and says whether it will answer.
func (g *revokedGroup) note(h *aheadHost, phase string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	key := phase + "/" + h.id.String()
	g.seen[key] = true
	return !g.held[key]
}

// checkpointHeld makes the primary stall on recovery metadata for one phase of the parent, which
// holds back every command attached to that metadata.
func (g *revokedGroup) checkpointHeld(t *testing.T, parent uuid.UUID) bool {
	g.mu.Lock()
	held := g.heldCheckpoint
	g.mu.Unlock()
	if held == "" {
		return false
	}
	var phase string
	if err := g.f.st.Pool.QueryRow(g.ctx, `SELECT phase FROM cross_host_operations WHERE id=$1`, parent).Scan(&phase); err != nil {
		t.Error(err)
		return true
	}
	return phase == held
}

func fixtureImages(apps []domain.Application) map[string]string {
	images := map[string]string{}
	for _, app := range apps {
		images[app.ID.String()] = *app.Source.Image
	}
	return images
}

func fixtureResources(plan deployment.Plan) []deployment.Resource {
	resources := []deployment.Resource{}
	for _, app := range plan.LocalApplications() {
		resource := deployment.Resource{ID: app.ID, Kind: "service", State: "running", Image: plan.Images[app.ID.String()]}
		for _, id := range plan.InstanceIDs(app.ID) {
			resource.Instances = append(resource.Instances, deployment.InstanceState{InstanceID: id, State: "running"})
		}
		resources = append(resources, resource)
	}
	return resources
}

// waitFor polls without answering anything itself; the hosts run on their own.
func (g *revokedGroup) waitFor(t *testing.T, what string, done func() bool) {
	t.Helper()
	for until := time.Now().Add(90 * time.Second); !done(); time.Sleep(100 * time.Millisecond) {
		if time.Now().After(until) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func (g *revokedGroup) networkReady(t *testing.T) {
	t.Helper()
	g.waitFor(t, "the network to be ready", func() bool {
		if err := g.f.execution.SweepNetworks(g.ctx); err != nil {
			t.Fatal(err)
		}
		return g.view(t).State == "ready"
	})
}

// reviewed waits for the plan the hosts resolved.
func (g *revokedGroup) reviewed(t *testing.T, e *primaryEnvironment, got reply) deployment.Status {
	t.Helper()
	if got.err != nil || got.status != 201 {
		t.Fatalf("plan = %d %s %v", got.status, got.body, got.err)
	}
	var status deployment.Status
	if err := json.Unmarshal(got.body, &status); err != nil {
		t.Fatal(err)
	}
	g.waitFor(t, "the plan to resolve", func() bool {
		g.b.call("GET", e.path+"/deployment-plans/"+status.ID.String(), nil, 200, &status)
		return status.Plan != nil
	})
	return status
}

func (g *revokedGroup) apply(t *testing.T, e *primaryEnvironment, status deployment.Status) api.Operation {
	t.Helper()
	var op api.Operation
	g.b.call("POST", e.path+"/deployment-plans/"+status.ID.String()+"/apply", api.ApplyVisualPlan{IdempotencyKey: uuid.New(), ApproveHostChanges: approved()}, 201, &op)
	return op
}

func (g *revokedGroup) operationState(t *testing.T, id uuid.UUID) (state, note string) {
	t.Helper()
	if err := g.f.st.Pool.QueryRow(g.ctx, `SELECT state,note FROM execution_operations WHERE id=$1`, id).Scan(&state, &note); err != nil {
		t.Fatal(err)
	}
	return state, note
}

func (g *revokedGroup) finished(t *testing.T, id uuid.UUID) string {
	t.Helper()
	var state, note string
	for until := time.Now().Add(60 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		state, note = g.operationState(t, id)
		if state == "succeeded" || state == "failed" || state == "cancelled" {
			break
		}
		if time.Now().After(until) {
			g.dumpOperation(t, id)
			t.Fatalf("operation still %s: %s", state, note)
		}
	}
	t.Logf("operation %s: %s", state, note)
	return state
}

// revokeBetweenTicks revokes a host and records the phase's command as having succeeded, all between
// two passes of the coordinator, which nothing else can arrange deterministically.
func (g *revokedGroup) revokeBetweenTicks(t *testing.T, parent uuid.UUID, gone *aheadHost, phase, next string) {
	t.Helper()
	// The coordinator's own transaction can take these rows in a different order, so a deadlock is
	// retried rather than treated as a failure.
	for attempt := 0; ; attempt++ {
		err := g.revokeInOneTransaction(parent, gone, phase, next)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "40P01" && attempt < 5 {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		break
	}
	gone.silent, gone.revoked = true, true
}

func (g *revokedGroup) revokeInOneTransaction(parent uuid.UUID, gone *aheadHost, phase, next string) error {
	tx, err := g.f.st.Pool.Begin(g.ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(g.ctx)
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`UPDATE servers SET revoked=true,credential_hash=NULL WHERE id=$1`, []any{gone.id}},
		{`UPDATE network_members SET enabled=false WHERE server_id=$1`, []any{gone.id}},
		{`UPDATE execution_operations SET state='succeeded',report_sequence=2 WHERE parent_id=$1 AND command->'crosshost'->>'phase'=$2`, []any{parent, phase}},
		{`UPDATE cross_host_operations SET phase=$2 WHERE id=$1`, []any{parent, next}},
	} {
		if _, err = tx.Exec(g.ctx, statement.query, statement.args...); err != nil {
			return err
		}
	}
	return tx.Commit(g.ctx)
}

// dumpOperation shows how far a stuck parent got.
func (g *revokedGroup) dumpOperation(t *testing.T, id uuid.UUID) {
	t.Helper()
	var phase string
	if err := g.f.st.Pool.QueryRow(g.ctx, `SELECT phase FROM cross_host_operations WHERE id=$1`, id).Scan(&phase); err != nil {
		t.Log(err)
	}
	t.Logf("phase %s", phase)
	rows, err := g.f.st.Pool.Query(g.ctx, `SELECT o.server_id::text,o.kind,COALESCE(o.command->'crosshost'->>'phase',''),o.state,o.note,COALESCE((SELECT j.errors::text FROM river_job j WHERE j.id=o.job_id),'') FROM execution_operations o WHERE o.parent_id=$1 OR o.id=$1 ORDER BY o.created_at`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var server, kind, step, state, note, errs string
		if err = rows.Scan(&server, &kind, &step, &state, &note, &errs); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s %s %s %s %q %s", server, kind, step, state, note, errs)
	}
}

func (g *revokedGroup) applied(t *testing.T, e *primaryEnvironment, got reply) deployment.Status {
	t.Helper()
	status := g.reviewed(t, e, got)
	op := g.apply(t, e, status)
	if state := g.finished(t, op.Id); state != "succeeded" {
		_, note := g.operationState(t, op.Id)
		t.Fatalf("apply ended %s: %s", state, note)
	}
	g.networkReady(t)
	return status
}

func (g *revokedGroup) reservations(t *testing.T) int {
	t.Helper()
	var count int
	if err := g.f.st.Pool.QueryRow(g.ctx, `SELECT count(*) FROM cross_host_reservations WHERE workspace_id=$1`, g.ws).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func (g *revokedGroup) runtimeUnits(t *testing.T, server uuid.UUID) int {
	t.Helper()
	var count int
	if err := g.f.st.Pool.QueryRow(g.ctx, `SELECT count(*) FROM cross_host_runtime WHERE workspace_id=$1 AND server_id=$2`, g.ws, server).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// deployed applies a specification on the primary through a real plan.
func (g *revokedGroup) deployed(t *testing.T, primary *aheadHost, spec func(path string) domain.Specification) primaryEnvironment {
	t.Helper()
	var project api.Project
	g.b.call("POST", g.base+"/projects", map[string]string{"name": "Stack"}, 201, &project)
	var environment api.Environment
	g.b.call("POST", g.base+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "Stack"}, 201, &environment)
	e := primaryEnvironment{path: g.base + "/projects/" + project.Id.String() + "/environments/" + environment.Id.String()}
	e.spec = spec(e.path)
	g.b.call("PUT", e.path+"/draft", map[string]any{"baseRevisionId": environment.DraftRevisionId, "specification": e.spec}, 200, &e.draft)
	g.applied(t, &e, <-g.b.send("POST", e.path+"/deployment-plans", api.VisualPlanRequest{BaseRevisionId: e.draft.Id, ServerId: primary.id}))
	return e
}

func webApplication(id uuid.UUID) domain.Application {
	image := execution.TestImage
	return domain.Application{ID: id, Name: "Web", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Health: &domain.HealthCheck{Port: 8080, Path: "/"}}
}

// filesStack is a web application next to a file service that stays on the primary, which is what
// makes its backup need every writer stopped.
func (g *revokedGroup) filesStack(t *testing.T, primary *aheadHost) (primaryEnvironment, uuid.UUID) {
	t.Helper()
	web, files, volume := uuid.New(), uuid.New(), uuid.New()
	image := execution.TestImage
	e := g.deployed(t, primary, func(string) domain.Specification {
		return domain.Specification{Version: 1, Variables: []domain.Variable{}, Volumes: []domain.Volume{{ID: volume, Name: "Files"}}, Applications: []domain.Application{webApplication(web), {ID: files, Name: "Files", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Mounts: []domain.VolumeMount{{VolumeID: volume, Target: "/srv/files"}}}}, Routes: []domain.Route{{ID: uuid.New(), TargetID: web, Protocol: "http", Domain: "files.example.test", Port: 8080, TLS: "off", AllowCIDRs: []string{}}}}
	})
	return e, volume
}

func (g *revokedGroup) destination(t *testing.T) uuid.UUID {
	t.Helper()
	var destination backup.Destination
	g.b.call("POST", g.base+"/backup-destinations", map[string]any{"name": "Fixture", "kind": "filesystem", "root": "/srv/fixture-backups", "endpoint": "", "bucket": "", "region": "", "access": map[string]string{"accessKey": "", "secretKey": "", "password": ""}}, 201, &destination)
	return destination.ID
}

func (g *revokedGroup) startBackup(t *testing.T, e *primaryEnvironment, volume uuid.UUID) api.Operation {
	t.Helper()
	password := "fixture-repository-password"
	var policy backup.Policy
	g.b.call("POST", e.path+"/backup-policies", api.BackupPolicyRequest{Name: "Files", ResourceId: volume, DestinationId: g.destination(t), Method: "quiesced-files", Schedule: api.DailySchedule{Hour: 2, Minute: 30, Timezone: "UTC"}, Retain: 7, Enabled: true, Command: []string{}, Password: &password}, 201, &policy)
	var op api.Operation
	g.b.call("POST", e.path+"/backup-policies/"+policy.ID.String()+"/commands", api.BackupCommandRequest{Action: "backup", RequestKey: uuid.New(), Snapshot: ""}, 202, &op)
	return op
}

// parkedUpgrade reaches an upgrade waiting for the owner's choice, with its database on the primary.
func (g *revokedGroup) parkedUpgrade(t *testing.T, gone, b *aheadHost) (primaryEnvironment, upgrade.View, api.Operation) {
	t.Helper()
	web, database, data := uuid.New(), uuid.New(), uuid.New()
	image := domain.Postgres17Image
	e := g.deployed(t, gone, func(path string) domain.Specification {
		var secret api.SecretVersion
		g.b.call("POST", path+"/secrets", map[string]string{"name": "Database", "value": "revoked-host-database-canary"}, 201, &secret)
		return domain.Specification{Version: 1, Variables: []domain.Variable{}, Volumes: []domain.Volume{{ID: data, Name: "Data"}}, Applications: []domain.Application{webApplication(web), {ID: database, Name: "Database", Kind: "postgres", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Postgres: &domain.Postgres{Database: "app", Username: "app", PasswordSecretVersionID: secret.Id}, Mounts: []domain.VolumeMount{{VolumeID: data, Target: domain.PostgresMount(image)}}}}, Bindings: []domain.Binding{{ID: uuid.New(), SourceID: database, TargetID: web, Variable: "DATABASE_URL"}}, Routes: []domain.Route{{ID: uuid.New(), TargetID: web, Protocol: "http", Domain: "upgrade.example.test", Port: 8080, TLS: "off", AllowCIDRs: []string{}}}}
	})
	g.applied(t, &e, g.place(t, &e, gone.id, b.id))
	password := "fixture-repository-password"
	var policy backup.Policy
	g.b.call("POST", e.path+"/backup-policies", api.BackupPolicyRequest{Name: "Recovery", ResourceId: database, DestinationId: g.destination(t), Method: "postgres-logical", Schedule: api.DailySchedule{Timezone: "UTC"}, Retain: 7, Enabled: true, Command: []string{}, Password: &password}, 201, &policy)
	var view upgrade.View
	g.b.call("POST", e.path+"/upgrades", api.UpgradeRequest{PolicyId: policy.ID, ResourceId: database, BaseRevisionId: e.draft.Id, RetainDays: 7}, 201, &view)
	var parent api.Operation
	g.b.call("POST", e.path+"/upgrades/"+view.Plan.ID.String()+"/actions", api.UpgradeActionRequest{Action: "prepare", ExpectedPhase: "planned", RequestKey: uuid.New()}, 202, &parent)
	g.waitFor(t, "the upgrade to wait for the owner", func() bool {
		var parked bool
		if err := g.f.st.Pool.QueryRow(g.ctx, `SELECT h.phase='maintenance-owner' AND j.state='completed' FROM cross_host_operations h JOIN execution_operations o ON o.id=h.id JOIN river_job j ON j.id=o.job_id WHERE h.id=$1`, parent.Id).Scan(&parked); err != nil {
			t.Fatal(err)
		}
		return parked
	})
	return e, view, parent
}

func (g *revokedGroup) wantUpgradeRejected(t *testing.T, view upgrade.View, parent api.Operation, b *aheadHost, outcome string) {
	t.Helper()
	if state := g.finished(t, parent.Id); state != outcome {
		t.Fatalf("an upgrade that lost its primary ended %s", state)
	}
	var phase, note string
	if err := g.f.st.Pool.QueryRow(g.ctx, `SELECT status->>'phase',status->>'note' FROM supported_upgrades WHERE id=$1`, view.Plan.ID).Scan(&phase, &note); err != nil || phase != "rejected" || !strings.Contains(note, "unknown") {
		t.Fatalf("upgrade = %q %q %v", phase, note, err)
	}
	if !g.received(b, "resume-source") || g.reservations(t) != 0 {
		t.Fatal("the consumer was left stopped")
	}
}
