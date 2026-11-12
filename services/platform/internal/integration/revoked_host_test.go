package integration

import (
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
)

// An instance on a revoked host moves to a healthy peer through an ordinary reviewed plan, which
// says what it can't do about the host it leaves behind.
func TestEnvironmentMovesOffARevokedSecondary(t *testing.T) {
	g := newRevokedGroup(t, 3)
	a, b, gone := g.hosts[0], g.hosts[1], g.hosts[2]
	e := g.primaryEnvironment(t, a)
	g.applied(t, &e, g.place(t, &e, a.id, gone.id))
	if g.runtimeUnits(t, gone.id) != 1 {
		t.Fatal("the instance never reached the host about to be revoked")
	}
	g.revoke(t, gone)
	g.networkReady(t)
	moved := g.applied(t, &e, g.place(t, &e, a.id, b.id))
	mentioned := false
	for _, effect := range moved.Plan.Effects {
		mentioned = mentioned || strings.Contains(effect, "revoked")
	}
	if !mentioned {
		t.Fatalf("the review doesn't name what happens to the revoked host: %q", moved.Plan.Effects)
	}
	for _, unit := range moved.Plan.Distributed.Units {
		if unit.Plan.ServerID == gone.id {
			t.Fatal("a revoked host is still a participant")
		}
	}
	if g.runtimeUnits(t, gone.id) != 0 || g.runtimeUnits(t, b.id) != 1 || g.reservations(t) != 0 {
		t.Fatal("the runtime doesn't show the instance on the peer alone")
	}
}

// Consolidating on the primary is what removing placement requires, so it must work from a revoked
// host, and removing placement after it must work too.
func TestEnvironmentConsolidatesOffARevokedSecondary(t *testing.T) {
	g := newRevokedGroup(t, 2)
	a, gone := g.hosts[0], g.hosts[1]
	e := g.primaryEnvironment(t, a)
	g.applied(t, &e, g.place(t, &e, a.id, gone.id))
	g.revoke(t, gone)
	g.networkReady(t)
	g.applied(t, &e, g.place(t, &e, a.id, a.id))
	g.removePlacement(t, &e, a)
}

func (g *revokedGroup) removePlacement(t *testing.T, e *primaryEnvironment, primary *aheadHost) deployment.Status {
	t.Helper()
	g.b.call("PUT", e.path+"/draft", map[string]any{"baseRevisionId": e.draft.Id, "specification": e.spec}, 200, &e.draft)
	return g.applied(t, e, <-g.b.send("POST", e.path+"/deployment-plans", api.VisualPlanRequest{BaseRevisionId: e.draft.Id, ServerId: primary.id}))
}

// A host that will never answer must not hold a deployment open, or the healthy hosts that are
// waiting on it, however far it got.
func TestRevocationReleasesAHeldDeployment(t *testing.T) {
	g := newRevokedGroup(t, 3)
	a, b, gone := g.hosts[0], g.hosts[1], g.hosts[2]
	e := g.primaryEnvironment(t, a)
	g.applied(t, &e, g.place(t, &e, a.id, b.id))
	g.hold(gone, "prepare")
	g.hold(b, "prepare")
	status := g.reviewed(t, &e, g.place(t, &e, a.id, gone.id))
	op := g.apply(t, &e, status)
	g.waitFor(t, "the hosts to receive prepare", func() bool { return g.received(gone, "prepare") && g.received(b, "prepare") })
	g.revoke(t, gone)
	// Cancelling a healthy host's preparation parks it for a local retry, so it's left to finish.
	time.Sleep(time.Second)
	g.release(b, "prepare")
	if state := g.finished(t, op.Id); state != "failed" {
		t.Fatalf("a deployment that lost a host before publication ended %s", state)
	}
	var interrupted bool
	if err := g.f.st.Pool.QueryRow(g.ctx, `SELECT cancel_requested FROM execution_operations WHERE parent_id=$1 AND server_id=$2 AND command->'crosshost'->>'phase'='prepare'`, op.Id, b.id).Scan(&interrupted); err != nil || interrupted {
		t.Fatalf("a healthy host's preparation was interrupted: %v %v", interrupted, err)
	}
	if !g.received(a, "abort") || !g.received(b, "abort") {
		t.Fatal("the healthy hosts were not told to clean up, so they keep their barriers")
	}
	if g.received(gone, "abort") {
		t.Fatal("the revoked host was sent more work")
	}
	if g.reservations(t) != 0 {
		t.Fatal("reservations outlived the deployment")
	}
}

