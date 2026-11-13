package integration

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"strings"
	"testing"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/agentidentity"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
)

// nul is U+0000, which PostgreSQL's text and jsonb cannot store. Its refusal used to abort the
// write's transaction and answer 500.
const nul = "\x00"

// noNUL fails when a stored value still holds NUL, written raw or as a JSON escape.
func noNUL(t *testing.T, what string, stored []byte) {
	t.Helper()
	if bytes.Contains(stored, []byte(nul)) || bytes.Contains(stored, []byte(`\`+`u0000`)) {
		t.Fatalf("%s kept NUL: %s", what, stored)
	}
}

// What a person sends is refused on its field, and what a host reported is stored with a space
// for each NUL, so the host's agent keeps working: the known paths from PLAT-312.
func TestNULIsRefusedFromPeopleAndReplacedFromHosts(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	prefix := "/api/v1/workspaces/" + ws.String()
	var project api.Project
	b.call("POST", prefix+"/projects", map[string]string{"name": "Recovery"}, 201, &project)
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

	// 4. Enrollment and heartbeats carry os-release and Docker's and Compose's versions, which
	// the host prints. A NUL in them refused every heartbeat, so the server stayed offline.
	enrolled, in, token := enrollment(t, f, b, ws, "deployment")
	yes := true
	in.Capabilities.Visual, in.Capabilities.Backups, in.Capabilities.Schedules = &yes, &yes, &yes
	compose := "2.29" + nul
	in.Preflight.OsRelease, in.Preflight.DockerVersion, in.Preflight.ComposeVersion = "ubuntu"+nul+"24.04", "27.3"+nul, &compose
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
	preflight := func() api.HostPreflight {
		t.Helper()
		var raw []byte
		if err := f.st.Pool.QueryRow(ctx, "SELECT preflight FROM servers WHERE id=$1", enrolled.Server.Id).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		noNUL(t, "the preflight", raw)
		var stored api.HostPreflight
		if err := json.Unmarshal(raw, &stored); err != nil {
			t.Fatal(err)
		}
		return stored
	}
	if stored := preflight(); stored.OsRelease != "ubuntu 24.04" || stored.DockerVersion != "27.3 " || stored.ComposeVersion == nil || *stored.ComposeVersion != "2.29 " {
		t.Fatalf("enrollment stored %+v", stored)
	}
	in.Preflight.OsRelease = "debian" + nul + "12"
	in.Capabilities.Version = "test" + nul
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/heartbeat", token, api.AgentHeartbeat{Capabilities: in.Capabilities, Preflight: in.Preflight}, 200, nil)
	if stored := preflight(); stored.OsRelease != "debian 12" {
		t.Fatalf("the heartbeat stored %+v", stored)
	}
	var capabilities []byte
	if err := f.st.Pool.QueryRow(ctx, "SELECT capabilities FROM servers WHERE id=$1", enrolled.Server.Id).Scan(&capabilities); err != nil {
		t.Fatal(err)
	}
	noNUL(t, "the capabilities", capabilities)

	poll := func(id uuid.UUID) api.ExecutionCommand {
		t.Helper()
		var p api.AgentPoll
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/poll", token, nil, 200, &p)
		if p.Command == nil || p.Command.Id != id {
			t.Fatal("command not dispatched")
		}
		return *p.Command
	}
	report := func(c api.ExecutionCommand, obs *deployment.Observation, steps []api.ExecutionStep) {
		t.Helper()
		r := api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 1, State: "succeeded", Steps: steps}
		if obs != nil {
			raw, _ := stablejson.Marshal(obs)
			r.Observation = (*jsontext.Value)(&raw)
		}
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/report", token, r, 204, nil)
	}
	var plan deployment.Status
	b.call("POST", path+"/deployment-plans", api.VisualPlanRequest{ServerId: enrolled.Server.Id, BaseRevisionId: draft.Id}, 201, &plan)
	report(poll(plan.OperationID), &deployment.Observation{ObservedAt: time.Now(), Resources: []deployment.Resource{}, Images: map[string]string{pg.String(): image}, StateHash: strings.Repeat("a", 64)}, []api.ExecutionStep{})
	b.call("GET", path+"/deployment-plans/"+plan.ID.String(), nil, 200, &plan)
	var op api.Operation
	b.call("POST", path+"/deployment-plans/"+plan.ID.String()+"/apply", api.ApplyVisualPlan{IdempotencyKey: uuid.New(), ApproveHostChanges: approved()}, 201, &op)
	report(poll(op.Id), &deployment.Observation{ObservedAt: time.Now(), Applied: true, Resources: []deployment.Resource{{ID: pg, Kind: "service", State: "healthy", Image: image}}, StateHash: strings.Repeat("b", 64)}, []api.ExecutionStep{})

	// 1. A filesystem destination's root.
	var refused api.Error
	b.call("POST", prefix+"/backup-destinations", map[string]any{"name": "Local", "kind": "filesystem", "root": "/var/backups" + nul, "endpoint": "", "bucket": "", "region": "", "access": map[string]string{"accessKey": "", "secretKey": "", "password": ""}}, 400, &refused)
	invalidField(t, refused, "root")
	var dest backup.Destination
	b.call("POST", prefix+"/backup-destinations", map[string]any{"name": "Recovery bucket", "kind": "s3", "root": "", "endpoint": "https://storage.example.test", "bucket": "recovery-fixture", "region": "test-1", "access": map[string]string{"accessKey": "storage-key", "secretKey": "storage-secret", "password": ""}}, 201, &dest)

	// 2. A policy's name.
	password := "repository-password"
	input := api.BackupPolicyRequest{Name: "Daily" + nul, ResourceId: pg, DestinationId: dest.ID, Method: "postgres-logical", Schedule: api.DailySchedule{Hour: 2, Minute: 30, Timezone: "Europe/London"}, Retain: 7, Enabled: true, Command: []string{}, Password: &password}
	refused = api.Error{}
	b.call("POST", path+"/backup-policies", input, 400, &refused)
	invalidField(t, refused, "name")
	input.Name = "Daily backup"
	var policy backup.Policy
	b.call("POST", path+"/backup-policies", input, 201, &policy)

	// 3. A command's snapshot, which only a restore used to check.
	commandPath := path + "/backup-policies/" + policy.ID.String() + "/commands"
	refused = api.Error{}
	b.call("POST", commandPath, api.BackupCommandRequest{Action: "install", RequestKey: uuid.New(), Snapshot: nul}, 400, &refused)
	invalidField(t, refused, "snapshot")

	// 5. A report's steps, which the host's output can name and describe.
	b.call("POST", commandPath, api.BackupCommandRequest{Action: "install", RequestKey: uuid.New(), Snapshot: ""}, 202, &op)
	installed := poll(op.Id)
	// A plan hash is matched, not shown, so a NUL in it, replaced, matches nothing and the report
	// is refused: a well-formed agent never sends one. The hash keeps its length, which the
	// schema bounds.
	hash := installed.PlanHash[:len(installed.PlanHash)-1] + nul
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/report", token, api.AgentReport{Id: installed.Id, Generation: installed.Generation, PlanHash: hash, Sequence: 1, State: "succeeded", Steps: []api.ExecutionStep{}}, 403, nil)
	report(installed, nil, []api.ExecutionStep{{Name: "install" + nul, Detail: "restic" + nul + "said", State: api.Done}})
	var steps []byte
	if err := f.st.Pool.QueryRow(ctx, "SELECT steps FROM execution_operations WHERE id=$1", op.Id).Scan(&steps); err != nil {
		t.Fatal(err)
	}
	noNUL(t, "the steps", steps)
	if !bytes.Contains(steps, []byte(`"restic said"`)) {
		t.Fatalf("the steps were stored as %s", steps)
	}

	// 6. A schedule report's runs, whose notes quote the backup tool.
	now := time.Now()
	run := backup.Run{ID: uuid.New(), PolicyID: policy.ID, Generation: 1, ScheduledAt: now, StartedAt: now, FinishedAt: &now, State: "failed", Note: "pg_dump" + nul + "failed", Snapshot: "", Manifest: nil}
	status := backup.Status{PolicyID: policy.ID, Generation: 1, Next: now.Add(time.Hour), Runs: []backup.Run{run}}
	raw, _ := stablejson.Marshal(status)
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/schedule-reports", token, map[string]any{"sequence": 1, "status": jsontext.Value(raw)}, 204, nil)
	var got backup.Status
	b.call("GET", path+"/backup-policies/"+policy.ID.String()+"/status", nil, 200, &got)
	if len(got.Runs) != 1 || got.Runs[0].Note != "pg_dump failed" {
		t.Fatalf("the schedule report stored %+v", got.Runs)
	}
}

// 7. An export never holds NUL, which PostgreSQL does not store, so an archive that does was
// made by hand and is refused like any other invalid archive, not with a 500.
func TestImportRefusesAnArchiveHoldingNUL(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	var project api.Project
	b.call("POST", "/api/v1/workspaces/"+ws.String()+"/projects", map[string]string{"name": "Exported"}, 201, &project)
	passphrase := "unique NUL archive test passphrase"
	var encrypted api.WorkspaceArchive
	b.call("POST", "/api/v1/workspaces/"+ws.String()+"/export", map[string]string{"passphrase": passphrase}, 200, &encrypted)
	plain, err := portability.Open(ctx, passphrase, encrypted.Archive)
	if err != nil {
		t.Fatal(err)
	}
	var archive portability.Archive
	if err = json.Unmarshal(plain, &archive); err != nil {
		t.Fatal(err)
	}
	if len(archive.Tables["projects"]) != 1 {
		t.Fatal("the archive holds", len(archive.Tables["projects"]), "projects")
	}
	archive.Tables["projects"][0]["name"] = jsontext.Value(`"a` + `\` + `u0000b"`)
	altered, err := stablejson.Marshal(archive)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := portability.Seal(ctx, passphrase, altered)
	if err != nil {
		t.Fatal(err)
	}
	destination := setup(t)
	owner := destination.browser(t)
	owner.login("owner@example.test", destination.password)
	var refused api.Error
	owner.call("POST", "/api/v1/workspaces/import", map[string]any{"archive": sealed, "passphrase": passphrase}, 400, &refused)
	if refused.Code == "internal_error" {
		t.Fatal("the archive was answered", refused)
	}
}

// A path that misses the check where its input is decoded is refused when PostgreSQL refuses the
// write, rather than failing, whether the NUL is in text or written as a jsonb escape.
func TestStoreRefusesUnstorableTextFromAnyPath(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	err := f.st.Transaction(ctx, func(q *db.Queries) error {
		return q.InsertWorkspace(ctx, db.InsertWorkspaceParams{ID: uuid.New(), Name: "a" + nul})
	})
	if !errors.Is(err, domain.UnstorableText) {
		t.Fatal("NUL in text was answered with", err)
	}
	_, err = f.st.Pool.Exec(ctx, "SELECT $1::jsonb", []byte(`{"a":"`+`\`+`u0000"}`))
	if !errors.Is(store.Normalize(err), domain.UnstorableText) {
		t.Fatal("NUL in jsonb was answered with", err)
	}
}

// An agent's NUL is replaced so its host's output can't stop it, but a value the server matches
// or verifies, such as a challenge or a signature, then no longer matches, and the request is
// refused: replacement makes a value the agent could have sent itself, never one it could not.
func TestAgentCredentialsHoldingNULAreRefused(t *testing.T) {
	_, _, _, v, a := keyedServer(t)
	key := agentKey(t, a)
	public, _ := agentidentity.PublicKey(key)
	token := func(nonce, signature string) {
		t.Helper()
		agentRequest(t, a.tls.Client(), a.tls.URL, "/agent/v1/token", "", api.AgentTokenRequest{ServerId: v.Server.Id, PublicKey: public, Nonce: nonce, StartCounter: 100, Signature: signature}, 401, nil)
	}
	var challenge api.AgentChallenge
	agentRequest(t, a.tls.Client(), a.tls.URL, "/agent/v1/challenge", "", api.AgentChallengeRequest{ServerId: v.Server.Id}, 200, &challenge)
	// Signed over the NUL, so only its replacement makes the request differ from what was signed.
	signed, _ := agentidentity.Sign(key, agentidentity.TokenMessage(v.Server.Id, challenge.Nonce+nul, 100))
	token(challenge.Nonce+nul, signed)
	signature, _ := agentidentity.Sign(key, agentidentity.TokenMessage(v.Server.Id, challenge.Nonce, 100))
	token(challenge.Nonce, signature+nul)
}
