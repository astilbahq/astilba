package integration

import (
	"encoding/json/v2"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
)

// retiringHosts lets every host in the group that is still enrolled retire environments.
func (g *revokedGroup) retiringHosts(t *testing.T) {
	t.Helper()
	yes := true
	for _, h := range g.hosts {
		if !h.revoked {
			g.reports(t, h, func(c *api.AgentCapabilities, _ *api.HostPreflight) { c.Retirement = &yes })
		}
	}
}

// retired retires the environment and waits for its host to finish, returning the command it was
// sent and the environment as it ends.
func (g *revokedGroup) retired(t *testing.T, e *primaryEnvironment) (deployment.Payload, api.Environment) {
	t.Helper()
	var retiring api.Environment
	g.b.call("POST", e.path+"/retire", api.EnvironmentLifecycleRequest{ConfirmName: "Stack"}, 201, &retiring)
	if retiring.State != api.EnvironmentStateRetiring || retiring.RetirementOperationId == nil {
		t.Fatalf("retirement was not sent to a host: %+v", retiring)
	}
	if state := g.finished(t, *retiring.RetirementOperationId); state != "succeeded" {
		t.Fatalf("retirement ended %s", state)
	}
	var command api.ExecutionCommand
	if err := g.f.st.Pool.QueryRow(g.ctx, `SELECT command FROM execution_operations WHERE id=$1`, *retiring.RetirementOperationId).Scan(&command); err != nil {
		t.Fatal(err)
	}
	var payload deployment.Payload
	if command.Visual == nil || json.Unmarshal(*command.Visual, &payload) != nil || payload.Action != "retire" {
		t.Fatal("not a retirement command")
	}
	var retired api.Environment
	g.b.call("GET", e.path, nil, 200, &retired)
	if retired.State != api.EnvironmentStateRetired {
		t.Fatalf("environment ended %s", retired.State)
	}
	return payload, retired
}

// A cross-host placement is refused, because one command retires one server's unit.
func TestRetirementRefusesACrossHostPlacement(t *testing.T) {
	g := newRevokedGroup(t, 2)
	a, b := g.hosts[0], g.hosts[1]
	g.retiringHosts(t)
	e, _ := g.filesStack(t, a)
	g.applied(t, &e, g.place(t, &e, a.id, b.id))
	var refused api.Error
	g.b.call("POST", e.path+"/retire", api.EnvironmentLifecycleRequest{ConfirmName: "Stack"}, 409, &refused)
	if refused.Fields == nil || len(*refused.Fields) != 1 || (*refused.Fields)[0] != "retirement."+domain.RetirementCrossHost {
		t.Fatalf("a cross-host placement was not refused as one: %+v", refused)
	}
}

// A replaced primary leaves its demoted unit behind, detached from its revoked server. That runs
// nothing, so it does not hold retirement, which goes to the new primary's server.
func TestRetirementAfterAPrimaryReplacement(t *testing.T) {
	g := newRevokedGroup(t, 2)
	gone, b := g.hosts[0], g.hosts[1]
	e, volume := g.filesStack(t, gone)
	g.revoke(t, gone)
	g.retiringHosts(t)
	replaced := g.applied(t, &e, g.replace(&e, b.id, volume))
	payload, retired := g.retired(t, &e)
	if payload.Plan.ID != replaced.ID || payload.Plan.ServerID != b.id {
		t.Fatalf("retirement removed plan %s on %s, not the replacement %s on %s", payload.Plan.ID, payload.Plan.ServerID, replaced.ID, b.id)
	}
	if retired.RetiredWithoutHost == nil || *retired.RetiredWithoutHost {
		t.Fatal("a retirement its server finished was recorded as not contacting it")
	}
}

// A replacement whose deployment failed has moved the environment's unit to the new server, while
// the runtime still names the plan on the revoked one. Retirement removes what the attempt left on
// the new server and records that the revoked one was not contacted.
func TestRetirementAfterAFailedPrimaryReplacement(t *testing.T) {
	g := newRevokedGroup(t, 2)
	gone, b := g.hosts[0], g.hosts[1]
	e, volume := g.filesStack(t, gone)
	g.revoke(t, gone)
	g.retiringHosts(t)
	g.mu.Lock()
	g.failing["visual-deploy/"+b.id.String()] = true
	g.mu.Unlock()
	status := g.reviewed(t, &e, g.replace(&e, b.id, volume))
	if state := g.finished(t, g.apply(t, &e, status).Id); state != "failed" {
		t.Fatalf("the failing attempt ended %s", state)
	}
	unit, server := g.unitOf(t, &e, true)
	if server != b.id {
		t.Fatal("the failed replacement did not move the unit")
	}
	payload, retired := g.retired(t, &e)
	if payload.Plan.ServerID != b.id || payload.Plan.UnitID != unit || len(payload.Plan.Specification.Applications) != 0 {
		t.Fatalf("retirement was not sent for the new unit on its server: %+v", payload.Plan)
	}
	if retired.RetiredWithoutHost == nil || !*retired.RetiredWithoutHost {
		t.Fatal("the revoked primary was not recorded as not contacted")
	}
	var runtime int
	if err := g.f.st.Pool.QueryRow(g.ctx, `SELECT count(*) FROM visual_runtime WHERE environment_id=$1`, uuid.MustParse(e.path[len(e.path)-36:])).Scan(&runtime); err != nil || runtime != 0 {
		t.Fatalf("runtime rows after retirement: %d %v", runtime, err)
	}
}
