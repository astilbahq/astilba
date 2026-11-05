package integration

import (
	"context"
	"encoding/json/jsontext"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

func TestManagedNetworkPartialRestrictionConvergesAfterHostIdle(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	base := "/api/v1/workspaces/" + ws.String()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	defer func() { cancel(); wg.Wait() }()
	members := []api.ManagedNetworkMemberInput{}
	ids := []uuid.UUID{}
	var stopPrimary context.CancelFunc
	primaryDone := make(chan struct{})
	for i := 0; i < 2; i++ {
		enrolled, in, token := enrollment(t, f, b, ws, "deployment")
		yes := true
		in.Capabilities.CrossHost = &yes
		in.Capabilities.MaintenanceContinuation = &yes
		in.Capabilities.Visual = &yes
		in.Preflight.CrossHostInventory = &api.CrossHostInventory{CpuMillis: 4000, MemoryMiB: 8192, UnboundedInventoryHash: hostnetwork.StableInventory(nil)}
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
		identity, err := f.execution.Authenticate(ctx, enrolled.Server.Id, strings.SplitN(token, ".", 2)[1])
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, enrolled.Server.Id)
		members = append(members, api.ManagedNetworkMemberInput{ServerId: enrolled.Server.Id, Endpoint: []string{"192.0.2.1:51820", "192.0.2.2:51820"}[i], ListenPort: 51820, CpuMillis: 2000, MemoryMiB: 4096, UnboundedInventoryHash: hostnetwork.StableInventory(nil)})
		host := hostnetwork.Host{Dir: t.TempDir()}
		hostCtx, stopHost := context.WithCancel(ctx)
		defer stopHost()
		if i == 0 {
			stopPrimary = stopHost
		}
		wg.Go(func() {
			if i == 0 {
				defer close(primaryDone)
			}
			last := time.Now()
			var installedNetwork hostnetwork.Manifest
			for hostCtx.Err() == nil {
				if time.Since(last) > 10*time.Second {
					if _, err := f.execution.Heartbeat(hostCtx, identity, api.AgentHeartbeat{Capabilities: in.Capabilities, Preflight: in.Preflight}); err != nil {
						if hostCtx.Err() == nil {
							t.Error(err)
						}
						return
					}
					last = time.Now()
				}
				poll, done := context.WithTimeout(hostCtx, time.Second)
				c, err := f.execution.Poll(poll, identity)
				expired := poll.Err()
				done()
				if err != nil {
					if waitForFixturePollLease(hostCtx, err) || expired != nil || hostCtx.Err() != nil {
						continue
					}
					t.Error(err)
					return
				}
				if c == nil {
					continue
				}
				if c.Kind != "network" {
					time.Sleep(30 * time.Millisecond)
					continue
				}
				receipt, err := fixtureNetworkReceipt(host, *c, &installedNetwork, nil, 0)
				if err != nil {
					t.Error(err)
					return
				}
				raw, _ := stablejson.Marshal(receipt)
				message := jsontext.Value(raw)
				if err = f.execution.Report(hostCtx, identity, api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, State: "succeeded", Sequence: 1, Steps: []api.ExecutionStep{}, Network: &message}); err != nil && hostCtx.Err() == nil {
					t.Error(err)
					return
				}
			}
		})
	}
	var network struct {
		Generation int64  `json:"generation"`
		State      string `json:"state"`
	}
	b.call("PUT", base+"/network", api.ConfigureManagedNetwork{Prefix: "10.251.0.0/24", Members: members}, 200, &network)
	waitNetwork := func(check func() bool) {
		t.Helper()
		until := time.Now().Add(35 * time.Second)
		for time.Now().Before(until) {
			if check() {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		var diagnostic []byte
		_ = f.st.Pool.QueryRow(ctx, `SELECT jsonb_build_object('members',(SELECT jsonb_agg(jsonb_build_object('server',server_id,'generation',generation,'ack',acknowledged_generation,'installed',installed_manifest)) FROM network_members WHERE workspace_id=$1),'operations',(SELECT jsonb_agg(jsonb_build_object('kind',o.kind,'state',o.state,'note',o.note,'action',o.command->'network'->>'action','errors',j.errors)) FROM execution_operations o LEFT JOIN river_job j ON j.id=o.job_id WHERE o.workspace_id=$1))`, ws).Scan(&diagnostic)
		t.Fatalf("network did not converge: %s", diagnostic)
	}
	waitNetwork(func() bool { b.call("GET", base+"/network", nil, 200, &network); return network.State == "ready" })
	uncertain := uuid.New()
	c := api.ExecutionCommand{Id: uncertain, ServerId: ids[1], Kind: "test", ResourceId: uuid.New(), Generation: 9999, PlanHash: strings.Repeat("b", 64), Action: "inspect", Deadline: time.Now().Add(time.Hour)}
	raw, _ := stablejson.Marshal(c)
	if _, err := f.st.Pool.Exec(ctx, `INSERT INTO execution_operations(id,workspace_id,server_id,generation,request_key,command,kind,state) VALUES($1,$2,$3,9999,$4,$5,'test','needs_attention')`, uncertain, ws, ids[1], uuid.New(), raw); err != nil {
		t.Fatal(err)
	}
	members[0].Endpoint = "192.0.2.9:51820"
	b.call("PUT", base+"/network", api.ConfigureManagedNetwork{Prefix: "10.251.0.0/24", BaseGeneration: network.Generation, Members: members}, 200, &network)
	waitNetwork(func() bool {
		var pending bool
		if err := f.st.Pool.QueryRow(ctx, `SELECT acknowledged_generation<generation AND NOT COALESCE(installed_manifest->'peers' @> jsonb_build_array(jsonb_build_object('serverId',$3::text)),false) FROM network_members WHERE workspace_id=$1 AND server_id=$2`, ws, ids[1], ids[0].String()).Scan(&pending); err != nil {
			t.Fatal(err)
		}
		return pending
	})
	b.call("GET", base+"/network", nil, 200, &network)
	if network.State == "ready" {
		t.Fatal("restriction falsely reported full convergence")
	}
	if _, err := f.st.Pool.Exec(ctx, `UPDATE execution_operations SET state='failed' WHERE id=$1`, uncertain); err != nil {
		t.Fatal(err)
	}
	waitNetwork(func() bool {
		var complete bool
		if err := f.st.Pool.QueryRow(ctx, `SELECT acknowledged_generation=generation AND COALESCE(installed_manifest->'peers' @> jsonb_build_array(jsonb_build_object('serverId',$3::text,'endpoint',$4::text)),false) FROM network_members WHERE workspace_id=$1 AND server_id=$2`, ws, ids[1], ids[0].String(), members[0].Endpoint).Scan(&complete); err != nil {
			t.Fatal(err)
		}
		return complete
	})
	stopPrimary()
	<-primaryDone
	if _, err := f.st.Pool.Exec(ctx, `UPDATE execution_operations SET state='needs_attention' WHERE id=$1`, uncertain); err != nil {
		t.Fatal(err)
	}
	b.call("POST", base+"/servers/"+ids[0].String()+"/revoke", nil, 204, nil)
	waitNetwork(func() bool {
		var removed bool
		if err := f.st.Pool.QueryRow(ctx, `SELECT acknowledged_generation=generation AND NOT COALESCE(installed_manifest->'peers' @> jsonb_build_array(jsonb_build_object('serverId',$3::text)),false) FROM network_members WHERE workspace_id=$1 AND server_id=$2`, ws, ids[1], ids[0].String()).Scan(&removed); err != nil {
			t.Fatal(err)
		}
		return removed
	})
	var uncertainState string
	if err := f.st.Pool.QueryRow(ctx, `SELECT state FROM execution_operations WHERE id=$1`, uncertain).Scan(&uncertainState); err != nil || uncertainState != "needs_attention" {
		t.Fatal("peer revocation discarded unrelated uncertainty", err)
	}
}
