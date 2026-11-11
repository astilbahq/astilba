package integration

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	dbmigrate "github.com/astilbahq/astilba/services/platform/db"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pressly/goose/v3"
)

// networkWithPendingRevocation gives ws a converged-looking network of three members whose
// first member was just revoked, so the two survivors owe a peer removal.
func networkWithPendingRevocation(t *testing.T, f *fixture, b *browser, ws uuid.UUID) (survivors []uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.st.Pool.Exec(ctx, `INSERT INTO workspace_networks(id,workspace_id,prefix,generation) VALUES($1,$1,'10.251.0.0/24',2)`, ws); err != nil {
		t.Fatal(err)
	}
	var prior []hostnetwork.Manifest
	for _, address := range []string{"10.251.0.2", "10.251.0.3", "10.251.0.4"} {
		_, in, _ := enrollment(t, f, b, ws, "deployment")
		if _, err := f.execution.Enroll(ctx, in, "test"); err != nil {
			t.Fatal(err)
		}
		membership := uuid.New()
		identity, err := (hostnetwork.Host{Dir: t.TempDir()}).Identity(membership)
		if err != nil {
			t.Fatal(err)
		}
		prior = append(prior, hostnetwork.Manifest{Version: 1, NetworkID: ws, MembershipID: membership, ServerID: in.ServerId, Generation: 1, Prefix: "10.251.0.0/24", Address: address, PublicKey: identity.PublicKey, ListenPort: 51820})
	}
	for i := range prior {
		for j, peer := range prior {
			if i != j {
				prior[i].Peers = append(prior[i].Peers, hostnetwork.Peer{ServerID: peer.ServerID, MembershipID: peer.MembershipID, PublicKey: peer.PublicKey, Address: peer.Address, Endpoint: "192.0.2.2:51820"})
			}
		}
		prior[i].Sort()
		raw, _ := stablejson.Marshal(prior[i])
		if _, err := f.st.Pool.Exec(ctx, `INSERT INTO network_members(workspace_id,network_id,server_id,membership_id,address,endpoint,listen_port,cpu_millis,memory_mib,unbounded_inventory_hash,public_key,enabled,approved_controller,generation,acknowledged_generation,installed_manifest) VALUES($1,$1,$2,$3,$4,'192.0.2.2:51820',51820,1000,1024,$5,$6,$7,0,2,1,$8)`, ws, prior[i].ServerID, prior[i].MembershipID, prior[i].Address, hostnetwork.StableInventory(nil), prior[i].PublicKey, i != 0, raw); err != nil {
			t.Fatal(err)
		}
		if i != 0 {
			survivors = append(survivors, prior[i].ServerID)
		}
	}
	return survivors
}

