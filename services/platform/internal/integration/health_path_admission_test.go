package integration

import (
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

// A draft may not introduce a health path that would open a template action in a proxy
// route file. A draft already stored with one must stay loadable and replaceable, and an
// applied one must stay allowed, because an application outside a cross-host placement
// cannot differ from the applied specification.
func TestHealthPathTemplateActionIsRefusedOnlyWhenSaved(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	prefix := "/api/v1/workspaces/" + ws.String()
	var project api.Project
	b.call("POST", prefix+"/projects", map[string]string{"name": "Health"}, 201, &project)
	var env api.Environment
	b.call("POST", prefix+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "Paths"}, 201, &env)
	path := prefix + "/projects/" + project.Id.String() + "/environments/" + env.Id.String()
	app, image := uuid.New(), "busybox:stable"
	spec := func(health string) domain.Specification {
		return domain.Specification{Version: 1, Variables: []domain.Variable{}, Applications: []domain.Application{{ID: app, Name: "Web", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Health: &domain.HealthCheck{Port: 8080, Path: health}}}}
	}
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": env.DraftRevisionId, "specification": spec("/{{x}}")}, 400, nil)

	raw, err := stablejson.Marshal(spec("/{{x}}"))
	if err != nil {
		t.Fatal(err)
	}
	stored := uuid.New()
	if _, err = f.st.Pool.Exec(t.Context(), "INSERT INTO specification_revisions(workspace_id,environment_id,id,author_id,specification,content_hash) VALUES($1,$2,$3,$4,$5,$6)", ws, env.Id, stored, f.owner, raw, make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	if _, err = f.st.Pool.Exec(t.Context(), "UPDATE environments SET draft_revision_id=$1 WHERE id=$2", stored, env.Id); err != nil {
		t.Fatal(err)
	}
	b.call("PUT", path+"/layout", map[string]any{"positions": map[string]any{app.String(): map[string]int{"x": 1, "y": 2}}}, 200, nil)
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": stored, "specification": spec("/{{x}}")}, 400, nil)

	if _, err = f.st.Pool.Exec(t.Context(), "UPDATE environments SET applied_revision_id=$1 WHERE id=$2", stored, env.Id); err != nil {
		t.Fatal(err)
	}
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": stored, "specification": spec("/{{y}}")}, 400, nil)
	var left api.Revision
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": stored, "specification": spec("/ready")}, 200, &left)
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": left.Id, "specification": spec("/{{x}}")}, 200, nil)
}
