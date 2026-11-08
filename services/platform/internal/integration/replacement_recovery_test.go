package integration

import (
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/google/uuid"
)

// replacedEnvironment is a second environment in the recovery workspace whose primary, on a server
// since revoked, is being replaced by a unit on the healthy peer. Its replacement hasn't finished,
// so its applied plan still names the revoked primary's unit.
type replacedEnvironment struct {
	env, former, next, volume, app, revoked, peer uuid.UUID
}

func seedReplacedEnvironment(t *testing.T, f *fixture, b *browser, ws, peer uuid.UUID) replacedEnvironment {
	t.Helper()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := f.st.Pool.Exec(t.Context(), query, args...); err != nil {
			t.Fatal(query, err)
		}
	}
	base := "/api/v1/workspaces/" + ws.String()
	_, gone, _ := enrollment(t, f, b, ws, "deployment")
	yes := true
	gone.Capabilities.Visual = &yes
	if _, err := f.execution.Enroll(t.Context(), gone); err != nil {
		t.Fatal(err)
	}
	var project api.Project
	b.call("POST", base+"/projects", map[string]string{"name": "Replaced"}, 201, &project)
	var environment api.Environment
	b.call("POST", base+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "Files"}, 201, &environment)
	r := replacedEnvironment{env: environment.Id, next: uuid.New(), volume: uuid.New(), app: uuid.New(), revoked: gone.ServerId, peer: peer}
	image := execution.TestImage
	spec := domain.Specification{Version: 1, Variables: []domain.Variable{}, Volumes: []domain.Volume{{ID: r.volume, Name: "Files"}}, Applications: []domain.Application{{ID: r.app, Name: "Files", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Mounts: []domain.VolumeMount{{VolumeID: r.volume, Target: "/srv/files"}}}}}
	var draft api.Revision
	b.call("PUT", base+"/projects/"+project.Id.String()+"/environments/"+r.env.String()+"/draft", map[string]any{"baseRevisionId": environment.DraftRevisionId, "specification": spec}, 200, &draft)
	r.former = claimUUID(t, f, `SELECT id FROM deployment_units WHERE workspace_id=$1 AND environment_id=$2`, ws, r.env)
	exec(`UPDATE deployment_units SET server_id=$1 WHERE id=$2`, gone.ServerId, r.former)
	source := deployment.Plan{ID: uuid.New(), ServerID: gone.ServerId, EnvironmentID: r.env, UnitID: r.former, RevisionID: draft.Id, Recipe: deployment.Recipe, ExpiresAt: time.Now().Add(time.Hour), Specification: spec, Images: map[string]string{r.app.String(): image}}.WithHash()
	body := deployment.Payload{Action: "deploy", Plan: source}
	applied := api.ExecutionCommand{Id: uuid.New(), ServerId: gone.ServerId, EnvironmentId: r.env, ResourceId: r.former, Generation: 1, Kind: "visual", Action: "inspect", PlanHash: body.Digest(), Deadline: time.Now().Add(time.Hour), Visual: claimMessage(t, body)}
	exec(`INSERT INTO execution_operations(id,workspace_id,server_id,environment_id,generation,request_key,command,kind,state,report_sequence) VALUES($1,$2,$3,$4,1,$5,$6,'visual','succeeded',1)`, applied.Id, ws, gone.ServerId, r.env, uuid.New(), claimBytes(t, applied))
	exec(`INSERT INTO visual_requests(id,workspace_id,server_id,environment_id,unit_id,revision_id,operation_id,candidate) VALUES($1,$2,$3,$4,$5,$6,$7,'{}')`, source.ID, ws, gone.ServerId, r.env, r.former, draft.Id, applied.Id)
	exec(`INSERT INTO visual_plans(id,workspace_id,environment_id,plan) VALUES($1,$2,$3,$4)`, source.ID, ws, r.env, claimBytes(t, source))
	exec(`UPDATE execution_operations SET runtime_outcome='applied',runtime_plan_id=$2 WHERE id=$1`, applied.Id, source.ID)
	exec(`INSERT INTO visual_runtime(workspace_id,environment_id,unit_id,server_id,plan_id,observation) VALUES($1,$2,$3,$4,$5,'{}')`, ws, r.env, r.former, gone.ServerId, source.ID)
	exec(`UPDATE environments SET applied_revision_id=$2,runtime_operation_id=$3,execution_generation=2 WHERE id=$1`, r.env, draft.Id, applied.Id)
	exec(`INSERT INTO managed_volumes(workspace_id,environment_id,unit_id,id) VALUES($1,$2,$3,$4)`, ws, r.env, r.former, r.volume)
	exec(`INSERT INTO managed_resources(workspace_id,environment_id,unit_id,id) VALUES($1,$2,$3,$4)`, ws, r.env, r.former, r.app)
	exec(`UPDATE servers SET revoked=true,credential_hash=NULL WHERE id=$1`, gone.ServerId)
	exec(`INSERT INTO deployment_units(workspace_id,id,environment_id,server_id,primary_unit) VALUES($1,$2,$3,$4,false)`, ws, r.next, r.env, peer)
	return r
}

