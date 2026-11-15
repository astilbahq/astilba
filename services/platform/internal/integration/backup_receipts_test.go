package integration

import (
	"context"
	"encoding/json/jsontext"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

// reportingPolicy is a PostgreSQL backup policy on a deployed environment, with the agent of the
// server it's bound to.
type reportingPolicy struct {
	f        *fixture
	ws       uuid.UUID
	path     string
	project  uuid.UUID
	policy   backup.Policy
	server   uuid.UUID
	token    string
	sequence int64
}

func newReportingPolicy(t *testing.T, f *fixture, b *browser, ws uuid.UUID, name string) *reportingPolicy {
	t.Helper()
	prefix := "/api/v1/workspaces/" + ws.String()
	var project api.Project
	b.call("POST", prefix+"/projects", map[string]string{"name": name}, 201, &project)
	var env api.Environment
	b.call("POST", prefix+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "Database"}, 201, &env)
	path := prefix + "/projects/" + project.Id.String() + "/environments/" + env.Id.String()
	var secret api.SecretVersion
	b.call("POST", path+"/secrets", map[string]string{"name": "Database password", "value": "database-password"}, 201, &secret)
	pg, vol := uuid.New(), uuid.New()
	image := domain.PostgresImage
	spec := domain.Specification{Version: 1, Variables: []domain.Variable{}, Applications: []domain.Application{{ID: pg, Name: "Database", Kind: "postgres", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Postgres: &domain.Postgres{Username: "app", Database: "app", PasswordSecretVersionID: secret.Id}, Mounts: []domain.VolumeMount{{VolumeID: vol, Target: "/var/lib/postgresql"}}}}, Volumes: []domain.Volume{{ID: vol, Name: "Data"}}}
	var draft api.Revision
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": env.DraftRevisionId, "specification": spec}, 200, &draft)
	enrolled, in, token := enrollment(t, f, b, ws, "deployment")
	yes := true
	in.Capabilities.Visual, in.Capabilities.Backups = &yes, &yes
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
	poll := func(id uuid.UUID) api.ExecutionCommand {
		t.Helper()
		var p api.AgentPoll
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/poll", token, nil, 200, &p)
		if p.Command == nil || p.Command.Id != id {
			t.Fatal("command not dispatched")
		}
		return *p.Command
	}
	report := func(c api.ExecutionCommand, obs deployment.Observation) {
		t.Helper()
		raw, _ := stablejson.Marshal(obs)
		r := api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 1, State: "succeeded", Steps: []api.ExecutionStep{}, Observation: (*jsontext.Value)(&raw)}
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/report", token, r, 204, nil)
	}
	var plan deployment.Status
	b.call("POST", path+"/deployment-plans", api.VisualPlanRequest{ServerId: enrolled.Server.Id, BaseRevisionId: draft.Id}, 201, &plan)
	report(poll(plan.OperationID), deployment.Observation{ObservedAt: time.Now(), Resources: []deployment.Resource{}, Images: map[string]string{pg.String(): image}, StateHash: strings.Repeat("a", 64)})
	var op api.Operation
	b.call("POST", path+"/deployment-plans/"+plan.ID.String()+"/apply", api.ApplyVisualPlan{IdempotencyKey: uuid.New(), ApproveHostChanges: approved()}, 201, &op)
	report(poll(op.Id), deployment.Observation{ObservedAt: time.Now(), Applied: true, Resources: []deployment.Resource{{ID: pg, Kind: "service", State: "healthy", Image: image}}, StateHash: strings.Repeat("b", 64)})
	var dest backup.Destination
	b.call("POST", prefix+"/backup-destinations", map[string]any{"name": name + " bucket", "kind": "s3", "root": "", "endpoint": "https://storage.example.test", "bucket": "receipts-fixture", "region": "test-1", "access": map[string]string{"accessKey": "storage-key", "secretKey": "storage-secret", "password": ""}}, 201, &dest)
	password := "repository-password-fixture"
	var policy backup.Policy
	b.call("POST", path+"/backup-policies", api.BackupPolicyRequest{Name: "Daily backup", ResourceId: pg, DestinationId: dest.ID, Method: "postgres-logical", Schedule: api.DailySchedule{Hour: 2, Minute: 30, Timezone: "UTC"}, Retain: 7, Enabled: true, Command: []string{}, Password: &password}, 201, &policy)
	return &reportingPolicy{f: f, ws: ws, path: path + "/backup-policies/" + policy.ID.String(), project: project.Id, policy: policy, server: enrolled.Server.Id, token: token}
}

