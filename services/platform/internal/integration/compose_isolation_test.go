package integration

import (
	"context"
	"crypto/sha256"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

// A shared host's isolation rules stop a file reaching a host, and say what to change, at
// every point a new draft or plan is made. A file saved before them keeps loading, or its
// environment could not be read, restored or fixed.
func TestComposeIsolationRulesApplyToNewDraftsAndPlans(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	prefix := "/api/v1/workspaces/" + ws.String()
	var project api.Project
	b.call("POST", prefix+"/projects", map[string]string{"name": "Isolation"}, 201, &project)
	var env api.Environment
	b.call("POST", prefix+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "Files", "configurationOwner": "compose"}, 201, &env)
	path := prefix + "/projects/" + project.Id.String() + "/environments/" + env.Id.String()
	var secret api.SecretVersion
	b.call("POST", path+"/secrets", map[string]string{"name": "TOKEN", "value": "service-11111111-1111-4111-8111-111111111111"}, 201, &secret)
	spec := func(content string) domain.Specification {
		return domain.Specification{Version: 1, Applications: []domain.Application{}, Variables: []domain.Variable{{Name: "TOKEN", SecretVersionID: &secret.Id}}, Compose: &composeproject.Bundle{Namespace: env.DeploymentUnit.Id, Entrypoints: []string{"compose.yaml"}, Files: []composeproject.File{{Path: "compose.yaml", Content: content}}}}
	}
	web := "services:\n  web:\n    image: " + execution.TestImage + "\n"
	refused := map[string]string{
		"change the logging driver": web + "    logging:\n      driver: syslog\n",
		"change this alias":         web + "    networks:\n      default:\n        aliases: [service-11111111-1111-4111-8111-111111111111]\n",
		"change this hostname":      web + "    hostname: astilba-x\n",
		"use a plain value":         web + "    hostname: \"${TOKEN}\"\n",
		"rename this service":       "services:\n  astilba-worker:\n    image: " + execution.TestImage + "\n",
	}
	var draft api.Revision
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": env.DraftRevisionId, "specification": spec(web)}, 200, &draft)
	for message, content := range refused {
		body := b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": draft.Id, "specification": spec(content)}, 400, nil)
		if !strings.Contains(string(body), message) {
			t.Fatalf("a refused draft did not say %q: %s", message, body)
		}
		var preview api.ComposePreview
		raw := b.call("POST", path+"/compose/preview", map[string]any{"specification": spec(content)}, 200, &preview)
		if !strings.Contains(string(raw), message) {
			t.Fatalf("a preview did not say %q: %s", message, raw)
		}
	}

	enrolled, in, _ := enrollment(t, f, b, ws, "deployment")
	yes := true
	in.Capabilities.Visual = &yes
	in.Capabilities.Compose = &yes
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)

	for message, content := range refused {
		raw, err := jsonSpecification(spec(content))
		if err != nil {
			t.Fatal(err)
		}
		saved, hash := uuid.New(), sha256.Sum256(raw)
		if _, err = f.st.Pool.Exec(context.Background(), "INSERT INTO specification_revisions(workspace_id,environment_id,id,author_id,specification,content_hash) SELECT workspace_id,environment_id,$2,author_id,$3,$4 FROM specification_revisions WHERE id=$1", draft.Id, saved, raw, hash[:]); err != nil {
			t.Fatal(err)
		}
		if _, err = f.st.Pool.Exec(context.Background(), "UPDATE environments SET draft_revision_id=$2 WHERE id=$1", env.Id, saved); err != nil {
			t.Fatal(err)
		}
		var read api.Revision
		b.call("GET", path+"/draft", nil, 200, &read)
		if read.Id != saved {
			t.Fatal("a stored draft could not be read")
		}
		body := b.call("POST", path+"/deployment-plans", api.VisualPlanRequest{BaseRevisionId: saved, ServerId: enrolled.Server.Id, AllowHostChanges: true}, 400, nil)
		if !strings.Contains(string(body), message) {
			t.Fatalf("a plan from a stored file did not say %q: %s", message, body)
		}
		draft = read
	}
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": draft.Id, "specification": spec(web)}, 200, &draft)
	b.call("POST", path+"/deployment-plans", api.VisualPlanRequest{BaseRevisionId: draft.Id, ServerId: enrolled.Server.Id, AllowHostChanges: true}, 201, nil)
}

