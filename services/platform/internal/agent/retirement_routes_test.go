package agent

import (
	"slices"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
)

// Retiring an environment withdraws its routes and changes nothing else about the host's ingress,
// even when its last plan approved host changes and the host's settings have moved on since: the
// certificate email, managed egress, ports and so the proxy's arguments stay as they are, and other
// environments' routes are kept.
func TestRetirementWithdrawsRoutesWithoutHostChanges(t *testing.T) {
	server, env, app := uuid.New(), uuid.New(), uuid.New()
	plan := deployment.Plan{ID: uuid.New(), ServerID: server, EnvironmentID: env, UnitID: uuid.New(), HostChanges: true, AcceptDrift: true,
		Specification: domain.Specification{Version: 1, Applications: []domain.Application{{ID: app, Name: "Web"}},
			Routes: []domain.Route{{ID: uuid.New(), TargetID: app, Protocol: "http", Domain: "retired.example.test", Port: 8080, TLS: "managed", AllowCIDRs: []string{}},
				{ID: uuid.New(), TargetID: app, Protocol: "tcp", ListenPort: 5433, Port: 5432, AllowCIDRs: []string{}}}}}
	other, otherApp := uuid.New(), uuid.New()
	current := routeSnapshot{
		Sets: map[string]deployment.RouteSet{
			plan.UnitID.String(): {UnitID: plan.UnitID, EnvironmentID: env, Routes: plan.Specification.Routes, Backends: map[string]deployment.Backend{app.String(): plan.Backend(app)}},
			other.String():       {UnitID: other, EnvironmentID: uuid.New(), Routes: []domain.Route{{ID: uuid.New(), TargetID: otherApp, Protocol: "http", Domain: "kept.example.test", Port: 80, TLS: "off", AllowCIDRs: []string{}}}, Backends: map[string]deployment.Backend{otherApp.String(): {Address: deployment.ServiceName(otherApp)}}},
		},
		Ports: []int{80, 443, 5433}, Email: "previous@example.test", QualifiedBackends: true,
	}
	engine := &Engine{ACMEEmail: "changed@example.test"}

	withdrawn := withdrawnRoutes(plan)
	if withdrawn.HostChanges || withdrawn.AcceptDrift || len(withdrawn.Specification.Routes) != 0 {
		t.Fatalf("the withdrawn plan still carries host approval or routes: %+v", withdrawn)
	}
	next, err := engine.candidateRoutes(current, withdrawn)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := next.Sets[plan.UnitID.String()]; ok {
		t.Fatal("the retired unit's routes were kept")
	}
	if _, ok := next.Sets[other.String()]; !ok {
		t.Fatal("another environment's routes were removed")
	}
	if next.Email != current.Email || next.ManagedEgress != current.ManagedEgress || !slices.Equal(next.Ports, current.Ports) || !slices.Equal(proxyArgs(next), proxyArgs(current)) {
		t.Fatalf("withdrawal changed the host's ingress: email %q, egress %v, ports %v", next.Email, next.ManagedEgress, next.Ports)
	}

	// The last environment on the host leaves the proxy's ports and settings as they were too.
	delete(current.Sets, other.String())
	if next, err = engine.candidateRoutes(current, withdrawn); err != nil || len(next.Sets) != 0 || !slices.Equal(proxyArgs(next), proxyArgs(current)) {
		t.Fatalf("withdrawing the last routes changed the proxy: %v %+v", err, next)
	}
}