// upload is a run that uploaded snapshot, as its server reports it, at a time the server claims.
func (p *reportingPolicy) upload(snapshot string, claimed time.Time) backup.Run {
	m := backup.Manifest{Roles: []backup.Role{}, Extensions: []string{}, Recipe: 1, PolicyID: p.policy.ID, EnvironmentID: p.policy.EnvironmentID, ResourceID: p.policy.ResourceID, Method: "postgres-logical", Image: domain.PostgresImage, CreatedAt: claimed}
	return backup.Run{ID: uuid.New(), PolicyID: p.policy.ID, Generation: 1, ScheduledAt: claimed, StartedAt: claimed, FinishedAt: &claimed, State: "succeeded", Snapshot: snapshot, Manifest: &m, ManifestHash: backup.Hash(m)}
}

// report sends the policy's next schedule report from its server.
func (p *reportingPolicy) report(t *testing.T, want int, runs ...backup.Run) {
	t.Helper()
	p.sequence++
	p.reportAt(t, p.sequence, want, runs...)
}

func (p *reportingPolicy) reportAt(t *testing.T, sequence int64, want int, runs ...backup.Run) {
	t.Helper()
	status := backup.Status{Enabled: true, PolicyID: p.policy.ID, Generation: 1, Next: time.Now().Add(time.Hour), Runs: append([]backup.Run{}, runs...)}
	raw, _ := stablejson.Marshal(status)
	agentRequest(t, p.f.server.Client(), p.f.server.URL, "/agent/v1/schedule-reports", p.token, map[string]any{"sequence": sequence, "status": jsontext.Value(raw)}, want, nil)
}

// receipts reads the policy's receipts by snapshot, through the owner's API.
func (p *reportingPolicy) receipts(t *testing.T, b *browser) map[string]api.BackupSnapshotReceipt {
	t.Helper()
	var listed []api.BackupSnapshotReceipt
	b.call("GET", p.path+"/receipts", nil, 200, &listed)
	out := map[string]api.BackupSnapshotReceipt{}
	for _, r := range listed {
		out[r.Snapshot] = r
	}
	return out
}

func snapshotID(c string) string { return strings.Repeat(c, 64) }

