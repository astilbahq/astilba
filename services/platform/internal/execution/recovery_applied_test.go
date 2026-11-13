package execution

import (
	"encoding/base64"
	"encoding/json/v2"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/astilbahq/astilba/services/platform/internal/upgrade"
	"github.com/google/uuid"
)

func appliedTestRow(values map[string]any) portability.Row {
	row := portability.Row{}
	for key, value := range values {
		recoverySet(row, key, value)
	}
	return row
}

func appliedTestPlan() deployment.Plan {
	image, app := TestImage, uuid.New()
	return deployment.Plan{ID: uuid.New(), ServerID: uuid.New(), EnvironmentID: uuid.New(), UnitID: uuid.New(), RevisionID: uuid.New(), Recipe: deployment.Recipe, ExpiresAt: time.Now().Add(time.Hour), Specification: domain.Specification{Version: 1, Variables: []domain.Variable{}, Applications: []domain.Application{{ID: app, Name: "Web", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Health: &domain.HealthCheck{Port: 8080, Path: "/"}}}}, Images: map[string]string{app.String(): image}}.WithHash()
}

func appliedTestArchive(t *testing.T) (portability.Archive, deployment.Plan) {
	t.Helper()
	p := appliedTestPlan()
	if err := p.Validate(true); err != nil {
		t.Fatal(err)
	}
	a := portability.Archive{Version: 6, Workspace: uuid.New(), CreatedAt: time.Now().UTC(), Tables: map[string][]portability.Row{}}
	return appliedTestDeploy(a, p, 1), p
}

func appliedTestDeploy(a portability.Archive, p deployment.Plan, generation int64) portability.Archive {
	raw, _ := stablejson.Marshal(a)
	var out portability.Archive
	_ = json.Unmarshal(raw, &out)
	body := deployment.Payload{Action: "deploy", Plan: p}
	command := api.ExecutionCommand{Id: uuid.New(), ServerId: p.ServerID, EnvironmentId: p.EnvironmentID, ResourceId: p.UnitID, Generation: generation, Kind: "visual", Action: "inspect", PlanHash: body.Digest(), Visual: rawMessage(mustJSON(body))}
	out.Tables["execution_operations"] = append(out.Tables["execution_operations"], appliedTestRow(map[string]any{"id": command.Id, "workspace_id": a.Workspace, "environment_id": p.EnvironmentID, "server_id": p.ServerID, "kind": "visual", "parent_id": nil, "generation": generation, "report_sequence": 1, "state": "succeeded", "command": command, "visual_plan_id": p.ID, "runtime_outcome": "applied", "runtime_plan_id": p.ID}))
	out.Tables["visual_plans"] = append(out.Tables["visual_plans"], appliedTestRow(map[string]any{"id": p.ID, "plan": p}))
	out.Tables["environments"] = []portability.Row{appliedTestRow(map[string]any{"id": p.EnvironmentID, "applied_revision_id": p.RevisionID, "runtime_operation_id": command.Id, "execution_generation": generation})}
	out.Tables["visual_runtime"] = []portability.Row{appliedTestRow(map[string]any{"environment_id": p.EnvironmentID, "plan_id": p.ID})}
	return out
}

