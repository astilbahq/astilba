package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/applicationdns"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
)

type dnsTransport struct{ url *url.URL }

func (d dnsTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host != "api.cloudflare.com" {
		return nil, fmt.Errorf("unexpected provider origin")
	}
	clone := r.Clone(r.Context())
	u := *r.URL
	clone.URL = &u
	clone.URL.Scheme = d.url.Scheme
	clone.URL.Host = d.url.Host
	return http.DefaultTransport.RoundTrip(clone)
}
func TestApplicationDomainScopeAndRecovery(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.st.ApplicationDomain = "apps.example.test"
	b, scope, env, path := automationEnvironment(t, f)
	ws := scope.WorkspaceId
	enrolled, input, _ := enrollment(t, f, b, ws, "deployment")
	_, build, _ := enrollment(t, f, b, ws, "build")
	other, _, _ := enrollment(t, f, b, ws, "deployment")
	yes := true
	input.Capabilities.Visual = &yes
	if _, e := f.execution.Enroll(ctx, input); e != nil {
		t.Fatal(e)
	}
	prefix := "/api/v1/workspaces/" + ws.String()
	domainPath := prefix + "/servers/" + enrolled.Server.Id.String() + "/domain"
	b.call("PUT", prefix+"/servers/"+build.ServerId.String()+"/domain", api.ConfigureServerDomain{Ipv4: "198.51.100.7"}, 400, nil)
	b.call("PUT", domainPath, api.ConfigureServerDomain{Ipv4: "127.0.0.1"}, 400, nil)
	var profile api.ServerDomain
	b.call("PUT", domainPath, api.ConfigureServerDomain{Ipv4: "198.51.100.7"}, 200, &profile)
	if profile.State != "manual" || profile.Wildcard != "*."+domain.ServerDomain(f.st.ApplicationDomain, enrolled.Server.Id) {
		t.Fatal("manual setup did not expose exact provider records", profile)
	}
	b.call("PUT", domainPath, api.ConfigureServerDomain{Ipv4: "198.51.100.8"}, 409, nil)
	app := uuid.New()
	image := "nginx:alpine"
	spec := domain.Specification{Version: 1, Applications: []domain.Application{{ID: app, Name: "Web", Source: domain.ApplicationSource{Kind: "image", Image: &image}}}, Variables: []domain.Variable{}}
	var draft api.Revision
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": env.DraftRevisionId, "specification": spec}, 200, &draft)
	var suggestion api.DomainSuggestion
	b.call("POST", path+"/domain-suggestion", map[string]any{"applicationId": app, "serverId": enrolled.Server.Id}, 200, &suggestion)
	if suggestion.Hostname != domain.ApplicationHostname(f.st.ApplicationDomain, ws, env.Id, app, enrolled.Server.Id) {
		t.Fatal("wrong identity binding")
	}
	b.call("POST", strings.Replace(path, scope.ProjectId.String(), uuid.NewString(), 1)+"/domain-suggestion", map[string]any{"applicationId": app, "serverId": enrolled.Server.Id}, 404, nil)
	spec.Routes = []domain.Route{{ID: uuid.New(), TargetID: app, Domain: suggestion.Hostname, Protocol: "http", Port: 8080, TLS: "managed", AllowCIDRs: []string{}}}
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": draft.Id, "specification": spec}, 200, &draft)
	grant := issueAutomation(t, b, path, domain.ConfigurationWrite, domain.EnvironmentRead, domain.DeploymentPlan)
	bearerCall(t, f, *grant.Token, "GET", domainPath, nil, 403, nil)
	bearerCall(t, f, *grant.Token, "POST", path+"/domain-suggestion", map[string]any{"applicationId": app, "serverId": enrolled.Server.Id}, 403, nil)
	spec.Routes[0].Domain = domain.ApplicationHostname(f.st.ApplicationDomain, ws, uuid.New(), app, enrolled.Server.Id)
	bearerCall(t, f, *grant.Token, "PUT", path+"/draft", map[string]any{"baseRevisionId": draft.Id, "specification": spec}, 400, nil)
	spec.Routes[0].Domain = suggestion.Hostname
	b.call("POST", path+"/deployment-plans", api.VisualPlanRequest{ServerId: other.Server.Id, BaseRevisionId: draft.Id, AllowHostChanges: true}, 400, nil)
	// Ownership/recent authentication stay on the domain mutation itself.
	account, e := f.st.Queries.AccountByID(ctx, f.owner)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.execution.ConfigureServerDomain(ctx, store.Principal{Account: account}, ws, enrolled.Server.Id, api.ConfigureServerDomain{BaseGeneration: profile.Generation, Ipv4: "198.51.100.9"}); e != domain.RecentRequired {
		t.Fatalf("missing recent authentication: %v", e)
	}

	var mu sync.Mutex
	records := map[string]map[string]any{}
	lost := true
	requests := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		requests++
		if r.Header.Get("Authorization") != "Bearer fixture-dns" {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		var result any
		switch r.Method {
		case "GET":
			list := []map[string]any{}
			for _, v := range records {
				if v["name"] == r.URL.Query().Get("name") {
					list = append(list, v)
				}
			}
			result = list
		case "POST", "PUT":
			raw, _ := io.ReadAll(r.Body)
			var v map[string]any
			if json.Unmarshal(raw, &v) != nil {
				w.WriteHeader(400)
				return
			}
			id := strings.ReplaceAll(uuid.NewString(), "-", "")
			if r.Method == "PUT" {
				id = r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			}
			v["id"] = id
			records[id] = v
			result = v
			if lost {
				lost = false
				w.WriteHeader(502)
				return
			}
		case "DELETE":
			delete(records, r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:])
			result = map[string]any{}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": result})
	}))
	defer provider.Close()
	u, _ := url.Parse(provider.URL)
	f.execution.DNS = &applicationdns.Cloudflare{Zone: strings.Repeat("a", 32), Token: "fixture-dns", HTTP: &http.Client{Transport: dnsTransport{u}, Timeout: time.Second}}
	b.call("PUT", domainPath, api.ConfigureServerDomain{BaseGeneration: profile.Generation, Ipv4: "198.51.100.10"}, 200, &profile)
	wait := func(state api.ServerDomainState) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			b.call("GET", domainPath, nil, 200, &profile)
			if profile.State == state {
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Fatalf("DNS state %s, wanted %s", profile.State, state)
	}
	wait("failed")
	// Reapproval after a lost successful create must find its marker, not duplicate it.
	b.call("PUT", domainPath, api.ConfigureServerDomain{BaseGeneration: profile.Generation, Ipv4: "198.51.100.11"}, 200, &profile)
	wait("applied")
	mu.Lock()
	if len(records) != 1 {
		t.Fatal("lost create duplicated DNS", records)
	}
	mu.Unlock()
	if e = f.execution.Stop(ctx); e != nil {
		t.Fatal(e)
	}
	// A management snapshot contains old desired address A while the live provider
	// now has B. Recovery must fence A even after controller transfer completes.
	_, e = f.st.Pool.Exec(ctx, "UPDATE server_domains SET ipv4='198.51.100.10',generation=2,state='pending' WHERE server_id=$1", enrolled.Server.Id)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.st.PrepareRecovery(ctx, enrolled.Server.Id, 1); e != nil {
		t.Fatal(e)
	}
	ticket, e := f.execution.PrepareTransfer(ctx, store.Principal{Account: account, Recent: true}, ws, enrolled.Server.Id)
	if e != nil {
		t.Fatal(e)
	}
	input.Token = ticket.Token
	input.ControllerGeneration = &ticket.Generation
	input.Inventory = &api.ControllerInventory{Units: map[string]string{}, Schedules: map[string]int64{}}
	if _, e = f.execution.Enroll(ctx, input); e != nil {
		t.Fatal(e)
	}
	if e = f.execution.SweepDomains(ctx); e != nil {
		t.Fatal(e)
	}
	var queued int
	if e = f.st.Pool.QueryRow(ctx, "SELECT count(*) FROM river_job WHERE kind='platform_dns_v1'").Scan(&queued); e != nil || queued != 0 {
		t.Fatalf("recovery resumed DNS: %d %v", queued, e)
	}
	b.call("GET", domainPath, nil, 200, &profile)
	if profile.State != "review_required" || profile.Ipv4 != "198.51.100.10" {
		t.Fatal("recovery lost config or approval fence", profile)
	}
	mu.Lock()
	for _, v := range records {
		if v["content"] != "198.51.100.11" {
			t.Fatal("stale recovery rewrote live DNS")
		}
	}
	before := requests
	mu.Unlock()
	if e = f.execution.Start(ctx); e != nil {
		t.Fatal(e)
	}
	b.call("PUT", domainPath, api.ConfigureServerDomain{BaseGeneration: profile.Generation, Ipv4: "198.51.100.12"}, 200, &profile)
	wait("applied")
	mu.Lock()
	if requests <= before {
		t.Fatal("renewed approval did not reconcile")
	}
	for _, v := range records {
		v["comment"] = "foreign owner"
	}
	mu.Unlock()
	b.call("PUT", domainPath, api.ConfigureServerDomain{BaseGeneration: profile.Generation, Ipv4: "198.51.100.13"}, 200, &profile)
	wait("failed")
	mu.Lock()
	defer mu.Unlock()
	for _, v := range records {
		if v["content"] != "198.51.100.12" {
			t.Fatal("foreign record was overwritten")
		}
	}
}