// A server's report can backdate its snapshots as far as it likes. Management records when it first
// received each one by its own clock, keeps that time while reports go on listing the snapshot, and
// records nothing for a restore, a failure or a pruned snapshot. A snapshot taken out of a report
// loses its receipt, and one reported again later gets a later time, never an earlier one.
func TestManagementRecordsWhenItFirstReceivedEachSnapshot(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	p := newReportingPolicy(t, f, b, ws, "Receipts")
	if got := p.receipts(t, b); len(got) != 0 {
		t.Fatal("a policy with no reports has receipts", got)
	}

	claimed := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	first, second, pruned, failed := p.upload(snapshotID("a"), claimed), p.upload(snapshotID("b"), claimed), p.upload(snapshotID("c"), claimed), p.upload(snapshotID("d"), claimed)
	pruned.Pruned = true
	failed.State = "failed"
	restore := p.upload(snapshotID("e"), claimed)
	restore.TargetID, restore.Verified = uuid.New(), true
	before := time.Now().UTC()
	p.report(t, 204, first, second, pruned, failed, restore)
	after := time.Now().UTC()
	got := p.receipts(t, b)
	if len(got) != 2 {
		t.Fatalf("receipts for %d snapshots, want the 2 available uploads: %v", len(got), got)
	}
	received := got[first.Snapshot].FirstReportedAt
	for _, snapshot := range []string{first.Snapshot, second.Snapshot} {
		r := got[snapshot]
		if r.ServerId != p.server || r.FirstReportedAt.Before(before.Add(-time.Second)) || r.FirstReportedAt.After(after.Add(time.Second)) {
			t.Fatalf("the receipt for %s doesn't record its reporting server at management's time: %+v, reported between %s and %s", snapshot, r, before, after)
		}
	}

	// Reporting it again, or replaying an older report, keeps its time.
	third := p.upload(snapshotID("f"), claimed)
	p.report(t, 204, first, second, third)
	p.reportAt(t, 1, 204, first)
	got = p.receipts(t, b)
	if len(got) != 3 || !got[first.Snapshot].FirstReportedAt.Equal(received) {
		t.Fatalf("a later report moved or lost a receipt: %v", got)
	}

	// A snapshot pruned since, or no longer listed, loses its receipt. Listed again, it is newer.
	second.Pruned = true
	p.report(t, 204, first, second)
	got = p.receipts(t, b)
	if _, kept := got[second.Snapshot]; kept || len(got) != 1 {
		t.Fatalf("receipts outlived their snapshots: %v", got)
	}
	time.Sleep(10 * time.Millisecond)
	p.report(t, 204, first, third)
	if again := p.receipts(t, b)[third.Snapshot].FirstReportedAt; !again.After(received) {
		t.Fatal("a snapshot listed again kept a time from before it was taken out", again, received)
	}

	// Only recovery moves a receipt, and only to an earlier time.
	ctx := context.Background()
	if _, err := f.st.Pool.Exec(ctx, `UPDATE backup_snapshot_receipts SET first_reported_at=first_reported_at+interval '1 day' WHERE policy_id=$1`, p.policy.ID); err == nil {
		t.Fatal("a receipt moved to a later time")
	}
	if _, err := f.st.Pool.Exec(ctx, `UPDATE backup_snapshot_receipts SET server_id=$2 WHERE policy_id=$1`, p.policy.ID, uuid.New()); err == nil {
		t.Fatal("a receipt changed its server without an earlier time")
	}
	if _, err := f.st.Pool.Exec(ctx, `UPDATE backup_snapshot_receipts SET first_reported_at=first_reported_at-interval '1 day' WHERE policy_id=$1`, p.policy.ID); err != nil {
		t.Fatal("a receipt couldn't move to an earlier time", err)
	}

	// Anyone in the workspace reads them, only through the policy's own environment.
	b.call("GET", strings.Replace(p.path, p.project.String(), uuid.NewString(), 1)+"/receipts", nil, 404, nil)
	b.call("POST", "/api/v1/workspaces/"+ws.String()+"/invitations", map[string]string{"email": "receipts-viewer@example.test", "role": "viewer"}, 201, nil)
	invite := tokenFromLinkMail(t, f, "/invitation")
	viewer := f.browser(t)
	var session api.Session
	viewer.call("POST", "/api/v1/auth/invitations/accept", map[string]string{"token": invite, "name": "Viewer", "password": f.password}, 200, &session)
	viewer.csrf = session.CsrfToken
	if listed := p.receipts(t, viewer); len(listed) != 2 {
		t.Fatal("a viewer couldn't read the receipts", listed)
	}
}