// A host that was revoked after the group was published has nothing more to contribute. The others
// finish, and the plan still names that host until the owner plans it away.
func TestDeploymentFinishesWithoutAHostRevokedAfterPublication(t *testing.T) {
	g := newRevokedGroup(t, 3)
	a, b, gone := g.hosts[0], g.hosts[1], g.hosts[2]
	e := g.primaryEnvironment(t, a)
	g.applied(t, &e, g.place(t, &e, a.id, b.id))
	g.hold(gone, "retire")
	g.hold(b, "retire")
	status := g.reviewed(t, &e, g.place(t, &e, a.id, gone.id))
	op := g.apply(t, &e, status)
	g.waitFor(t, "the hosts to receive retire", func() bool { return g.received(gone, "retire") && g.received(b, "retire") })
	g.revoke(t, gone)
	// The coordinator decides what to skip from the revocation, so it has to hold until it finishes.
	g.b.call("POST", g.base+"/servers/"+gone.id.String()+"/enrollment", nil, 409, nil)
	g.release(b, "retire")
	if state := g.finished(t, op.Id); state != "succeeded" {
		t.Fatalf("a published deployment that lost a host ended %s", state)
	}
	if _, note := g.operationState(t, op.Id); !strings.Contains(note, "revoked") {
		t.Fatalf("the note hides the revoked host: %q", note)
	}
	if !g.received(b, "complete") || g.reservations(t) != 0 {
		t.Fatal("the remaining hosts were not released")
	}
	var recorded int
	if err := g.f.st.Pool.QueryRow(g.ctx, `SELECT count(*) FROM cross_host_runtime r JOIN visual_runtime v ON v.plan_id=r.plan_id WHERE r.server_id=$1`, gone.id).Scan(&recorded); err != nil || recorded != 1 {
		t.Fatalf("recovery needs a runtime row for every active unit of the applied plan: %d %v", recorded, err)
	}
	g.networkReady(t)
	moved := g.applied(t, &e, g.place(t, &e, a.id, b.id))
	for _, unit := range moved.Plan.Distributed.Units {
		if unit.Plan.ServerID == gone.id {
			t.Fatal("the owner could not plan the revoked host away")
		}
	}
}

// Losing the primary ends a deployment outright, and the hosts it was going to serve still get told.
func TestRevokingAPrimaryReleasesItsSecondaries(t *testing.T) {
	g := newRevokedGroup(t, 2)
	gone, b := g.hosts[0], g.hosts[1]
	e := g.primaryEnvironment(t, gone)
	g.applied(t, &e, g.place(t, &e, gone.id, gone.id))
	g.hold(gone, "prepare")
	status := g.reviewed(t, &e, g.place(t, &e, gone.id, b.id))
	op := g.apply(t, &e, status)
	g.waitFor(t, "the primary to receive prepare", func() bool { return g.received(gone, "prepare") })
	g.revoke(t, gone)
	if state := g.finished(t, op.Id); state != "failed" {
		t.Fatalf("a deployment that lost its primary ended %s", state)
	}
	if !g.received(b, "abort") || g.reservations(t) != 0 {
		t.Fatal("the secondary was not told to clean up")
	}
}

