package integration

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"slices"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

const overTheLimit = "the configuration would be over the 128 KiB limit."

// paddedBelowTheLimit pads spec so that it fits as stored and grow, the change a background writer
// will make to it, takes it over.
func paddedBelowTheLimit(t *testing.T, f *fixture, spec domain.Specification, grow func(domain.Specification) domain.Specification) domain.Specification {
	t.Helper()
	probe := withDenseVariables(t, f, spec, 10000)
	canonical, text := storedSizes(t, f, probe)
	_, grown := storedSizes(t, f, grow(probe))
	growth := grown - text
	if growth < 2 {
		t.Fatalf("the change grows the specification by %d bytes", growth)
	}
	padded := withDenseVariables(t, f, spec, text-canonical+growth/2)
	if _, text = storedSizes(t, f, padded); text > domain.SpecificationLimit {
		t.Fatalf("the padded specification is %d bytes as stored", text)
	}
	if _, grown = storedSizes(t, f, grow(padded)); grown <= domain.SpecificationLimit {
		t.Fatalf("the change leaves the specification at %d bytes as stored", grown)
	}
	return padded
}

// promoted is spec with its first application deploying a build's artifact, as a promotion
// leaves it.
func promoted(spec domain.Specification) domain.Specification {
	image, artifact := "astilba-build-"+uuid.NewString()+":built", uuid.New()
	spec.Applications = slices.Clone(spec.Applications)
	spec.Applications[0].Source = domain.ApplicationSource{Kind: "image", Image: &image, ArtifactID: &artifact}
	return spec
}

// PostgreSQL's jsonb refuses NUL, and the refused cast aborted the save's transaction with a 500.
func TestDraftSaveRefusesNULOnItsField(t *testing.T) {
	f := setup(t)
	b, _, env, path := automationEnvironment(t, f)
	image := "busybox:1.37.0" + string(rune(0))
	spec := domain.Specification{Version: 1, Variables: []domain.Variable{}, Applications: []domain.Application{{ID: uuid.New(), Name: "web", Source: domain.ApplicationSource{Kind: "image", Image: &image}}}}
	var refused api.Error
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": env.DraftRevisionId, "specification": spec}, 400, &refused)
	invalidField(t, refused, "specification.applications.source.image")
}

// A build whose automatic deployment would take the draft over the limit keeps its result and
// artifact, says why it did not deploy, and is not reported again until the command expires.
func TestAutomaticDeploymentOverTheLimitKeepsTheBuild(t *testing.T) {
	h := newHungBuild(t)
	padded := paddedBelowTheLimit(t, h.f, h.spec, promoted)
	h.b.call("PUT", h.path+"/draft", map[string]any{"baseRevisionId": h.draft.Id, "specification": padded}, 200, &h.draft)
	config := h.config
	config.AutoBuild, config.AutoDeploy = true, true
	c := h.deliver(t, config)
	agentRequest(t, h.f.server.Client(), h.f.server.URL, "/agent/v1/report", h.token, h.succeeded(t, c), 204, nil)
	var state, note string
	var artifacts, deployments int
	var draft uuid.UUID
	if err := h.f.st.Pool.QueryRow(t.Context(), "SELECT o.state,o.note,(SELECT count(*) FROM build_artifacts WHERE id=o.id),(SELECT count(*) FROM pipeline_auto_deployments WHERE id=o.id),(SELECT draft_revision_id FROM environments WHERE id=o.environment_id) FROM execution_operations o WHERE o.id=$1", c.Id).Scan(&state, &note, &artifacts, &deployments, &draft); err != nil {
		t.Fatal(err)
	}
	if state != "succeeded" || artifacts != 1 || deployments != 0 || draft != h.draft.Id {
		t.Fatal("the refused deployment changed the build's outcome", state, artifacts, deployments)
	}
	if note != "Built, but not deployed automatically: "+overTheLimit {
		t.Fatalf("the build's note is %q", note)
	}
}

// A preview environment's generated hostnames can take its template over the limit. The preview
// fails with the reason rather than leaving its job to retry, and holds no environment.
func TestPreviewCreationOverTheLimitFails(t *testing.T) {
	h := newHungPreview(t)
	h.f.st.ApplicationDomain = "apps.example.test"
	execSQL(t, h.hungBuild, "INSERT INTO server_domains(server_id,workspace_id,base,ipv4,approved_controller,state,observed_at) SELECT id,workspace_id,$2,'198.51.100.1',controller_generation,'applied',now() FROM servers WHERE id=$1", h.server, h.f.st.ApplicationDomain)
	spec := h.spec
	spec.Applications = slices.Clone(spec.Applications)
	spec.Applications[0].Kind = ""
	spec.Applications[0].Health = &domain.HealthCheck{Port: 8080, Path: "/"}
	spec.Routes = []domain.Route{{ID: uuid.New(), TargetID: h.app, Protocol: "http", Domain: "p.example.test", Port: 8080, TLS: "off", AllowCIDRs: []string{}}}
	hostname := domain.ApplicationHostname(h.f.st.ApplicationDomain, uuid.New(), uuid.New(), h.app, h.server)
	padded := paddedBelowTheLimit(t, h.f, spec, func(s domain.Specification) domain.Specification {
		route := s.Routes[0]
		route.Domain, route.TLS, route.ListenPort = hostname, "managed", 0
		s.Routes = []domain.Route{route}
		return s
	})
	var draft api.Revision
	h.b.call("PUT", h.path+"/draft", map[string]any{"baseRevisionId": h.draft.Id, "specification": padded}, 200, &draft)
	h.policy["baseRevisionId"], h.policy["generatedDomains"] = draft.Id, true
	h.b.call("PUT", h.path+"/preview-policy", map[string]any{"enabled": true, "generation": 0, "configuration": h.policy}, 200, nil)
	preview := h.waitPreview(t, "approval_required")
	h.b.call("POST", h.path+"/previews/"+preview.Id.String()+"/actions", map[string]any{"action": "approve", "head": preview.Head, "headRepository": preview.HeadRepository}, 200, nil)
	preview = h.waitPreview(t, "failed")
	if preview.Note != "Preview environment was not created: "+overTheLimit || preview.EnvironmentId != nil {
		t.Fatal("the preview did not fail with the reason, or kept an environment", preview.Note, preview.EnvironmentId)
	}
}