// Revoking a server records when the revocation began, which renewing it clears. A revoked server's
// reports are refused, so it can't add or remove a receipt afterwards.
func TestRevokedServerRecordsWhenItsRevocationBegan(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	p := newReportingPolicy(t, f, b, ws, "Revoked")
	kept := p.upload(snapshotID("a"), time.Now())
	p.report(t, 204, kept)
	revokedAt := func() *time.Time {
		t.Helper()
		var servers []api.Server
		b.call("GET", "/api/v1/workspaces/"+ws.String()+"/servers", nil, 200, &servers)
		for _, s := range servers {
			if s.Id == p.server {
				return s.RevokedAt
			}
		}
		t.Fatal("server not listed")
		return nil
	}
	if revokedAt() != nil {
		t.Fatal("a server that was never revoked shows a revocation time")
	}
	before := time.Now().UTC()
	revokeServer(b, ws, p.server)
	at := revokedAt()
	if at == nil || at.Before(before.Add(-time.Second)) || at.After(time.Now().Add(time.Second)) {
		t.Fatal("revoking the server didn't record when", at)
	}
	time.Sleep(10 * time.Millisecond)
	revokeServer(b, ws, p.server)
	if again := revokedAt(); again == nil || !again.Equal(*at) {
		t.Fatal("revoking a revoked server again moved when its revocation began", again, at)
	}

	p.report(t, 401, p.upload(snapshotID("b"), time.Now()))
	if got := p.receipts(t, b); len(got) != 1 || got[kept.Snapshot].Snapshot == "" {
		t.Fatal("a revoked server's report changed the receipts", got)
	}

	b.call("POST", "/api/v1/workspaces/"+ws.String()+"/servers/"+p.server.String()+"/enrollment", nil, 201, nil)
	if revokedAt() != nil {
		t.Fatal("renewing the server kept its revocation time")
	}
	if claimCounter(t, f, `SELECT count(*) FROM servers WHERE id=$1 AND revoked_at IS NULL`, p.server) != 1 {
		t.Fatal("renewing the server left a revocation time behind")
	}
}

// A workspace export carries when each server's revocation began and each snapshot's receipt, so an
// imported workspace shows the owner the same times.
func TestWorkspaceImportKeepsRevocationTimesAndSnapshotReceipts(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	p := newReportingPolicy(t, f, b, ws, "Exported")
	p.report(t, 204, p.upload(snapshotID("a"), time.Now()), p.upload(snapshotID("b"), time.Now()))
	revokeServer(b, ws, p.server)
	var receipts, revoked []byte
	read := func(f *fixture) ([]byte, []byte) {
		t.Helper()
		var r, s []byte
		if err := f.st.Pool.QueryRow(t.Context(), `SELECT jsonb_agg(to_jsonb(r) ORDER BY snapshot) FROM backup_snapshot_receipts r WHERE policy_id=$1`, p.policy.ID).Scan(&r); err != nil {
			t.Fatal(err)
		}
		if err := f.st.Pool.QueryRow(t.Context(), `SELECT to_jsonb(revoked_at) FROM servers WHERE id=$1 AND revoked`, p.server).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return r, s
	}
	receipts, revoked = read(f)
	if string(revoked) == "null" || !strings.Contains(string(receipts), snapshotID("b")) {
		t.Fatal("the fixture has no revocation time or receipts", string(revoked), string(receipts))
	}
	const passphrase = "unique archive passphrase for fixture"
	var encrypted api.WorkspaceArchive
	b.call("POST", "/api/v1/workspaces/"+ws.String()+"/export", map[string]string{"passphrase": passphrase}, 200, &encrypted)

	destination := setup(t)
	owner := destination.browser(t)
	owner.login("owner@example.test", destination.password)
	owner.call("POST", "/api/v1/workspaces/import", map[string]any{"archive": encrypted.Archive, "passphrase": passphrase}, 201, nil)
	gotReceipts, gotRevoked := read(destination)
	if string(gotReceipts) != string(receipts) || string(gotRevoked) != string(revoked) {
		t.Fatalf("the import changed the receipts or the revocation time:\n%s\n%s\nwant\n%s\n%s", gotReceipts, gotRevoked, receipts, revoked)
	}
}

