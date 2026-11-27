package integration

import (
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
	enroll   api.AgentEnrollment // the server's enrollment, for enrolling it again
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
	spec := domain.Specification{Version: 1, Variables: []domain.Variable{}, Applications: []domain.Application{{ID: pg, Name: "Database", Kind: "postgres", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Postgres: &domain.Postgres{Username: "app", Database: "app", PasswordSecretVersionID: secret.Id, Roles: domain.PostgresOwner}, Mounts: []domain.VolumeMount{{VolumeID: vol, Target: "/var/lib/postgresql"}}}}, Volumes: []domain.Volume{{ID: vol, Name: "Data"}}}
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
	// Each policy's destination has a bucket of its own, as each server's does.
	var dest backup.Destination
	b.call("POST", prefix+"/backup-destinations", map[string]any{"name": name + " bucket", "kind": "s3", "serverId": enrolled.Server.Id, "root": "", "endpoint": "https://storage.example.test", "bucket": "receipts-" + strings.ToLower(strings.ReplaceAll(name, " ", "-")), "region": "test-1", "access": map[string]string{"accessKey": "storage-key", "secretKey": "storage-secret", "password": ""}}, 201, &dest)
	password := "repository-password-fixture"
	var policy backup.Policy
	b.call("POST", path+"/backup-policies", api.BackupPolicyRequest{Name: "Daily backup", ResourceId: pg, DestinationId: dest.ID, Method: "postgres-logical", Schedule: api.DailySchedule{Hour: 2, Minute: 30, Timezone: "UTC"}, Retain: 7, Enabled: true, Command: []string{}, Password: &password}, 201, &policy)
	return &reportingPolicy{f: f, ws: ws, path: path + "/backup-policies/" + policy.ID.String(), project: project.Id, policy: policy, server: enrolled.Server.Id, token: token, enroll: in}
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

// A server's report can backdate its snapshots as far as it likes. Management records when an upload
// first listed each one by its own clock, keeps that time while reports go on listing the snapshot,
// even by a restore that verified it after the upload left the agent's history, and records nothing
// for a failure, a pruned snapshot or one only a restore names. A snapshot taken out of a report
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
		if r := got[snapshot]; r.FirstReportedAt.Before(before.Add(-time.Second)) || r.FirstReportedAt.After(after.Add(time.Second)) {
			t.Fatalf("the receipt for %s doesn't record management's time: %+v, reported between %s and %s", snapshot, r, before, after)
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

	// Once the agent's history keeps only a restore that verified a snapshot, the restore goes on
	// listing it, and the snapshot keeps its time.
	verified := first
	verified.ID, verified.TargetID, verified.Verified = uuid.New(), uuid.New(), true
	p.report(t, 204, verified, third)
	if got = p.receipts(t, b); len(got) != 2 || !got[first.Snapshot].FirstReportedAt.Equal(received) {
		t.Fatalf("a snapshot only a restore lists lost its receipt or its time: %v", got)
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
		if err := f.st.Pool.QueryRow(t.Context(), `SELECT snapshot_receipts FROM backup_policies WHERE id=$1`, p.policy.ID).Scan(&r); err != nil {
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
// keeps the earlier of the two copies' times for each revocation, and shows a revocation whose start
// one copy doesn't know as unknown. It keeps a receipt for each snapshot the newer report lists, at
// the earlier time of the copies that have one.
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
	type times struct {
		receipts    backup.Receipts
		revocations map[string]*time.Time
	}
	read := func() times {
		t.Helper()
		out := times{revocations: map[string]*time.Time{}}
		var raw []byte
		if err := r.f.st.Pool.QueryRow(r.ctx, `SELECT snapshot_receipts FROM backup_policies WHERE id=$1`, p.policy.ID).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var err error
		if out.receipts, err = backup.ParseReceipts(raw); err != nil {
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
	if len(sealed.receipts) != 2 {
		t.Fatal("the checkpoint doesn't have both receipts", sealed.receipts)
	}

	// The restored database: the policy's server not yet revoked, one server revoked later and one
	// with no time; and a newer report than the checkpoint's, which lists the first snapshot,
	// reported again after the restore, and a third only since, but no longer the second.
	third := p.upload(snapshotID("c"), time.Now())
	since := time.Now().UTC().Truncate(time.Microsecond)
	status, _ := stablejson.Marshal(backup.Status{Enabled: true, PolicyID: p.policy.ID, Generation: 1, Next: since.Add(time.Hour), Runs: []backup.Run{first, third}})
	receipts, _ := stablejson.Marshal(backup.Receipts{first.Snapshot: sealed.receipts[first.Snapshot].Add(time.Hour), third.Snapshot: since})
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`UPDATE servers SET revoked=false,revoked_at=NULL WHERE id=$1`, []any{p.server}},
		{`UPDATE servers SET revoked_at=revoked_at+interval '1 hour' WHERE id=$1`, []any{later}},
		{`UPDATE servers SET revoked_at=NULL WHERE id=$1`, []any{unknown}},
		{`UPDATE backup_policies SET status=$2::jsonb,status_sequence=status_sequence+1,snapshot_receipts=$3::jsonb WHERE id=$1`, []any{p.policy.ID, string(status), string(receipts)}},
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
	if at, ok := got.receipts[first.Snapshot]; !ok || !at.Equal(sealed.receipts[first.Snapshot]) {
		t.Fatal("recovery didn't keep the checkpoint's earlier receipt", at, sealed.receipts[first.Snapshot])
	}
	if at, ok := got.receipts[third.Snapshot]; !ok || !at.Equal(since) {
		t.Fatal("recovery didn't keep the receipt of a snapshot only the newer report lists", got.receipts)
	}
	if _, kept := got.receipts[second.Snapshot]; kept || len(got.receipts) != 2 {
		t.Fatal("recovery kept a receipt the newer report no longer lists", got.receipts)
	}
}

// A restore's result is recorded once, by its succeeded report. Recovery from a checkpoint that
// holds the result keeps it when the restored database had the restore still running, so the
// restore doesn't end succeeded with nothing recorded of what it restored.
func TestRecoveryKeepsARestoresRecordedResult(t *testing.T) {
	var op uuid.UUID
	var result []byte
	r := newClaimRecovery(t, 1, func(f *fixture, b *browser, ws, _ uuid.UUID) {
		p := newReportingPolicy(t, f, b, ws, "Restored backups")
		receiver := enrolPreviewHost(t, f, b, ws)
		env := p.policy.EnvironmentID
		var plan uuid.UUID
		if err := f.st.Pool.QueryRow(t.Context(), `SELECT plan_id FROM visual_runtime WHERE environment_id=$1`, env).Scan(&plan); err != nil {
			t.Fatal(err)
		}
		op = uuid.New()
		target, snapshot := uuid.New(), snapshotID("a")
		result, _ = stablejson.Marshal(backup.RecoveryResult{Snapshot: snapshot, TargetID: target, Manifest: backup.Manifest{Recipe: backup.Recipe, PolicyID: p.policy.ID, RunID: uuid.New(), EnvironmentID: env, ResourceID: p.policy.ResourceID, Method: p.policy.Method, Bytes: 4096, SHA256: snapshotID("d"), Roles: []backup.Role{}, Extensions: []string{}}})
		for _, statement := range []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO execution_operations(id,workspace_id,server_id,environment_id,generation,request_key,command,kind,state,report_sequence) VALUES($1,$2,$3,$4,1000,$5,'{"kind":"restore"}','restore','succeeded',1)`, []any{op, ws, receiver, env, uuid.New()}},
			{`INSERT INTO backup_recoveries(id,workspace_id,environment_id,policy_id,operation_id,source_server_id,server_id,plan_id,snapshot,first_reported_at,target_volume_id,created_at,result) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,now(),$10,now(),$11::jsonb)`, []any{uuid.New(), ws, env, p.policy.ID, op, p.server, receiver, plan, snapshot, target, string(result)}},
		} {
			if _, err := f.st.Pool.Exec(t.Context(), statement.sql, statement.args...); err != nil {
				t.Fatal(statement.sql, err)
			}
		}
		for _, server := range []uuid.UUID{p.server, receiver} {
			revokeServer(b, ws, server)
		}
	})
	// The restored database: the restore still running, with no result yet.
	for _, statement := range []string{
		`UPDATE backup_recoveries SET result=NULL WHERE operation_id=$1`,
		`UPDATE execution_operations SET state='running',report_sequence=0 WHERE id=$1`,
	} {
		if _, err := r.f.st.Pool.Exec(r.ctx, statement, op); err != nil {
			t.Fatal(statement, err)
		}
	}
	r.recoverAll(t, r.exports(t, claimExport{}))
	var state string
	var got []byte
	if err := r.f.st.Pool.QueryRow(r.ctx, `SELECT o.state,r.result FROM backup_recoveries r JOIN execution_operations o ON o.id=r.operation_id WHERE r.operation_id=$1`, op).Scan(&state, &got); err != nil {
		t.Fatal(err)
	}
	if state != "succeeded" || got == nil {
		t.Fatalf("recovery left the restore %s with result %s", state, got)
	}
	if string(stablejson.Normalize(got)) != string(stablejson.Normalize(result)) {
		t.Fatalf("recovery changed the restore's result: %s, want %s", got, result)
	}
}
