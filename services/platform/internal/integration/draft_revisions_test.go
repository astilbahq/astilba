package integration

import (
	"crypto/sha256"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

// withVariable is the specification with one more plain variable, for a save that changes the draft.
func withVariable(t *testing.T, raw jsontext.Value, name string) jsontext.Value {
	t.Helper()
	var spec domain.Specification
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	value := "set"
	spec.Variables = append(spec.Variables, domain.Variable{Name: name, Value: &value})
	out, err := stablejson.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// resaveDraft gives the draft a new revision holding spec, even when the draft already holds it, by
// moving it to another specification and back.
func resaveDraft(t *testing.T, b *browser, path string, base uuid.UUID, spec any) api.Revision {
	t.Helper()
	raw, err := stablejson.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	var moved api.Revision
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": base, "specification": withVariable(t, raw, "MOVED")}, 200, &moved)
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": moved.Id, "specification": jsontext.Value(raw)}, 200, &moved)
	return moved
}

// Saving the draft without changing it answers with the current draft and adds no revision, so
// repeated saves don't grow an environment's history. The comparison is of the documents: a stored
// content hash, which an import carries as the archive gave it, decides nothing.
func TestSavingAnUnchangedDraftAddsNoRevision(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	prefix := "/api/v1/workspaces/" + ws.String()
	var project api.Project
	b.call("POST", prefix+"/projects", map[string]string{"name": "Drafts"}, 201, &project)
	var env api.Environment
	b.call("POST", prefix+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "Saves"}, 201, &env)
	path := prefix + "/projects/" + project.Id.String() + "/environments/" + env.Id.String()
	image := "busybox:stable"
	spec := domain.Specification{Version: 1, Variables: []domain.Variable{}, Applications: []domain.Application{{ID: uuid.New(), Name: "Web", Source: domain.ApplicationSource{Kind: "image", Image: &image}}}}
	revisions := func() (count, saves int) {
		t.Helper()
		if err := f.st.Pool.QueryRow(t.Context(), "SELECT (SELECT count(*) FROM specification_revisions WHERE environment_id=$1),(SELECT count(*) FROM audit_events WHERE workspace_id=$2 AND action='draft.save')", env.Id, ws).Scan(&count, &saves); err != nil {
			t.Fatal(err)
		}
		return count, saves
	}

	var first api.Revision
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": env.DraftRevisionId, "specification": spec}, 200, &first)
	count, saves := revisions()
	// The draft as a client reads it back, and the same specification sent again, are both unchanged.
	var read api.Revision
	b.call("GET", path+"/draft", nil, 200, &read)
	for _, unchanged := range []any{read.Specification, spec} {
		var again api.Revision
		b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": first.Id, "specification": unchanged}, 200, &again)
		if again.Id != first.Id || again.Hash != first.Hash {
			t.Fatalf("an unchanged save answered with revision %s, not the draft %s", again.Id, first.Id)
		}
		if c, s := revisions(); c != count || s != saves {
			t.Fatalf("an unchanged save added %d revisions and %d audit events", c-count, s-saves)
		}
	}

	// A draft stored, as an import can store it, with a hash that matches another specification
	// doesn't make saving that specification a no-op.
	changed := withVariable(t, read.Specification, "CHANGED")
	_, canonical, err := domain.ParseSpec(changed)
	if err != nil {
		t.Fatal(err)
	}
	misleading, stored := sha256.Sum256(canonical), uuid.New()
	if _, err = f.st.Pool.Exec(t.Context(), "INSERT INTO specification_revisions(workspace_id,environment_id,id,author_id,specification,content_hash) SELECT workspace_id,environment_id,$2,author_id,specification,$3 FROM specification_revisions WHERE id=$1", first.Id, stored, misleading[:]); err != nil {
		t.Fatal(err)
	}
	if _, err = f.st.Pool.Exec(t.Context(), "UPDATE environments SET draft_revision_id=$2 WHERE id=$1", env.Id, stored); err != nil {
		t.Fatal(err)
	}
	count++
	var second api.Revision
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": stored, "specification": changed}, 200, &second)
	if c, s := revisions(); second.Id == stored || c != count+1 || s != saves+1 {
		t.Fatalf("a changed save answered with %s and added %d revisions and %d audit events", second.Id, c-count, s-saves)
	}
	// An unchanged save still has to name the current draft as its base.
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": stored, "specification": changed}, 409, nil)
}