// replace records what applying the replacement does to the units, volumes and resources.
func (r replacedEnvironment) replace(t *testing.T, f *fixture) {
	t.Helper()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := f.st.Pool.Exec(t.Context(), query, args...); err != nil {
			t.Fatal(query, err)
		}
	}
	exec(`UPDATE deployment_units SET primary_unit=false,server_id=NULL WHERE id=$1`, r.former)
	exec(`UPDATE deployment_units SET primary_unit=true WHERE id=$1`, r.next)
	exec(`UPDATE managed_volumes SET unit_id=$2 WHERE unit_id=$1`, r.former, r.next)
	exec(`UPDATE managed_resources SET unit_id=$2 WHERE unit_id=$1`, r.former, r.next)
}

// undo puts the rows back as they were before the replacement, as an older backup holds them.
func (r replacedEnvironment) undo(t *testing.T, f *fixture) {
	t.Helper()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := f.st.Pool.Exec(t.Context(), query, args...); err != nil {
			t.Fatal(query, err)
		}
	}
	exec(`UPDATE managed_volumes SET unit_id=$1 WHERE unit_id=$2`, r.former, r.next)
	exec(`UPDATE managed_resources SET unit_id=$1 WHERE unit_id=$2`, r.former, r.next)
	exec(`UPDATE deployment_units SET primary_unit=false WHERE id=$1`, r.next)
	exec(`UPDATE deployment_units SET primary_unit=true,server_id=$2 WHERE id=$1`, r.former, r.revoked)
}

func (r replacedEnvironment) want(t *testing.T, f *fixture) {
	t.Helper()
	var formerServer *uuid.UUID
	var formerPrimary, nextPrimary bool
	var nextServer, volumeUnit, resourceUnit uuid.UUID
	if err := f.st.Pool.QueryRow(t.Context(), `SELECT (SELECT server_id FROM deployment_units WHERE id=$1),(SELECT primary_unit FROM deployment_units WHERE id=$1),(SELECT primary_unit FROM deployment_units WHERE id=$2),(SELECT server_id FROM deployment_units WHERE id=$2),(SELECT unit_id FROM managed_volumes WHERE id=$3),(SELECT unit_id FROM managed_resources WHERE id=$4)`, r.former, r.next, r.volume, r.app).Scan(&formerServer, &formerPrimary, &nextPrimary, &nextServer, &volumeUnit, &resourceUnit); err != nil {
		t.Fatal(err)
	}
	if formerServer != nil || formerPrimary {
		t.Fatalf("the former primary came back: server %v, primary %v", formerServer, formerPrimary)
	}
	if !nextPrimary || nextServer != r.peer || volumeUnit != r.next || resourceUnit != r.next {
		t.Fatalf("the new primary %s lost its role or its data: primary %v on %s, volume on %s, resource on %s", r.next, nextPrimary, nextServer, volumeUnit, resourceUnit)
	}
}

// Recovering an unrelated interrupted root keeps a replacement whichever side knows of it: the host
// capsule, frozen before management's backup was taken, or the backup itself.
func TestRecoveryKeepsAReplacedPrimaryDetached(t *testing.T) {
	t.Run("only the capsule knows", func(t *testing.T) {
		var replaced replacedEnvironment
		var r *claimRecovery
		r = newClaimRecovery(t, 1, func(f *fixture, b *browser, ws, _ uuid.UUID) {
			peer := claimUUID(t, f, `SELECT id FROM servers WHERE workspace_id=$1 AND NOT revoked ORDER BY id DESC LIMIT 1`, ws)
			replaced = seedReplacedEnvironment(t, f, b, ws, peer)
			replaced.replace(t, f)
		})
		replaced.undo(t, r.f)
		r.recoverAll(t, r.exports(t, claimExport{}))
		replaced.want(t, r.f)
	})
	t.Run("only the backup knows", func(t *testing.T) {
		var replaced replacedEnvironment
		r := newClaimRecovery(t, 1, func(f *fixture, b *browser, ws, _ uuid.UUID) {
			peer := claimUUID(t, f, `SELECT id FROM servers WHERE workspace_id=$1 AND NOT revoked ORDER BY id DESC LIMIT 1`, ws)
			replaced = seedReplacedEnvironment(t, f, b, ws, peer)
		})
		replaced.replace(t, r.f)
		r.recoverAll(t, r.exports(t, claimExport{}))
		replaced.want(t, r.f)
	})
}
