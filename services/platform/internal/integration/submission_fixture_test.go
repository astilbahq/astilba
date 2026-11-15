package integration

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline/pipelinetest"
	"github.com/google/uuid"
)

// submissionFixture is a workspace whose environments import from a Gitea provider that answers
// reads and fails every write, so each submission's first attempt fails and waits to be retried.
// With a forge, the provider is that forge instead, and takes submissions.
type submissionFixture struct {
	f       *fixture
	owner   *browser
	ctx     context.Context
	prefix  string
	project api.Project
	source  pipeline.Source
	runner  authorityAgent
	tree    gitTree
	forge   *pipelinetest.Forge
	head    string
	// writes counts the provider's refused writes, and requests every request it was sent.
	writes, requests atomic.Int64
}

// submissionOriginal is the compose file every fixture environment imports.
const submissionOriginal = "services:\n  web:\n    image: " + pipelineTestImage + "\n"

func newSubmissionFixture(t *testing.T, ctx context.Context) *submissionFixture {
	t.Helper()
	return newSubmissionFixtureWith(t, ctx, false)
}

// newSubmissionFixtureWith is newSubmissionFixture, whose provider is a forge holding
// submissionOriginal when forge is set.
func newSubmissionFixtureWith(t *testing.T, ctx context.Context, forge bool) *submissionFixture {
	t.Helper()
	f := setup(t)
	s := &submissionFixture{f: f, owner: f.browser(t), ctx: ctx, head: strings.Repeat("a", 40)}
	ws := s.owner.login("owner@example.test", f.password).Workspaces[0].Id
	s.prefix = "/api/v1/workspaces/" + ws.String()
	writer, reader := "submit-write-canary", "submit-read-canary"
	if forge {
		s.forge, s.head = pipelinetest.NewForge("gitea", map[string]pipelinetest.File{"compose.yaml": {Content: submissionOriginal, Mode: "100644"}})
		writer, reader = pipelinetest.Writer, pipelinetest.Reader
	}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.requests.Add(1)
		if s.forge != nil {
			s.forge.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/team/app.git/info/refs" {
			w.WriteHeader(403)
			return
		}
		if r.Method != http.MethodGet {
			s.writes.Add(1)
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if s.tree.answer(w, r) {
			return
		}
		if strings.Contains(r.URL.Path, "/branches/") {
			if strings.Contains(r.URL.Path, "/branches/astilba/") {
				w.WriteHeader(404)
				return
			}
			answerBranch(w, "gitea", s.head)
			return
		}
		answerRepository(w, "gitea", 12, true)
	}))
	t.Cleanup(provider.Close)
	var connection struct {
		Connection pipeline.Connection `json:"connection"`
	}
	s.owner.call("POST", s.prefix+"/git-connections", map[string]any{"name": "Source", "provider": "gitea", "baseUrl": provider.URL, "repository": "team/app", "api": map[string]string{"username": "writer", "password": writer}, "clone": map[string]string{"username": "reader", "password": reader}}, 201, &connection)
	s.owner.call("POST", s.prefix+"/projects", map[string]string{"name": "Submissions"}, 201, &s.project)
	enrolled, in, token := enrollment(t, f, s.owner, ws, "build")
	yes := true
	in.Capabilities.Build = &yes
	in.Capabilities.BuildNetwork = &yes
	in.Capabilities.RootlessBuilds = &yes
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
	s.runner = authorityAgent{t, f, token, in}
	s.source = pipeline.Source{ConnectionID: connection.Connection.ID, Branch: "main", RunnerID: enrolled.Server.Id, Paths: []string{"compose.yaml"}, Entrypoints: []string{"compose.yaml"}}
	return s
}

// prepare leaves an imported environment with plant's change in its saved draft.
func (s *submissionFixture) prepare(t *testing.T, name string, plant func(*composeproject.Bundle), baseline ...composeproject.File) (string, api.Revision) {
	t.Helper()
	b := *s.owner
	b.t = t
	var env api.Environment
	b.call("POST", s.prefix+"/projects/"+s.project.Id.String()+"/environments", map[string]string{"name": name, "configurationOwner": "compose"}, 201, &env)
	path := s.prefix + "/projects/" + s.project.Id.String() + "/environments/" + env.Id.String()
	source := s.source
	source.Paths = append([]string{}, source.Paths...)
	for _, file := range baseline {
		source.Paths = append(source.Paths, file.Path)
	}
	files := append([]composeproject.File{{Path: "compose.yaml", Content: submissionOriginal}}, baseline...)
	s.tree.record(s.head, files)
	var imported api.Operation
	b.call("PUT", path+"/source", map[string]any{"baseRevisionId": env.DraftRevisionId, "configuration": source}, 202, &imported)
	s.runner.t = t
	command := s.runner.next()
	if command.Id != imported.Id {
		t.Fatal("import did not reach the runner")
	}
	s.runner.reportImport(command, s.head, files)
	var draft api.Revision
	b.call("GET", path+"/draft", nil, 200, &draft)
	var spec domain.Specification
	if err := json.Unmarshal(draft.Specification, &spec); err != nil {
		t.Fatal(err)
	}
	plant(spec.Compose)
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": draft.Id, "specification": spec}, 200, &draft)
	return path, draft
}

// submit proposes the saved draft as by, the owner when by is nil.
func (s *submissionFixture) submit(t *testing.T, by *browser, path string, draft api.Revision) api.SourceSubmission {
	t.Helper()
	if by == nil {
		by = s.owner
	}
	b := *by
	b.t = t
	var out api.SourceSubmission
	b.call("POST", path+"/source/submit", map[string]any{"baseRevisionId": draft.Id, "requestKey": uuid.New()}, 202, &out)
	return out
}

// await waits until done accepts the submission's state and note.
func (s *submissionFixture) await(t *testing.T, id uuid.UUID, done func(state, note string) bool) (string, string) {
	t.Helper()
	var state, note string
	for deadline := time.Now().Add(45 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if err := s.f.st.Pool.QueryRow(s.ctx, "SELECT state,note FROM source_submissions WHERE id=$1", id).Scan(&state, &note); err != nil {
			t.Fatal(err)
		}
		if done(state, note) {
			return state, note
		}
		if time.Now().After(deadline) {
			t.Fatal("the submission did not settle", state, note)
		}
	}
}

func submissionRetrying(state, note string) bool {
	return state == "queued" && strings.HasPrefix(note, "Attempt 1 of")
}

func submissionFinished(state, _ string) bool { return state != "queued" }
