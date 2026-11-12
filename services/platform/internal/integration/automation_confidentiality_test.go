package integration

import (
	"bytes"
	"crypto/sha256"
	"encoding/json/v2"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/automationclient"
	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// withoutConfiguration fails if a response to a credential that may not read the environment carries
// any of the configuration it must not see.
func withoutConfiguration(t *testing.T, what string, raw []byte, configuration ...string) {
	t.Helper()
	for _, value := range append(configuration, `"specification"`) {
		if bytes.Contains(raw, []byte(value)) {
			t.Fatalf("%s showed %s to a credential that may not read the environment", what, value)
		}
	}
}

// A plan carries the draft it deploys, so a credential that may plan or apply but not read the
// environment gets only what review and apply need: the plan's hash, effects and image digests.
func TestPlanAndApplyCredentialsReceiveNoConfiguration(t *testing.T) {
	f := setup(t)
	b, scope, env, path := automationEnvironment(t, f)
	const canary = "plaintext-configuration-canary"
	app, image, value := uuid.New(), "busybox:stable", canary
	spec := domain.Specification{Version: 1, Variables: []domain.Variable{}, Applications: []domain.Application{{ID: app, Name: "Web", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Variables: []domain.Variable{{Name: "GREETING", Value: &value}}}}}
	var draft api.Revision
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": env.DraftRevisionId, "specification": spec}, 200, &draft)
	enrolled, in, agentToken := enrollment(t, f, b, scope.WorkspaceId, "deployment")
	yes := true
	in.Capabilities.Visual = &yes
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
	agent := authorityAgent{t, f, agentToken, in}
	images := map[string]string{app.String(): "busybox@sha256:" + strings.Repeat("a", 64)}

	planner := *issueAutomation(t, b, path, domain.DeploymentPlan).Token
	applier := *issueAutomation(t, b, path, domain.DeploymentApply).Token
	reader := *issueAutomation(t, b, path, domain.EnvironmentRead, domain.DeploymentPlan).Token

	var created deployment.Status
	withoutConfiguration(t, "preparing a plan", bearerCall(t, f, planner, "POST", path+"/deployment-plans", api.VisualPlanRequest{ServerId: enrolled.Server.Id, BaseRevisionId: draft.Id}, 201, &created), canary)
	agent.observe(agent.next(), deployment.Observation{Images: images, StateHash: strings.Repeat("f", 64), HostChangesReported: true})

	var full deployment.Status
	bearerCall(t, f, reader, "GET", path+"/deployment-plans/"+created.ID.String(), nil, 200, &full)
	if full.Plan == nil || full.Review == nil || full.Review.Hash != full.Plan.Hash || len(full.Plan.Specification.Applications) != 1 {
		t.Fatalf("a credential that may read lost the plan: %+v", full)
	}
	for name, token := range map[string]string{"plan-only": planner, "apply-only": applier} {
		var status deployment.Status
		raw := bearerCall(t, f, token, "GET", path+"/deployment-plans/"+created.ID.String(), nil, 200, &status)
		withoutConfiguration(t, "a "+name+" credential's plan", raw, canary, "GREETING")
		if status.Plan != nil || status.State != "ready" || status.Review == nil {
			t.Fatalf("a %s credential's plan was %+v", name, status)
		}
		review := *status.Review
		if review.ID != created.ID || review.RevisionID != draft.Id || review.ServerID != enrolled.Server.Id || review.Hash != full.Plan.Hash || len(review.Effects) == 0 || !slices.Equal(review.Effects, full.Plan.Effects) || review.Images[app.String()] != images[app.String()] {
			t.Fatalf("a %s credential's review is missing what review and apply need: %+v", name, review)
		}
	}

	// The apply-only credential applies what it reviewed.
	var operation api.Operation
	withoutConfiguration(t, "applying a plan", bearerCall(t, f, applier, "POST", path+"/deployment-plans/"+created.ID.String()+"/apply", api.ApplyVisualPlan{IdempotencyKey: uuid.New()}, 201, &operation), canary)
	if deployed := agent.applied(agent.next(), images); deployed.ID != created.ID {
		t.Fatal("the apply-only credential deployed another plan")
	}
	withoutConfiguration(t, "an applied plan's operation", bearerCall(t, f, applier, "GET", path+"/operations/"+operation.Id.String(), nil, 200, nil), canary)
}

// A Compose plan's review shows the host permissions its file asks for, but not the file.
func TestComposePlanReviewShowsPermissionsWithoutTheFile(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	ws, _, env, path := composeEnvironment(t, f, b)
	const canary = "compose-file-canary"
	content := "services:\n  web:\n    image: " + execution.TestImage + "\n    cap_add: [NET_ADMIN]\n    environment:\n      GREETING: " + canary + "\n"
	spec := domain.Specification{Version: 1, Applications: []domain.Application{}, Variables: []domain.Variable{}, Compose: &composeproject.Bundle{Entrypoints: []string{"compose.yaml"}, Files: []composeproject.File{{Path: "compose.yaml", Content: content}}}}
	var draft api.Revision
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": env.DraftRevisionId, "specification": spec}, 200, &draft)
	enrolled, in, agentToken := enrollment(t, f, b, ws, "deployment")
	yes := true
	in.Capabilities.Visual, in.Capabilities.Compose = &yes, &yes
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
	agent := authorityAgent{t, f, agentToken, in}
	var saved domain.Specification
	if err := json.Unmarshal(draft.Specification, &saved); err != nil || len(saved.Applications) != 1 {
		t.Fatal("the Compose draft has no service", err)
	}
	images := map[string]string{saved.Applications[0].ID.String(): execution.TestImage}

	planner := *issueAutomation(t, b, path, domain.DeploymentPlan).Token
	var status deployment.Status
	bearerCall(t, f, planner, "POST", path+"/deployment-plans", api.VisualPlanRequest{ServerId: enrolled.Server.Id, BaseRevisionId: draft.Id}, 201, &status)
	agent.observe(agent.next(), deployment.Observation{Images: images, StateHash: strings.Repeat("f", 64), HostChangesReported: true})
	raw := bearerCall(t, f, planner, "GET", path+"/deployment-plans/"+status.ID.String(), nil, 200, &status)
	withoutConfiguration(t, "a Compose plan", raw, canary, "compose.yaml", `"files"`)
	if status.Plan != nil || status.Review == nil || !status.HostReview || !slices.Equal(status.Review.Permissions, []string{"web: adds Linux capabilities NET_ADMIN"}) || status.Review.Images[saved.Applications[0].ID.String()] != execution.TestImage {
		t.Fatalf("a plan-only credential's Compose review was %+v", status)
	}
}

// A credential that may write the configuration and run builds, but not read the environment, can
// name the current draft through its build targets. It saves without sending the Compose namespace
// or the Git origin it can't know: the draft keeps both. Sending either is refused the same way
// whatever it holds, and a save answers only with the new revision's identity, so nothing it sends
// confirms a guess about them.
func TestWriteWithoutReadSavesConfirmNothing(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	_, _, env, path := composeEnvironment(t, f, b)
	unit := env.DeploymentUnit.Id
	origin := composeproject.Origin{Provider: "github", Repository: "https://git.example.test/acme/private-app", Commit: strings.Repeat("a1", 20)}
	bundle := func(namespace uuid.UUID, o *composeproject.Origin, content string) domain.Specification {
		return domain.Specification{Version: 1, Applications: []domain.Application{}, Variables: []domain.Variable{}, Compose: &composeproject.Bundle{Namespace: namespace, Entrypoints: []string{"compose.yaml"}, Files: []composeproject.File{{Path: "compose.yaml", Content: content}}, Origin: o}}
	}
	const services = "services:\n  web:\n    image: busybox:stable\nvolumes:\n  data: {}\n"
	// An import records the Git source, as no save can.
	_, canonical, err := domain.ParseSpec(claimBytes(t, bundle(unit, &origin, services)))
	if err != nil {
		t.Fatal(err)
	}
	imported, hash := api.Revision{Id: uuid.New()}, sha256.Sum256(canonical)
	if _, err = f.st.Pool.Exec(t.Context(), "INSERT INTO specification_revisions(workspace_id,environment_id,id,author_id,specification,content_hash) SELECT workspace_id,environment_id,$2,author_id,$3,$4 FROM specification_revisions WHERE id=$1", env.DraftRevisionId, imported.Id, canonical, hash[:]); err != nil {
		t.Fatal(err)
	}
	if _, err = f.st.Pool.Exec(t.Context(), "UPDATE environments SET draft_revision_id=$2 WHERE id=$1", env.Id, imported.Id); err != nil {
		t.Fatal(err)
	}
	current := func() (api.Revision, domain.Specification) { return draftSpec(t, b, path) }
	_, importedSpec := current()
	if importedSpec.Compose.Origin == nil || len(importedSpec.Volumes) != 1 {
		t.Fatal("the imported draft is not as an import leaves it")
	}

	writer := *issueAutomation(t, b, path, domain.ConfigurationWrite, domain.BuildRun).Token
	edited := services + "# edited by CI\n"
	var saved api.SavedDraft
	raw := bearerCall(t, f, writer, "PUT", path+"/draft", api.DraftUpdate{BaseRevisionId: imported.Id, Specification: claimBytes(t, bundle(uuid.Nil, nil, edited))}, 200, &saved)
	withoutConfiguration(t, "a save", raw, origin.Repository, origin.Commit, unit.String(), `"hash"`)
	draft, spec := current()
	if draft.Id != saved.Id || spec.Compose.Namespace != unit || spec.Compose.Origin == nil || *spec.Compose.Origin != origin || spec.Compose.Files[0].Content != edited {
		t.Fatalf("the save didn't keep the draft's namespace and origin: %+v", spec.Compose)
	}
	if len(spec.Volumes) != 1 || spec.Volumes[0].ID != importedSpec.Volumes[0].ID || spec.Applications[0].ID != importedSpec.Applications[0].ID {
		t.Fatal("the save changed the identity of the draft's services or volumes")
	}

	// Naming the current draft, every guess about the namespace or the origin gets the same refusal,
	// and none changes the draft.
	otherCommit, otherRepository := origin, origin
	otherCommit.Commit = strings.Repeat("b2", 20)
	otherRepository.Repository = "https://git.example.test/acme/other-app"
	guess := func(namespace uuid.UUID, o *composeproject.Origin) api.Error {
		return guessedError(t, f, writer, "PUT", path+"/draft", api.DraftUpdate{BaseRevisionId: draft.Id, Specification: claimBytes(t, bundle(namespace, o, edited+"# guess\n"))}, 400)
	}
	origins := map[string]api.Error{"right": guess(uuid.Nil, &origin), "wrong commit": guess(uuid.Nil, &otherCommit), "wrong repository": guess(uuid.Nil, &otherRepository)}
	sameAnswers(t, "Compose origin", origins)
	invalidField(t, origins["right"], "compose.origin")
	namespaces := map[string]api.Error{"right": guess(unit, nil), "wrong": guess(uuid.New(), nil), "right with origin": guess(unit, &origin)}
	sameAnswers(t, "Compose namespace", namespaces)
	invalidField(t, namespaces["right"], "compose.namespace")
	if after, _ := current(); after.Id != draft.Id {
		t.Fatal("a guess replaced the draft")
	}

	// Nor does whether a save fits the size limit: a credential that may not read the environment
	// leaves room for the longest origin, whatever the draft holds, where a reader needs room only for
	// the draft's own.
	padded := bundle(unit, nil, edited)
	limit := domain.SpecificationLimit - 6000
	for i := 0; ; i++ {
		_, canonical, err := domain.ParseSpec(claimBytes(t, padded))
		if err != nil {
			t.Fatal(err)
		}
		if len(canonical) >= limit-100 {
			if len(canonical) > limit {
				t.Fatalf("padded the draft to %d bytes", len(canonical))
			}
			break
		}
		value := strings.Repeat("x", min(4000, limit-len(canonical)-60))
		padded.Variables = append(padded.Variables, domain.Variable{Name: fmt.Sprintf("PAD_%d", i), Value: &value})
	}
	padded.Compose.Namespace = uuid.Nil
	invalidField(t, guessedError(t, f, writer, "PUT", path+"/draft", api.DraftUpdate{BaseRevisionId: draft.Id, Specification: claimBytes(t, padded)}, 400), "specification")
	var fits api.SavedDraft
	bearerCall(t, f, *issueAutomation(t, b, path, domain.EnvironmentRead, domain.ConfigurationWrite).Token, "PUT", path+"/draft", api.DraftUpdate{BaseRevisionId: draft.Id, Specification: claimBytes(t, padded)}, 200, &fits)
	if draft, spec = current(); draft.Id != fits.Id || spec.Compose.Origin == nil || *spec.Compose.Origin != origin {
		t.Fatal("a reader's save near the size limit lost the origin")
	}

	// A principal that may read the environment may also leave both out, or send them unchanged.
	readerWriter := *issueAutomation(t, b, path, domain.EnvironmentRead, domain.ConfigurationWrite).Token
	var full api.SavedDraft
	bearerCall(t, f, readerWriter, "PUT", path+"/draft", api.DraftUpdate{BaseRevisionId: draft.Id, Specification: claimBytes(t, bundle(uuid.Nil, nil, services))}, 200, &full)
	if full.Hash == nil || full.Specification == nil {
		t.Fatal("a credential that may read lost the saved revision")
	}
	var kept domain.Specification
	if err = json.Unmarshal(*full.Specification, &kept); err != nil || kept.Compose.Namespace != unit || kept.Compose.Origin == nil || *kept.Compose.Origin != origin {
		t.Fatal("a reader's save didn't keep the draft's namespace and origin", err)
	}
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": full.Id, "specification": bundle(unit, &origin, edited)}, 200, nil)
}

// The MCP tools and the CLI client save, plan and apply for a credential that may not read the
// environment, with no configuration in what they return.
func TestAutomationToolsWorkWithoutReadAccess(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	ws, _, env, path := composeEnvironment(t, f, b)
	const canary = "compose-tool-canary"
	content := "services:\n  web:\n    image: " + execution.TestImage + "\n    environment:\n      GREETING: " + canary + "\n"
	bundle := domain.Specification{Version: 1, Applications: []domain.Application{}, Variables: []domain.Variable{}, Compose: &composeproject.Bundle{Entrypoints: []string{"compose.yaml"}, Files: []composeproject.File{{Path: "compose.yaml", Content: content}}}}
	var draft api.Revision
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": env.DraftRevisionId, "specification": bundle}, 200, &draft)
	enrolled, in, agentToken := enrollment(t, f, b, ws, "deployment")
	yes := true
	in.Capabilities.Visual, in.Capabilities.Compose = &yes, &yes
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
	agent := authorityAgent{t, f, agentToken, in}

	token := *issueAutomation(t, b, path, domain.ConfigurationWrite, domain.DeploymentPlan, domain.DeploymentApply).Token
	client, err := automationclient.New(f.server.URL, token, true)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := client.Context(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	serverSide, clientSide := mcp.NewInMemoryTransports()
	if _, err = client.MCP(scope, "test").Connect(t.Context(), serverSide, nil); err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "test"}, nil).Connect(t.Context(), clientSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	call := func(name string, arguments, out any) []byte {
		t.Helper()
		result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: arguments})
		if err != nil || result.IsError {
			t.Fatalf("%s failed: %v %+v", name, err, result)
		}
		raw := claimBytes(t, result.StructuredContent)
		if err = json.Unmarshal(raw, out); err != nil {
			t.Fatal(err)
		}
		return raw
	}

	var specification map[string]any
	edited := *bundle.Compose
	edited.Files = []composeproject.File{{Path: "compose.yaml", Content: content + "# edited by CI\n"}}
	bundle.Compose = &edited
	if err = json.Unmarshal(claimBytes(t, bundle), &specification); err != nil {
		t.Fatal(err)
	}
	var saved api.SavedDraft
	withoutConfiguration(t, "draft_update", call("draft_update", map[string]any{"baseRevisionId": draft.Id.String(), "specification": specification}, &saved), canary, env.DeploymentUnit.Id.String(), `"hash"`)
	var status deployment.Status
	call("plan_prepare", map[string]any{"baseRevisionId": saved.Id.String(), "serverId": enrolled.Server.Id.String()}, &status)
	var current domain.Specification
	if _, current = draftSpec(t, b, path); len(current.Applications) != 1 {
		t.Fatal("the saved draft has no service")
	}
	images := map[string]string{current.Applications[0].ID.String(): execution.TestImage}
	agent.observe(agent.next(), deployment.Observation{Images: images, StateHash: strings.Repeat("f", 64), HostChangesReported: true})
	withoutConfiguration(t, "plan_get", call("plan_get", map[string]any{"id": status.ID.String()}, &status), canary, "compose.yaml")
	if status.Plan != nil || status.Review == nil || status.Review.RevisionID != saved.Id || status.Review.Images[current.Applications[0].ID.String()] != execution.TestImage {
		t.Fatalf("plan_get answered %+v", status)
	}
	if _, err = client.ApplyPlan(t.Context(), scope, status.ID, uuid.New()); err != nil {
		t.Fatal(err)
	}
	agent.applied(agent.next(), images)
}
