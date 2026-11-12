package integration

import (
	"crypto/sha256"
	"slices"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

// guessedError is the error a save or plan answered with, without the request ID every answer has
// its own of.
func guessedError(t *testing.T, f *fixture, token, method, path string, body any, status int) api.Error {
	t.Helper()
	var out api.Error
	bearerCall(t, f, token, method, path, body, status, &out)
	out.RequestId = ""
	return out
}

func sameAnswers(t *testing.T, what string, answers map[string]api.Error) {
	t.Helper()
	var first *api.Error
	var firstName string
	for name, answer := range answers {
		if answer.CurrentRevision != nil {
			t.Fatalf("%s: the %s guess was shown the current draft", what, name)
		}
		if first == nil {
			first, firstName = &answer, name
			continue
		}
		a, _ := stablejson.Marshal(*first)
		b, _ := stablejson.Marshal(answer)
		if string(a) != string(b) {
			t.Fatalf("%s: the %s guess answered %s and the %s guess %s", what, firstName, a, name, b)
		}
	}
}

func invalidField(t *testing.T, e api.Error, field string) {
	t.Helper()
	if e.Code != "invalid_input" || e.Fields == nil || !slices.Contains(*e.Fields, field) {
		t.Fatalf("answered %+v, wanted invalid %s", e, field)
	}
}

// A credential that may change the configuration but not read it can't use a draft save to find out
// whether the environment is Compose-owned, or which Git source its draft was imported from: without
// the current draft's revision, a right guess and a wrong one get the same conflict. Principals that
// may read keep their errors.
func TestWriteOnlySaveCannotConfirmGuessesAboutTheDraft(t *testing.T) {
	f := setup(t)
	b, _, plain, plainPath := automationEnvironment(t, f)
	environments := strings.TrimSuffix(plainPath, "/"+plain.Id.String())
	var composed api.Environment
	b.call("POST", environments, map[string]string{"name": "Imported", "configurationOwner": "compose"}, 201, &composed)
	composedPath := environments + "/" + composed.Id.String()

	// The Compose draft records the Git source it was imported from, as an import leaves it.
	origin := composeproject.Origin{Provider: "github", Repository: "https://git.example.test/acme/private-app", Commit: strings.Repeat("a1", 20)}
	bundle := func(namespace uuid.UUID, o *composeproject.Origin) domain.Specification {
		return domain.Specification{Version: 1, Applications: []domain.Application{}, Variables: []domain.Variable{}, Compose: &composeproject.Bundle{Namespace: namespace, Entrypoints: []string{"compose.yaml"}, Files: []composeproject.File{{Path: "compose.yaml", Content: "services: {}\n"}}, Origin: o}}
	}
	raw, err := stablejson.Marshal(bundle(composed.DeploymentUnit.Id, &origin))
	if err != nil {
		t.Fatal(err)
	}
	_, canonical, err := domain.ParseSpec(raw)
	if err != nil {
		t.Fatal(err)
	}
	imported, hash := uuid.New(), sha256.Sum256(canonical)
	if _, err = f.st.Pool.Exec(t.Context(), "INSERT INTO specification_revisions(workspace_id,environment_id,id,author_id,specification,content_hash) SELECT workspace_id,environment_id,$2,author_id,$3,$4 FROM specification_revisions WHERE id=$1", composed.DraftRevisionId, imported, canonical, hash[:]); err != nil {
		t.Fatal(err)
	}
	if _, err = f.st.Pool.Exec(t.Context(), "UPDATE environments SET draft_revision_id=$2 WHERE id=$1", composed.Id, imported); err != nil {
		t.Fatal(err)
	}
	empty := domain.Specification{Version: 1, Applications: []domain.Application{}, Variables: []domain.Variable{}}
	var plainDraft api.Revision
	b.call("PUT", plainPath+"/draft", map[string]any{"baseRevisionId": plain.DraftRevisionId, "specification": withVariable(t, mustJSON(t, empty), "SAVED")}, 200, &plainDraft)

	// Each base is one the credential could hold without being able to read the draft: a revision the
	// draft has since moved on from, and one that never existed.
	otherCommit := origin
	otherCommit.Commit = strings.Repeat("b2", 20)
	otherRepository := origin
	otherRepository.Repository = "https://git.example.test/acme/other-app"
	composeWriter := issueAutomation(t, b, composedPath, domain.ConfigurationWrite)
	plainWriter := issueAutomation(t, b, plainPath, domain.ConfigurationWrite)
	for _, stale := range []struct{ compose, plain uuid.UUID }{{composed.DraftRevisionId, plain.DraftRevisionId}, {uuid.New(), uuid.New()}} {
		save := func(spec domain.Specification) api.Error {
			return guessedError(t, f, *composeWriter.Token, "PUT", composedPath+"/draft", api.DraftUpdate{BaseRevisionId: stale.compose, Specification: mustJSON(t, spec)}, 409)
		}
		sameAnswers(t, "Compose source", map[string]api.Error{
			"right":                save(bundle(composed.DeploymentUnit.Id, &origin)),
			"wrong commit":         save(bundle(composed.DeploymentUnit.Id, &otherCommit)),
			"wrong repository":     save(bundle(composed.DeploymentUnit.Id, &otherRepository)),
			"no source":            save(bundle(composed.DeploymentUnit.Id, nil)),
			"wrong owner":          save(empty),
			"wrong unit namespace": save(bundle(uuid.New(), &origin)),
		})

		saveDashboard := func(spec domain.Specification) api.Error {
			return guessedError(t, f, *plainWriter.Token, "PUT", plainPath+"/draft", api.DraftUpdate{BaseRevisionId: stale.plain, Specification: mustJSON(t, spec)}, 409)
		}
		sameAnswers(t, "configuration owner", map[string]api.Error{
			"right": saveDashboard(empty),
			"wrong": saveDashboard(bundle(plain.DeploymentUnit.Id, nil)),
		})
	}

	// Naming the current draft, a write-only credential still saves, and a wrong source is refused.
	invalidField(t, guessedError(t, f, *composeWriter.Token, "PUT", composedPath+"/draft", api.DraftUpdate{BaseRevisionId: imported, Specification: mustJSON(t, bundle(composed.DeploymentUnit.Id, &otherCommit))}, 400), "compose.origin")
	var written api.Revision
	edited := bundle(composed.DeploymentUnit.Id, &origin)
	edited.Compose.Files[0].Content = "services: {}\n# edited\n"
	bearerCall(t, f, *composeWriter.Token, "PUT", composedPath+"/draft", api.DraftUpdate{BaseRevisionId: imported, Specification: mustJSON(t, edited)}, 200, &written)

	// A browser and a credential that may read keep their errors, and see the current draft on a conflict.
	reader := issueAutomation(t, b, composedPath, domain.EnvironmentRead, domain.ConfigurationWrite)
	plainReader := issueAutomation(t, b, plainPath, domain.EnvironmentRead, domain.ConfigurationWrite)
	var browserError api.Error
	b.call("PUT", composedPath+"/draft", map[string]any{"baseRevisionId": written.Id, "specification": bundle(composed.DeploymentUnit.Id, &otherCommit)}, 400, &browserError)
	invalidField(t, browserError, "compose.origin")
	b.call("PUT", composedPath+"/draft", map[string]any{"baseRevisionId": written.Id, "specification": empty}, 400, &browserError)
	invalidField(t, browserError, "configurationOwner")
	b.call("PUT", plainPath+"/draft", map[string]any{"baseRevisionId": plainDraft.Id, "specification": bundle(plain.DeploymentUnit.Id, nil)}, 400, &browserError)
	invalidField(t, browserError, "configurationOwner")
	invalidField(t, guessedError(t, f, *reader.Token, "PUT", composedPath+"/draft", api.DraftUpdate{BaseRevisionId: written.Id, Specification: mustJSON(t, bundle(composed.DeploymentUnit.Id, &otherCommit))}, 400), "compose.origin")
	invalidField(t, guessedError(t, f, *reader.Token, "PUT", composedPath+"/draft", api.DraftUpdate{BaseRevisionId: written.Id, Specification: mustJSON(t, empty)}, 400), "configurationOwner")
	invalidField(t, guessedError(t, f, *plainReader.Token, "PUT", plainPath+"/draft", api.DraftUpdate{BaseRevisionId: plainDraft.Id, Specification: mustJSON(t, bundle(plain.DeploymentUnit.Id, nil))}, 400), "configurationOwner")
	b.call("PUT", composedPath+"/draft", map[string]any{"baseRevisionId": imported, "specification": edited}, 409, &browserError)
	if browserError.CurrentRevision == nil || browserError.CurrentRevision.Id != written.Id {
		t.Fatal("a browser's conflict didn't show the current draft")
	}
	if e := guessedError(t, f, *reader.Token, "PUT", composedPath+"/draft", api.DraftUpdate{BaseRevisionId: imported, Specification: mustJSON(t, edited)}, 409); e.CurrentRevision == nil || e.CurrentRevision.Id != written.Id {
		t.Fatal("a reading credential's conflict didn't show the current draft")
	}
}

// A credential that may plan but not read can't use a plan request with a stale base to find out
// whether the environment's primary server was revoked.
func TestStalePlanCannotConfirmAReplacementIsNeeded(t *testing.T) {
	f := setup(t)
	b, _, env, path := automationEnvironment(t, f)
	planner := issueAutomation(t, b, path, domain.DeploymentPlan)
	plan := func(replacement *api.PrimaryReplacementRequest) api.Error {
		return guessedError(t, f, *planner.Token, "POST", path+"/deployment-plans", api.VisualPlanRequest{BaseRevisionId: uuid.New(), ServerId: uuid.New(), Replacement: replacement}, 409)
	}
	sameAnswers(t, "primary replacement", map[string]api.Error{
		"no replacement":   plan(nil),
		"replacement":      plan(&api.PrimaryReplacementRequest{AbandonedVolumeIds: []uuid.UUID{}}),
		"guessed a volume": plan(&api.PrimaryReplacementRequest{AbandonedVolumeIds: []uuid.UUID{uuid.New()}}),
	})
	// With the current draft named, the request is checked against the environment as before.
	invalidField(t, guessedError(t, f, *planner.Token, "POST", path+"/deployment-plans", api.VisualPlanRequest{BaseRevisionId: env.DraftRevisionId, ServerId: uuid.New(), Replacement: &api.PrimaryReplacementRequest{AbandonedVolumeIds: []uuid.UUID{}}}, 400), "replacement")
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := stablejson.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
