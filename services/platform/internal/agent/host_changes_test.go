package agent

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

// A plan resolution reports what applying it would change on the host, by the rules a deploy
// follows: new ports, the certificate contact email and managed egress, and nothing for a
// change that stays within the environment's own unit.
func TestResolutionReportsHostChanges(t *testing.T) {
	j, _ := testVisualJournal(t)
	e := &Engine{Journal: j, ACMEEmail: "ops@example.test"}
	set := smallSet()
	kinds := func(changes []deployment.HostChange) []string {
		out := []string{}
		for _, change := range changes {
			out = append(out, change.Kind)
		}
		return out
	}

	// A fresh host: its first route publishes the web ports and sets up the proxy's settings.
	first, err := e.hostChanges(routesPlan(set))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(kinds(first), []string{deployment.HostChangePorts, deployment.HostChangeCertificateEmail, deployment.HostChangeManagedEgress}) || !slices.Equal(first[0].Ports, []int{80, 443}) {
		t.Fatalf("a first route's host changes: %+v", first)
	}

	// Once those are in place, changing the environment's own routes changes nothing else.
	current := routeSnapshot{Revision: uuid.NewString(), Ports: []int{80, 443}, Email: "ops@example.test", ManagedEgress: true, QualifiedBackends: true, Sets: map[string]deployment.RouteSet{set.UnitID.String(): set}}
	if err := j.saveRouting(routeState{Current: current}); err != nil {
		t.Fatal(err)
	}
	changed := set
	changed.Routes = slices.Clone(set.Routes)
	changed.Routes[0].Domain = "changed." + changed.Routes[0].Domain
	if same, err := e.hostChanges(routesPlan(changed)); err != nil || len(same) != 0 {
		t.Fatalf("a change within the environment was reported as a host change: %+v %v", same, err)
	}

	// A TCP route publishes its port.
	changed.Routes = append(changed.Routes, domain.Route{ID: uuid.New(), TargetID: changed.Routes[0].TargetID, Protocol: "tcp", Domain: "db.example.test", Port: 5432, ListenPort: 15432, TLS: "off", AllowCIDRs: []string{"0.0.0.0/0"}})
	tcp, err := e.hostChanges(routesPlan(changed))
	if err != nil || !slices.Equal(kinds(tcp), []string{deployment.HostChangePorts}) || !slices.Equal(tcp[0].Ports, []int{15432}) {
		t.Fatalf("a new TCP port's host changes: %+v %v", tcp, err)
	}

	// A host with no routes, before or after, is left as it is.
	empty := routesPlan(set)
	empty.UnitID = uuid.New()
	empty.Specification = domain.Specification{Version: 1}
	if err := j.saveRouting(routeState{Current: routeSnapshot{Revision: uuid.NewString()}}); err != nil {
		t.Fatal(err)
	}
	if none, err := e.hostChanges(empty); err != nil || len(none) != 0 {
		t.Fatalf("a plan with no routes changed a host with none: %+v %v", none, err)
	}
}

// A controller that reviews no host changes refuses an observation naming them, so it gets the
// observation older agents send.
func TestHostChangesAreLeftOutForAnOlderController(t *testing.T) {
	observed := deployment.Observation{ObservedAt: time.Now().UTC(), Resources: []deployment.Resource{}, StateHash: "a", HostChanges: []deployment.HostChange{{Kind: deployment.HostChangeManagedEgress}}}
	raw, _ := stablejson.Marshal(observed)
	value := jsontext.Value(raw)
	var stripped deployment.Observation
	if err := json.Unmarshal(*withoutHostChanges(&value), &stripped); err != nil || stripped.HostChanges != nil || stripped.StateHash != "a" {
		t.Fatalf("host changes were not left out: %+v %v", stripped, err)
	}
	observed.HostChanges = nil
	raw, _ = stablejson.Marshal(observed)
	plain := jsontext.Value(raw)
	if got := withoutHostChanges(&plain); string(*got) != string(plain) {
		t.Fatal("an observation without host changes was rewritten")
	}
}

// Management advertises that it reviews host changes on every response. A report naming them
// waits until management has answered since the agent started, rather than lose them; a
// controller that does not advertise it gets the report without them, which it accepts.
func TestHostChangesFollowTheController(t *testing.T) {
	j, _ := testVisualJournal(t)
	advertised := true
	received := []bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var report api.AgentReport
		_ = json.UnmarshalRead(r.Body, &report)
		received = append(received, report.Observation != nil && withHostChanges(report.Observation))
		w.Header().Set("Astilba-Agent-Features", "build-controls")
		if advertised {
			w.Header().Set("Astilba-Agent-Host-Changes", "v1")
		}
	}))
	t.Cleanup(server.Close)
	cfg, err := j.Config()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Origin = server.URL
	if err = j.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	e := &Engine{Journal: j}
	c := NewClient(j, e)
	c.HTTP = server.Client()
	raw, _ := stablejson.Marshal(deployment.Observation{ObservedAt: time.Now().UTC(), Resources: []deployment.Resource{}, StateHash: "a", HostChanges: []deployment.HostChange{{Kind: deployment.HostChangeManagedEgress}}})
	observation := jsontext.Value(raw)
	report := api.AgentReport{Id: uuid.New(), State: "succeeded", Observation: &observation}
	if err = c.call(t.Context(), "POST", "/agent/v1/report", report, nil, "fixture"); err == nil || len(received) != 0 {
		t.Fatalf("a report was sent before management said whether it reviews host changes: %v", err)
	}
	if err = c.call(t.Context(), "POST", "/agent/v1/heartbeat", api.AgentHeartbeat{}, nil, "fixture"); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		advertise bool
		sent      bool
	}{{true, true}, {false, true}, {false, false}, {true, false}, {true, true}} {
		// The response to each report sets what the next one may send.
		advertised = step.advertise
		if err = c.call(t.Context(), "POST", "/agent/v1/report", report, nil, "fixture"); err != nil {
			t.Fatal(err)
		}
		if received[len(received)-1] != step.sent {
			t.Fatalf("sent the host changes %v, want %v", received[len(received)-1], step.sent)
		}
	}
}

// A resolution that found no host changes still says it looked, so management can tell it from
// an agent that never does; a controller that does not read host changes gets neither.
func TestReportingNoHostChangesIsStillAReport(t *testing.T) {
	raw, _ := stablejson.Marshal(deployment.Observation{ObservedAt: time.Now().UTC(), Resources: []deployment.Resource{}, StateHash: "a", HostChangesReported: true})
	observation := jsontext.Value(raw)
	if !withHostChanges(&observation) {
		t.Fatal("a resolution that found no host changes was not treated as reporting them")
	}
	var stripped deployment.Observation
	if err := json.Unmarshal(*withoutHostChanges(&observation), &stripped); err != nil || stripped.HostChangesReported || stripped.HostChanges != nil {
		t.Fatalf("an older controller would receive host change reporting: %+v %v", stripped, err)
	}
}