func TestAppliedHistoryUsesCommittedOperation(t *testing.T) {
	base, plan := appliedTestArchive(t)
	old, err := buildAppliedOrigin(base, uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	plan.ID = uuid.New()
	plan = plan.WithHash()
	next := appliedTestDeploy(base, plan, 2)
	next.CreatedAt = base.CreatedAt.Add(time.Second)
	current, err := buildAppliedOrigin(next, uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	env := plan.EnvironmentID
	if order, err := compareApplied(current, current.Environments[env], old, old.Environments[env]); err != nil || order != 1 {
		t.Fatal("same-revision replacement plan lost its causal order", order, err)
	}
	t.Run("read_counter", func(t *testing.T) {
		recoverySet(base.Tables["environments"][0], "execution_generation", 99999)
		read, err := buildAppliedOrigin(base, uuid.Nil)
		if err != nil {
			t.Fatal(err)
		}
		if order, err := compareApplied(current, current.Environments[env], read, read.Environments[env]); err != nil || order != 1 {
			t.Fatal("read/preparation counter replaced applied ordering", order, err)
		}
	})
	t.Run("non_apply", func(t *testing.T) {
		row := next.Tables["execution_operations"][1]
		var command api.ExecutionCommand
		_ = json.Unmarshal(row["command"], &command)
		var body deployment.Payload
		_ = json.Unmarshal(*command.Visual, &body)
		body.Action = "observe"
		command.Visual, command.PlanHash = rawMessage(mustJSON(body)), body.Digest()
		recoverySet(row, "command", command)
		if _, err := buildAppliedOrigin(next, uuid.Nil); err == nil {
			t.Fatal("successful inspection became applied provenance")
		}
	})
}

func TestAppliedOriginsRejectForksBeforeMerging(t *testing.T) {
	base, p := appliedTestArchive(t)
	p.ID = uuid.New()
	p = p.WithHash()
	left := appliedTestDeploy(base, p, 2)
	p.ID = uuid.New()
	p = p.WithHash()
	right := appliedTestDeploy(base, p, 3)
	left.CreatedAt, right.CreatedAt = base.CreatedAt.Add(time.Second), base.CreatedAt.Add(2*time.Second)
	for _, input := range [][]portability.Archive{{left, right}, {right, left}} {
		var origins appliedOrigins
		if err := origins.add(input[0], uuid.New()); err != nil {
			t.Fatal(err)
		}
		if err := origins.add(input[1], uuid.New()); err == nil {
			t.Fatal("later capture hid a fork which did not know the applied predecessor")
		}
	}
	t.Run("equal_generation", func(t *testing.T) {
		same := appliedTestDeploy(base, p, 1)
		if _, err := buildAppliedOrigin(same, uuid.Nil); err == nil {
			t.Fatal("equal-generation conflicting application accepted")
		}
	})
	t.Run("controller", func(t *testing.T) {
		prior, _ := buildAppliedOrigin(base, uuid.Nil)
		next, _ := buildAppliedOrigin(left, uuid.Nil)
		old := prior.Environments[p.EnvironmentID]
		evidence := prior.Operations[old.Operation]
		evidence.Controller = 2
		prior.Operations[old.Operation] = evidence
		if _, err := compareApplied(next, next.Environments[p.EnvironmentID], prior, old); err == nil {
			t.Fatal("older controller replaced known applied authority")
		}
	})
	t.Run("aggregate_bound", func(t *testing.T) {
		origins := appliedOrigins{Records: maxAppliedOriginRecords}
		if err := origins.add(base, uuid.New()); err == nil {
			t.Fatal("unbounded per-origin evidence")
		}
	})
}

func TestAppliedOriginsSelectCausalityBeforeCaptureTime(t *testing.T) {
	base, p := appliedTestArchive(t)
	p.ID = uuid.New()
	p = p.WithHash()
	next := appliedTestDeploy(base, p, 2)
	// Controller clock differences do not turn a causally older runtime into
	// the source merely because its metadata snapshot has a later timestamp.
	next.CreatedAt = base.CreatedAt.Add(-time.Minute)
	var origins appliedOrigins
	if err := origins.add(next, uuid.New()); err != nil {
		t.Fatal(err)
	}
	if err := origins.add(base, uuid.New()); err != nil {
		t.Fatal(err)
	}
	selected, err := origins.latest(p.EnvironmentID)
	if err != nil || selected.Environments[p.EnvironmentID].Plan != p.ID {
		t.Fatal("capture time discarded the newer applied source", err)
	}
}

func TestLegacyAppliedProvenanceRequiresFreshApply(t *testing.T) {
	a, p := appliedTestArchive(t)
	recoverySet(a.Tables["environments"][0], "runtime_operation_id", nil)
	old, err := buildAppliedOrigin(a, uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	tuple := old.Environments[p.EnvironmentID]
	if order, err := compareApplied(old, tuple, old, tuple); err != nil || order != 0 {
		t.Fatal("unchanged unrelated legacy runtime could not be preserved", order, err)
	}
	empty := &appliedOrigin{Operations: map[uuid.UUID]appliedEvidence{}}
	if _, err := compareApplied(old, tuple, empty, appliedTuple{}); err != errAppliedProvenance {
		t.Fatal("legacy runtime was adopted without provenance", err)
	}
	if order, err := compareApplied(empty, appliedTuple{}, old, tuple); err != nil || order != -1 {
		t.Fatal("empty source erased known runtime", order, err)
	}
	a.Tables["visual_runtime"] = nil
	removed, err := buildAppliedOrigin(a, uuid.Nil)
	if err != nil {
		t.Fatal("legacy removed runtime rejected", err)
	}
	if order, err := compareApplied(removed, removed.Environments[p.EnvironmentID], removed, removed.Environments[p.EnvironmentID]); err != nil || order != 0 {
		t.Fatal("unchanged legacy removed runtime could not be preserved", order, err)
	}
	if _, err := compareApplied(removed, removed.Environments[p.EnvironmentID], empty, appliedTuple{}); err != errAppliedProvenance {
		t.Fatal("legacy removed runtime was adopted", err)
	}
}

func appliedTestGroup(t *testing.T) deployment.Plan {
	t.Helper()
	p := appliedTestPlan()
	app, network, volume := p.Specification.Applications[0].ID, uuid.New(), uuid.New()
	p.Specification.Volumes = []domain.Volume{{ID: volume, Name: "Data"}}
	p.Specification.Routes = []domain.Route{{ID: uuid.New(), TargetID: app, Protocol: "http", Domain: "test.example.test", Port: 8080, TLS: "off"}}
	p.Specification.Placement = &domain.Placement{NetworkID: network, IngressServerID: p.ServerID, Applications: []domain.ApplicationPlacement{{ApplicationID: app, CPUMillis: 100, MemoryMiB: 64, Targets: []domain.PlacementTarget{{ServerID: p.ServerID, Count: 1}}}}}
	p.Recipe = deployment.CrossHostRecipe
	local := p
	local.ID = uuid.New()
	local.Local = &deployment.LocalPlacement{GroupID: p.ID, Primary: true, Network: hostnetwork.Manifest{Version: 1, NetworkID: network, MembershipID: uuid.New(), ServerID: p.ServerID, Generation: 1, Prefix: "10.249.0.0/24", Address: "10.249.0.1", PublicKey: base64.StdEncoding.EncodeToString([]byte(strings.Repeat("a", 32))), ListenPort: 51820}, Instances: map[string][]uuid.UUID{app.String(): {uuid.New()}}, Requests: map[string]deployment.ResourceRequest{app.String(): {CPUMillis: 100, MemoryMiB: 64}}}
	local = local.WithHash()
	p.Images = map[string]string{}
	p.Distributed = &deployment.DistributedPlan{NetworkID: network, NetworkGeneration: 1, Units: []deployment.DistributedUnit{{Plan: local, CPUMillis: 100, MemoryMiB: 64, InventoryHash: strings.Repeat("a", 64)}}}
	p = p.WithHash()
	if err := p.Validate(true); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAppliedMaintenanceRequiresActualTargetOutcome(t *testing.T) {
	source := appliedTestGroup(t)
	restore := deployment.RestoreCutover{SourcePlanID: source.ID, SourceHash: source.Hash, PolicyID: uuid.New(), RunID: uuid.New(), ManifestHash: strings.Repeat("a", 64), OldVolumeID: source.Specification.Volumes[0].ID, TargetVolumeID: uuid.New()}
	target, err := deployment.RestoredGroup(source, restore, uuid.New(), uuid.New(), time.Now().Add(time.Hour))
	if err != nil || target.Validate(true) != nil {
		t.Fatal("restored group", err)
	}
	body := coordinationCommand{Action: "maintenance", Plan: source, Maintenance: &coordinatedMaintenance{Restore: &target}}
	parent := api.ExecutionCommand{Id: uuid.New(), EnvironmentId: source.EnvironmentID, ServerId: source.ServerID, ResourceId: source.UnitID, Kind: "crosshost", Generation: 10, PlanHash: Hash(body), Crosshost: rawMessage(mustJSON(body))}
	for _, state := range []api.ExecutionState{"succeeded", "failed", "cancelled"} {
		e := appliedEvidence{State: state, Runtime: portability.RuntimeOutcome{Kind: "applied", Plan: target.ID}}
		got, witness, _ := committedApplied(parent, e)
		if got != (appliedTuple{target.RevisionID, target.ID, parent.Id}) || witness == "" {
			t.Fatal("confirmed target lost", state, got)
		}
		e.Runtime = portability.RuntimeOutcome{}
		if got, _, _ := committedApplied(parent, e); !got.empty() {
			t.Fatal("terminal maintenance inferred application", state)
		}
	}
}

// Preview cleanup and environment retirement both remove a unit's runtime, and recovery has to
// keep either confirmed removal in order instead of refusing the checkpoint that holds it.
func TestRemovedRuntimeHasCausalOrder(t *testing.T) {
	for _, action := range []string{"preview-cleanup", retireAction} {
		t.Run(action, func(t *testing.T) { removedRuntimeHasCausalOrder(t, action) })
	}
}

func removedRuntimeHasCausalOrder(t *testing.T, action string) {
	base, p := appliedTestArchive(t)
	old, err := buildAppliedOrigin(base, uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	if action == "preview-cleanup" {
		p.Recipe = deployment.PreviewRecipe
		p.Preview = &domain.PreviewIdentity{RequestID: uuid.New(), Generation: 1, Limits: domain.PreviewLimits{CPUs: 1, MemoryMiB: 128, Pids: 64}}
		p = p.WithHash()
	}
	body := deployment.Payload{Action: action, Plan: p}
	c := api.ExecutionCommand{Id: uuid.New(), EnvironmentId: p.EnvironmentID, ServerId: p.ServerID, ResourceId: p.UnitID, Kind: "visual", Generation: 2, PlanHash: body.Digest(), Visual: rawMessage(mustJSON(body))}
	base.Tables["execution_operations"] = append(base.Tables["execution_operations"], appliedTestRow(map[string]any{"id": c.Id, "workspace_id": base.Workspace, "environment_id": p.EnvironmentID, "server_id": p.ServerID, "kind": "visual", "parent_id": nil, "generation": 2, "report_sequence": 1, "state": "succeeded", "command": c, "runtime_outcome": "removed", "runtime_plan_id": nil, "cancel_requested": false}))
	recoverySet(base.Tables["environments"][0], "applied_revision_id", nil)
	recoverySet(base.Tables["environments"][0], "runtime_operation_id", c.Id)
	base.Tables["visual_runtime"] = nil
	next, err := buildAppliedOrigin(base, uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	if order, err := compareApplied(next, next.Environments[p.EnvironmentID], old, old.Environments[p.EnvironmentID]); err != nil || order != 1 {
		t.Fatal("confirmed removal lost causal order", order, err)
	}
	recoverySet(base.Tables["execution_operations"][1], "state", "cancelled")
	recoverySet(base.Tables["execution_operations"][1], "report_sequence", 0)
	transferred, err := buildAppliedOrigin(base, uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	if order, err := compareApplied(transferred, transferred.Environments[p.EnvironmentID], next, next.Environments[p.EnvironmentID]); err != nil || order != 0 {
		t.Fatal("transfer acknowledgement changed removal", order, err)
	}
	prior := base.Tables["execution_operations"][1]
	reported := appliedTestRow(map[string]any{})
	for key, value := range prior {
		reported[key] = value
	}
	recoverySet(reported, "state", "succeeded")
	recoverySet(reported, "report_sequence", 1)
	if _, err := recoveryOperationKnowledge(prior, reported); err != nil {
		t.Fatal("management merge rejected equivalent removal", err)
	}
}

func TestAppliedHistoryRejectsUnappliedAncestor(t *testing.T) {
	base, p := appliedTestArchive(t)
	old, err := buildAppliedOrigin(base, uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	p.ID = uuid.New()
	p = p.WithHash()
	next := appliedTestDeploy(base, p, 2)
	recoverySet(next.Tables["execution_operations"][0], "runtime_outcome", nil)
	recoverySet(next.Tables["execution_operations"][0], "runtime_plan_id", nil)
	candidate, err := buildAppliedOrigin(next, uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = compareApplied(candidate, candidate.Environments[p.EnvironmentID], old, old.Environments[p.EnvironmentID]); err == nil {
		t.Fatal("successful non-applied predecessor became ancestry")
	}
}

func TestUpgradeRuntimeOutcomeCanRecordReturnedSource(t *testing.T) {
	source := appliedTestPlan()
	app := &source.Specification.Applications[0]
	image := domain.Postgres17Image
	volume := uuid.New()
	app.Kind = "postgres"
	app.Health = nil
	app.Source.Image = &image
	app.Postgres = &domain.Postgres{Database: "app", Username: "app", PasswordSecretVersionID: uuid.New()}
	app.Mounts = []domain.VolumeMount{{VolumeID: volume, Target: domain.PostgresMount(image)}}
	source.Specification.Volumes = []domain.Volume{{ID: volume, Name: "Database"}}
	source.Images[app.ID.String()] = image
	source = source.WithHash()
	plan := upgrade.Plan{ID: uuid.New(), Recipe: upgrade.Recipe, PolicyID: uuid.New(), ResourceID: app.ID, TargetVolumeID: uuid.New(), TargetPlanID: uuid.New(), TargetRevisionID: uuid.New(), RetainDays: 1, ExpiresAt: time.Now().Add(time.Hour), Source: source}
	if err := source.Validate(true); err != nil {
		t.Fatalf("source: %#v", err)
	}
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
	body := upgrade.Command{Action: "return", Plan: plan}
	c := api.ExecutionCommand{Id: uuid.New(), Kind: "upgrade", ServerId: source.ServerID, EnvironmentId: source.EnvironmentID, ResourceId: plan.ID, PlanHash: body.Digest(), Upgrade: rawMessage(mustJSON(body))}
	got, _, _ := committedApplied(c, appliedEvidence{State: "succeeded", Sequence: 1, Runtime: portability.RuntimeOutcome{Kind: "applied", Plan: source.ID}})
	if got != (appliedTuple{source.RevisionID, source.ID, c.Id}) {
		t.Fatal("returned source was replaced by inferred target", got)
	}
}