// A command held by a host that's revoked ends there, and nothing can reopen it.
func TestRevocationEndsStandaloneWorkForGood(t *testing.T) {
	g := newRevokedGroup(t, 2)
	gone := g.hosts[1]
	e := g.primaryEnvironment(t, gone)
	image := *e.spec.Applications[0].Source.Image
	e.spec.Variables = append(e.spec.Variables, domain.Variable{Name: "CHANGED", Value: &image})
	g.b.call("PUT", e.path+"/draft", map[string]any{"baseRevisionId": e.draft.Id, "specification": e.spec}, 200, &e.draft)
	g.hold(gone, "visual-deploy")
	status := g.reviewed(t, &e, <-g.b.send("POST", e.path+"/deployment-plans", api.VisualPlanRequest{BaseRevisionId: e.draft.Id, ServerId: gone.id}))
	op := g.apply(t, &e, status)
	g.waitFor(t, "the host to receive the deployment", func() bool { return g.received(gone, "visual-deploy") })
	g.revoke(t, gone)
	state, note := g.operationState(t, op.Id)
	if state != "cancelled" || !strings.Contains(note, "unknown") {
		t.Fatalf("held work after revocation = %s %q", state, note)
	}
	var generation int64
	var hash string
	if err := g.f.st.Pool.QueryRow(g.ctx, `SELECT generation,command->>'planHash' FROM execution_operations WHERE id=$1`, op.Id).Scan(&generation, &hash); err != nil {
		t.Fatal(err)
	}
	late := api.AgentReport{Id: op.Id, Generation: generation, PlanHash: hash, Sequence: 99, State: "succeeded", Steps: []api.ExecutionStep{}}
	if err := g.f.execution.Report(g.ctx, gone.identity, late); !errors.Is(err, domain.Unauthenticated) {
		t.Fatalf("a revoked host's late report = %v", err)
	}
	// Renewal gives the same machine a credential again, and the outcome still stays closed.
	var renewed api.TransferTicket
	g.b.call("POST", g.base+"/servers/"+gone.id.String()+"/enrollment", nil, 201, &renewed)
	credential := sha256.Sum256([]byte(gone.identity.Token))
	if _, err := g.f.st.Pool.Exec(g.ctx, `UPDATE servers SET credential_hash=$2,enrollment_hash=NULL,revoked=false WHERE id=$1`, gone.id, credential[:]); err != nil {
		t.Fatal(err)
	}
	// It has to be answered, or the host never acknowledges its journal and takes no more work.
	if err := g.f.execution.Report(g.ctx, gone.identity, late); err != nil {
		t.Fatalf("a renewed host's late report = %v", err)
	}
	if state, _ = g.operationState(t, op.Id); state != "cancelled" {
		t.Fatalf("a late report reopened settled work: %s", state)
	}
}

// A backup can't claim consistency with a writer nobody can confirm has stopped, so it doesn't
// start, and the writers on the other hosts run again.
func TestRevokedConsumerStopsABackupBeforeItStarts(t *testing.T) {
	g := newRevokedGroup(t, 2)
	a, gone := g.hosts[0], g.hosts[1]
	e, volume := g.filesStack(t, a)
	g.applied(t, &e, g.place(t, &e, a.id, gone.id))
	g.hold(gone, "pause")
	op := g.startBackup(t, &e, volume)
	g.waitFor(t, "the consumer to receive its pause", func() bool { return g.received(gone, "pause") })
	g.revoke(t, gone)
	if state := g.finished(t, op.Id); state != "failed" {
		t.Fatalf("a backup that lost a writer ended %s", state)
	}
	if g.received(a, "backup") {
		t.Fatal("the backup ran without confirming every writer")
	}
	if !g.received(a, "resume-source") || !g.received(a, "complete") || g.reservations(t) != 0 {
		t.Fatal("the primary's writers were left stopped")
	}
}

// Consumers normally wait for their provider to recover first. A revoked one never will.
func TestRevokedPrimaryResumesItsConsumers(t *testing.T) {
	g := newRevokedGroup(t, 2)
	gone, b := g.hosts[0], g.hosts[1]
	e, volume := g.filesStack(t, gone)
	g.applied(t, &e, g.place(t, &e, gone.id, b.id))
	g.hold(gone, "pause")
	op := g.startBackup(t, &e, volume)
	g.waitFor(t, "every host to receive its pause", func() bool { return g.received(gone, "pause") && g.received(b, "pause") })
	g.revoke(t, gone)
	if state := g.finished(t, op.Id); state != "failed" {
		t.Fatalf("a backup that lost its primary ended %s", state)
	}
	if !g.received(b, "resume-source") || g.reservations(t) != 0 {
		t.Fatal("the consumer's writers were left stopped behind a revoked provider")
	}
}

// An upgrade waiting for the owner has no running job, so revocation has to wake it. There is no
// choice left to make once the database's host is gone.
func TestRevokedPrimaryEndsAnUpgradeWaitingForTheOwner(t *testing.T) {
	g := newRevokedGroup(t, 2)
	gone, b := g.hosts[0], g.hosts[1]
	_, view, parent := g.parkedUpgrade(t, gone, b)
	g.revoke(t, gone)
	g.wantUpgradeRejected(t, view, parent, b, "failed")
}