func waitFor(t *testing.T, what string, limit time.Duration, test func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for !test() {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func firingAlerts(t *testing.T, f *fixture, ws uuid.UUID, kind string) int {
	t.Helper()
	var count int
	if err := f.st.Pool.QueryRow(context.Background(), `SELECT count(*) FROM operational_alerts WHERE workspace_id=$1 AND kind=$2 AND state='firing'`, ws, kind).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// A workspace whose reconciliation fails sits first in the scan and stays there unless the
// sweep moves on, which would leave every other tenant's revoked peers installed.
func TestNetworkSweepDeliversRevocationsPastAnotherWorkspacesFailure(t *testing.T) {
	for _, tc := range []struct {
		name          string
		poison, clear string
	}{
		{name: "manifest_fails_validation", poison: `UPDATE network_members SET endpoint='not-an-endpoint' WHERE workspace_id=$1 AND enabled`, clear: `UPDATE network_members SET endpoint='192.0.2.2:51820' WHERE workspace_id=$1`},
		{name: "counter_overflows", poison: `UPDATE servers SET update_generation=9223372036854775807 WHERE workspace_id=$1`, clear: `UPDATE servers SET update_generation=0 WHERE workspace_id=$1`},
		{name: "workspace_lock_held"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			b := f.browser(t)
			b.login("owner@example.test", f.password)
			if err := f.execution.Stop(ctx); err != nil {
				t.Fatal(err)
			}
			failing, victim := uuid.New(), uuid.New()
			for _, ws := range []uuid.UUID{failing, victim} {
				if _, err := f.st.Pool.Exec(ctx, `INSERT INTO workspaces(id,name) VALUES($1,'Network tenant')`, ws); err != nil {
					t.Fatal(err)
				}
				if _, err := f.st.Pool.Exec(ctx, `INSERT INTO memberships(workspace_id,account_id,role) VALUES($1,$2,'owner')`, ws, f.owner); err != nil {
					t.Fatal(err)
				}
			}
			survivors := map[uuid.UUID][]uuid.UUID{failing: networkWithPendingRevocation(t, f, b, failing), victim: networkWithPendingRevocation(t, f, b, victim)}
			// Already swept once, so only the failing workspace is at the head of the scan.
			if _, err := f.st.Pool.Exec(ctx, `UPDATE workspace_networks SET checked_at=now() WHERE workspace_id=$1`, victim); err != nil {
				t.Fatal(err)
			}
			if tc.poison != "" {
				if _, err := f.st.Pool.Exec(ctx, tc.poison, failing); err != nil {
					t.Fatal(err)
				}
			}
			release := func() {}
			if tc.poison == "" {
				_, done, err := f.st.BeginWorkspaceWrite(ctx, failing)
				if err != nil {
					t.Fatal(err)
				}
				release = done
				defer func() { release() }()
			}
			current, err := execution.New(f.st)
			if err != nil {
				t.Fatal(err)
			}
			if err = current.Start(ctx); err != nil {
				t.Fatal(err)
			}
			defer current.Stop(ctx)
			restrictions := func(ws uuid.UUID) int {
				t.Helper()
				var count int
				if err := f.st.Pool.QueryRow(ctx, `SELECT count(DISTINCT server_id) FROM execution_operations WHERE workspace_id=$1 AND server_id=ANY($2) AND kind='network' AND command->'network'->>'action'='restrict'`, ws, survivors[ws]).Scan(&count); err != nil {
					t.Fatal(err)
				}
				return count
			}
			waitFor(t, "another workspace's failure stopped this workspace's peer removal", 30*time.Second, func() bool { return restrictions(victim) == 2 })
			var failingSince bool
			waitFor(t, "failure was not recorded against its workspace", 30*time.Second, func() bool {
				if err := f.st.Pool.QueryRow(ctx, `SELECT reconcile_failing_since IS NOT NULL FROM workspace_networks WHERE workspace_id=$1`, failing).Scan(&failingSince); err != nil {
					t.Fatal(err)
				}
				return failingSince
			})
			if count := restrictions(failing); count != 0 {
				t.Fatalf("failing workspace reported %d peer removals", count)
			}
			// A removal that was skipped still reads as unfinished.
			var view struct {
				State   string `json:"state"`
				Members []struct {
					State string `json:"state"`
				} `json:"members"`
			}
			b.call("GET", "/api/v1/workspaces/"+failing.String()+"/network", nil, 200, &view)
			if view.State != "pending" || view.Members == nil {
				t.Fatalf("skipped revocation reads as finished: %+v", view)
			}
			pending := 0
			for _, m := range view.Members {
				if m.State == "revocation_pending" {
					pending++
				}
			}
			if pending != 1 {
				t.Fatalf("skipped revocation reads as finished: %+v", view)
			}
			var retried int
			if err = f.st.Pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind='platform_network_sweep_v1' AND state='retryable'`).Scan(&retried); err != nil {
				t.Fatal(err)
			}
			if retried != 0 {
				t.Fatalf("%d sweep jobs treated one workspace's failure as their own", retried)
			}
			if tc.poison != "" {
				// The alert waits out a brief failure, then stays until the sweep succeeds.
				alertSweep(t, f, failing)
				if firing := firingAlerts(t, f, failing, "network_problem"); firing != 0 {
					t.Fatalf("%d alerts for a failure that has only just begun", firing)
				}
				if _, err = f.st.Pool.Exec(ctx, `UPDATE workspace_networks SET reconcile_failing_since=now()-interval '3 minutes' WHERE workspace_id=$1`, failing); err != nil {
					t.Fatal(err)
				}
				alertSweep(t, f, failing)
				if firing := firingAlerts(t, f, failing, "network_problem"); firing != 1 {
					t.Fatalf("%d alerts for a sweep that keeps failing", firing)
				}
				if _, err = f.st.Pool.Exec(ctx, tc.clear, failing); err != nil {
					t.Fatal(err)
				}
			}
			release()
			release = func() {}
			waitFor(t, "workspace did not recover after its failure was cleared", 30*time.Second, func() bool {
				if err := f.st.Pool.QueryRow(ctx, `SELECT reconcile_failing_since IS NOT NULL FROM workspace_networks WHERE workspace_id=$1`, failing).Scan(&failingSince); err != nil {
					t.Fatal(err)
				}
				return !failingSince && restrictions(failing) == 2
			})
			alertSweep(t, f, failing)
			if firing := firingAlerts(t, f, failing, "network_problem"); firing != 0 {
				t.Fatalf("%d alerts remain after recovery", firing)
			}
		})
	}
}

func identifyReport(t *testing.T, c *api.ExecutionCommand, sequence int64, key string) api.AgentReport {
	t.Helper()
	var command hostnetwork.Command
	if c.Network == nil || hostnetwork.Decode(*c.Network, &command) != nil || command.Action != "identify" {
		t.Fatalf("expected an identify command, got %+v", c)
	}
	raw, err := stablejson.Marshal(hostnetwork.Receipt{NetworkID: command.NetworkID, Identity: hostnetwork.Identity{MembershipID: command.MembershipID, PublicKey: key}})
	if err != nil {
		t.Fatal(err)
	}
	message := jsontext.Value(raw)
	return api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: sequence, State: "succeeded", Network: &message}
}

func TestNetworkMembersCannotShareAKey(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	if _, err := f.st.Pool.Exec(ctx, `INSERT INTO workspace_networks(id,workspace_id,prefix,generation) VALUES($1,$1,'10.251.0.0/24',1)`, ws); err != nil {
		t.Fatal(err)
	}
	type member struct {
		identity execution.Identity
		server   uuid.UUID
	}
	members := []member{}
	for i, address := range []string{"10.251.0.2", "10.251.0.3"} {
		_, in, _ := enrollment(t, f, b, ws, "deployment")
		if _, err := f.execution.Enroll(ctx, in, "test"); err != nil {
			t.Fatal(err)
		}
		identity, err := f.execution.Authenticate(ctx, in.ServerId, *in.Credential)
		if err != nil {
			t.Fatal(err)
		}
		members = append(members, member{identity, in.ServerId})
		if _, err := f.st.Pool.Exec(ctx, `INSERT INTO network_members(workspace_id,network_id,server_id,membership_id,address,endpoint,listen_port,cpu_millis,memory_mib,unbounded_inventory_hash,approved_controller,generation) VALUES($1,$1,$2,$3,$4,'192.0.2.2:51820',$5,1000,1024,$6,0,1)`, ws, in.ServerId, uuid.New(), address, 51820+i, hostnetwork.StableInventory(nil)); err != nil {
			t.Fatal(err)
		}
	}
	key := func(fill byte) string {
		raw := make([]byte, 32)
		raw[0] = fill
		return base64.StdEncoding.EncodeToString(raw)
	}
	identify := func(m member, sequence int64, public string) uuid.UUID {
		t.Helper()
		var command *api.ExecutionCommand
		waitFor(t, "no identify command reached the member", 30*time.Second, func() bool {
			var err error
			if command, err = f.execution.Poll(ctx, m.identity); err != nil {
				t.Fatal(err)
			}
			return command != nil
		})
		if err := f.execution.Report(ctx, m.identity, identifyReport(t, command, sequence, public)); err != nil {
			t.Fatal(err)
		}
		return command.Id
	}
	stored := func(m member) (public, note string) {
		t.Helper()
		if err := f.st.Pool.QueryRow(ctx, `SELECT public_key,note FROM network_members WHERE workspace_id=$1 AND server_id=$2`, ws, m.server).Scan(&public, &note); err != nil {
			t.Fatal(err)
		}
		return public, note
	}
	identify(members[0], 1, key(1))
	if public, _ := stored(members[0]); public != key(1) {
		t.Fatal("first member's key was not recorded")
	}
	refused := identify(members[1], 1, key(1))
	public, note := stored(members[1])
	if public != "" || note == "" {
		t.Fatalf("repeated key: stored %q with note %q", public, note)
	}
	var state, operationNote string
	if err := f.st.Pool.QueryRow(ctx, `SELECT state,note FROM execution_operations WHERE id=$1`, refused).Scan(&state, &operationNote); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || operationNote != note {
		t.Fatalf("refused identify ended %s with note %q", state, operationNote)
	}
	// The database refuses the state however a write arrives.
	var database *pgconn.PgError
	if _, err := f.st.Pool.Exec(ctx, `UPDATE network_members SET public_key=$2 WHERE server_id=$1`, members[1].server, key(1)); !errors.As(err, &database) || database.Code != "23505" {
		t.Fatalf("second holder of a key was stored: %v", err)
	}
	// Asked again, it would report the same key, so the sweep sends it nothing until the network
	// changes, rather than a new identify on every pass.
	for range 3 {
		if err := f.execution.SweepNetworks(ctx); err != nil {
			t.Fatal(err)
		}
		poll, done := context.WithTimeout(ctx, 200*time.Millisecond)
		command, err := f.execution.Poll(poll, members[1].identity)
		expired := poll.Err()
		done()
		if err != nil && expired == nil {
			t.Fatal(err)
		}
		if command != nil {
			t.Fatalf("a member refused for another member's key was sent %s again", command.Kind)
		}
	}
	// Revoking the key's holder changes the network, and a member that then reports its own key
	// carries on.
	b.call("POST", "/api/v1/workspaces/"+ws.String()+"/servers/"+members[0].server.String()+"/revoke", nil, 204, nil)
	identify(members[1], 2, key(2))
	if public, note = stored(members[1]); public != key(2) || note != "" {
		t.Fatalf("own key: stored %q with note %q", public, note)
	}
}

// Peers remove a member without a key, so the migration must not take a key from the
// member whose links run on it, and must leave the network able to deliver revocations.
func TestMigrationKeepsAProvenKeyAndClearsOtherHolders(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	if err := f.execution.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	conn, err := sql.Open("pgx", f.st.Pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, conn, os.DirFS("../../db/migrations"), goose.WithDisableGlobalRegistry(true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = provider.ApplyVersion(ctx, 27, false); err != nil {
		t.Fatal(err)
	}
	if _, err = f.st.Pool.Exec(ctx, `INSERT INTO workspace_networks(id,workspace_id,prefix,generation) VALUES($1,$1,'10.251.0.0/24',7)`, ws); err != nil {
		t.Fatal(err)
	}
	key := func(fill byte) string {
		raw := make([]byte, 32)
		raw[0] = fill
		return base64.StdEncoding.EncodeToString(raw)
	}
	type row struct {
		name, address, key string
		enabled            bool
		installed          bool // a valid manifest exists for this member at generation 6
		wantKey            string
	}
	rows := []row{
		{"holder with working links keeps the key", "10.251.0.2", key(1), true, true, key(1)},
		{"later holder has none to lose", "10.251.0.3", key(1), true, false, ""},
		{"peer with working links", "10.251.0.4", key(2), true, true, key(2)},
		{"revoked peer awaiting removal", "10.251.0.5", key(4), false, true, key(4)},
		{"revoked holder is not a peer of anyone", "10.251.0.9", key(1), false, false, key(1)},
		{"unproven holder", "10.251.0.6", key(3), true, false, ""},
		{"second unproven holder", "10.251.0.7", key(3), true, false, ""},
		{"member not yet identified", "10.251.0.8", "", true, false, ""},
	}
	manifests := make([]hostnetwork.Manifest, len(rows))
	for i, r := range rows {
		_, in, _ := enrollment(t, f, b, ws, "deployment")
		if _, err = f.execution.Enroll(ctx, in, "test"); err != nil {
			t.Fatal(err)
		}
		manifests[i] = hostnetwork.Manifest{Version: 1, NetworkID: ws, MembershipID: uuid.New(), ServerID: in.ServerId, Generation: 6, Prefix: "10.251.0.0/24", Address: r.address, PublicKey: r.key, ListenPort: 51820}
	}
	for i, r := range rows {
		for j, peer := range rows {
			if r.installed && peer.installed && i != j {
				manifests[i].Peers = append(manifests[i].Peers, hostnetwork.Peer{ServerID: manifests[j].ServerID, MembershipID: manifests[j].MembershipID, PublicKey: peer.key, Address: peer.address, Endpoint: "192.0.2.2:51820"})
			}
		}
		manifests[i].Sort()
		var installed []byte
		if r.installed {
			installed, _ = stablejson.Marshal(manifests[i])
		}
		if _, err = f.st.Pool.Exec(ctx, `INSERT INTO network_members(workspace_id,network_id,server_id,membership_id,address,endpoint,listen_port,cpu_millis,memory_mib,unbounded_inventory_hash,public_key,enabled,approved_controller,generation,acknowledged_generation,installed_manifest) VALUES($1,$1,$2,$3,$4,'192.0.2.2:51820',51820,1000,1024,$5,$6,$7,0,7,6,$8)`, ws, manifests[i].ServerID, manifests[i].MembershipID, r.address, hostnetwork.StableInventory(nil), r.key, r.enabled, installed); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = provider.ApplyVersion(ctx, 27, true); err != nil {
		t.Fatal(err)
	}
	if err = dbmigrate.Check(ctx, f.st.Pool.Config().ConnString()); err != nil {
		t.Fatal(err)
	}
	for i, r := range rows {
		var stored, note string
		var generation int64
		if err = f.st.Pool.QueryRow(ctx, `SELECT public_key,note,generation FROM network_members WHERE server_id=$1`, manifests[i].ServerID).Scan(&stored, &note, &generation); err != nil {
			t.Fatal(err)
		}
		cleared := r.key != "" && r.wantKey == ""
		if stored != r.wantKey || (note != "") != cleared || generation != 7 {
			t.Errorf("%s: key %q, note %q, generation %d", r.name, stored, note, generation)
		}
	}
	// The next pass removes the revoked peer from the member still on the network, and leaves
	// the proven holder installed there while the cleared members wait to identify.
	current, err := execution.New(f.st)
	if err != nil {
		t.Fatal(err)
	}
	if err = current.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer current.Stop(ctx)
	var raw []byte
	waitFor(t, "revocation was not delivered after the migration", 30*time.Second, func() bool {
		err := f.st.Pool.QueryRow(ctx, `SELECT command->'network' FROM execution_operations WHERE server_id=$1 AND kind='network' AND command->'network'->>'action'='restrict' LIMIT 1`, manifests[2].ServerID).Scan(&raw)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal(err)
		}
		return err == nil
	})
	var removal hostnetwork.Command
	if err = json.Unmarshal(raw, &removal); err != nil {
		t.Fatal(err)
	}
	if len(removal.Manifest.Peers) != 1 || removal.Manifest.Peers[0].ServerID != manifests[0].ServerID {
		t.Fatalf("after the migration the surviving member keeps peers %+v, want only the proven holder", removal.Manifest.Peers)
	}
}

// coordinatedPolicyFor moves a valid coordinated policy to another workspace's
// environment and server.
func coordinatedPolicyFor(t *testing.T, policy backup.Policy, ws, env, server uuid.UUID) backup.Policy {
	t.Helper()
	configuration, _ := stablejson.Marshal(policy)
	var moved backup.Policy
	if err := json.Unmarshal(configuration, &moved); err != nil {
		t.Fatal(err)
	}
	moved.WorkspaceID, moved.EnvironmentID, moved.ServerID = ws, env, server
	priorServer := moved.Plan.ServerID
	moved.Plan.EnvironmentID, moved.Plan.ServerID = env, server
	moved.Plan.Specification.Placement.IngressServerID = server
	for i := range moved.Plan.Specification.Placement.Applications {
		for j := range moved.Plan.Specification.Placement.Applications[i].Targets {
			target := &moved.Plan.Specification.Placement.Applications[i].Targets[j]
			if target.ServerID == priorServer {
				target.ServerID = server
			}
		}
	}
	for key, endpoints := range moved.Plan.Local.Backends {
		for i := range endpoints {
			if endpoints[i].ServerID == priorServer {
				endpoints[i].ServerID = server
			}
		}
		moved.Plan.Local.Backends[key] = endpoints
	}
	moved.Plan.Local.Network.ServerID = server
	moved.Plan = moved.Plan.WithHash()
	if err := moved.Validate(); err != nil {
		t.Fatal("invalid moved policy fixture", err)
	}
	return moved
}

// holdCoordinatedPolicyFirst gives ws a due policy at the head of the coordinated scan and
// holds its workspace lock, so each sweep's attempt at that policy fails with a plain error.
func holdCoordinatedPolicyFirst(t *testing.T, f *fixture, policy backup.Policy, ws, env, server uuid.UUID, due time.Time) (release func()) {
	t.Helper()
	ctx := context.Background()
	moved := coordinatedPolicyFor(t, policy, ws, env, server)
	moved.ID = uuid.Nil
	configuration, _ := stablejson.Marshal(moved)
	status, _ := stablejson.Marshal(backup.Status{Enabled: true, PolicyID: uuid.Nil, Generation: moved.Generation, Next: due, Runs: []backup.Run{}})
	if _, err := f.st.Pool.Exec(ctx, `INSERT INTO backup_policies(id,workspace_id,environment_id,server_id,configuration,encrypted_access,status,status_sequence) VALUES($1,$2,$3,$4,$5,'x',$6,1)`, uuid.Nil, ws, env, server, configuration, status); err != nil {
		t.Fatal(err)
	}
	_, held, err := f.st.BeginWorkspaceWrite(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	// The fixture's pool cannot close while this holds a connection, so a failure before
	// release would otherwise hang until the package timeout.
	done := sync.OnceFunc(held)
	t.Cleanup(done)
	return func() {
		done()
		var attempted bool
		if err := f.st.Pool.QueryRow(ctx, `SELECT coordinated_checked_at>'-infinity' FROM backup_policies WHERE id=$1`, uuid.Nil).Scan(&attempted); err != nil || !attempted {
			t.Fatal("the held policy never had its turn", err)
		}
		if _, err := f.st.Pool.Exec(ctx, `DELETE FROM backup_policies WHERE id=$1`, uuid.Nil); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOverdueCoordinatedBackupRaisesAlert(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	if err := f.execution.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	env := envForExecution(t, f, b, ws)
	enrolled, _, _ := enrollment(t, f, b, ws, "deployment")
	server := enrolled.Server.Id
	policy := uuid.New()
	configuration, _ := stablejson.Marshal(backup.Policy{ID: policy, Enabled: true, Coordinated: true, Method: backup.QuiescedFiles, Generation: 1})
	now := time.Now().UTC()
	statusAt := func(next time.Time, runs ...backup.Run) []byte {
		raw, _ := stablejson.Marshal(backup.Status{Enabled: true, PolicyID: policy, Generation: 1, Next: next, Runs: append([]backup.Run{}, runs...)})
		return raw
	}
	if _, err := f.st.Pool.Exec(ctx, `INSERT INTO backup_policies(id,workspace_id,environment_id,server_id,configuration,encrypted_access,status) VALUES($1,$2,$3,$4,$5,'x',$6)`, policy, ws, env, server, configuration, statusAt(now.Add(-time.Minute))); err != nil {
		t.Fatal(err)
	}
	check := func(what string, status []byte, want int) {
		t.Helper()
		if _, err := f.st.Pool.Exec(ctx, `UPDATE backup_policies SET status=$2 WHERE id=$1`, policy, status); err != nil {
			t.Fatal(err)
		}
		alertSweep(t, f, ws)
		if firing := firingAlerts(t, f, ws, "backup_schedule"); firing != want {
			t.Fatalf("%s: %d alerts, want %d", what, firing, want)
		}
	}
	check("the scheduler has not tried since it fell due, as after an outage", statusAt(now.Add(-time.Hour)), 0)
	if _, err := f.st.Pool.Exec(ctx, `UPDATE backup_policies SET coordinated_checked_at=now() WHERE id=$1`, policy); err != nil {
		t.Fatal(err)
	}
	check("within the grace period", statusAt(now.Add(-time.Minute)), 0)
	running := backup.Run{ID: uuid.New(), State: "running", StartedAt: now}
	check("a run is under way", statusAt(now.Add(-time.Hour), running), 0)
	stuck := backup.Run{ID: uuid.New(), State: "running", StartedAt: now.Add(-3 * time.Hour)}
	check("a run left marked running blocks occurrences", statusAt(now.Add(-time.Hour), stuck), 1)
	check("no occurrence started", statusAt(now.Add(-time.Hour)), 1)
	done := now.Add(-time.Second)
	check("the next occurrence succeeded", statusAt(now.Add(time.Hour), backup.Run{ID: uuid.New(), State: "succeeded", FinishedAt: &done}), 0)
}

// A claim skips a workspace whose row is locked, so this blocks evaluation after its claim.
func TestAlertSweepPassesAWorkspaceThatBlocksEvaluation(t *testing.T) {
	f := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	blocked := uuid.New()
	if _, err := f.st.Pool.Exec(ctx, `INSERT INTO workspaces(id,name) VALUES($1,'Blocked')`, blocked); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Pool.Exec(ctx, `INSERT INTO memberships(workspace_id,account_id,role) VALUES($1,$2,'owner')`, blocked, f.owner); err != nil {
		t.Fatal(err)
	}
	server := func(workspace uuid.UUID) uuid.UUID {
		enrolled, _, _ := enrollment(t, f, b, workspace, "deployment")
		alertOperation(t, f, workspace, enrolled.Server.Id, envForExecution(t, f, b, workspace), "needs_attention")
		return enrolled.Server.Id
	}
	blockedServer := server(blocked)
	server(ws)
	for workspace, scanned := range map[uuid.UUID]string{blocked: "1970-01-01", ws: "1971-01-01"} {
		if _, err := f.st.Pool.Exec(ctx, `UPDATE workspaces SET alert_scanned_at=$2::timestamptz WHERE id=$1`, workspace, scanned); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := f.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `SELECT 1 FROM servers WHERE id=$1 FOR UPDATE`, blockedServer); err != nil {
		t.Fatal(err)
	}
	if err = f.apiServer.Alerts.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	var alerts int
	if err = f.st.Pool.QueryRow(ctx, `SELECT count(*) FROM operational_alerts WHERE workspace_id=$1`, ws).Scan(&alerts); err != nil || alerts == 0 {
		t.Fatal("one workspace blocking evaluation stopped another workspace's alerts", err)
	}
}

// A pass runs one item at a time, so a workspace with many due policies would spend the
// whole of every pass waiting for the same lock, and hold other tenants behind them.
func TestCoordinatedSweepWaitsOnceForAStalledWorkspace(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	if err := f.execution.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	b := f.browser(t)
	stalled := b.login("owner@example.test", f.password).Workspaces[0].Id
	other := uuid.New()
	if _, err := f.st.Pool.Exec(ctx, `INSERT INTO workspaces(id,name) VALUES($1,'Other')`, other); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Pool.Exec(ctx, `INSERT INTO memberships(workspace_id,account_id,role) VALUES($1,$2,'owner')`, other, f.owner); err != nil {
		t.Fatal(err)
	}
	due := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	insert := func(ws uuid.UUID, ids string) {
		t.Helper()
		env := envForExecution(t, f, b, ws)
		enrolled, _, _ := enrollment(t, f, b, ws, "deployment")
		if _, err := f.st.Pool.Exec(ctx, `INSERT INTO backup_policies(id,workspace_id,environment_id,server_id,configuration,encrypted_access,status)
		 SELECT id,$1,$2,$3,'{"coordinated":true,"enabled":true,"generation":1}','x',jsonb_build_object('policyId',id,'generation',1,'enabled',true,'next',$4::text,'runs','[]'::jsonb)
		 FROM (`+ids+`) seeds`, ws, env, enrolled.Server.Id, due); err != nil {
			t.Fatal(err)
		}
	}
	// Sorted so that without the rule, every stalled policy is tried before the other tenant's.
	insert(stalled, `SELECT lpad(to_hex(n),32,'0')::uuid id FROM generate_series(1,20) n`)
	insert(other, `SELECT 'ffffffff-ffff-ffff-ffff-ffffffffffff'::uuid id`)
	_, held, err := f.st.BeginWorkspaceWrite(ctx, stalled)
	if err != nil {
		t.Fatal(err)
	}
	defer held()
	current, err := execution.New(f.st)
	if err != nil {
		t.Fatal(err)
	}
	if err = current.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer current.Stop(ctx)
	// The first pass can wait over 20 seconds for the stopped client's leadership lease to end.
	// Without the rule a pass tries about three stalled policies every 30 seconds, so this limit
	// still ends well before the other tenant's turn.
	waitFor(t, "a stalled workspace's policies held another tenant's behind them", time.Minute, func() bool {
		var reached bool
		if err := f.st.Pool.QueryRow(ctx, `SELECT coordinated_checked_at>'-infinity' FROM backup_policies WHERE workspace_id=$1`, other).Scan(&reached); err != nil {
			t.Fatal(err)
		}
		return reached
	})
	var tried int
	if err = f.st.Pool.QueryRow(ctx, `SELECT count(*) FROM backup_policies WHERE workspace_id=$1 AND coordinated_checked_at>'-infinity'`, stalled).Scan(&tried); err != nil {
		t.Fatal(err)
	}
	if tried != 1 {
		t.Fatalf("the stalled workspace had %d policies tried in the pass, want 1", tried)
	}
}