// Promoting a preview's build can take its draft over the limit. The preview fails with the reason,
// and the build's report is accepted.
func TestPreviewPromotionOverTheLimitFails(t *testing.T) {
	h := newHungPreview(t)
	padded := paddedBelowTheLimit(t, h.f, h.spec, promoted)
	var draft api.Revision
	h.b.call("PUT", h.path+"/draft", map[string]any{"baseRevisionId": h.draft.Id, "specification": padded}, 200, &draft)
	h.policy["baseRevisionId"] = draft.Id
	h.b.call("PUT", h.path+"/preview-policy", map[string]any{"enabled": true, "generation": 0, "configuration": h.policy}, 200, nil)
	preview := approvePreview(t, h, h.path)
	build := nextPreviewCommand(t, h)
	var command pipeline.Command
	if err := json.Unmarshal(*build.Pipeline, &command); err != nil {
		t.Fatal(err)
	}
	raw, _ := stablejson.Marshal(pipeline.Result{Commit: command.Commit, Image: "astilba-build-" + build.Id.String() + ":built", Digest: "sha256:" + strings.Repeat("c", 64), Architecture: "arm64"})
	result := jsontext.Value(raw)
	report := api.AgentReport{Id: build.Id, Generation: build.Generation, PlanHash: build.PlanHash, Sequence: 2, State: "succeeded", Steps: []api.ExecutionStep{}, Pipeline: &result}
	agentRequest(t, h.f.server.Client(), h.f.server.URL, "/agent/v1/report", h.token, report, 204, nil)
	preview = h.waitPreview(t, "failed")
	if preview.Note != "Preview was not deployed: "+overTheLimit {
		t.Fatalf("the preview's note is %q", preview.Note)
	}
	var plans int
	if err := h.f.st.Pool.QueryRow(t.Context(), "SELECT count(*) FROM visual_requests WHERE environment_id=$1", *preview.EnvironmentId).Scan(&plans); err != nil {
		t.Fatal(err)
	}
	if plans != 0 {
		t.Fatal("the refused promotion left a deployment plan behind")
	}
}

// An upgrade stores its target revision only once the host serves the target, when nothing can
// refuse it. No current recipe makes the target larger than its source, so this enlarges the
// applied plan's own specification past what a revision can hold, as a recipe that grows the target
// would leave it, and the upgrade is refused before anything reaches the host.
func TestUpgradeWhoseTargetCannotBeStoredIsRefusedWhenPlanned(t *testing.T) {
	f := setup(t)
	h := newUpgradeHost(t, f)
	var raw []byte
	if err := f.st.Pool.QueryRow(t.Context(), "SELECT plan FROM visual_plans WHERE id=$1", h.deploymentPlan.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var plan deployment.Plan
	if err := json.Unmarshal(raw, &plan); err != nil {
		t.Fatal(err)
	}
	plan.Specification = withDenseVariables(t, f, plan.Specification, 100)
	if canonical, text := storedSizes(t, f, plan.Specification); canonical > domain.SpecificationLimit || text <= domain.SpecificationLimit {
		t.Fatalf("the specification is %d bytes canonically and %d as jsonb text", canonical, text)
	}
	raw, _ = stablejson.Marshal(plan.WithHash())
	// Plans are immutable, so the edit goes around the trigger that keeps them so.
	tx, err := f.st.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if _, err = tx.Exec(t.Context(), "SET LOCAL session_replication_role = replica"); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(t.Context(), "UPDATE visual_plans SET plan=$2 WHERE id=$1", h.deploymentPlan.ID, raw); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	var dest backup.Destination
	h.b.call("POST", h.prefix+"/backup-destinations", map[string]any{"name": "Upgrade storage", "kind": "filesystem", "root": "/srv/backups", "endpoint": "", "bucket": "", "region": "", "access": map[string]string{"accessKey": "", "secretKey": "", "password": ""}}, 201, &dest)
	password := "upgrade-repository-canary"
	var policy backup.Policy
	h.b.call("POST", h.path+"/backup-policies", api.BackupPolicyRequest{Name: "Before upgrade", ResourceId: h.pg, DestinationId: dest.ID, Method: "postgres-logical", Schedule: api.DailySchedule{Timezone: "UTC"}, Retain: 7, Enabled: true, Command: []string{}, Password: &password}, 201, &policy)
	var refused api.Error
	h.b.call("POST", h.path+"/upgrades", api.UpgradeRequest{PolicyId: policy.ID, ResourceId: h.pg, BaseRevisionId: h.draft.Id, RetainDays: 30}, 400, &refused)
	invalidField(t, refused, "specification")
	var upgrades int
	if err := f.st.Pool.QueryRow(t.Context(), "SELECT count(*) FROM supported_upgrades WHERE environment_id=$1", h.env).Scan(&upgrades); err != nil {
		t.Fatal(err)
	}
	if upgrades != 0 {
		t.Fatal("the refused upgrade was planned")
	}
}
