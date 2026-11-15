package integration

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/google/uuid"
)

// hold makes every record lookup wait until release, which the test's cleanup also calls.
func (z *dnsZone) hold(t *testing.T) (<-chan struct{}, func()) {
	t.Helper()
	gate, entered := make(chan struct{}), make(chan struct{}, 64)
	z.mu.Lock()
	z.gate, z.entered = gate, entered
	z.mu.Unlock()
	release := sync.OnceFunc(func() {
		z.mu.Lock()
		z.gate = nil
		z.mu.Unlock()
		close(gate)
	})
	t.Cleanup(release)
	return entered, release
}

// hungDNSZone is a zone whose client waits as long as a held lookup does, rather than failing it.
func hungDNSZone(t *testing.T, f *fixture) *dnsZone {
	z := newDNSZone(t, f)
	f.execution.DNS.HTTP.Timeout = 30 * time.Second
	return z
}

// configuredServer creates a server and approves addresses for it, which queues a DNS job.
func configuredServer(t *testing.T, s dnsServer, name, ipv4 string) dnsServer {
	t.Helper()
	var enrollment api.Enrollment
	s.b.call("POST", "/api/v1/workspaces/"+s.ws.String()+"/servers", api.CreateServer{Name: name, Role: "deployment"}, 201, &enrollment)
	other := s
	other.id = enrollment.Server.Id
	other.path = "/api/v1/workspaces/" + s.ws.String() + "/servers/" + other.id.String()
	other.marker = s.marker[:len(s.marker)-len(s.id.String())] + other.id.String()
	other.b.call("PUT", other.path+"/domain", api.ConfigureServerDomain{Ipv4: ipv4}, 200, nil)
	return other
}

// requireNoDNSLock shows that no transaction or session holds the server's DNS lock, which
// revocation, recovery and configuration wait for.
func requireNoDNSLock(t *testing.T, f *fixture, server uuid.UUID) {
	t.Helper()
	var free bool
	if e := f.st.Pool.QueryRow(t.Context(), "SELECT pg_try_advisory_xact_lock(hashtextextended($1,32))", server.String()).Scan(&free); e != nil {
		t.Fatal(e)
	}
	if !free {
		t.Fatal("the server's DNS lock is held across a provider call")
	}
}

// A hung DNS provider must leave the pool to everyone else, however many jobs wait on it, and
// must not hold up the revocation, recovery or configuration of the servers it serves. Each
// of these waited on the job's session lock before.
func TestHungDNSProviderHoldsNothing(t *testing.T) {
	f := setup(t)
	f.st.ApplicationDomain = "apps.example.test"
	z := hungDNSZone(t, f)
	s := publishedServer(t, f, z)
	entered, release := z.hold(t)
	// DNS jobs share the default queue, which runs four at a time: fill it.
	servers := []dnsServer{s}
	s.b.call("PUT", s.path+"/domain", api.ConfigureServerDomain{Ipv4: "198.51.100.21", Ipv6: "2001:db8::21", BaseGeneration: 1}, 200, nil)
	for i, address := range []string{"198.51.100.31", "198.51.100.32", "198.51.100.33"} {
		servers = append(servers, configuredServer(t, s, "Hung "+string(rune('A'+i)), address))
	}
	for range servers {
		waitEntered(t, entered)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	// River keeps a listener and may be running a job, so leave it room.
	for range f.st.Pool.Config().MaxConns - 2 {
		conn, err := f.st.Pool.Acquire(ctx)
		if err != nil {
			t.Fatal("hung DNS jobs pinned the pool:", err)
		}
		defer conn.Release()
	}
	for _, server := range servers {
		requireNoDNSLock(t, f, server.id)
	}
	requireNoOpenTransaction(t, ctx, f)
	revoked, reconfigured := servers[0], servers[1]
	finished := make(chan reply, 2)
	go func() { finished <- <-revoked.b.send("POST", revoked.path+"/revoke", nil) }()
	go func() {
		finished <- <-reconfigured.b.send("PUT", reconfigured.path+"/domain", api.ConfigureServerDomain{Ipv4: "198.51.100.42", BaseGeneration: 1})
	}()
	for range 2 {
		select {
		case r := <-finished:
			if r.err != nil || r.status != 204 && r.status != 200 {
				t.Fatalf("a write failed during the hang: %d %s %v", r.status, r.body, r.err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("a revocation or configuration waited for the DNS provider")
		}
	}
	release()
	// The jobs that were asking still converge on what the database says now.
	eventually(t, "the revoked server's records are removed", func() bool {
		return z.marked(revoked.name(), revoked.marker) == 0 && revoked.publications(t) == 0
	})
	reconfigured.waitState(t, "applied")
	eventually(t, "the latest approved address is published", func() bool {
		for _, v := range z.snapshot() {
			if v["name"] == reconfigured.name() && v["comment"] == reconfigured.marker && v["content"] != "198.51.100.42" {
				return false
			}
		}
		return z.marked(reconfigured.name(), reconfigured.marker) == 1
	})
}

// A job's lease can run out while the provider hangs, and another job take it. The first
// job's later write must be refused, and the job holding the lease decides the outcome.
func TestDNSWriteIsRefusedOnceTheLeaseIsTakenOver(t *testing.T) {
	f := setup(t)
	f.st.ApplicationDomain = "apps.example.test"
	z := hungDNSZone(t, f)
	s := publishedServer(t, f, z)
	entered, release := z.hold(t)
	s.b.call("PUT", s.path+"/domain", api.ConfigureServerDomain{Ipv4: "198.51.100.21", BaseGeneration: 1}, 200, nil)
	waitEntered(t, entered)
	var first uuid.UUID
	if e := f.st.Pool.QueryRow(t.Context(), "SELECT holder FROM server_dns_leases WHERE server_id=$1", s.id).Scan(&first); e != nil {
		t.Fatal("the asking job holds no lease", e)
	}
	// The lease term passes while the provider hangs. The database's clock decides expiry.
	if _, e := f.st.Pool.Exec(t.Context(), "UPDATE server_dns_leases SET expires_at=now()-interval '1 second' WHERE server_id=$1", s.id); e != nil {
		t.Fatal(e)
	}
	// A new approval queues another job, which takes the expired lease and asks in turn.
	s.b.call("PUT", s.path+"/domain", api.ConfigureServerDomain{Ipv4: "198.51.100.22", BaseGeneration: 2}, 200, nil)
	waitEntered(t, entered)
	var second uuid.UUID
	if e := f.st.Pool.QueryRow(t.Context(), "SELECT holder FROM server_dns_leases WHERE server_id=$1", s.id).Scan(&second); e != nil || second == first {
		t.Fatal("the second job did not take over the lease", e)
	}
	release()
	eventually(t, "the first job's write is refused", func() bool {
		var refused bool
		if e := f.st.Pool.QueryRow(t.Context(), "SELECT EXISTS(SELECT 1 FROM river_job WHERE kind='platform_dns_v1' AND args->>'server'=$1 AND (args->>'generation')::bigint=2 AND errors::text LIKE '%DNS lease%')", s.id.String()).Scan(&refused); e != nil {
			t.Fatal(e)
		}
		return refused
	})
	s.waitState(t, "applied")
	eventually(t, "only the latest approved address is published", func() bool {
		for _, v := range z.snapshot() {
			if v["name"] == s.name() && v["comment"] == s.marker && v["content"] != "198.51.100.22" {
				return false
			}
		}
		return z.marked(s.name(), s.marker) == 1
	})
}
