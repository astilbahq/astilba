package integration

import (
	"context"
	"encoding/json/jsontext"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

// listedIDs runs one of the app's list queries and returns the IDs it shows, newest first.
func listedIDs(t *testing.T, f *fixture, query string, args ...any) []uuid.UUID {
	t.Helper()
	rows, err := f.st.Pool.Query(context.Background(), query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := []uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func rowExists(t *testing.T, f *fixture, query string, args ...any) bool {
	t.Helper()
	var exists bool
	if err := f.st.Pool.QueryRow(context.Background(), "SELECT EXISTS("+query+")", args...).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	return exists
}

// The app's lists, as the queries that show them order and limit them.
const (
	newestOperations = `SELECT id FROM execution_operations WHERE workspace_id=$1 ORDER BY created_at DESC LIMIT 100`
	listedReleases   = `SELECT o.id FROM execution_operations o JOIN visual_plans p ON p.id=o.visual_plan_id WHERE o.workspace_id=$1 AND o.environment_id=$2 AND o.state='succeeded' AND o.command->'visual'->>'action'='deploy' ORDER BY o.created_at DESC,o.id DESC LIMIT 20`
	listedRevisions  = `SELECT id FROM specification_revisions WHERE workspace_id=$1 AND environment_id=$2 ORDER BY created_at DESC,id DESC LIMIT 50`
	listedAudit      = `SELECT id FROM audit_events WHERE workspace_id=$1 ORDER BY created_at DESC,id DESC LIMIT 100`
)

// A busy workspace's exports and recovery bases used to carry every audit event, revision and
// operation it ever had, so they grew until they passed the archive's limits. An export now carries
// what the app lists, and a base drops the audit events and revisions nothing names beyond those
// lists. An imported workspace shows the same lists, and still has every secret version and the
// build artifact a configuration pins, which no foreign key names.
func TestExportAndBaseCarryWhatTheAppLists(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	prefix := "/api/v1/workspaces/" + ws.String()
	var project api.Project
	b.call("POST", prefix+"/projects", map[string]string{"name": "Busy"}, 201, &project)
	var env api.Environment
	base := prefix + "/projects/" + project.Id.String() + "/environments"
	b.call("POST", base, map[string]string{"name": "Web"}, 201, &env)
	path := base + "/" + env.Id.String()
	var pinned api.SecretVersion
	b.call("POST", path+"/secrets", map[string]string{"name": "TOKEN", "value": "pinned-version-canary"}, 201, &pinned)
	image := execution.TestImage
	app := uuid.New()
	spec := domain.Specification{Version: 1, Variables: []domain.Variable{{Name: "TOKEN", SecretVersionID: &pinned.Id}}, Applications: []domain.Application{{ID: app, Name: "Web", Source: domain.ApplicationSource{Kind: "image", Image: &image}}}}
	var draft api.Revision
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": env.DraftRevisionId, "specification": spec}, 200, &draft)
	server, input, token := enrollment(t, f, b, ws, "deployment")
	yes := true
	input.Capabilities.Visual = &yes
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", input, 200, nil)
	report := func(c api.ExecutionCommand, resolve bool) {
		observation := deployment.Observation{ObservedAt: time.Now().UTC(), Applied: !resolve, Resources: []deployment.Resource{}, StateHash: strings.Repeat("a", 64)}
		if resolve {
			observation.Images = map[string]string{app.String(): image}
		} else {
			observation.Resources = []deployment.Resource{{ID: app, State: "running", Image: image}}
		}
		raw, _ := stablejson.Marshal(observation)
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/report", token, api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 1, State: "succeeded", Steps: []api.ExecutionStep{}, Observation: (*jsontext.Value)(&raw)}, 204, nil)
	}
	var plan deployment.Status
	b.call("POST", path+"/deployment-plans", api.VisualPlanRequest{ServerId: server.Server.Id, BaseRevisionId: draft.Id, AllowHostChanges: true}, 201, &plan)
	var polled api.AgentPoll
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/poll", token, nil, 200, &polled)
	resolve := *polled.Command
	report(resolve, true)
	var operation api.Operation
	b.call("POST", path+"/deployment-plans/"+plan.ID.String()+"/apply", api.ApplyPlan{IdempotencyKey: uuid.New()}, 201, &operation)
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/poll", token, nil, 200, &polled)
	report(*polled.Command, false)
	if !rowExists(t, f, `SELECT 1 FROM environments WHERE id=$1 AND applied_revision_id=$2`, env.Id, draft.Id) {
		t.Fatal("the deployment did not apply")
	}
	// Rotating the secret makes a newer version current; the applied revision still pins the first.
	b.call("POST", path+"/secrets/"+pinned.SecretId.String()+"/versions", map[string]string{"value": "rotated-version-canary"}, 201, nil)

	// History older than every window: ended operations, releases, revisions and audit events.
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := f.st.Pool.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO execution_operations(id,workspace_id,server_id,environment_id,generation,request_key,command,kind,state,report_sequence,visual_plan_id,created_at)
SELECT n.id,o.workspace_id,o.server_id,o.environment_id,(SELECT max(generation) FROM execution_operations WHERE environment_id=$1)+n.i,gen_random_uuid(),jsonb_set(o.command,'{id}',to_jsonb(n.id::text)),o.kind,o.state,o.report_sequence,NULL,now()-interval '1 day'-n.i*interval '1 second'
FROM (SELECT * FROM execution_operations WHERE id=$2) o CROSS JOIN (SELECT gen_random_uuid() AS id,i FROM generate_series(1,150) i) n`, env.Id, operation.Id)
	// The 25 newest are releases, each with a plan of its own, as a real deployment has.
	exec(`WITH clones AS (SELECT id FROM execution_operations WHERE environment_id=$1 AND created_at<now()-interval '1 day' ORDER BY created_at DESC LIMIT 25),
requests AS (INSERT INTO visual_requests(id,workspace_id,server_id,environment_id,unit_id,revision_id,operation_id,candidate)
	SELECT gen_random_uuid(),v.workspace_id,v.server_id,v.environment_id,v.unit_id,v.revision_id,c.id,v.candidate FROM clones c, visual_requests v WHERE v.id=(SELECT visual_plan_id FROM execution_operations WHERE id=$2) RETURNING id,operation_id),
plans AS (INSERT INTO visual_plans(id,workspace_id,environment_id,plan) SELECT r.id,p.workspace_id,p.environment_id,p.plan FROM requests r, visual_plans p WHERE p.id=(SELECT visual_plan_id FROM execution_operations WHERE id=$2) RETURNING id)
UPDATE execution_operations o SET visual_plan_id=r.id FROM requests r WHERE o.id=r.operation_id`, env.Id, operation.Id)
	exec(`UPDATE environments SET execution_generation=(SELECT max(generation) FROM execution_operations WHERE environment_id=$1) WHERE id=$1`, env.Id)
	exec(`INSERT INTO specification_revisions(workspace_id,environment_id,id,author_id,specification,content_hash,created_at)
SELECT workspace_id,environment_id,gen_random_uuid(),author_id,specification,content_hash,now()-interval '1 day'-i*interval '1 second' FROM specification_revisions, generate_series(1,60) i WHERE id=$1`, draft.Id)
	exec(`INSERT INTO audit_events(workspace_id,id,actor_id,action,resource_id,created_at)
SELECT $1,gen_random_uuid(),$2,'draft.save',$3,now()-interval '1 day'-i*interval '1 second' FROM generate_series(1,150) i`, ws, f.owner, env.Id)
	oldestRevision := listedIDs(t, f, `SELECT id FROM specification_revisions WHERE environment_id=$1 ORDER BY created_at LIMIT 1`, env.Id)[0]
	oldestAudit := listedIDs(t, f, `SELECT id FROM audit_events WHERE workspace_id=$1 ORDER BY created_at LIMIT 1`, ws)[0]
	oldestOperation := listedIDs(t, f, `SELECT id FROM execution_operations WHERE workspace_id=$1 ORDER BY created_at LIMIT 1`, ws)[0]
	// Older still: a revision only a build names, that build's artifact, which a revision the app
	// lists pins by ID, and 100 newer builds, so neither falls in a list.
	// That build's revision pins an older build's artifact, as an automatic deployment's draft does,
	// which a build records but nothing the app lists uses.
	oldBuild, pinnedArtifact, buildHistory := uuid.New(), uuid.New(), uuid.New()
	exec(`WITH builds AS (INSERT INTO execution_operations(id,workspace_id,server_id,environment_id,generation,request_key,command,kind,state,report_sequence,created_at)
	SELECT $5,$1,$2,$3,(SELECT max(generation) FROM execution_operations WHERE environment_id=$3)+1,gen_random_uuid(),'{}','pipeline','succeeded',1,now()-interval '4 days' RETURNING id,created_at),
runs AS (INSERT INTO pipeline_runs(id,workspace_id,environment_id,application_id,revision_id,configuration,created_at) SELECT b.id,$1,$3,$4,$6,'{}',b.created_at FROM builds b RETURNING id,created_at)
INSERT INTO build_artifacts(id,workspace_id,environment_id,identity,created_at) SELECT id,$1,$3,'{}',created_at FROM runs`, ws, server.Server.Id, env.Id, app, buildHistory, draft.Id)
	exec(`INSERT INTO specification_revisions(workspace_id,environment_id,id,author_id,specification,content_hash,created_at)
SELECT workspace_id,environment_id,$2,author_id,jsonb_set(specification,'{applications,0,source}',jsonb_build_object('kind','artifact','artifactId',$3::text)),content_hash,now()-interval '3 days' FROM specification_revisions WHERE id=$1`, draft.Id, oldBuild, buildHistory)
	exec(`WITH n AS (SELECT CASE WHEN i=0 THEN $5::uuid ELSE gen_random_uuid() END AS id,i FROM generate_series(0,100) i),
builds AS (INSERT INTO execution_operations(id,workspace_id,server_id,environment_id,generation,request_key,command,kind,state,report_sequence,created_at)
	SELECT n.id,$1,$2,$3,(SELECT max(generation) FROM execution_operations WHERE environment_id=$3)+1+n.i,gen_random_uuid(),'{}','pipeline','succeeded',1,now()-interval '2 days'+n.i*interval '1 second' FROM n RETURNING id,created_at),
runs AS (INSERT INTO pipeline_runs(id,workspace_id,environment_id,application_id,revision_id,configuration,created_at)
	SELECT b.id,$1,$3,$4,CASE WHEN b.id=$5::uuid THEN $6::uuid ELSE $7::uuid END,'{}',b.created_at FROM builds b RETURNING id,created_at)
INSERT INTO build_artifacts(id,workspace_id,environment_id,identity,created_at) SELECT id,$1,$3,'{}',created_at FROM runs`, ws, server.Server.Id, env.Id, app, pinnedArtifact, oldBuild, draft.Id)
	exec(`UPDATE environments SET execution_generation=(SELECT max(generation) FROM execution_operations WHERE environment_id=$1) WHERE id=$1`, env.Id)
	exec(`INSERT INTO specification_revisions(workspace_id,environment_id,id,author_id,specification,content_hash)
SELECT workspace_id,environment_id,gen_random_uuid(),author_id,jsonb_set(specification,'{applications,0,source}',jsonb_build_object('kind','artifact','artifactId',$2::text)),content_hash FROM specification_revisions WHERE id=$1`, draft.Id, pinnedArtifact)
	// A Compose configuration names its build by image, and planning takes the newest artifact
	// with that image.
	imageArtifact := uuid.New()
	exec(`WITH builds AS (INSERT INTO execution_operations(id,workspace_id,server_id,environment_id,generation,request_key,command,kind,state,report_sequence,created_at)
	SELECT $5,$1,$2,$3,(SELECT max(generation) FROM execution_operations WHERE environment_id=$3)+1,gen_random_uuid(),'{}','pipeline','succeeded',1,now()-interval '5 days' RETURNING id,created_at),
runs AS (INSERT INTO pipeline_runs(id,workspace_id,environment_id,application_id,revision_id,configuration,created_at) SELECT b.id,$1,$3,$4,$6,'{}',b.created_at FROM builds b RETURNING id,created_at)
INSERT INTO build_artifacts(id,workspace_id,environment_id,identity,created_at) SELECT id,$1,$3,jsonb_build_object('image','astilba-build-compose:built'),created_at FROM runs`, ws, server.Server.Id, env.Id, app, imageArtifact, draft.Id)
	exec(`UPDATE environments SET execution_generation=(SELECT max(generation) FROM execution_operations WHERE environment_id=$1) WHERE id=$1`, env.Id)
	exec(`INSERT INTO specification_revisions(workspace_id,environment_id,id,author_id,specification,content_hash)
SELECT workspace_id,environment_id,gen_random_uuid(),author_id,jsonb_set(specification,'{applications,0,source}',jsonb_build_object('kind','image','image','astilba-build-compose:built')),content_hash FROM specification_revisions WHERE id=$1`, draft.Id)

	// A recovery base keeps the newest 100 audit events and the revisions the app lists or a row
	// names, but still every operation recovery needs.
	tx, err := f.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := portability.CaptureRecovery(ctx, tx, ws)
	tx.Rollback(ctx)
	if err != nil {
		t.Fatal(err)
	}
	capture, err := portability.DecodeRecovery(raw)
	if err != nil {
		t.Fatal(err)
	}
	carried := func(a portability.Archive, table string, id uuid.UUID) bool {
		for _, row := range a.Tables[table] {
			if string(row["id"]) == `"`+id.String()+`"` {
				return true
			}
		}
		return false
	}
	if len(capture.Tables["audit_events"]) != 100 || carried(capture, "audit_events", oldestAudit) {
		t.Fatal("a recovery base carried more than the newest 100 audit events", len(capture.Tables["audit_events"]))
	}
	if carried(capture, "specification_revisions", oldestRevision) || !carried(capture, "specification_revisions", oldBuild) {
		t.Fatal("a recovery base carried an unlisted revision nothing names, or dropped one a row names")
	}
	if !carried(capture, "execution_operations", operation.Id) || !carried(capture, "execution_operations", resolve.Id) {
		t.Fatal("a recovery base dropped an operation recovery needs")
	}

	// An imported workspace lists what its source lists.
	account, err := f.st.Queries.AccountByID(ctx, f.owner)
	if err != nil {
		t.Fatal(err)
	}
	// The export records itself after reading the workspace, so its own audit event isn't in it.
	listedAuditEvents := listedIDs(t, f, listedAudit, ws)
	const passphrase = "history window transfer passphrase"
	archive, err := (&portability.Service{Store: f.st}).Export(ctx, browserPrincipal(t, f.st, account, true), ws, passphrase)
	if err != nil {
		t.Fatal(err)
	}
	destination := setup(t)
	importer, err := destination.st.Queries.AccountByID(ctx, destination.owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = (&portability.Service{Store: destination.st}).Import(ctx, browserPrincipal(t, destination.st, importer, true), passphrase, archive, ""); err != nil {
		t.Fatal(err)
	}
	for _, list := range []struct {
		name, query string
		args        []any
	}{
		{"operations", newestOperations, []any{ws}},
		{"releases", listedReleases, []any{ws, env.Id}},
		{"revisions", listedRevisions, []any{ws, env.Id}},
	} {
		source, imported := listedIDs(t, f, list.query, list.args...), listedIDs(t, destination, list.query, list.args...)
		if !slices.Equal(source, imported) {
			t.Fatalf("the imported %s list differs from its source's: %d against %d", list.name, len(imported), len(source))
		}
	}
	for _, id := range listedAuditEvents {
		if !rowExists(t, destination, `SELECT 1 FROM audit_events WHERE id=$1`, id) {
			t.Fatal("the import lost a listed audit event")
		}
	}
	if len(listedIDs(t, f, listedReleases, ws, env.Id)) != 20 || len(listedIDs(t, f, listedRevisions, ws, env.Id)) != 50 {
		t.Fatal("the fixture no longer fills the release and revision lists")
	}
	for _, unlisted := range []struct{ table, id string }{{"audit_events", oldestAudit.String()}, {"specification_revisions", oldestRevision.String()}, {"execution_operations", oldestOperation.String()}} {
		if rowExists(t, destination, `SELECT 1 FROM `+unlisted.table+` WHERE id=$1`, unlisted.id) {
			t.Fatal("the export carried history the app doesn't list", unlisted.table)
		}
	}
	if !rowExists(t, destination, `SELECT 1 FROM secret_versions WHERE id=$1`, pinned.Id) || !rowExists(t, destination, `SELECT 1 FROM secret_versions v JOIN secrets s ON s.id=v.secret_id WHERE s.id=$1 AND v.id<>$2`, pinned.SecretId, pinned.Id) {
		t.Fatal("the import lost a version of a secret")
	}
	if !rowExists(t, destination, `SELECT 1 FROM build_artifacts WHERE id=$1`, pinnedArtifact) || !rowExists(t, destination, `SELECT 1 FROM build_artifacts WHERE id=$1`, imageArtifact) || !rowExists(t, destination, `SELECT 1 FROM specification_revisions WHERE id=$1`, oldBuild) {
		t.Fatal("the import lost the build artifact a listed revision pins, or the revision its build names")
	}
	if rowExists(t, destination, `SELECT 1 FROM build_artifacts WHERE id=$1`, buildHistory) {
		t.Fatal("the export followed build history: an artifact only an old build's revision pins")
	}
}
