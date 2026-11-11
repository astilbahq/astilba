package integration

import (
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

// A real Gitea, not a fixture of one, takes a connection through import, a build, a pushed
// commit's webhook, the runner's report and automatic deployment. Fixtures that named a
// branch's commit sha, which Gitea never does, had hidden that no Gitea connection could
// start an import or a build. The fixtures are then checked against what this Gitea answers.
func TestGiteaRunsTheGitPathAsFixturesDo(t *testing.T) {
	g := startGitea(t)
	first := g.commit("First", importHonest, true)

	t.Run("import, build, webhook, report and deployment", func(t *testing.T) {
		f := setup(t)
		b := f.browser(t)
		ws, prefix, env, path := composeEnvironment(t, f, b)
		var created struct {
			Connection    pipeline.Connection `json:"connection"`
			WebhookSecret string              `json:"webhookSecret"`
		}
		b.call("POST", prefix+"/git-connections", map[string]any{"name": "Gitea", "provider": "gitea", "baseUrl": g.base, "repository": "owner/app", "api": map[string]string{"username": "owner", "password": g.write}, "clone": map[string]string{"username": "owner", "password": g.read}}, 201, &created)
		if !created.Connection.Private || created.Connection.RepositoryID < 1 {
			t.Fatalf("the connection did not take Gitea's repository identity: %+v", created.Connection)
		}
		g.hook(created.WebhookSecret)

		yes := true
		enrolled, in, token := enrollment(t, f, b, ws, "build")
		in.Capabilities.Build = &yes
		in.Capabilities.BuildNetwork = &yes
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
		runner := authorityAgent{t, f, token, in}
		server, deployIn, deployToken := enrollment(t, f, b, ws, "deployment")
		deployIn.Capabilities.Visual, deployIn.Capabilities.Compose = &yes, &yes
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", deployIn, 200, nil)
		deploy := authorityAgent{t, f, deployToken, deployIn}

		b.call("PUT", path+"/source", map[string]any{"baseRevisionId": env.DraftRevisionId, "configuration": pipeline.Source{ConnectionID: created.Connection.ID, Branch: "main", RunnerID: enrolled.Server.Id, Paths: importPaths, Entrypoints: []string{"compose.yaml"}}}, 202, nil)
		c := runner.next()
		if command := pipelineCommand(t, c); command.Action != "import" || command.Commit != first || command.Access == nil || command.Access.Clone.Password != g.read {
			t.Fatalf("the import is not of Gitea's head with the clone credential: %s %s", command.Action, command.Commit)
		}
		runner.reportImport(c, first, g.files(first, importPaths))
		draft, spec := draftSpec(t, b, path)
		if draft.Id == env.DraftRevisionId || spec.Compose.Origin.Commit != first {
			t.Fatal("the files Gitea holds were not imported")
		}
		cfg := pipeline.BuildConfig{ConnectionID: created.Connection.ID, Branch: "main", RunnerID: enrolled.Server.Id, Context: ".", Dockerfile: "Dockerfile", Architecture: "arm64", Arguments: map[string]string{}, Secrets: map[string]uuid.UUID{}}
		var build api.Operation
		b.call("POST", path+"/builds", map[string]any{"baseRevisionId": draft.Id, "applicationId": spec.Applications[0].ID, "requestKey": uuid.New(), "configuration": cfg}, 202, &build)
		c = runner.next()
		if command := pipelineCommand(t, c); c.Id != build.Id || command.Action != "build" || command.Commit != first {
			t.Fatal("the build is not of Gitea's head", command.Action, command.Commit)
		}
		var op api.Operation
		runner.finishPipeline(c, "succeeded", pipeline.Result{Commit: first, Architecture: "arm64", Image: "astilba-build-" + c.Id.String() + ":built", Digest: "sha256:" + strings.Repeat("d", 64)})
		if b.call("GET", path+"/operations/"+c.Id.String(), nil, 200, &op); op.State != "succeeded" {
			t.Fatalf("the build of Gitea's head was refused: %s %q", op.State, op.Note)
		}
		// Deployed once by a person, so the next import's policy deploys without one.
		recordReviewedDeployment(t, f, ws, env.Id, draft.Id, server.Server.Id, nil)
		b.call("PUT", path+"/source/policy", map[string]bool{"autoDeploy": true}, 204, nil)

		next := []composeproject.File{{Path: "compose.yaml", Content: "# next\n" + importHonest[0].Content}}
		second := g.commit("Second", next, false)
		if status := g.nextDelivery().deliver(t, f, created.Connection.ID); status != 202 {
			t.Fatal("Gitea's own webhook delivery was refused", status)
		}
		c = runner.next()
		if command := pipelineCommand(t, c); command.Action != "import" || command.Commit != second {
			t.Fatal("the push did not import the commit Gitea now holds", command.Action, command.Commit)
		}
		honest := g.files(second, importPaths)
		runner.reportImport(c, second, withForgedService(honest))
		b.call("GET", path+"/operations/"+c.Id.String(), nil, 200, &op)
		if op.State != "failed" || !strings.Contains(op.Note, "differ from what the Git provider holds") {
			t.Fatalf("files Gitea does not hold were accepted: %s %q", op.State, op.Note)
		}
		var refresh api.Operation
		b.call("POST", path+"/source/refresh", map[string]any{"baseRevisionId": draft.Id}, 202, &refresh)
		c = runner.next()
		runner.reportImport(c, second, honest)
		if b.call("GET", path+"/operations/"+c.Id.String(), nil, 200, &op); op.State != "succeeded" {
			t.Fatalf("the files Gitea holds were refused: %s %q", op.State, op.Note)
		}
		if resolve := deploy.next(); resolve.ServerId != server.Server.Id || resolve.Kind != "visual" {
			t.Fatal("the import did not reach automatic deployment", resolve.ServerId, resolve.Kind)
		}

	})

	// What Gitea answers differently from the other providers: lists that ignore head and base,
	// a fork's request from a branch named like a submission's, and owners only some can see.
	t.Run("provider", func(t *testing.T) {
		var repo struct {
			ID int64 `json:"id"`
		}
		g.call("GET", "/repos/owner/app", nil, 200, &repo)
		connect := func(repository string, id int64, private bool) *pipeline.Provider {
			p, err := pipeline.NewProvider(pipeline.Connection{Provider: "gitea", BaseURL: g.base, Repository: repository, RepositoryID: id, Private: private}, pipeline.Credential{Password: g.write}, pipeline.Policy{Development: true})
			if err != nil {
				t.Fatal(err)
			}
			return p
		}
		p := connect("owner/app", repo.ID, true)
		file := func(name string) []composeproject.File {
			return []composeproject.File{{Path: name + ".txt", Content: name + "\n"}}
		}
		g.commitTo(g.admin, "owner/app", "astilba/ours", "main", "Ours", file("ours"), true)
		ours, err := p.Review(t.Context(), "main", "astilba/ours")
		if err != nil || ours.Number < 1 {
			t.Fatal("cannot open a review", err)
		}
		// More open requests than Gitea lists on a page, opened after it.
		for n := range 31 {
			name := "feature-" + strconv.Itoa(n)
			g.commitTo(g.admin, "owner/app", name, "main", name, file(name), true)
			g.call("POST", "/repos/owner/app/pulls", map[string]string{"title": name, "base": "main", "head": name}, 201, nil)
		}
		g.call("POST", "/repos/owner/app/branches", map[string]string{"new_branch_name": "other", "old_branch_name": "main"}, 201, nil)
		g.commitTo(g.admin, "owner/app", "to-other", "other", "Other", file("to-other"), true)
		var other struct {
			Number int `json:"number"`
		}
		g.call("POST", "/repos/owner/app/pulls", map[string]string{"title": "Other", "base": "other", "head": "to-other"}, 201, &other)
		if again, err := p.Review(t.Context(), "main", "astilba/ours"); err != nil || again != ours {
			t.Fatalf("the open review was not found again: %+v %v", again, err)
		}

		outsider := g.user("outsider", false)
		g.call("PUT", "/repos/owner/app/collaborators/outsider", map[string]string{"permission": "read"}, 204, nil)
		g.callAs(outsider, "POST", "/repos/owner/app/forks", map[string]any{}, 202, nil)
		g.commitTo(outsider, "outsider/app", "astilba/theirs", "main", "Theirs", file("theirs"), true)
		var theirs struct {
			Number int `json:"number"`
		}
		g.callAs(outsider, "POST", "/repos/owner/app/pulls", map[string]string{"title": "Theirs", "base": "main", "head": "outsider:astilba/theirs"}, 201, &theirs)
		g.commitTo(g.admin, "owner/app", "astilba/theirs", "main", "Ours too", file("ours-too"), true)
		if review, err := p.Review(t.Context(), "main", "astilba/theirs"); err != nil || review.Number == theirs.Number {
			t.Fatalf("a fork's request was taken for this repository's review: %+v %v", review, err)
		}

		open, err := p.OpenPullRequests(t.Context(), "main", 1)
		if err != nil || len(open) < 33 || open[0] != ours.Number || !slices.IsSorted(open) || slices.Contains(open, other.Number) {
			t.Fatalf("discovery did not page main's requests oldest first: %v %v", open, err)
		}
		var merged struct {
			Number int `json:"number"`
		}
		g.commitTo(g.admin, "owner/app", "to-merge", "main", "Merged", file("merged"), true)
		g.call("POST", "/repos/owner/app/pulls", map[string]string{"title": "Merged", "base": "main", "head": "to-merge"}, 201, &merged)
		g.call("POST", "/repos/owner/app/pulls/"+strconv.Itoa(merged.Number)+"/merge", map[string]string{"Do": "merge"}, 200, nil)
		head, err := p.Resolve(t.Context(), "main")
		if err != nil {
			t.Fatal(err)
		}
		if commit, err := p.MergedCommit(t.Context(), merged.Number); err != nil || commit != head {
			t.Fatalf("merged as %q, not the branch's %q: %v", commit, head, err)
		}

		for visibility, private := range map[string]bool{"public": false, "limited": true, "private": true} {
			g.call("POST", "/orgs", map[string]string{"username": visibility + "-team", "visibility": visibility}, 201, nil)
			g.call("POST", "/orgs/"+visibility+"-team/repos", map[string]any{"name": "lib", "private": false, "auto_init": true}, 201, &repo)
			if _, got, err := connect(visibility+"-team/lib", repo.ID, private).Repository(t.Context()); err != nil || got != private {
				t.Errorf("a public repository of a %s owner read as private=%t: %v", visibility, got, err)
			}
		}
	})

	t.Run("fixtures", func(t *testing.T) {
		var tree gitTree
		tree.record(first, importHonest)
		trees := httptest.NewRecorder()
		tree.answer(trees, httptest.NewRequest("GET", "/api/v1/repos/owner/app/git/trees/"+first, nil))
		var fixtureTree any
		_ = json.Unmarshal(trees.Body.Bytes(), &fixtureTree)
		g.commitTo(g.admin, "owner/app", "fixture", "main", "Fixture", []composeproject.File{{Path: "fixture.txt", Content: "fixture\n"}}, true)
		var pull struct {
			Number int `json:"number"`
			Head   struct {
				SHA string `json:"sha"`
			} `json:"head"`
		}
		g.call("POST", "/repos/owner/app/pulls", map[string]string{"title": "Fixture", "base": "main", "head": "fixture"}, 201, &pull)
		pulls := "/repos/owner/app/pulls/" + strconv.Itoa(pull.Number)
		fixtures := []struct {
			path    string
			fixture any
		}{
			{"/repos/owner/app", repositoryAnswer("gitea", 1, true)},
			{"/repos/owner/app/branches/main", branchAnswer("gitea", first)},
			{"/repos/owner/app/git/trees/" + first, fixtureTree},
			{pulls, pullAnswer(pull.Number, "open", 1, pull.Head.SHA, 1)},
			{pulls, mergedAnswer(pull.Number, "")},
			{"/repos/owner/app/pulls?state=open", []map[string]int{{"number": pull.Number}}},
		}
		compare := func(path string, fixture any) {
			t.Helper()
			var real any
			if err := json.Unmarshal(g.call("GET", path, nil, http.StatusOK, nil), &real); err != nil {
				t.Fatal(err)
			}
			raw, _ := stablejson.Marshal(fixture)
			var answer any
			_ = json.Unmarshal(raw, &answer)
			if missing := unanswered(answer, real, ""); len(missing) > 0 {
				t.Errorf("the fixture for %s names what Gitea does not: %v", path, missing)
			}
		}
		for _, f := range fixtures {
			compare(f.path, f.fixture)
		}
		g.call("POST", pulls+"/merge", map[string]string{"Do": "merge"}, 200, nil)
		var merged struct {
			MergeCommit string `json:"merge_commit_sha"`
		}
		g.call("GET", pulls, nil, 200, &merged)
		compare(pulls, mergedAnswer(pull.Number, merged.MergeCommit))
	})
}

func pipelineCommand(t *testing.T, c api.ExecutionCommand) pipeline.Command {
	t.Helper()
	var command pipeline.Command
	if c.Pipeline == nil || json.Unmarshal(*c.Pipeline, &command) != nil {
		t.Fatal("expected a pipeline command", c.Kind)
	}
	return command
}

// unanswered lists the fields a fixture answers that the real answer lacks or gives another
// kind of value. A list is compared through its first entry.
func unanswered(fixture, real any, at string) []string {
	missing := []string{}
	switch f := fixture.(type) {
	case map[string]any:
		r, ok := real.(map[string]any)
		if !ok {
			return []string{at}
		}
		for key, value := range f {
			inner, found := r[key]
			if !found {
				missing = append(missing, at+"."+key)
				continue
			}
			missing = append(missing, unanswered(value, inner, at+"."+key)...)
		}
	case []any:
		r, ok := real.([]any)
		if !ok {
			return []string{at}
		}
		if len(f) > 0 && len(r) > 0 {
			missing = append(missing, unanswered(f[0], r[0], at+"[]")...)
		}
	case string, float64, bool:
		if real == nil || kindOf(fixture) != kindOf(real) {
			return []string{at}
		}
	}
	slices.Sort(missing)
	return missing
}

func kindOf(v any) string {
	switch v.(type) {
	case string:
		return "string"
	case float64:
		return "number"
	case bool:
		return "bool"
	}
	return "other"
}