// A Git import is a new draft: its author learns what to change from the import itself, and
// the draft stays as it was. A later commit that fixes the file imports as usual.
func TestGitImportOfARefusedFileNamesTheFix(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	ws, prefix, env, path := composeEnvironment(t, f, b)
	source := newGitSource(t, f, b, prefix)
	enrolled, in, agentToken := enrollment(t, f, b, ws, "combined")
	yes := true
	in.Capabilities.Build, in.Capabilities.Visual, in.Capabilities.Compose = &yes, &yes, &yes
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
	agent := authorityAgent{t, f, agentToken, in}
	web := "services:\n  web:\n    image: " + pipelineTestImage + "\n"
	long := strings.Repeat("x", 4096)
	tests := []struct{ file, note string }{
		{web + "    logging:\n      driver: syslog\n", "compose.yaml:5:7"},
		{web + "    ports: [\"\\0:80\"]\n", "needs a change"},
		{web + "    ports: [!!binary /w==]\n", "needs a change"},
		{web + "    user: \"${MISSING:?" + long + "}\"\n", "needs a change"},
	}
	// The repository holds each refused file at its own commit, and the fixed file at the last.
	commitHolding := func(content string) {
		source.tree.record(source.head.Load().(string), []composeproject.File{{Path: "compose.yaml", Content: content}})
	}
	commitHolding(tests[0].file)
	var op api.Operation
	b.call("PUT", path+"/source", map[string]any{"baseRevisionId": env.DraftRevisionId, "configuration": pipeline.Source{ConnectionID: source.connection.ID, Branch: "main", RunnerID: enrolled.Server.Id, Paths: []string{"compose.yaml"}, Entrypoints: []string{"compose.yaml"}}}, 202, &op)
	for commit, test := range tests {
		refused := agent.next()
		agent.finishPipeline(refused, "succeeded", pipeline.Result{Commit: source.head.Load().(string), Files: []composeproject.File{{Path: "compose.yaml", Content: test.file}}})
		var report api.Operation
		b.call("GET", path+"/operations/"+refused.Id.String(), nil, 200, &report)
		if report.State != "failed" || !strings.Contains(report.Note, test.note) || len(report.Note) > 1024 || strings.ContainsRune(report.Note, 0) || !utf8.ValidString(report.Note) {
			t.Fatalf("a refused import reported %s: %q", report.State, report.Note)
		}
		if commit == 0 && !strings.Contains(report.Note, "change the logging driver") {
			t.Fatalf("a refused import did not name the fix: %q", report.Note)
		}
		var draft api.Revision
		b.call("GET", path+"/draft", nil, 200, &draft)
		if draft.Id != env.DraftRevisionId {
			t.Fatal("a refused import moved the draft")
		}
		source.head.Store(strings.Repeat(strconv.Itoa(commit+1), 40))
		next := web
		if commit+1 < len(tests) {
			next = tests[commit+1].file
		}
		commitHolding(next)
		b.call("POST", path+"/source/refresh", map[string]any{"baseRevisionId": draft.Id}, 202, &op)
	}
	var draft api.Revision
	agent.finishPipeline(agent.next(), "succeeded", pipeline.Result{Commit: source.head.Load().(string), Files: []composeproject.File{{Path: "compose.yaml", Content: web}}})
	b.call("GET", path+"/draft", nil, 200, &draft)
	if draft.Id == env.DraftRevisionId {
		t.Fatal("a fixed import did not update the draft")
	}
}

// jsonSpecification is what a revision stores for a specification.
func jsonSpecification(spec domain.Specification) ([]byte, error) {
	raw, err := stablejson.Marshal(spec)
	if err != nil {
		return nil, err
	}
	_, canonical, err := domain.ParseSpec(raw)
	return canonical, err
}
