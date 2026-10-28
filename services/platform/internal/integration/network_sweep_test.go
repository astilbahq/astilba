package integration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/google/uuid"
	"slices"
)

func TestManagedNetworkSweepYieldsBlockedAndCompletedWorkspaces(t *testing.T) {
	for _, tc := range []struct {
		name       string
		hosted     bool
		identified bool
		inert      int
	}{
		{name: "expired_tenant_unknown_peer", hosted: true, inert: 1},
		{name: "expired_tenant_identified_peer", hosted: true, identified: true, inert: 1},
		{name: "beyond_first_batch", inert: 1000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			b := f.browser(t)
			b.login("owner@example.test", f.password)
			if err := f.execution.Stop(ctx); err != nil {
				t.Fatal(err)
			}
			first := uuid.MustParse("00000000-0000-0000-0000-000000000001")
			last := uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff")
			if _, err := f.st.Pool.Exec(ctx, `INSERT INTO workspaces(id,name) SELECT lpad(to_hex(i),32,'0')::uuid,'Network fixture' FROM generate_series(1,$1::int) i`, tc.inert); err != nil {
				t.Fatal(err)
			}
			if _, err := f.st.Pool.Exec(ctx, `INSERT INTO workspaces(id,name) VALUES($1,'Pending network')`, last); err != nil {
				t.Fatal(err)
			}
			if _, err := f.st.Pool.Exec(ctx, `INSERT INTO memberships(workspace_id,account_id,role) VALUES($1,$3,'owner'),($2,$3,'owner')`, first, last, f.owner); err != nil {
				t.Fatal(err)
			}
			if _, err := f.st.Pool.Exec(ctx, `INSERT INTO workspace_networks(id,workspace_id,prefix,generation) SELECT id,id,'10.251.0.0/24',1 FROM workspaces WHERE name IN ('Network fixture','Pending network')`); err != nil {
				t.Fatal(err)
			}
			pending := []uuid.UUID{last}
			var addedServer uuid.UUID
			if tc.hosted {
				pending = append([]uuid.UUID{first}, pending...)
			}
			for _, ws := range pending {
				_, in, _ := enrollment(t, f, b, ws, "deployment")
				if _, err := f.execution.Enroll(ctx, in); err != nil {
					t.Fatal(err)
				}
				if ws == first {
					addedServer = in.ServerId
				}
				if _, err := f.st.Pool.Exec(ctx, `INSERT INTO network_members(workspace_id,network_id,server_id,membership_id,address,endpoint,listen_port,cpu_millis,memory_mib,unbounded_inventory_hash,approved_controller,generation) VALUES($1,$1,$2,$3,'10.251.0.1','192.0.2.1:51820',51820,1000,1024,$4,0,1)`, ws, in.ServerId, uuid.New(), hostnetwork.StableInventory(nil)); err != nil {
					t.Fatal(err)
				}
			}
			var removedServer, retainedServer, unaffectedServer uuid.UUID
			if tc.hosted {
				var prior []hostnetwork.Manifest
				for _, address := range []string{"10.251.0.2", "10.251.0.3", "10.251.0.4"} {
					_, in, _ := enrollment(t, f, b, first, "deployment")
					if _, err := f.execution.Enroll(ctx, in); err != nil {
						t.Fatal(err)
					}
					membership := uuid.New()
					identity, err := (hostnetwork.Host{Dir: t.TempDir()}).Identity(membership)
					if err != nil {
						t.Fatal(err)
					}
					prior = append(prior, hostnetwork.Manifest{Version: 1, NetworkID: first, MembershipID: membership, ServerID: in.ServerId, Generation: 1, Prefix: "10.251.0.0/24", Address: address, PublicKey: identity.PublicKey, ListenPort: 51820})
				}
				removedServer, retainedServer, unaffectedServer = prior[0].ServerID, prior[1].ServerID, prior[2].ServerID
				for i := range prior {
					for j, peer := range prior {
						if i != j {
							prior[i].Peers = append(prior[i].Peers, hostnetwork.Peer{ServerID: peer.ServerID, MembershipID: peer.MembershipID, PublicKey: peer.PublicKey, Address: peer.Address, Endpoint: "192.0.2.2:51820"})
						}
					}
					prior[i].Sort()
					raw, _ := json.Marshal(prior[i])
					if _, err := f.st.Pool.Exec(ctx, `INSERT INTO network_members(workspace_id,network_id,server_id,membership_id,address,endpoint,listen_port,cpu_millis,memory_mib,unbounded_inventory_hash,public_key,enabled,approved_controller,generation,acknowledged_generation,installed_manifest) VALUES($1,$1,$2,$3,$4,'192.0.2.2:51820',51820,1000,1024,$5,$6,$7,0,2,1,$8)`, first, prior[i].ServerID, prior[i].MembershipID, prior[i].Address, hostnetwork.StableInventory(nil), prior[i].PublicKey, i != 0, raw); err != nil {
						t.Fatal(err)
					}
				}
				if tc.identified {
					identity, err := (hostnetwork.Host{Dir: t.TempDir()}).Identity(uuid.New())
					if err != nil {
						t.Fatal(err)
					}
					if _, err = f.st.Pool.Exec(ctx, `UPDATE network_members SET public_key=$2 WHERE server_id=$1`, addedServer, identity.PublicKey); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := f.st.Pool.Exec(ctx, `UPDATE workspace_networks SET generation=2 WHERE workspace_id=$1`, first); err != nil {
					t.Fatal(err)
				}
			}
			f.st.Hosted = tc.hosted
			if tc.hosted {
				if _, err := f.st.Pool.Exec(ctx, `INSERT INTO hosted_subscriptions(workspace_id,status,management_until,server_limit) VALUES($1,'granted',now()-interval '1 hour',4)`, first); err != nil {
					t.Fatal(err)
				}
				if err := f.st.GrantManagement(ctx, last, time.Now().Add(time.Hour), 2); err != nil {
					t.Fatal(err)
				}
			}
			current, err := execution.New(f.st)
			if err != nil {
				t.Fatal(err)
			}
			if err = current.Start(ctx); err != nil {
				t.Fatal(err)
			}
			defer current.Stop(ctx)
			deadline := time.Now().Add(20 * time.Second)
			for {
				var admitted bool
				if err = f.st.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM execution_operations WHERE workspace_id=$1 AND kind='network')`, last).Scan(&admitted); err != nil {
					t.Fatal(err)
				}
				var restricted bool
				if tc.hosted {
					if err = f.st.Pool.QueryRow(ctx, `SELECT count(DISTINCT o.server_id)=3 FROM execution_operations o JOIN river_job j ON j.id=o.job_id WHERE o.workspace_id=$1 AND o.server_id=ANY($2) AND o.command->'network'->>'action'='restrict'`, first, []uuid.UUID{removedServer, retainedServer, unaffectedServer}).Scan(&restricted); err != nil {
						t.Fatal(err)
					}
				}
				if admitted && (!tc.hosted || restricted) {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("pending network was starved by earlier workspaces")
				}
				time.Sleep(25 * time.Millisecond)
			}
			if tc.hosted {
				var count int
				if err = f.st.Pool.QueryRow(ctx, `SELECT count(*) FROM execution_operations WHERE workspace_id=$1 AND (server_id=$2 OR command->'network'->>'action'<>'restrict')`, first, addedServer).Scan(&count); err != nil || count != 0 {
					t.Fatal("blocked tenant gained command authority", count, err)
				}
				var raw []byte
				if err = f.st.Pool.QueryRow(ctx, `SELECT command->'network' FROM execution_operations WHERE workspace_id=$1 AND server_id=$2 LIMIT 1`, first, retainedServer).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				var restricted hostnetwork.Command
				if json.Unmarshal(raw, &restricted) != nil || restricted.Validate() != nil || len(restricted.Manifest.Peers) != 1 || !slices.ContainsFunc(restricted.Manifest.Peers, func(p hostnetwork.Peer) bool { return p.ServerID == unaffectedServer }) {
					t.Fatal("partial removal lost the unaffected peer or admitted pending membership")
				}
			}
		})
	}
}