// Cancelling while the primary doesn't answer leaves the parent waiting on that host, and revoking
// it must still release the others rather than asking the owner about a database that's gone.
func TestRevokedPrimaryEndsACancelledUpgrade(t *testing.T) {
	g := newRevokedGroup(t, 2)
	gone, b := g.hosts[0], g.hosts[1]
	e, view, parent := g.parkedUpgrade(t, gone, b)
	g.hold(gone, "effect")
	g.b.call("POST", e.path+"/upgrades/"+view.Plan.ID.String()+"/actions", api.UpgradeActionRequest{Action: "cutover", ExpectedPhase: "ready", RequestKey: uuid.New()}, 202, nil)
	g.waitFor(t, "the primary to receive the effect", func() bool { return g.received(gone, "effect") })
	g.b.call("POST", g.base+"/operations/"+parent.Id.String()+"/cancel", nil, 204, nil)
	g.revoke(t, gone)
	g.wantUpgradeRejected(t, view, parent, b, "cancelled")
}

// A release command runs once and can't be taken back, so it must not start in a deployment that
// losing a host has already ended.
func TestRevocationStopsAReleaseCommandThatHasNotStarted(t *testing.T) {
	g := newRevokedGroup(t, 2)
	gone, b := g.hosts[0], g.hosts[1]
	e := g.primaryEnvironment(t, gone)
	g.applied(t, &e, g.place(t, &e, gone.id, gone.id))
	// Saves the placement. The plan that counts is the one below, which carries the command.
	g.reviewed(t, &e, g.place(t, &e, gone.id, b.id))
	command := []string{"migrate"}
	var status deployment.Status
	g.b.call("POST", e.path+"/deployment-plans", api.VisualPlanRequest{BaseRevisionId: e.draft.Id, ServerId: gone.id, ReleaseOptions: &api.ReleaseOptions{Command: command, CommandTimeoutSeconds: 60, DrainSeconds: 30}}, 201, &status)
	g.waitFor(t, "the plan to resolve", func() bool {
		g.b.call("GET", e.path+"/deployment-plans/"+status.ID.String(), nil, 200, &status)
		return status.Plan != nil
	})
	g.mu.Lock()
	g.heldCheckpoint = "hook"
	g.mu.Unlock()
	op := g.apply(t, &e, status)
	g.waitFor(t, "the release command to wait on recovery metadata", func() bool {
		var issued bool
		if err := g.f.st.Pool.QueryRow(g.ctx, `SELECT EXISTS(SELECT 1 FROM execution_operations WHERE parent_id=$1 AND command->'crosshost'->>'phase'='hook')`, op.Id).Scan(&issued); err != nil {
			t.Fatal(err)
		}
		return issued
	})
	g.revoke(t, gone)
	if state := g.finished(t, op.Id); state != "failed" {
		t.Fatalf("a deployment that lost its primary ended %s", state)
	}
	if g.received(b, "hook") {
		t.Fatal("the release command ran in a deployment that had already ended")
	}
	if !g.received(b, "abort") || g.reservations(t) != 0 {
		t.Fatal("the secondary was not told to clean up")
	}
}

// Refreshing an environment still works while its plan names a revoked host, and the host's
// instances read as unknown rather than disappearing.
func TestRefreshShowsARevokedHostAsUnknown(t *testing.T) {
	g := newRevokedGroup(t, 2)
	a, gone := g.hosts[0], g.hosts[1]
	e := g.primaryEnvironment(t, a)
	e.spec.Applications[0].Replicas = 2
	g.applied(t, &e, g.place(t, &e, a.id, a.id, gone.id))
	g.hold(gone, "observe")
	g.revoke(t, gone)
	// Nothing ends a network command already on its way to the host, and it mustn't stop a refresh.
	open := api.ExecutionCommand{Id: uuid.New(), ServerId: gone.id, EnvironmentId: uuid.Nil, ResourceId: uuid.New(), Generation: 900001, Kind: "network", Action: "inspect", PlanHash: strings.Repeat("b", 64), Deadline: time.Now().Add(time.Hour)}
	if _, err := g.f.st.Pool.Exec(g.ctx, `INSERT INTO execution_operations(id,workspace_id,server_id,generation,request_key,command,kind,state,report_sequence) VALUES($1,$2,$3,$4,$5,$6,'network','dispatched',0)`, open.Id, g.ws, gone.id, open.Generation, uuid.New(), claimBytes(t, open)); err != nil {
		t.Fatal(err)
	}
	var op api.Operation
	g.b.call("POST", e.path+"/runtime/refresh", nil, 201, &op)
	if state := g.finished(t, op.Id); state != "succeeded" {
		t.Fatalf("a refresh ended %s", state)
	}
	var runtime deployment.Observation
	g.b.call("GET", e.path+"/runtime", nil, 200, &runtime)
	known := false
	for _, host := range runtime.Hosts {
		known = known || host.ServerID == gone.id && host.State == "unknown"
	}
	if !known || len(runtime.Resources) != 1 || len(runtime.Resources[0].Instances) != 2 || runtime.Resources[0].State == "healthy" || runtime.Resources[0].State == "running" {
		t.Fatalf("the revoked host's instances don't read as unknown: %+v", runtime)
	}
}