func TestApplicationDomainSweepSkipsExpiredWorkspaces(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	if e := f.execution.Stop(ctx); e != nil {
		t.Fatal(e)
	}
	f.st.Hosted = true
	f.st.ApplicationDomain = "apps.example.test"
	f.execution.DNS = &applicationdns.Cloudflare{}
	var activeWS uuid.UUID
	if e := f.st.Pool.QueryRow(ctx, "SELECT id FROM workspaces LIMIT 1").Scan(&activeWS); e != nil {
		t.Fatal(e)
	}
	inactiveWS := uuid.New()
	_, e := f.st.Pool.Exec(ctx, "INSERT INTO workspaces(id,name) VALUES($1,'Expired DNS fixture')", inactiveWS)
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.st.Pool.Exec(ctx, "INSERT INTO hosted_subscriptions(workspace_id,status,management_until,server_limit) VALUES($1,'granted',now()+interval '1 hour',100),($2,'granted',now()-interval '1 hour',100)", activeWS, inactiveWS)
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.st.Pool.Exec(ctx, `INSERT INTO servers(id,workspace_id,name,role) SELECT ('00000000-0000-4000-8000-'||lpad(i::text,12,'0'))::uuid,$1,'Expired','deployment' FROM generate_series(1,100) i`, inactiveWS)
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.st.Pool.Exec(ctx, `INSERT INTO server_domains(server_id,workspace_id,base,ipv4,approved_controller,state,observed_at) SELECT id,workspace_id,$1,'198.51.100.1',controller_generation,'applied','2000-01-01' FROM servers WHERE workspace_id=$2`, f.st.ApplicationDomain, inactiveWS)
	if e != nil {
		t.Fatal(e)
	}
	active := uuid.MustParse("ffffffff-ffff-4fff-8fff-ffffffffffff")
	_, e = f.st.Pool.Exec(ctx, "INSERT INTO servers(id,workspace_id,name,role) VALUES($1,$2,'Active','deployment')", active, activeWS)
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.st.Pool.Exec(ctx, "INSERT INTO server_domains(server_id,workspace_id,base,ipv4,approved_controller,state,observed_at) VALUES($1,$2,$3,'198.51.100.2',0,'applied','2001-01-01')", active, activeWS, f.st.ApplicationDomain)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.execution.SweepDomains(ctx); e != nil {
		t.Fatal(e)
	}
	var count int
	var server string
	if e = f.st.Pool.QueryRow(ctx, "SELECT count(*),min(args->>'server') FROM river_job WHERE kind='platform_dns_v1'").Scan(&count, &server); e != nil || count != 1 || server != active.String() {
		t.Fatalf("eligible DNS drift checks starved: count=%d server=%s error=%v", count, server, e)
	}
}