// A database restored from before a revocation, or one whose hosts reported snapshots again after
// the restore, has later times than management first recorded, or none. Recovery from a checkpoint
// keeps the earlier of the two copies' times for each receipt and each revocation, keeps a
// receipt either copy has, and shows a revocation whose start one copy doesn't know as unknown.
func TestRecoveryKeepsTheEarliestReceiptsAndRevocationTimes(t *testing.T) {
	var p *reportingPolicy
	var later, unknown uuid.UUID
	var first, second backup.Run
	r := newClaimRecovery(t, 1, func(f *fixture, b *browser, ws, _ uuid.UUID) {
		p = newReportingPolicy(t, f, b, ws, "Recovered backups")
		first, second = p.upload(snapshotID("a"), time.Now()), p.upload(snapshotID("b"), time.Now())
		p.report(t, 204, first, second)
		later, unknown = enrolPreviewHost(t, f, b, ws), enrolPreviewHost(t, f, b, ws)
		for _, server := range []uuid.UUID{p.server, later, unknown} {
			revokeServer(b, ws, server)
		}
	})
	type times struct{ receipts, revocations map[string]*time.Time }
	read := func() times {
		t.Helper()
		out := times{map[string]*time.Time{}, map[string]*time.Time{}}
		rows, err := r.f.st.Pool.Query(r.ctx, `SELECT snapshot,first_reported_at FROM backup_snapshot_receipts WHERE policy_id=$1`, p.policy.ID)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var snapshot string
			var at time.Time
			if err = rows.Scan(&snapshot, &at); err != nil {
				t.Fatal(err)
			}
			out.receipts[snapshot] = &at
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		for _, server := range []uuid.UUID{p.server, later, unknown} {
			var revoked bool
			var at *time.Time
			if err = r.f.st.Pool.QueryRow(r.ctx, `SELECT revoked,revoked_at FROM servers WHERE id=$1`, server).Scan(&revoked, &at); err != nil {
				t.Fatal(err)
			}
			if !revoked {
				at = nil
			}
			out.revocations[server.String()] = at
		}
		return out
	}
	sealed := read()
	for _, server := range []uuid.UUID{p.server, later, unknown} {
		if sealed.revocations[server.String()] == nil {
			t.Fatal("the checkpoint has no revocation time")
		}
	}

	// The restored database: the policy's server not yet revoked, one server revoked later and one
	// with no time; the first snapshot reported again after the restore, the second never, and a
	// third only since.
	third := snapshotID("c")
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`UPDATE servers SET revoked=false,revoked_at=NULL WHERE id=$1`, []any{p.server}},
		{`UPDATE servers SET revoked_at=revoked_at+interval '1 hour' WHERE id=$1`, []any{later}},
		{`UPDATE servers SET revoked_at=NULL WHERE id=$1`, []any{unknown}},
		{`DELETE FROM backup_snapshot_receipts WHERE policy_id=$1`, []any{p.policy.ID}},
		{`INSERT INTO backup_snapshot_receipts(workspace_id,environment_id,policy_id,snapshot,server_id,first_reported_at) VALUES($1,$2,$3,$4,$5,clock_timestamp()+interval '1 hour'),($1,$2,$3,$6,$5,clock_timestamp())`, []any{r.ws, p.policy.EnvironmentID, p.policy.ID, first.Snapshot, p.server, third}},
	} {
		if _, err := r.f.st.Pool.Exec(r.ctx, statement.sql, statement.args...); err != nil {
			t.Fatal(statement.sql, err)
		}
	}

	r.recoverAll(t, r.exports(t, claimExport{}))
	got := read()
	for _, server := range []uuid.UUID{p.server, later} {
		if at := got.revocations[server.String()]; at == nil || !at.Equal(*sealed.revocations[server.String()]) {
			t.Fatal("recovery didn't keep the earlier revocation time", server, at, sealed.revocations[server.String()])
		}
	}
	if at := got.revocations[unknown.String()]; at != nil {
		t.Fatal("recovery gave a revocation whose start one copy doesn't know a time", at)
	}
	for _, snapshot := range []string{first.Snapshot, second.Snapshot} {
		if at := got.receipts[snapshot]; at == nil || !at.Equal(*sealed.receipts[snapshot]) {
			t.Fatal("recovery didn't keep the checkpoint's earlier receipt", snapshot, at, sealed.receipts[snapshot])
		}
	}
	if got.receipts[third] == nil || len(got.receipts) != 3 {
		t.Fatal("recovery dropped a receipt only the restored database had", got.receipts)
	}
}