// A backup that finished is a backup, whatever happens to a consumer on the way back up.
func TestBackupSurvivesLosingAConsumerAfterIt(t *testing.T) {
	g := newRevokedGroup(t, 2)
	a, gone := g.hosts[0], g.hosts[1]
	e, volume := g.filesStack(t, a)
	g.applied(t, &e, g.place(t, &e, a.id, gone.id))
	g.hold(gone, "resume-source")
	op := g.startBackup(t, &e, volume)
	g.waitFor(t, "the consumer to receive its resumption", func() bool { return g.received(gone, "resume-source") })
	g.revoke(t, gone)
	if state := g.finished(t, op.Id); state != "succeeded" {
		t.Fatalf("a backup that finished ended %s", state)
	}
	if !g.received(a, "backup") || g.reservations(t) != 0 {
		t.Fatal("the primary was not released")
	}
}

// Stopping before any host was reserved closes the checkpoint the way cancelling there does, so the
// primary doesn't keep a capsule for a root that's over.
func TestRevocationDuringCheckpointClosesIt(t *testing.T) {
	g := newRevokedGroup(t, 3)
	a, b, gone := g.hosts[0], g.hosts[1], g.hosts[2]
	e := g.primaryEnvironment(t, a)
	g.applied(t, &e, g.place(t, &e, a.id, b.id))
	status := g.reviewed(t, &e, g.place(t, &e, a.id, gone.id))
	g.hold(a, "checkpoint")
	g.hold(b, "reserve")
	op := g.apply(t, &e, status)
	g.waitFor(t, "the primary to receive the checkpoint", func() bool { return g.received(a, "checkpoint") })
	g.revoke(t, gone)
	g.release(a, "checkpoint")
	if state := g.finished(t, op.Id); state != "failed" {
		t.Fatalf("a deployment stopped at its checkpoint ended %s", state)
	}
	if _, note := g.operationState(t, op.Id); strings.Contains(note, "candidates") {
		t.Fatalf("no host had candidates to remove: %q", note)
	}
	g.waitFor(t, "the primary to close the checkpoint", func() bool { return g.received(a, "checkpoint-closed") })
	if g.received(b, "reserve") {
		t.Fatal("a host was reserved after the deployment stopped")
	}
}

// A plan that left a server behind relied on its revocation, so it stops once that's lifted, even
// after the network has settled again.
func TestPlanLeavingAServerBehindNeedsItStillRevoked(t *testing.T) {
	g := newRevokedGroup(t, 3)
	a, b, gone := g.hosts[0], g.hosts[1], g.hosts[2]
	e := g.primaryEnvironment(t, a)
	g.applied(t, &e, g.place(t, &e, a.id, gone.id))
	g.revoke(t, gone)
	g.networkReady(t)
	status := g.reviewed(t, &e, g.place(t, &e, a.id, b.id))
	if len(status.Plan.Distributed.Abandoned) != 1 || status.Plan.Distributed.Abandoned[0] != gone.id {
		t.Fatalf("the plan doesn't record what it left behind: %v", status.Plan.Distributed.Abandoned)
	}
	g.b.call("POST", g.base+"/servers/"+gone.id.String()+"/enrollment", nil, 201, nil)
	if _, err := g.f.st.Pool.Exec(g.ctx, `UPDATE network_members SET acknowledged_generation=generation,approved_controller=(SELECT controller_generation FROM servers WHERE id=$1) WHERE server_id=$1`, gone.id); err != nil {
		t.Fatal(err)
	}
	g.networkReady(t)
	g.b.call("POST", e.path+"/deployment-plans/"+status.ID.String()+"/apply", api.ApplyVisualPlan{IdempotencyKey: uuid.New(), ApproveHostChanges: approved()}, 409, nil)
}

