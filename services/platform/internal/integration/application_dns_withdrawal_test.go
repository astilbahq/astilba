package integration

import (
	"context"
	"encoding/json/v2"
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
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

// dnsZone is a Cloudflare zone fixture. failDelete answers that many deletions with an
// error; with lose set, each of those deletions happens anyway and only the answer is lost.
type dnsZone struct {
	mu         sync.Mutex
	records    map[string]map[string]any
	failDelete int
	lose       bool
	deletes    int
}

func newDNSZone(t *testing.T, f *fixture) *dnsZone {
	z := &dnsZone{records: map[string]map[string]any{}}
	provider := httptest.NewServer(http.HandlerFunc(z.serve))
	t.Cleanup(provider.Close)
	u, _ := url.Parse(provider.URL)
	f.execution.DNS = &applicationdns.Cloudflare{Zone: strings.Repeat("a", 32), Token: "fixture-dns", HTTP: &http.Client{Transport: dnsTransport{u}, Timeout: time.Second}}
	return z
}

func (z *dnsZone) serve(w http.ResponseWriter, r *http.Request) {
	z.mu.Lock()
	defer z.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer fixture-dns" {
		w.WriteHeader(401)
		return
	}
	var result any = map[string]any{}
	id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	switch r.Method {
	case "GET":
		list := []map[string]any{}
		for _, v := range z.records {
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
		if r.Method == "POST" {
			id = strings.ReplaceAll(uuid.NewString(), "-", "")
		}
		v["id"] = id
		z.records[id] = v
		result = v
	case "DELETE":
		z.deletes++
		if z.failDelete > 0 {
			z.failDelete--
			if z.lose {
				delete(z.records, id)
			}
			w.WriteHeader(502)
			return
		}
		if _, ok := z.records[id]; !ok {
			w.WriteHeader(404)
			return
		}
		delete(z.records, id)
	}
	_ = stablejson.Encode(w, map[string]any{"success": true, "result": result})
}

func (z *dnsZone) add(kind, name, content, comment string) {
	z.mu.Lock()
	defer z.mu.Unlock()
	id := strings.ReplaceAll(uuid.NewString(), "-", "")
	z.records[id] = map[string]any{"id": id, "type": kind, "name": name, "content": content, "comment": comment, "proxied": false, "ttl": 300}
}

// marked counts the address records at name that carry marker.
func (z *dnsZone) marked(name, marker string) int {
	z.mu.Lock()
	defer z.mu.Unlock()
	n := 0
	for _, v := range z.records {
		if v["name"] == name && v["comment"] == marker && (v["type"] == "A" || v["type"] == "AAAA") {
			n++
		}
	}
	return n
}

func (z *dnsZone) snapshot() map[string]map[string]any {
	z.mu.Lock()
	defer z.mu.Unlock()
	out := map[string]map[string]any{}
	for k, v := range z.records {
		out[k] = v
	}
	return out
}

func eventually(t *testing.T, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting until", what)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

type dnsServer struct {
	f            *fixture
	b            *browser
	ws, id       uuid.UUID
	path, marker string
}

// publishedServer enrolls a deployment server and waits until its approved addresses are applied.
func publishedServer(t *testing.T, f *fixture, z *dnsZone) dnsServer {
	ctx := context.Background()
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	enrolled, input, _ := enrollment(t, f, b, ws, "deployment")
	if _, e := f.execution.Enroll(ctx, acknowledging(input), "test"); e != nil {
		t.Fatal(e)
	}
	var installation uuid.UUID
	if e := f.st.Pool.QueryRow(ctx, "SELECT id FROM installation").Scan(&installation); e != nil {
		t.Fatal(e)
	}
	s := dnsServer{f: f, b: b, ws: ws, id: enrolled.Server.Id, path: "/api/v1/workspaces/" + ws.String() + "/servers/" + enrolled.Server.Id.String(), marker: "astilba:" + installation.String() + ":" + enrolled.Server.Id.String()}
	b.call("PUT", s.path+"/domain", api.ConfigureServerDomain{Ipv4: "198.51.100.20", Ipv6: "2001:db8::20"}, 200, nil)
	s.waitState(t, "applied")
	if n := z.marked(s.name(), s.marker); n != 2 {
		t.Fatal("expected an A and an AAAA record, got", n)
	}
	return s
}

func (s dnsServer) name() string { return s.nameUnder(s.f.st.ApplicationDomain) }
func (s dnsServer) nameUnder(base string) string {
	return "*." + domain.ServerDomain(base, s.id)
}
func (s dnsServer) waitState(t *testing.T, state api.ServerDomainState) {
	t.Helper()
	var profile api.ServerDomain
	eventually(t, "the DNS state is "+string(state), func() bool {
		s.b.call("GET", s.path+"/domain", nil, 200, &profile)
		return profile.State == state
	})
}

// removals returns the audited deletions by reason, and how many are unconfirmed.
func (s dnsServer) removals(t *testing.T) (map[string]int, int) {
	rows, e := s.f.st.Pool.Query(context.Background(), "SELECT reason,confirmed_at IS NULL FROM dns_record_removals WHERE server_id=$1", s.id)
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	out, pending := map[string]int{}, 0
	for rows.Next() {
		var reason string
		var unconfirmed bool
		if e = rows.Scan(&reason, &unconfirmed); e != nil {
			t.Fatal(e)
		}
		out[reason]++
		if unconfirmed {
			pending++
		}
	}
	return out, pending
}

func (s dnsServer) publications(t *testing.T) int {
	var n int
	if e := s.f.st.Pool.QueryRow(context.Background(), "SELECT count(*) FROM dns_publications WHERE server_id=$1", s.id).Scan(&n); e != nil {
		t.Fatal(e)
	}
	return n
}

func TestRevokingAServerRemovesOnlyItsDNSRecords(t *testing.T) {
	f := setup(t)
	f.st.ApplicationDomain = "apps.example.test"
	z := newDNSZone(t, f)
	s := publishedServer(t, f, z)
	foreign := map[string]bool{}
	z.add("A", s.name(), "203.0.113.9", "someone else's record")
	z.add("A", s.name(), "203.0.113.10", "astilba:"+uuid.NewString()+":"+s.id.String())
	z.add("TXT", s.name(), "verification", s.marker)
	z.add("A", "*.s"+strings.Repeat("0", 32)+".apps.example.test", "203.0.113.11", s.marker)
	for id := range z.snapshot() {
		foreign[id] = true
	}
	for id, v := range z.snapshot() {
		if v["comment"] == s.marker && v["name"] == s.name() && v["type"] != "TXT" {
			delete(foreign, id)
		}
	}
	// The first deletion happens, but its answer is lost, so the job fails and is retried.
	z.mu.Lock()
	z.failDelete, z.lose = 1, true
	z.mu.Unlock()
	s.b.call("POST", s.path+"/revoke", nil, 204, nil)
	eventually(t, "the revoked server's records are removed", func() bool { return z.marked(s.name(), s.marker) == 0 && s.publications(t) == 0 })
	left := z.snapshot()
	if len(left) != len(foreign) {
		t.Fatal("records Astilba doesn't own were removed", left)
	}
	for id := range foreign {
		if left[id] == nil {
			t.Fatal("a foreign record was removed", id)
		}
	}
	z.mu.Lock()
	deletes := z.deletes
	z.mu.Unlock()
	if deletes != 2 {
		t.Fatal("expected one lost and one retried deletion, got", deletes)
	}
	reasons, pending := s.removals(t)
	if reasons["revoked"] != 2 || len(reasons) != 1 || pending != 0 {
		t.Fatal("deletions were not audited and confirmed", reasons, pending)
	}
	var state string
	var approved *int64
	if e := f.st.Pool.QueryRow(context.Background(), "SELECT state,approved_controller FROM server_domains WHERE server_id=$1", s.id).Scan(&state, &approved); e != nil || state != "review_required" || approved != nil {
		t.Fatal("revocation left the address approval in place", state, approved, e)
	}
}

func TestDNSWithdrawalSurvivesARestartAndALostJob(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.st.ApplicationDomain = "apps.example.test"
	z := newDNSZone(t, f)
	s := publishedServer(t, f, z)
	if e := f.execution.Stop(ctx); e != nil {
		t.Fatal(e)
	}
	s.b.call("POST", s.path+"/revoke", nil, 204, nil)
	// Management restarts from a state that has lost the queued job.
	if _, e := f.st.Pool.Exec(ctx, "DELETE FROM river_job WHERE kind='platform_dns_v1'"); e != nil {
		t.Fatal(e)
	}
	z.mu.Lock()
	z.failDelete = 1
	z.mu.Unlock()
	if e := f.execution.Start(ctx); e != nil {
		t.Fatal(e)
	}
	if e := f.execution.SweepDomains(ctx); e != nil {
		t.Fatal(e)
	}
	eventually(t, "the sweep removes the revoked server's records", func() bool { return z.marked(s.name(), s.marker) == 0 && s.publications(t) == 0 })
	if reasons, pending := s.removals(t); reasons["revoked"] != 2 || pending != 0 {
		t.Fatal("deletions were not audited and confirmed", reasons, pending)
	}
	jobs := func() (all, active int) {
		if e := f.st.Pool.QueryRow(ctx, "SELECT count(*),count(*) FILTER (WHERE state NOT IN ('completed','discarded','cancelled')) FROM river_job WHERE kind='platform_dns_v1'").Scan(&all, &active); e != nil {
			t.Fatal(e)
		}
		return all, active
	}
	eventually(t, "the withdrawal job finishes", func() bool { _, active := jobs(); return active == 0 })
	before, _ := jobs()
	if e := f.execution.SweepDomains(ctx); e != nil {
		t.Fatal(e)
	}
	if after, _ := jobs(); after != before {
		t.Fatal("a finished withdrawal was queued again")
	}
}

func TestDNSFollowsHostedManagementAndTheApplicationDomain(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.st.ApplicationDomain = "apps.example.test"
	var ws uuid.UUID
	if e := f.st.Pool.QueryRow(ctx, "SELECT id FROM workspaces LIMIT 1").Scan(&ws); e != nil {
		t.Fatal(e)
	}
	if _, e := f.st.Pool.Exec(ctx, "INSERT INTO hosted_subscriptions(workspace_id,status,management_until,server_limit) VALUES($1,'granted',now()+interval '1 hour',100)", ws); e != nil {
		t.Fatal(e)
	}
	restart := func(change func()) {
		t.Helper()
		if e := f.execution.Stop(ctx); e != nil {
			t.Fatal(e)
		}
		change()
		if e := f.execution.Start(ctx); e != nil {
			t.Fatal(e)
		}
		if e := f.execution.SweepDomains(ctx); e != nil {
			t.Fatal(e)
		}
	}
	z := newDNSZone(t, f)
	s := publishedServer(t, f, z)

	restart(func() {
		f.st.Hosted = true
		if _, e := f.st.Pool.Exec(ctx, "UPDATE hosted_subscriptions SET management_until=now()-interval '1 second' WHERE workspace_id=$1", ws); e != nil {
			t.Fatal(e)
		}
	})
	eventually(t, "a lapsed workspace's records are removed", func() bool { return z.marked(s.name(), s.marker) == 0 && s.publications(t) == 0 })
	var state, note string
	if e := f.st.Pool.QueryRow(ctx, "SELECT state,note FROM server_domains WHERE server_id=$1", s.id).Scan(&state, &note); e != nil || state != "pending" || !strings.Contains(note, "management is inactive") {
		t.Fatal("the profile doesn't say why its records went", state, note, e)
	}

	// Management resumes with the approval intact, so the records return without an owner.
	restart(func() {
		if _, e := f.st.Pool.Exec(ctx, "UPDATE hosted_subscriptions SET management_until=now()+interval '1 hour' WHERE workspace_id=$1", ws); e != nil {
			t.Fatal(e)
		}
	})
	eventually(t, "resumed management publishes again", func() bool { return z.marked(s.name(), s.marker) == 2 && s.publications(t) == 1 })
	z.add("A", s.name(), "203.0.113.9", "someone else's record")

	old := s.name()
	restart(func() { f.st.ApplicationDomain = "apps2.example.test" })
	eventually(t, "the previous domain's records are removed", func() bool { return z.marked(old, s.marker) == 0 && s.publications(t) == 0 })
	reasons, pending := s.removals(t)
	if reasons["management_inactive"] != 2 || reasons["base_changed"] != 2 || pending != 0 {
		t.Fatal("deletions were not audited and confirmed", reasons, pending)
	}
	left := 0
	for _, v := range z.snapshot() {
		if v["comment"] == "someone else's record" {
			left++
		}
	}
	if left != 1 || z.marked(s.nameUnder("apps2.example.test"), s.marker) != 0 {
		t.Fatal("a foreign record was removed, or a new name was published without approval", z.snapshot())
	}
}

func TestRenewingARevokedServerStillRemovesItsDNSRecords(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.st.ApplicationDomain = "apps.example.test"
	z := newDNSZone(t, f)
	s := publishedServer(t, f, z)
	if e := f.execution.Stop(ctx); e != nil {
		t.Fatal(e)
	}
	// The renewal lands before the withdrawal has run.
	s.b.call("POST", s.path+"/revoke", nil, 204, nil)
	s.b.call("POST", s.path+"/enrollment", nil, 201, nil)
	if e := f.execution.Start(ctx); e != nil {
		t.Fatal(e)
	}
	eventually(t, "the renewed server's old records are removed", func() bool { return z.marked(s.name(), s.marker) == 0 && s.publications(t) == 0 })
	if e := f.execution.SweepDomains(ctx); e != nil {
		t.Fatal(e)
	}
	// Only a new approval publishes again.
	var profile api.ServerDomain
	s.b.call("GET", s.path+"/domain", nil, 200, &profile)
	if profile.State != "review_required" || z.marked(s.name(), s.marker) != 0 {
		t.Fatal("records returned without a new approval", profile.State)
	}
	s.b.call("PUT", s.path+"/domain", api.ConfigureServerDomain{BaseGeneration: profile.Generation, Ipv4: "198.51.100.21"}, 200, nil)
	s.waitState(t, "applied")
	var revoked bool
	if e := f.st.Pool.QueryRow(ctx, "SELECT revoked FROM dns_publications WHERE server_id=$1", s.id).Scan(&revoked); e != nil || revoked || z.marked(s.name(), s.marker) != 1 {
		t.Fatal("a new approval did not publish afresh", revoked, e)
	}
}

func TestRevokingWhileDNSAutomationIsOffRemovesRecordsLater(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.st.ApplicationDomain = "apps.example.test"
	z := newDNSZone(t, f)
	s := publishedServer(t, f, z)
	if e := f.execution.Stop(ctx); e != nil {
		t.Fatal(e)
	}
	provider := f.execution.DNS
	f.execution.DNS = nil
	s.b.call("POST", s.path+"/revoke", nil, 204, nil)
	s.b.call("POST", s.path+"/enrollment", nil, 201, nil)
	f.execution.DNS = provider
	if e := f.execution.Start(ctx); e != nil {
		t.Fatal(e)
	}
	if e := f.execution.SweepDomains(ctx); e != nil {
		t.Fatal(e)
	}
	eventually(t, "records revoked while automation was off are removed", func() bool { return z.marked(s.name(), s.marker) == 0 && s.publications(t) == 0 })
}

func TestTheSweepRemovesRecordsOfAServerRevokedOutsideRevocation(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.st.ApplicationDomain = "apps.example.test"
	z := newDNSZone(t, f)
	s := publishedServer(t, f, z)
	// Offline recovery merges revocations from newer exports straight into servers.
	if _, e := f.st.Pool.Exec(ctx, "UPDATE servers SET revoked=true WHERE id=$1", s.id); e != nil {
		t.Fatal(e)
	}
	// Management sweeps every minute; here each wait step sweeps.
	eventually(t, "the sweep removes the revoked server's records", func() bool {
		if e := f.execution.SweepDomains(ctx); e != nil {
			t.Fatal(e)
		}
		return z.marked(s.name(), s.marker) == 0 && s.publications(t) == 0
	})
}