// A host revoked with nothing left to run still loses its runtime record when the plan finishes.
func TestRevokedHostWithNothingLeftLosesItsRuntimeRecord(t *testing.T) {
	g := newRevokedGroup(t, 3)
	a, b, gone := g.hosts[0], g.hosts[1], g.hosts[2]
	e := g.primaryEnvironment(t, a)
	g.applied(t, &e, g.place(t, &e, a.id, gone.id))
	g.hold(gone, "retire")
	g.hold(b, "retire")
	status := g.reviewed(t, &e, g.place(t, &e, a.id, b.id))
	op := g.apply(t, &e, status)
	g.waitFor(t, "the hosts to receive retire", func() bool { return g.received(gone, "retire") && g.received(b, "retire") })
	g.revoke(t, gone)
	g.release(b, "retire")
	if state := g.finished(t, op.Id); state != "succeeded" {
		t.Fatalf("a published move ended %s", state)
	}
	if g.runtimeUnits(t, gone.id) != 0 {
		t.Fatal("a host with nothing left to run keeps its capacity, metrics and log authority")
	}
}

// A secondary held in preparation, here because the service it binds to was on the revoked server,
// is told to stop instead of holding every other host until someone reaches it. What the agent does
// with that is covered by TestCancellationEndsAHeldPreparation.
func TestRevocationReleasesAHostHeldInPreparation(t *testing.T) {
	g := newRevokedGroup(t, 3)
	a, b, gone := g.hosts[0], g.hosts[1], g.hosts[2]
	e := g.primaryEnvironment(t, a)
	g.applied(t, &e, g.place(t, &e, a.id, b.id))
	g.attention["prepare/"+b.id.String()] = true
	g.hold(gone, "prepare")
	status := g.reviewed(t, &e, g.place(t, &e, a.id, gone.id))
	op := g.apply(t, &e, status)
	g.waitFor(t, "the host to be held in preparation", func() bool { return g.received(b, "prepare") && g.received(gone, "prepare") })
	g.revoke(t, gone)
	if state := g.finished(t, op.Id); state != "failed" {
		t.Fatalf("a deployment that lost a host ended %s", state)
	}
	if !g.received(b, "abort") || g.reservations(t) != 0 {
		t.Fatal("the held host was not released")
	}
}

// A publication the primary already reported is one the routes point at, so the deployment is
// finished rather than unwound, however soon after it the primary is revoked.
func TestPublishedDeploymentSurvivesLosingItsPrimaryBeforeTheNextPass(t *testing.T) {
	g := newRevokedGroup(t, 2)
	gone, b := g.hosts[0], g.hosts[1]
	e := g.primaryEnvironment(t, gone)
	g.applied(t, &e, g.place(t, &e, gone.id, gone.id))
	g.hold(gone, "publish")
	status := g.reviewed(t, &e, g.place(t, &e, gone.id, b.id))
	op := g.apply(t, &e, status)
	g.waitFor(t, "the primary to receive the publication", func() bool { return g.received(gone, "publish") })
	g.revokeBetweenTicks(t, op.Id, gone, "publish", "publish")
	if state := g.finished(t, op.Id); state != "succeeded" {
		t.Fatalf("a published deployment that lost its primary ended %s", state)
	}
	if g.received(b, "abort") || !g.received(b, "complete") {
		t.Fatal("the secondary's published candidates were removed")
	}
}

// A data effect is only sent once its writers are confirmed stopped, so losing one just before
// sending it stops the effect however the passes fall.
func TestRevokedConsumerStopsABackupNotYetSent(t *testing.T) {
	g := newRevokedGroup(t, 2)
	a, gone := g.hosts[0], g.hosts[1]
	e, volume := g.filesStack(t, a)
	g.applied(t, &e, g.place(t, &e, a.id, gone.id))
	g.hold(a, "effect")
	op := g.startBackup(t, &e, volume)
	g.waitFor(t, "the primary to receive the effect", func() bool { return g.received(a, "effect") })
	g.revokeBetweenTicks(t, op.Id, gone, "effect", "maintenance-run:backup")
	if state := g.finished(t, op.Id); state != "failed" {
		t.Fatalf("a backup that lost a writer ended %s", state)
	}
	if g.received(a, "backup") {
		t.Fatal("the backup ran with a writer nobody can confirm stopped")
	}
	if !g.received(a, "resume-source") || g.reservations(t) != 0 {
		t.Fatal("the primary's writers were left stopped")
	}
}
