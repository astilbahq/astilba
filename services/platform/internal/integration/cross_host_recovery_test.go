package integration

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/agent"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
)

// Real host journals retain the encrypted management checkpoint. Data-plane
// commands stop before reservation; native tests separately exercise Docker and
// packet effects. The destination has no project or operation from this run.
func TestNativeCrossHostOlderBackupRecovery(t *testing.T) {
	if testing.Short() || os.Getenv("ASTILBA_NATIVE_AGENT") != "1" {
		t.Skip("requires the explicitly prepared disposable native agent host")
	}
	ctx := context.Background()
	source := setup(t)
	docker, err := client.New(client.WithHost(os.Getenv("PLAT_APP_DOCKER_HOST")))
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close()
	b := source.browser(t)
	ws := b.login("owner@example.test", source.password).Workspaces[0].Id
	base := "/api/v1/workspaces/" + ws.String()
	var project api.Project
	b.call("POST", base+"/projects", map[string]string{"name": "Recovery"}, 201, &project)
	var env api.Environment
	b.call("POST", base+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "Web"}, 201, &env)
	path := base + "/projects/" + project.Id.String() + "/environments/" + env.Id.String()
	var journals []*agent.Journal
	var enrollments []api.AgentEnrollment
	var sourceIdentities []execution.Identity
	var members []api.ManagedNetworkMemberInput
	var fixtureCLIs []string
	var fixtureHosts []hostnetwork.Host
	var wg sync.WaitGroup
	workers, cancel := context.WithCancel(ctx)
	defer func() {
		cancel()
		wg.Wait()
		for _, in := range enrollments {
			containers, err := docker.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: client.Filters{}.Add("label", "com.astilba.server="+in.ServerId.String())})
			if err != nil {
				t.Error(err)
				continue
			}
			for _, c := range containers.Items {
				if c.Labels["com.astilba.server"] == in.ServerId.String() {
					if _, err = docker.ContainerRemove(ctx, c.ID, client.ContainerRemoveOptions{Force: true}); err != nil {
						t.Error(err)
					}
				}
			}
			networks, err := docker.NetworkList(ctx, client.NetworkListOptions{Filters: client.Filters{}.Add("label", "com.astilba.server="+in.ServerId.String())})
			if err != nil {
				t.Error(err)
				continue
			}
			for _, n := range networks.Items {
				if n.Labels["com.astilba.server"] == in.ServerId.String() {
					if _, err = docker.NetworkRemove(ctx, n.ID, client.NetworkRemoveOptions{}); err != nil {
						t.Error(err)
					}
				}
			}
		}
	}()
	for i := 0; i < 4; i++ {
		enrolled, in, token := enrollment(t, source, b, ws, "deployment")
		yes := true
		in.Capabilities.Visual, in.Capabilities.CrossHost, in.Capabilities.Replicas, in.Capabilities.MaintenanceContinuation = &yes, &yes, &yes, &yes
		in.Capabilities.ServiceOutputs, in.Capabilities.SafeReleases = &yes, &yes
		inventory := hostnetwork.StableInventory(nil)
		in.Preflight.CrossHostInventory = &api.CrossHostInventory{IngressReady: &yes, CpuMillis: 4000, MemoryMiB: 8192, UnboundedInventoryHash: inventory}
		if _, err := source.execution.Enroll(ctx, in); err != nil {
			t.Fatal(err)
		}
		identity, err := source.execution.Authenticate(ctx, in.ServerId, strings.SplitN(token, ".", 2)[1])
		if err != nil {
			t.Fatal(err)
		}
		sourceIdentities = append(sourceIdentities, identity)
		journalDir := filepath.Join(t.TempDir(), "journal")
		j, err := agent.Open(journalDir)
		if err != nil {
			t.Fatal(err)
		}
		// The real agent isolates registry configuration. Expose only a selected
		// fixture's CLI plugins there; never copy the developer's auth config.
		plugins := "/opt/astilba-plat4/cli-plugins"
		if config := os.Getenv("DOCKER_CONFIG"); config != "" {
			plugins = filepath.Join(config, "cli-plugins")
		}
		if info, err := os.Stat(plugins); err == nil && info.IsDir() {
			if err = os.MkdirAll(filepath.Join(journalDir, "docker"), 0700); err != nil {
				t.Fatal(err)
			}
			if err = os.Symlink(plugins, filepath.Join(journalDir, "docker", "cli-plugins")); err != nil {
				t.Fatal(err)
			}
		}
		cli := filepath.Join(journalDir, "fixture-docker")
		configPath := strings.ReplaceAll(filepath.Join(journalDir, "docker"), "'", "'\\''")
		if err = os.WriteFile(cli, []byte("#!/bin/sh\nexport DOCKER_CONFIG='"+configPath+"'\nexec docker \"$@\"\n"), 0700); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { j.Close() })
		if err = j.SetConfig(agent.Config{Origin: "https://source.example.test", ServerID: in.ServerId, MachineID: in.MachineId, Role: "deployment", Enrolled: true, Credential: in.Credential}); err != nil {
			t.Fatal(err)
		}
		journals = append(journals, j)
		enrollments = append(enrollments, in)
		members = append(members, api.ManagedNetworkMemberInput{ServerId: enrolled.Server.Id, Endpoint: []string{"192.0.2.1:51820", "192.0.2.2:51820", "192.0.2.3:51820", "192.0.2.4:51820"}[i], ListenPort: 51820, CpuMillis: 2000, MemoryMiB: 4096, UnboundedInventoryHash: inventory})
		host := hostnetwork.Host{Dir: journalDir}
		fixtureCLIs = append(fixtureCLIs, cli)
		fixtureHosts = append(fixtureHosts, host)
	}
	wait := func(label string, check func() bool) {
		t.Helper()
		until := time.Now().Add(45 * time.Second)
		for time.Now().Before(until) {
			if check() {
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Fatal("timed out:", label)
	}
	var network struct {
		ID    uuid.UUID `json:"id"`
		State string    `json:"state"`
	}
	b.call("PUT", base+"/network", api.ConfigureManagedNetwork{Prefix: "10.251.0.0/24", Members: members}, 200, &network)
	preIdentifyTx, err := source.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	preIdentifyRaw, err := portability.CaptureRecovery(ctx, preIdentifyTx, ws)
	preIdentifyTx.Rollback(ctx)
	if err != nil {
		t.Fatal(err)
	}
	preIdentify, err := portability.DecodeRecovery(preIdentifyRaw)
	if err != nil {
		t.Fatal(err)
	}
	for i, in := range enrollments {
		identity := sourceIdentities[i]
		wg.Go(func() {
			runRecoveryFixtureHost(t, workers, source.execution, identity, in, newRecoveryFixtureHost(source.execution, identity, journals[i], docker, fixtureCLIs[i]), fixtureHosts[i])
		})
	}
	wait("network", func() bool { b.call("GET", base+"/network", nil, 200, &network); return network.State == "ready" })
	app, image := uuid.New(), execution.TestImage
	spec := domain.Specification{Version: 1, Variables: []domain.Variable{}, Applications: []domain.Application{{ID: app, Name: "Web", Replicas: 1, Command: []string{"sh", "-c", "mkdir -p /tmp/www; echo ready > /tmp/www/index.html; exec httpd -f -p 8080 -h /tmp/www"}, Source: domain.ApplicationSource{Kind: "image", Image: &image}, Health: &domain.HealthCheck{Port: 8080, Path: "/"}}}, Routes: []domain.Route{{ID: uuid.New(), TargetID: app, Protocol: "http", Domain: "recovery.example.test", Port: 8080, TLS: "off", AllowCIDRs: []string{}}}, Placement: &domain.Placement{NetworkID: network.ID, IngressServerID: enrollments[0].ServerId, Applications: []domain.ApplicationPlacement{{ApplicationID: app, CPUMillis: 500, MemoryMiB: 128, Targets: []domain.PlacementTarget{{ServerID: enrollments[1].ServerId, Count: 1}}}}}}
	var draft api.Revision
	initial := spec
	initial.Placement = nil
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": env.DraftRevisionId, "specification": initial}, 200, &draft)
	var primary deployment.Status
	b.call("POST", path+"/deployment-plans", api.VisualPlanRequest{BaseRevisionId: draft.Id, ServerId: enrollments[0].ServerId, AllowHostChanges: true}, 201, &primary)
	wait("primary plan", func() bool {
		b.call("GET", path+"/deployment-plans/"+primary.ID.String(), nil, 200, &primary)
		return primary.Plan != nil
	})
	beforeApplyTx, err := source.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	beforeApplyRaw, err := portability.CaptureRecovery(ctx, beforeApplyTx, ws)
	beforeApplyTx.Rollback(ctx)
	if err != nil {
		t.Fatal(err)
	}
	beforeApply, err := portability.DecodeRecovery(beforeApplyRaw)
	if err != nil {
		t.Fatal(err)
	}
	var primaryOperation api.Operation
	b.call("POST", path+"/deployment-plans/"+primary.ID.String()+"/apply", api.ApplyPlan{IdempotencyKey: uuid.New()}, 201, &primaryOperation)
	wait("primary runtime", func() bool {
		var state, note string
		if err := source.st.Pool.QueryRow(ctx, `SELECT state,note FROM execution_operations WHERE id=$1`, primaryOperation.Id).Scan(&state, &note); err != nil {
			t.Fatal(err)
		}
		if state == "failed" || state == "needs_attention" {
			t.Fatal("primary deployment", state, note)
		}
		var current deployment.Observation
		b.call("GET", path+"/runtime", nil, 200, &current)
		return current.Applied
	})
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": draft.Id, "specification": spec}, 200, &draft)
	var plan deployment.Status
	b.call("POST", path+"/deployment-plans", api.VisualPlanRequest{BaseRevisionId: draft.Id, ServerId: enrollments[0].ServerId, AllowHostChanges: true}, 201, &plan)
	wait("frozen plan", func() bool {
		b.call("GET", path+"/deployment-plans/"+plan.ID.String(), nil, 200, &plan)
		return plan.Plan != nil
	})
	var operation api.Operation
	b.call("POST", path+"/deployment-plans/"+plan.ID.String()+"/apply", api.ApplyPlan{IdempotencyKey: uuid.New()}, 201, &operation)
	wait("durable reserve continuation", func() bool {
		evidence, err := journals[0].RecoveryEvidence()
		if err != nil {
			t.Fatal(err)
		}
		for _, root := range evidence.Roots {
			if root.Descriptor.ParentID == operation.Id && root.Descriptor.Sequence >= 1 {
				record, err := journals[0].Record(uuid.NewSHA1(root.Descriptor.CheckpointID, []byte("recovery-delivery")))
				return err == nil && record.Acknowledged
			}
		}
		return false
	})
	cancel()
	wg.Wait()
	// Advance only the simulated control-plane boundary, then lose delivery of
	// the next real capsule. No reserve or link effect runs on either host.
	rows, err := source.st.Pool.Query(ctx, `SELECT command FROM execution_operations WHERE parent_id=$1 AND command->'crosshost'->>'phase'='reserve'`, operation.Id)
	if err != nil {
		t.Fatal(err)
	}
	var reserves []api.ExecutionCommand
	for rows.Next() {
		var raw []byte
		var c api.ExecutionCommand
		if err = rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if json.Unmarshal(raw, &c) != nil {
			t.Fatal("invalid reserve frame")
		}
		reserves = append(reserves, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(reserves) != 2 {
		t.Fatal("missing original reserve frames", err)
	}
	for _, c := range reserves {
		var command deployment.CrossHostCommand
		if json.Unmarshal(*c.Crosshost, &command) != nil {
			t.Fatal("invalid reserve payload")
		}
		var input api.AgentEnrollment
		for _, candidate := range enrollments {
			if candidate.ServerId == c.ServerId {
				input = candidate
			}
		}
		identity, err := source.execution.Authenticate(ctx, input.ServerId, input.Credential)
		if err != nil {
			t.Fatal(err)
		}
		receipt := deployment.CrossHostReceipt{ParentID: operation.Id, GroupHash: command.GroupHash, UnitID: c.ResourceId, Phase: command.Phase, PlanHash: command.Unit.Plan.Hash, Observation: deployment.Observation{ObservedAt: time.Now().UTC(), Resources: []deployment.Resource{}}}
		raw, _ := json.Marshal(receipt)
		value := json.RawMessage(raw)
		if err = source.execution.Report(ctx, identity, api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 2, State: "succeeded", Steps: []api.ExecutionStep{}, Crosshost: &value}); err != nil {
			t.Fatal(err)
		}
	}
	var interrupted api.ExecutionCommand
	wait("next checkpoint delivery", func() bool {
		var raw []byte
		err := source.st.Pool.QueryRow(ctx, `SELECT command FROM execution_operations WHERE parent_id=$1 AND command->'crosshost'->>'phase'='checkpoint' AND (command->'recoveryCheckpoint'->>'sequence')::bigint=2`, operation.Id).Scan(&raw)
		if err != nil {
			return false
		}
		if json.Unmarshal(raw, &interrupted) != nil {
			t.Fatal("invalid next metadata frame")
		}
		return true
	})
	metadataEngine := agent.Engine{Journal: journals[0], Docker: docker, FetchCheckpoint: func(context.Context, deployment.RecoveryDescriptor) ([]byte, error) {
		return nil, errors.New("controller lost before metadata retention")
	}}
	deliveryErr := metadataEngine.Execute(ctx, interrupted)
	if deliveryErr == nil {
		t.Fatal("metadata transport failure hidden")
	}
	interruptedRecord, err := journals[0].Record(interrupted.Id)
	if err != nil || interruptedRecord.Report.State != "running" {
		t.Fatalf("metadata delivery did not remain retryable: %v; execute: %v; report: %+v", err, deliveryErr, interruptedRecord.Report)
	}
	if err := source.execution.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	var exports []string
	for _, j := range journals {
		dir := filepath.Join(t.TempDir(), "export")
		if err := j.ExportRecovery(dir); err != nil {
			t.Fatal(err)
		}
		exports = append(exports, dir)
	}

	snapshotTx, err := source.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := portability.CaptureRecovery(ctx, snapshotTx, ws)
	snapshotTx.Rollback(ctx)
	if err != nil {
		t.Fatal(err)
	}
	savedArchive, err := portability.DecodeRecovery(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	checkpointRows, err := source.st.Pool.Query(ctx, `SELECT to_jsonb(c) FROM cross_host_checkpoints c WHERE parent_id=$1 ORDER BY sequence`, operation.Id)
	if err != nil {
		t.Fatal(err)
	}
	var savedCheckpoints [][]byte
	for checkpointRows.Next() {
		var raw []byte
		if err = checkpointRows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		savedCheckpoints = append(savedCheckpoints, raw)
	}
	err = checkpointRows.Err()
	checkpointRows.Close()
	if err != nil {
		t.Fatal(err)
	}
	olderManagement := func(saved bool) (*fixture, uuid.UUID) {
		destination := setup(t)
		owner := destination.browser(t).login("owner@example.test", destination.password).User.Id
		destination.server.Close()
		if err := destination.execution.Stop(ctx); err != nil {
			t.Fatal(err)
		}
		destination.st.Keys = source.st.Keys
		if _, err := destination.st.Pool.Exec(ctx, `INSERT INTO workspaces(id,name) VALUES($1,'Older management backup')`, ws); err != nil {
			t.Fatal(err)
		}
		if _, err := destination.st.Pool.Exec(ctx, `INSERT INTO memberships(workspace_id,account_id,role) VALUES($1,$2,'owner')`, ws, owner); err != nil {
			t.Fatal(err)
		}
		if saved {
			tx, err := destination.st.Pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			if err = portability.MergeRecovery(ctx, tx, savedArchive, owner); err != nil {
				t.Fatal("restore saved management domain snapshot", err)
			}
			for _, raw := range savedCheckpoints {
				if _, err = tx.Exec(ctx, `INSERT INTO cross_host_checkpoints SELECT * FROM jsonb_populate_record(NULL::cross_host_checkpoints,$1)`, raw); err != nil {
					t.Fatal("restore saved management checkpoints", err)
				}
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
		}
		return destination, owner
	}
	t.Run("disjoint_parent_bases", func(t *testing.T) {
		var fork *fixture
		wait := func(label string, check func() bool) {
			t.Helper()
			until := time.Now().Add(45 * time.Second)
			for time.Now().Before(until) {
				if check() {
					return
				}
				time.Sleep(25 * time.Millisecond)
			}
			var states []byte
			_ = fork.st.Pool.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(jsonb_build_object('kind',kind,'state',state,'note',note)),'[]') FROM execution_operations WHERE workspace_id=$1 AND state NOT IN ('succeeded','failed','cancelled')`, ws).Scan(&states)
			t.Fatal("timed out:", label, string(states))
		}
		fork = setupBootstrap(t, false)
		if err := fork.execution.Stop(ctx); err != nil {
			t.Fatal(err)
		}
		fork.st.Keys = source.st.Keys
		originalOwner, err := source.st.Queries.AccountByID(ctx, source.owner)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = fork.st.Pool.Exec(ctx, `INSERT INTO accounts(id,email,name,password_hash) VALUES($1,$2,$3,$4)`, originalOwner.ID, originalOwner.Email, originalOwner.Name, originalOwner.PasswordHash); err != nil {
			t.Fatal(err)
		}
		if _, err = fork.st.Pool.Exec(ctx, `INSERT INTO workspaces(id,name) VALUES($1,'Restored source history')`, ws); err != nil {
			t.Fatal(err)
		}
		if _, err = fork.st.Pool.Exec(ctx, `INSERT INTO memberships(workspace_id,account_id,role) VALUES($1,$2,'owner')`, ws, source.owner); err != nil {
			t.Fatal(err)
		}
		forkBase, err := portability.DecodeRecovery(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		// This branch starts from the reviewed plan, before the first parent
		// was admitted. Its later base therefore cannot know that parent.
		for _, table := range []string{"execution_operations", "cross_host_operations", "cross_host_reservations"} {
			kept := []portability.Row{}
			for _, row := range forkBase.Tables[table] {
				var id, parent uuid.UUID
				_ = json.Unmarshal(row["id"], &id)
				_ = json.Unmarshal(row["parent_id"], &parent)
				if id != operation.Id && parent != operation.Id {
					kept = append(kept, row)
				}
			}
			forkBase.Tables[table] = kept
		}
		tx, err := fork.st.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err = portability.MergeRecovery(ctx, tx, forkBase, source.owner); err != nil {
			tx.Rollback(ctx)
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		// Queue deliveries are not domain archive rows. Fence this branch's
		// outstanding ordinary controller work before running the new history.
		if _, err = fork.st.Pool.Exec(ctx, `UPDATE execution_operations SET state='cancelled',cancel_requested=true WHERE workspace_id=$1 AND state NOT IN ('succeeded','failed','cancelled')`, ws); err != nil {
			t.Fatal(err)
		}
		for _, input := range enrollments {
			var credential []byte
			if err = source.st.Pool.QueryRow(ctx, `SELECT credential_hash FROM servers WHERE id=$1`, input.ServerId).Scan(&credential); err != nil {
				t.Fatal(err)
			}
			if _, err = fork.st.Pool.Exec(ctx, `UPDATE servers SET credential_hash=$2,last_seen=now() WHERE id=$1`, input.ServerId, credential); err != nil {
				t.Fatal(err)
			}
		}
		fork.execution, err = execution.New(fork.st)
		if err != nil {
			t.Fatal(err)
		}
		fork.apiServer.Execution = fork.execution
		if err = fork.execution.Start(ctx); err != nil {
			t.Fatal(err)
		}
		forkDocker, forkNamespace := recoveryFixtureDocker(t, ctx, docker)
		forkWorkers, stopFork := context.WithCancel(ctx)
		var forkWG sync.WaitGroup
		var forkJournals []*recoveryFixtureHost
		var stopPolls []context.CancelFunc
		var pollDone []chan struct{}
		defer func() { stopFork(); forkWG.Wait() }()
		for i, input := range enrollments {
			journalDir := filepath.Join(t.TempDir(), "journal")
			j, err := agent.Open(journalDir)
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := journals[i].Config()
			if err != nil {
				t.Fatal(err)
			}
			if err = j.SetConfig(cfg); err != nil {
				t.Fatal(err)
			}
			identity, err := fork.execution.Authenticate(ctx, input.ServerId, input.Credential)
			if err != nil {
				t.Fatal(err)
			}
			host := newRecoveryFixtureHost(fork.execution, identity, j, forkDocker, fixtureCLIs[i])
			host.namespace, host.state = forkNamespace, journalDir
			forkJournals = append(forkJournals, host)
			t.Cleanup(func() {
				if err := host.Close(); err != nil {
					t.Error(err)
				}
			})
			poll, stopPoll := context.WithCancel(forkWorkers)
			done := make(chan struct{})
			stopPolls, pollDone = append(stopPolls, stopPoll), append(pollDone, done)
			forkWG.Go(func() {
				defer close(done)
				runRecoveryFixtureHost(t, poll, fork.execution, identity, input, host, fixtureHosts[i])
			})
		}
		fb := fork.browser(t)
		fb.login("owner@example.test", fork.password)
		var network struct {
			State string `json:"state"`
		}
		wait("fork network", func() bool { fb.call("GET", base+"/network", nil, 200, &network); return network.State == "ready" })
		var envB api.Environment
		fb.call("POST", base+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "Other branch"}, 201, &envB)
		pathB := base + "/projects/" + project.Id.String() + "/environments/" + envB.Id.String()
		raw, _ := json.Marshal(initial)
		var specB domain.Specification
		_ = json.Unmarshal(raw, &specB)
		specB.Applications[0].ID = uuid.New()
		specB.Routes[0].ID, specB.Routes[0].TargetID, specB.Routes[0].Domain = uuid.New(), specB.Applications[0].ID, "other-recovery.example.test"
		var draftB api.Revision
		fb.call("PUT", pathB+"/draft", map[string]any{"baseRevisionId": envB.DraftRevisionId, "specification": specB}, 200, &draftB)
		var primaryB deployment.Status
		fb.call("POST", pathB+"/deployment-plans", api.VisualPlanRequest{BaseRevisionId: draftB.Id, ServerId: enrollments[2].ServerId, AllowHostChanges: true}, 201, &primaryB)
		wait("fork primary plan", func() bool {
			fb.call("GET", pathB+"/deployment-plans/"+primaryB.ID.String(), nil, 200, &primaryB)
			if primaryB.State == "failed" || primaryB.State == "needs_attention" {
				var note string
				_ = fork.st.Pool.QueryRow(ctx, `SELECT note FROM execution_operations WHERE id=$1`, primaryB.OperationID).Scan(&note)
				t.Fatal("fork primary plan", primaryB, note)
			}
			return primaryB.Plan != nil
		})
		var initialB api.Operation
		fb.call("POST", pathB+"/deployment-plans/"+primaryB.ID.String()+"/apply", api.ApplyPlan{IdempotencyKey: uuid.New()}, 201, &initialB)
		wait("fork primary runtime", func() bool {
			var observed deployment.Observation
			fb.call("GET", pathB+"/runtime", nil, 200, &observed)
			return observed.Applied
		})
		specB.Placement = &domain.Placement{NetworkID: spec.Placement.NetworkID, IngressServerID: enrollments[2].ServerId, Applications: []domain.ApplicationPlacement{{ApplicationID: specB.Applications[0].ID, CPUMillis: 500, MemoryMiB: 128, Targets: []domain.PlacementTarget{{ServerID: enrollments[3].ServerId, Count: 1}}}}}
		fb.call("PUT", pathB+"/draft", map[string]any{"baseRevisionId": draftB.Id, "specification": specB}, 200, &draftB)
		var planB deployment.Status
		fb.call("POST", pathB+"/deployment-plans", api.VisualPlanRequest{BaseRevisionId: draftB.Id, ServerId: enrollments[2].ServerId, AllowHostChanges: true}, 201, &planB)
		wait("fork frozen plan", func() bool {
			fb.call("GET", pathB+"/deployment-plans/"+planB.ID.String(), nil, 200, &planB)
			return planB.Plan != nil
		})
		// A queued restriction has no host report. Its cancellation must survive
		// the later base even when the restored SQL has the same sequence zero.
		stopPolls[0]()
		<-pollDone[0]
		restriction := recoveryFixtureRestriction(t, ctx, fork.st, enrollments[0].ServerId)
		restrictionRaw, _ := json.Marshal(restriction)
		requestKey := uuid.New()
		if _, err = fork.st.Pool.Exec(ctx, `INSERT INTO execution_operations(id,workspace_id,server_id,generation,request_key,command,kind,state) VALUES($1,$2,$3,$4,$5,$6,'network','queued')`, restriction.Id, ws, restriction.ServerId, restriction.Generation, requestKey, restrictionRaw); err != nil {
			t.Fatal(err)
		}
		fb.call("POST", base+"/operations/"+restriction.Id.String()+"/cancel", nil, 204, nil)
		var operationB api.Operation
		fb.call("POST", pathB+"/deployment-plans/"+planB.ID.String()+"/apply", api.ApplyPlan{IdempotencyKey: uuid.New()}, 201, &operationB)
		wait("fork base retention", func() bool {
			evidence, err := forkJournals[2].RecoveryEvidence()
			if err != nil {
				t.Fatal(err)
			}
			for _, root := range evidence.Roots {
				if root.Descriptor.ParentID == operationB.Id && root.Descriptor.Sequence >= 1 {
					return true
				}
			}
			return false
		})
		stopFork()
		forkWG.Wait()
		if err = fork.execution.Stop(ctx); err != nil {
			t.Fatal(err)
		}
		// Cancellation can win after the parent closes the journal but before
		// the child starts. Evidence/export must still work on that same host.
		beforeCancellation, err := forkJournals[2].RecoveryEvidence()
		if err != nil {
			t.Fatal(err)
		}
		cancelled, cancelChild := context.WithCancel(ctx)
		cancelChild()
		probe := api.ExecutionCommand{Id: uuid.New(), ServerId: enrollments[2].ServerId, EnvironmentId: envB.Id, ResourceId: uuid.NewSHA1(enrollments[2].ServerId, []byte(envB.Id.String()+":probe:v1")), Generation: 1 << 40, Kind: "test", Action: "inspect", Image: execution.TestImage, PlanHash: strings.Repeat("a", 64), Deadline: time.Now().Add(time.Minute)}
		if _, err = forkJournals[2].Execute(cancelled, probe); !errors.Is(err, context.Canceled) {
			t.Fatal("cancelled child start did not report cancellation", err)
		}
		afterCancellation, err := forkJournals[2].RecoveryEvidence()
		if err != nil || deployment.Hash(beforeCancellation) != deployment.Hash(afterCancellation) {
			t.Fatal("cancelled child start lost the parent journal", err)
		}
		if _, err = forkJournals[2].Record(probe.Id); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("cancelled child admitted a host operation", err)
		}
		combined := append([]string{}, exports[:2]...)
		for _, j := range forkJournals[2:] {
			dir := filepath.Join(t.TempDir(), "other-export")
			if err = j.ExportRecovery(dir); err != nil {
				t.Fatal(err)
			}
			combined = append(combined, dir)
		}
		destination, owner := olderManagement(true)
		if _, err = destination.st.Pool.Exec(ctx, `INSERT INTO execution_operations(id,workspace_id,server_id,generation,request_key,command,kind,state) VALUES($1,$2,$3,$4,$5,$6,'network','queued')`, restriction.Id, ws, restriction.ServerId, restriction.Generation, requestKey, restrictionRaw); err != nil {
			t.Fatal(err)
		}
		input := execution.RecoveryImport{WorkspaceID: ws, OwnerID: owner, HostExports: combined}
		preview, err := destination.execution.RecoverCrossHost(ctx, input)
		if err != nil || len(preview.Parents) != 2 || len(preview.Handovers) != 4 {
			t.Fatal("inspect disjoint parent bases", err, preview)
		}
		input.ExpectedState = preview.StateHash
		applied, err := destination.execution.RecoverCrossHost(ctx, input)
		if err != nil || !applied.Applied {
			t.Fatal("apply disjoint parent bases", err)
		}
		var restrictionCancelled bool
		if err = destination.st.Pool.QueryRow(ctx, `SELECT cancel_requested FROM execution_operations WHERE id=$1`, restriction.Id).Scan(&restrictionCancelled); err != nil || !restrictionCancelled {
			t.Fatal("equal-sequence source cancellation was lost", err, restrictionCancelled)
		}
		repeated, err := destination.execution.RecoverCrossHost(ctx, input)
		if err != nil || !repeated.AlreadyApplied || deployment.Hash(repeated.Handovers) != deployment.Hash(applied.Handovers) {
			t.Fatal("retry disjoint parent bases", err)
		}
	})
	t.Run("recover_pre_identification_backup", func(t *testing.T) {
		older, owner := olderManagement(false)
		tx, err := older.st.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if err = portability.MergeRecovery(ctx, tx, preIdentify, owner); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		var missing int
		if err = older.st.Pool.QueryRow(ctx, `SELECT count(*) FROM network_members WHERE workspace_id=$1 AND public_key=''`, ws).Scan(&missing); err != nil || missing != len(enrollments) {
			t.Fatal("pre-identification fixture already knows host keys", err, missing)
		}
		input := execution.RecoveryImport{WorkspaceID: ws, OwnerID: owner, HostExports: exports}
		var conflictingKey string
		if err = source.st.Pool.QueryRow(ctx, `SELECT public_key FROM network_members WHERE server_id=$1`, enrollments[1].ServerId).Scan(&conflictingKey); err != nil {
			t.Fatal(err)
		}
		if _, err = older.st.Pool.Exec(ctx, `UPDATE network_members SET public_key=$2 WHERE server_id=$1`, enrollments[0].ServerId, conflictingKey); err != nil {
			t.Fatal(err)
		}
		if _, err = older.execution.RecoverCrossHost(ctx, input); err == nil || !strings.Contains(err.Error(), "network key differs") {
			t.Fatal("conflicting recorded key was replaced without host evidence", err)
		}
		if _, err = older.st.Pool.Exec(ctx, `UPDATE network_members SET public_key='' WHERE server_id=$1`, enrollments[0].ServerId); err != nil {
			t.Fatal(err)
		}
		preview, err := older.execution.RecoverCrossHost(ctx, input)
		if err != nil {
			t.Fatal("inspect pre-identification backup", err)
		}
		input.ExpectedState = preview.StateHash
		if _, err = older.execution.RecoverCrossHost(ctx, input); err != nil {
			t.Fatal("recover pre-identification backup", err)
		}
		for _, enrolled := range enrollments {
			var want, got string
			if err = source.st.Pool.QueryRow(ctx, `SELECT public_key FROM network_members WHERE server_id=$1`, enrolled.ServerId).Scan(&want); err != nil {
				t.Fatal(err)
			}
			if err = older.st.Pool.QueryRow(ctx, `SELECT public_key FROM network_members WHERE server_id=$1`, enrolled.ServerId).Scan(&got); err != nil || got == "" || got != want {
				t.Fatal("recovery lost the authenticated membership's identified key", err)
			}
		}
	})
	t.Run("preserve_unapplied_observation", func(t *testing.T) {
		known, owner := olderManagement(true)
		unapplied, revision, unit := uuid.New(), uuid.New(), uuid.New()
		obsolete := uuid.New()
		observation := deployment.Observation{ObservedAt: time.Now().UTC(), Resources: []deployment.Resource{}, StateHash: strings.Repeat("b", 64)}
		raw, _ := json.Marshal(observation)
		tx, err := known.st.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if _, err = tx.Exec(ctx, `INSERT INTO environments(workspace_id,project_id,id,name,draft_revision_id) VALUES($1,$2,$3,'Cancelled first deployment',$4)`, ws, project.Id, unapplied, revision); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO specification_revisions(workspace_id,environment_id,id,author_id,specification,content_hash) SELECT workspace_id,$2,$3,$4,specification,content_hash FROM specification_revisions WHERE workspace_id=$1 AND id=$5`, ws, unapplied, revision, owner, draft.Id); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO deployment_units(workspace_id,id,environment_id,server_id) VALUES($1,$2,$3,$4)`, ws, unit, unapplied, enrollments[3].ServerId); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO visual_runtime(workspace_id,environment_id,unit_id,server_id,observation) VALUES($1,$2,$3,$4,$5)`, ws, unapplied, unit, enrollments[3].ServerId, raw); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO network_endpoints(id,workspace_id,environment_id,server_id,unit_id,resource_id,instance_id,listen_port,container_port) VALUES($1,$2,$3,$4,$5,$6,$7,39998,8080)`, obsolete, ws, unapplied, enrollments[3].ServerId, unit, uuid.New(), uuid.New()); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO network_endpoint_sources(workspace_id,endpoint_id,server_id,unit_id,active) VALUES($1,$2,$3,$4,true)`, ws, obsolete, enrollments[1].ServerId, unit); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		input := execution.RecoveryImport{WorkspaceID: ws, OwnerID: owner, HostExports: exports}
		preview, err := known.execution.RecoverCrossHost(ctx, input)
		if err != nil {
			t.Fatal("unrelated observation blocked recovery", err)
		}
		input.ExpectedState = preview.StateHash
		if _, err = known.execution.RecoverCrossHost(ctx, input); err != nil {
			t.Fatal("recover alongside unapplied observation", err)
		}
		var plan *uuid.UUID
		var retained []byte
		var restored deployment.Observation
		if err = known.st.Pool.QueryRow(ctx, `SELECT plan_id,observation FROM visual_runtime WHERE environment_id=$1`, unapplied).Scan(&plan, &retained); err != nil || plan != nil || json.Unmarshal(retained, &restored) != nil || !reflect.DeepEqual(restored, observation) {
			t.Fatal("recovery discarded observation or inferred an applied plan", err, plan)
		}
		var retired, active bool
		if err = known.st.Pool.QueryRow(ctx, `SELECT e.retired,s.active FROM network_endpoints e JOIN network_endpoint_sources s ON s.endpoint_id=e.id WHERE e.id=$1`, obsolete).Scan(&retired, &active); err != nil || !retired || active {
			t.Fatal("settled unapplied environment retained abandoned grants", err, retired, active)
		}
	})
	t.Run("adopt_later_applied_source", func(t *testing.T) {
		older, owner := olderManagement(false)
		tx, err := older.st.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if err = portability.MergeRecovery(ctx, tx, beforeApply, owner); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		var oldBinding *uuid.UUID
		if err = older.st.Pool.QueryRow(ctx, `SELECT server_id FROM deployment_units WHERE environment_id=$1 AND primary_unit`, env.Id).Scan(&oldBinding); err != nil || oldBinding != nil {
			t.Fatal("fixture already bound before first apply", err, oldBinding)
		}
		// An obsolete grant in the backup is outside both the adopted source
		// and the admitted target. Later membership approval must not revive it.
		obsolete := uuid.New()
		if _, err = older.st.Pool.Exec(ctx, `INSERT INTO network_endpoints(id,workspace_id,environment_id,server_id,unit_id,resource_id,instance_id,listen_port,container_port) VALUES($1,$2,$3,$4,$5,$6,$7,39999,8080)`, obsolete, ws, env.Id, enrollments[0].ServerId, primary.Plan.UnitID, app, uuid.New()); err != nil {
			t.Fatal(err)
		}
		if _, err = older.st.Pool.Exec(ctx, `INSERT INTO network_endpoint_sources(workspace_id,endpoint_id,server_id,unit_id,active) VALUES($1,$2,$3,$4,true)`, ws, obsolete, enrollments[1].ServerId, primary.Plan.UnitID); err != nil {
			t.Fatal(err)
		}
		input := execution.RecoveryImport{WorkspaceID: ws, OwnerID: owner, HostExports: exports}
		preview, err := older.execution.RecoverCrossHost(ctx, input)
		if err != nil {
			t.Fatal("inspect later applied source", err)
		}
		input.ExpectedState = preview.StateHash
		if _, err = older.execution.RecoverCrossHost(ctx, input); err != nil {
			t.Fatal("adopt later applied source", err)
		}
		var binding, applied uuid.UUID
		if err = older.st.Pool.QueryRow(ctx, `SELECT u.server_id,e.applied_revision_id FROM deployment_units u JOIN environments e ON e.id=u.environment_id WHERE e.id=$1 AND u.primary_unit`, env.Id).Scan(&binding, &applied); err != nil || binding != enrollments[0].ServerId || applied != primary.Plan.RevisionID {
			t.Fatal("source adoption lost primary binding or applied revision", err, binding, applied)
		}
		var retired, active bool
		if err = older.st.Pool.QueryRow(ctx, `SELECT e.retired,s.active FROM network_endpoints e JOIN network_endpoint_sources s ON s.endpoint_id=e.id WHERE e.id=$1`, obsolete).Scan(&retired, &active); err != nil || !retired || active {
			t.Fatal("adopted runtime retained an obsolete private grant", err, retired, active)
		}
	})
	t.Run("preserve_known_metadata", func(t *testing.T) {
		known, owner := olderManagement(true)
		var delivered uuid.UUID
		if err := known.st.Pool.QueryRow(ctx, `SELECT id FROM execution_operations WHERE parent_id=$1 AND command->'crosshost'->>'phase'='checkpoint' AND state='succeeded' ORDER BY generation DESC LIMIT 1`, operation.Id).Scan(&delivered); err != nil {
			t.Fatal(err)
		}
		if _, err := known.st.Pool.Exec(ctx, `UPDATE execution_operations SET cancel_requested=true,note='Known committed report' WHERE id=$1`, delivered); err != nil {
			t.Fatal(err)
		}
		if _, err := known.st.Pool.Exec(ctx, `UPDATE environments SET name='Current environment name' WHERE id=$1`, env.Id); err != nil {
			t.Fatal(err)
		}
		var expectedGeneration int64
		if err := known.st.Pool.QueryRow(ctx, `UPDATE servers SET revoked=true,update_generation=update_generation+1 WHERE id=$1 RETURNING update_generation`, enrollments[3].ServerId).Scan(&expectedGeneration); err != nil {
			t.Fatal(err)
		}
		if _, err := known.st.Pool.Exec(ctx, `UPDATE network_members SET enabled=false WHERE server_id=$1`, enrollments[3].ServerId); err != nil {
			t.Fatal(err)
		}
		input := execution.RecoveryImport{WorkspaceID: ws, OwnerID: owner, HostExports: exports}
		preview, err := known.execution.RecoverCrossHost(ctx, input)
		if err != nil {
			t.Fatal("inspect known metadata", err)
		}
		input.ExpectedState = preview.StateHash
		if _, err = known.execution.RecoverCrossHost(ctx, input); err != nil {
			t.Fatal("recover known metadata", err)
		}
		var cancelled, revoked, enabled bool
		var note, name string
		var generation int64
		if err = known.st.Pool.QueryRow(ctx, `SELECT cancel_requested,note FROM execution_operations WHERE id=$1`, delivered).Scan(&cancelled, &note); err != nil || !cancelled || note != "Known committed report" {
			t.Fatal("same-sequence proof rewrote committed report metadata", err, cancelled, note)
		}
		if err = known.st.Pool.QueryRow(ctx, `SELECT name FROM environments WHERE id=$1`, env.Id).Scan(&name); err != nil || name != "Current environment name" {
			t.Fatal("recovery discarded current environment metadata", err, name)
		}
		if err = known.st.Pool.QueryRow(ctx, `SELECT s.revoked,s.update_generation,n.enabled FROM servers s JOIN network_members n ON n.server_id=s.id WHERE s.id=$1`, enrollments[3].ServerId).Scan(&revoked, &generation, &enabled); err != nil || !revoked || generation < expectedGeneration || enabled {
			t.Fatal("old base undid revocation or update counter", err, revoked, generation, enabled)
		}
	})
	t.Run("refuse_older_host_report", func(t *testing.T) {
		known, owner := olderManagement(true)
		if _, err := known.st.Pool.Exec(ctx, `UPDATE execution_operations SET report_sequence=report_sequence+1 WHERE parent_id=$1 AND command->'crosshost'->>'phase'='checkpoint' AND state='succeeded'`, operation.Id); err != nil {
			t.Fatal(err)
		}
		var before, after int64
		if err := known.st.Pool.QueryRow(ctx, `SELECT controller_generation FROM servers WHERE id=$1`, enrollments[0].ServerId).Scan(&before); err != nil {
			t.Fatal(err)
		}
		input := execution.RecoveryImport{WorkspaceID: ws, OwnerID: owner, HostExports: exports}
		if _, err := known.execution.RecoverCrossHost(ctx, input); err == nil || !strings.Contains(err.Error(), "newer management knowledge") {
			t.Fatal("older host proof overwrote known progress", err)
		}
		if err := known.st.Pool.QueryRow(ctx, `SELECT controller_generation FROM servers WHERE id=$1`, enrollments[0].ServerId).Scan(&after); err != nil || before != after {
			t.Fatal("refused preview changed controller authority", err, before, after)
		}
	})
	savedDestination, savedOwner := olderManagement(true)
	t.Run("undelivered_database_revocation", func(t *testing.T) {
		queued, queuedOwner := olderManagement(true)
		command := recoveryFixtureRestriction(t, ctx, queued.st, enrollments[0].ServerId)
		header, _ := json.Marshal(command)
		if _, err := queued.st.Pool.Exec(ctx, `INSERT INTO execution_operations(id,workspace_id,server_id,generation,request_key,command,kind,state) VALUES($1,$2,$3,$4,$5,$6,'network','queued')`, command.Id, ws, command.ServerId, command.Generation, uuid.New(), header); err != nil {
			t.Fatal(err)
		}
		input := execution.RecoveryImport{WorkspaceID: ws, OwnerID: queuedOwner, HostExports: exports}
		preview, err := queued.execution.RecoverCrossHost(ctx, input)
		if err != nil {
			t.Fatal("inspect queued removal absent from host proof", err)
		}
		input.ExpectedState = preview.StateHash
		if _, err = queued.execution.RecoverCrossHost(ctx, input); err != nil {
			t.Fatal("restore queued removal absent from host proof", err)
		}
		var restored []byte
		var state string
		var cancelled, enqueued bool
		if err = queued.st.Pool.QueryRow(ctx, `SELECT command,state,cancel_requested,job_id IS NOT NULL FROM execution_operations WHERE id=$1`, command.Id).Scan(&restored, &state, &cancelled, &enqueued); err != nil {
			t.Fatal(err)
		}
		var recovered api.ExecutionCommand
		if json.Unmarshal(restored, &recovered) != nil || recovered.PlanHash != command.PlanHash || recovered.Generation != command.Generation || !recovered.Deadline.Equal(command.Deadline) || state != "queued" || cancelled || !enqueued {
			t.Fatal("queued removal was discarded, rewritten or not re-enqueued")
		}
		if _, err = queued.st.Pool.Exec(ctx, `UPDATE execution_operations SET state='running',report_sequence=1 WHERE id=$1`, command.Id); err != nil {
			t.Fatal(err)
		}
		var beforeController, afterController int64
		if err = queued.st.Pool.QueryRow(ctx, `SELECT controller_generation FROM servers WHERE id=$1`, command.ServerId).Scan(&beforeController); err != nil {
			t.Fatal(err)
		}
		input.ExpectedState = ""
		if _, err = queued.execution.RecoverCrossHost(ctx, input); err == nil || !strings.Contains(err.Error(), "lacks retained progress evidence") {
			t.Fatal("missing delivered progress was treated as an unobserved queue item", err)
		}
		if err = queued.st.Pool.QueryRow(ctx, `SELECT controller_generation FROM servers WHERE id=$1`, command.ServerId).Scan(&afterController); err != nil || afterController != beforeController {
			t.Fatal("failed evidence preflight changed controller authority", err)
		}
	})
	savedInput := execution.RecoveryImport{WorkspaceID: ws, OwnerID: savedOwner, HostExports: exports}
	savedPreview, err := savedDestination.execution.RecoverCrossHost(ctx, savedInput)
	if err != nil {
		t.Fatal("inspect saved unretained checkpoint suffix", err)
	}
	savedInput.ExpectedState = savedPreview.StateHash
	savedApplied, err := savedDestination.execution.RecoverCrossHost(ctx, savedInput)
	if err != nil || !savedApplied.Applied {
		t.Fatal("recover available exact frozen suffix", err)
	}
	var savedDescriptor deployment.RecoveryDescriptor
	if interrupted.RecoveryCheckpoint == nil || json.Unmarshal(*interrupted.RecoveryCheckpoint, &savedDescriptor) != nil {
		t.Fatal("invalid original unretained descriptor")
	}
	if deployment.Hash(savedApplied.Handovers[enrollments[0].ServerId].Descriptor) != deployment.Hash(savedDescriptor) {
		t.Fatal("recovery replaced a saved frozen checkpoint suffix")
	}
	var suffixAcknowledged bool
	if err = savedDestination.st.Pool.QueryRow(ctx, `SELECT acknowledged FROM cross_host_checkpoints WHERE id=$1`, savedDescriptor.CheckpointID).Scan(&suffixAcknowledged); err != nil || suffixAcknowledged {
		t.Fatal("management-only segment bypassed primary retention", err)
	}
	var savedPhase, savedDeliveryState string
	if err = savedDestination.st.Pool.QueryRow(ctx, `SELECT phase FROM cross_host_operations WHERE id=$1`, operation.Id).Scan(&savedPhase); err != nil || savedPhase != "links" {
		t.Fatal("saved phase was rewound before continuation", err)
	}
	if err = savedDestination.st.Pool.QueryRow(ctx, `SELECT state FROM execution_operations WHERE id=$1`, interrupted.Id).Scan(&savedDeliveryState); err != nil || savedDeliveryState != "running" {
		t.Fatal("available original metadata was discarded", err)
	}
	destination, owner := olderManagement(false)

	in := execution.RecoveryImport{WorkspaceID: ws, OwnerID: owner, HostExports: exports}
	truncated := filepath.Join(t.TempDir(), "truncated")
	if err := journals[0].ExportRecovery(truncated); err != nil {
		t.Fatal(err)
	}
	evidence, err := journals[0].RecoveryEvidence()
	if err != nil {
		t.Fatal(err)
	}
	for _, root := range evidence.Roots {
		if root.Descriptor.ParentID == operation.Id {
			for _, suffix := range []string{".capsule", ".json"} {
				if err = os.Remove(filepath.Join(truncated, root.Descriptor.CheckpointID.String()+suffix)); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	incomplete := in
	incomplete.HostExports = append([]string{truncated}, exports[1:]...)
	if _, err = destination.execution.RecoverCrossHost(ctx, incomplete); err == nil {
		t.Fatal("a copied chain prefix older than the primary's retained head was accepted")
	}
	preview, err := destination.execution.RecoverCrossHost(ctx, in)
	if err != nil {
		t.Fatal("inspect", err)
	}
	if preview.Applied || len(preview.Parents) != 1 || preview.Parents[0] != operation.Id || len(preview.Handovers) != 2 {
		t.Fatal("wrong recovery preview", preview)
	}
	in.ExpectedState = preview.StateHash
	applied, err := destination.execution.RecoverCrossHost(ctx, in)
	if err != nil || !applied.Applied {
		t.Fatal("apply", err)
	}
	// Imported compatible bases may exceed the admission ceiling together.
	// Lost-response recovery still needs to inspect that trusted database.
	if _, err = destination.st.Pool.Exec(ctx, `INSERT INTO audit_events(workspace_id,id,actor_id,action,resource_id) SELECT $1,gen_random_uuid(),$2,'fixture.retained_history',$1 FROM generate_series(1,10001)`, ws, owner); err != nil {
		t.Fatal(err)
	}
	bounded, err := destination.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, captureErr := portability.CaptureRecovery(ctx, bounded, ws)
	bounded.Rollback(ctx)
	if !errors.Is(captureErr, portability.ErrArchive) {
		t.Fatal("new-base admission lost its archive bound", captureErr)
	}
	repeated, err := destination.execution.RecoverCrossHost(ctx, in)
	if err != nil || !repeated.AlreadyApplied || deployment.Hash(repeated.Handovers) != deployment.Hash(applied.Handovers) {
		t.Fatal("lost-response retry advanced authority", err)
	}
	var retainedDelivery uuid.UUID
	for _, root := range evidence.Roots {
		if root.Descriptor.ParentID == operation.Id {
			retainedDelivery = uuid.NewSHA1(root.Descriptor.CheckpointID, []byte("recovery-delivery"))
		}
	}
	// The restored row can predate a terminal report already acknowledged by
	// the host. Recovery must use that proof rather than depend on a resend.
	olderProofDestination, olderProofOwner := olderManagement(true)
	if _, err = olderProofDestination.st.Pool.Exec(ctx, `UPDATE execution_operations SET state='running',report_sequence=1 WHERE id=$1`, retainedDelivery); err != nil {
		t.Fatal(err)
	}
	newerProofInput := execution.RecoveryImport{WorkspaceID: ws, OwnerID: olderProofOwner, HostExports: exports}
	newerProofPreview, err := olderProofDestination.execution.RecoverCrossHost(ctx, newerProofInput)
	if err != nil {
		t.Fatal("inspect older metadata outcome", err)
	}
	newerProofInput.ExpectedState = newerProofPreview.StateHash
	_, err = olderProofDestination.execution.RecoverCrossHost(ctx, newerProofInput)
	if err != nil {
		t.Fatal("apply newer acknowledged metadata proof", err)
	}
	var reconciledState string
	var reconciledSequence int64
	if err = olderProofDestination.st.Pool.QueryRow(ctx, `SELECT state,report_sequence FROM execution_operations WHERE id=$1`, retainedDelivery).Scan(&reconciledState, &reconciledSequence); err != nil || reconciledState != "succeeded" || reconciledSequence <= 1 {
		t.Fatal("newer acknowledged metadata outcome was ignored", err)
	}
	var held bool
	if err = destination.st.Pool.QueryRow(ctx, `SELECT NOT opened FROM cross_host_recoveries WHERE parent_id=$1`, operation.Id).Scan(&held); err != nil || !held {
		t.Fatal("recovered operation was not held", err)
	}
	current, err := execution.New(destination.st)
	if err != nil {
		t.Fatal(err)
	}
	if err = current.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { current.Stop(ctx) }()
	account, err := destination.st.Queries.AccountByID(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	enrollRecovered := func(current *execution.Service, result execution.RecoveryPreview) []execution.Identity {
		var identities []execution.Identity
		for i, j := range journals {
			h, participant := result.Handovers[enrollments[i].ServerId]
			if !participant {
				continue
			}
			ticket, err := current.PrepareTransfer(ctx, store.Principal{Account: account, Recent: true, MFA: true}, ws, enrollments[i].ServerId)
			if err != nil {
				t.Fatal("transfer ticket", err)
			}
			if err = j.TransferRecovery("https://recovered.example.test", make([]byte, 32), ticket.Token, h); err != nil {
				t.Fatal("host handover", i, err)
			}
			if i == 0 {
				retired, err := j.Record(interrupted.Id)
				if err != nil || retired.Report.State != "cancelled" || !retired.Acknowledged {
					t.Fatal("unretained metadata was left ahead of the recovered phase", err)
				}
				var state string
				if err = destination.st.Pool.QueryRow(ctx, `SELECT state FROM execution_operations WHERE id=$1`, interrupted.Id).Scan(&state); err != nil || state != "cancelled" {
					t.Fatal("offline import revived an unfetchable delivery", err)
				}
			}
			inventory, err := j.Inventory()
			if err != nil {
				t.Fatal(err)
			}
			input := enrollments[i]
			input.Inventory, input.ControllerGeneration, input.Token = &inventory, &ticket.Generation, ticket.Token
			input.Credential, err = cryptobox.Token()
			if err != nil {
				t.Fatal(err)
			}
			wrong := input
			wrongInventory := inventory
			wrongInventory.Units = map[string]string{uuid.NewString(): strings.Repeat("a", 64)}
			wrong.Inventory = &wrongInventory
			if _, err = current.Enroll(ctx, wrong); !errors.Is(err, domain.Conflict) {
				t.Fatal("incompatible inventory admitted", err)
			}
			if _, err = current.Enroll(ctx, input); err != nil {
				var stagedRaw []byte
				if probeErr := destination.st.Pool.QueryRow(ctx, `SELECT inventory FROM cross_host_recovery_servers WHERE server_id=$1`, input.ServerId).Scan(&stagedRaw); probeErr != nil {
					t.Fatal(probeErr)
				}
				var staged api.ControllerInventory
				if json.Unmarshal(stagedRaw, &staged) != nil {
					t.Fatal("invalid staged inventory")
				}
				var want, got any
				if staged.Recovery != nil {
					_ = json.Unmarshal(*staged.Recovery, &want)
				}
				if inventory.Recovery != nil {
					_ = json.Unmarshal(*inventory.Recovery, &got)
				}
				t.Fatalf("verified handover: %v; units equal=%v; schedules equal=%v; recovery values equal=%v; recovery bytes equal=%v", err, reflect.DeepEqual(staged.Units, inventory.Units), reflect.DeepEqual(staged.Schedules, inventory.Schedules), reflect.DeepEqual(want, got), reflect.DeepEqual(staged.Recovery, inventory.Recovery))
			}
			cfg, err := j.Config()
			if err != nil {
				t.Fatal(err)
			}
			cfg.Enrolled, cfg.Credential, cfg.EnrollmentToken = true, input.Credential, ""
			if err = j.SetConfig(cfg); err != nil {
				t.Fatal(err)
			}
			identity, err := current.Authenticate(ctx, input.ServerId, input.Credential)
			if err != nil {
				t.Fatal(err)
			}
			identities = append(identities, identity)
			if err = j.TransferRecovery("https://recovered.example.test", make([]byte, 32), ticket.Token, h); err != nil {
				t.Fatal("handover retry", err)
			}
			retried, err := j.Config()
			if err != nil || !retried.Enrolled || retried.Credential != input.Credential {
				t.Fatal("handover retry replaced enrolled credentials", err)
			}
			if i == 0 {
				poll, done := context.WithTimeout(ctx, 500*time.Millisecond)
				command, err := current.Poll(poll, identity)
				done()
				if command != nil || err != nil && !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal("work dispatched before every participant proved its inventory", command, err)
				}
			}
		}
		return identities
	}
	identities := enrollRecovered(current, applied)
	wait("old recovery grants staged", func() bool {
		var count int
		if err := destination.st.Pool.QueryRow(ctx, `SELECT count(*) FROM execution_operations WHERE parent_id=$1 AND command->'crosshost'->>'phase'='recovery-open' AND state NOT IN ('succeeded','failed','cancelled')`, operation.Id).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count == 2
	})
	if err = current.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	var heldExports []string
	for _, j := range journals {
		dir := filepath.Join(t.TempDir(), "held-export")
		if err = j.ExportRecovery(dir); err != nil {
			t.Fatal(err)
		}
		heldExports = append(heldExports, dir)
	}
	heldInput := execution.RecoveryImport{WorkspaceID: ws, OwnerID: owner, HostExports: heldExports}
	heldPreview, err := destination.execution.RecoverCrossHost(ctx, heldInput)
	if err != nil {
		t.Fatal("inspect saved undelivered replay grants", err)
	}
	heldInput.ExpectedState = heldPreview.StateHash
	newer, err := destination.execution.RecoverCrossHost(ctx, heldInput)
	if err != nil {
		t.Fatal("recover pending replay grants", err)
	}
	var staleGrants int
	if err = destination.st.Pool.QueryRow(ctx, `SELECT count(*) FROM execution_operations WHERE parent_id=$1 AND command->'crosshost'->>'phase'='recovery-open' AND state NOT IN ('succeeded','failed','cancelled')`, operation.Id).Scan(&staleGrants); err != nil || staleGrants != 0 {
		t.Fatal("prior replay grants survived authority replacement", err)
	}
	current, err = execution.New(destination.st)
	if err != nil {
		t.Fatal(err)
	}
	if err = current.Start(ctx); err != nil {
		t.Fatal(err)
	}
	applied = newer
	identities = enrollRecovered(current, applied)

	for i, identity := range identities {
		poll, done := context.WithTimeout(ctx, 20*time.Second)
		command, err := current.Poll(poll, identity)
		done()
		if err != nil || command == nil {
			t.Fatal("recovery open missing", err)
		}
		var body deployment.CrossHostCommand
		if command.Crosshost == nil || json.Unmarshal(*command.Crosshost, &body) != nil || body.Phase != "recovery-open" {
			t.Fatal("ordinary work escaped closed recovery gate", command)
		}
		engine := agent.Engine{Journal: journals[i]}
		if err = engine.Execute(ctx, *command); err != nil {
			t.Fatal(err)
		}
		record, err := journals[i].Record(command.Id)
		if err != nil || record.Report.State != "succeeded" {
			t.Fatal("replay hold not opened", err)
		}
		if err = current.Report(ctx, identity, record.Report); err != nil {
			t.Fatal(err)
		}
		if err = journals[i].Acknowledge(command.Id, record.Report.Sequence); err != nil {
			t.Fatal(err)
		}
	}
	wait("all-host release", func() bool {
		var opened bool
		if err := destination.st.Pool.QueryRow(ctx, `SELECT opened FROM cross_host_recoveries WHERE parent_id=$1`, operation.Id).Scan(&opened); err != nil {
			t.Fatal(err)
		}
		return opened
	})
	for _, identity := range identities {
		poll, done := context.WithTimeout(ctx, 20*time.Second)
		command, err := current.Poll(poll, identity)
		done()
		if err != nil || command == nil {
			t.Fatal("frozen reservation did not resume", err)
		}
		var body deployment.CrossHostCommand
		if command.Crosshost == nil || json.Unmarshal(*command.Crosshost, &body) != nil || body.Phase != "reserve" || command.ParentId == nil || *command.ParentId != operation.Id || command.ControllerGeneration == nil || *command.ControllerGeneration != applied.Handovers[identity.ID].Authority.Controller {
			t.Fatal("recovery changed the original phase or authority", command)
		}
		var original []byte
		if err = source.st.Pool.QueryRow(ctx, `SELECT command FROM execution_operations WHERE id=$1`, command.Id).Scan(&original); err != nil {
			t.Fatal("recovery invented a replacement child", err)
		}
		var prior api.ExecutionCommand
		if json.Unmarshal(original, &prior) != nil || prior.PlanHash != command.PlanHash || prior.Generation != command.Generation || !prior.Deadline.Equal(command.Deadline) {
			t.Fatal("frozen child identity changed during recovery")
		}
	}
	if err = current.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	// A revocation may have been delivered independently of the frozen parent.
	// It has public inputs and must survive recovery without being acknowledged
	// as a side effect of replacing controller authority.
	var installedNetwork []byte
	if err = destination.st.Pool.QueryRow(ctx, `SELECT installed_manifest FROM network_members WHERE server_id=$1`, enrollments[0].ServerId).Scan(&installedNetwork); err != nil {
		t.Fatal(err)
	}
	var restrictionBaseline hostnetwork.Manifest
	if json.Unmarshal(installedNetwork, &restrictionBaseline) != nil || restrictionBaseline.Validate() != nil {
		t.Fatal("invalid public removal fixture baseline")
	}
	restrictionWanted := restrictionBaseline
	restrictionWanted.Generation++
	restrictionWanted.Exports, restrictionWanted.Egress = nil, nil
	restrictionBody := hostnetwork.Command{Action: "restrict", NetworkID: restrictionBaseline.NetworkID, MembershipID: restrictionBaseline.MembershipID, Baseline: &restrictionBaseline, Manifest: &restrictionWanted}
	restrictionRaw, _ := json.Marshal(restrictionBody)
	restrictionMessage := json.RawMessage(restrictionRaw)
	priorAuthority := applied.Handovers[enrollments[0].ServerId].Authority.Controller
	pendingRestriction := api.ExecutionCommand{Id: uuid.MustParse("00000000-0000-4000-8000-000000000011"), ServerId: enrollments[0].ServerId, Generation: 100000, ControllerGeneration: &priorAuthority, Kind: "network", Action: "inspect", ResourceId: restrictionBaseline.NetworkID, PlanHash: hostnetwork.Hash(restrictionBody), Deadline: time.Now().Add(time.Minute), Network: &restrictionMessage}
	completedRestriction := pendingRestriction
	completedRestriction.Id, completedRestriction.Generation = uuid.MustParse("ffffffff-ffff-4fff-bfff-fffffffffffe"), 99999
	acknowledgedRestriction := completedRestriction
	acknowledgedRestriction.Id, acknowledgedRestriction.Generation = uuid.MustParse("ffffffff-ffff-4fff-bfff-fffffffffffd"), 99998
	if _, err = journals[0].Accept(acknowledgedRestriction); err != nil {
		t.Fatal(err)
	}
	acknowledgedReceipt := hostnetwork.Receipt{NetworkID: restrictionBaseline.NetworkID, Identity: hostnetwork.Identity{MembershipID: restrictionBaseline.MembershipID, PublicKey: restrictionBaseline.PublicKey}, Generation: restrictionWanted.Generation, Hash: hostnetwork.Hash(restrictionWanted), Disabled: restrictionWanted.Disabled}
	acknowledgedRaw, _ := json.Marshal(acknowledgedReceipt)
	acknowledgedMessage := json.RawMessage(acknowledgedRaw)
	if err = journals[0].Mutate(acknowledgedRestriction.Id, func(r *agent.Record) { r.Report.State, r.Report.Network = "succeeded", &acknowledgedMessage }); err != nil {
		t.Fatal(err)
	}
	acknowledgedRecord, err := journals[0].Record(acknowledgedRestriction.Id)
	if err != nil {
		t.Fatal(err)
	}
	if err = journals[0].Acknowledge(acknowledgedRestriction.Id, acknowledgedRecord.Report.Sequence); err != nil {
		t.Fatal(err)
	}
	if _, err = journals[0].Accept(completedRestriction); err != nil {
		t.Fatal(err)
	}
	completedReceipt := hostnetwork.Receipt{NetworkID: restrictionBaseline.NetworkID, Identity: hostnetwork.Identity{MembershipID: restrictionBaseline.MembershipID, PublicKey: restrictionBaseline.PublicKey}, Generation: restrictionWanted.Generation, Hash: hostnetwork.Hash(restrictionWanted), Disabled: restrictionWanted.Disabled}
	completedReceiptRaw, _ := json.Marshal(completedReceipt)
	completedReceiptMessage := json.RawMessage(completedReceiptRaw)
	if err = journals[0].Mutate(completedRestriction.Id, func(r *agent.Record) { r.Report.State, r.Report.Network = "succeeded", &completedReceiptMessage }); err != nil {
		t.Fatal(err)
	}
	if _, err = journals[0].Accept(pendingRestriction); err != nil {
		t.Fatal("retain pending standalone removal", err)
	}
	var againExports []string
	for _, j := range journals {
		dir := filepath.Join(t.TempDir(), "second-export")
		if err = j.ExportRecovery(dir); err != nil {
			t.Fatal(err)
		}
		againExports = append(againExports, dir)
	}
	second, secondOwner := olderManagement(true)
	stalePending, _ := json.Marshal(acknowledgedRestriction)
	if _, err = second.st.Pool.Exec(ctx, `INSERT INTO execution_operations(id,workspace_id,server_id,generation,request_key,command,kind,state) VALUES($1,$2,$3,$4,$5,$6,'network','queued')`, acknowledgedRestriction.Id, ws, acknowledgedRestriction.ServerId, acknowledgedRestriction.Generation, uuid.New(), stalePending); err != nil {
		t.Fatal(err)
	}
	unobserved := pendingRestriction
	unobserved.Id, unobserved.Generation = uuid.MustParse("00000000-0000-4000-8000-000000000010"), 99997
	unobservedRaw, _ := json.Marshal(unobserved)
	if _, err = second.st.Pool.Exec(ctx, `INSERT INTO execution_operations(id,workspace_id,server_id,generation,request_key,command,kind,state) VALUES($1,$2,$3,$4,$5,$6,'network','dispatched')`, unobserved.Id, ws, unobserved.ServerId, unobserved.Generation, uuid.New(), unobservedRaw); err != nil {
		t.Fatal(err)
	}
	secondInput := execution.RecoveryImport{WorkspaceID: ws, OwnerID: secondOwner, HostExports: againExports}
	var nonparticipantRaw []byte
	if err = source.st.Pool.QueryRow(ctx, `SELECT installed_manifest FROM network_members WHERE server_id=$1`, enrollments[2].ServerId).Scan(&nonparticipantRaw); err != nil {
		t.Fatal(err)
	}
	var nonparticipantBaseline hostnetwork.Manifest
	if json.Unmarshal(nonparticipantRaw, &nonparticipantBaseline) != nil {
		t.Fatal("invalid nonparticipant baseline")
	}
	nonparticipantWanted := nonparticipantBaseline
	nonparticipantWanted.Generation++
	nonparticipantWanted.Peers, nonparticipantWanted.Exports, nonparticipantWanted.Egress = nil, nil, nil
	nonparticipantBody := hostnetwork.Command{Action: "restrict", NetworkID: nonparticipantBaseline.NetworkID, MembershipID: nonparticipantBaseline.MembershipID, Baseline: &nonparticipantBaseline, Manifest: &nonparticipantWanted}
	nonparticipantPayload, _ := json.Marshal(nonparticipantBody)
	nonparticipantMessage := json.RawMessage(nonparticipantPayload)
	nonparticipantCommand := api.ExecutionCommand{Id: uuid.New(), ServerId: enrollments[2].ServerId, Generation: 1000, Kind: "network", Action: "inspect", ResourceId: nonparticipantBaseline.NetworkID, PlanHash: hostnetwork.Hash(nonparticipantBody), Deadline: time.Now().Add(time.Minute), Network: &nonparticipantMessage}
	if _, err = journals[2].Accept(nonparticipantCommand); err != nil {
		t.Fatal(err)
	}
	unfinishedExport := filepath.Join(t.TempDir(), "unfinished-nonparticipant")
	if err = journals[2].ExportRecovery(unfinishedExport); err != nil {
		t.Fatal(err)
	}
	unfinished := secondInput
	unfinished.HostExports = append([]string{}, againExports...)
	unfinished.HostExports[2] = unfinishedExport
	var beforeController, afterController int64
	if err = second.st.Pool.QueryRow(ctx, `SELECT controller_generation FROM servers WHERE id=$1`, enrollments[2].ServerId).Scan(&beforeController); err != nil {
		t.Fatal(err)
	}
	if _, err = second.execution.RecoverCrossHost(ctx, unfinished); err == nil || !strings.Contains(err.Error(), "nonparticipant has an unfinished") {
		t.Fatal("unfinished nonparticipant was not refused during preflight", err)
	}
	if err = second.st.Pool.QueryRow(ctx, `SELECT controller_generation FROM servers WHERE id=$1`, enrollments[2].ServerId).Scan(&afterController); err != nil || afterController != beforeController {
		t.Fatal("nonparticipant refusal changed controller authority", err)
	}
	if err = journals[2].Mutate(nonparticipantCommand.Id, func(r *agent.Record) { r.Report.State = "cancelled" }); err != nil {
		t.Fatal(err)
	}
	settledExport := filepath.Join(t.TempDir(), "settled-nonparticipant")
	if err = journals[2].ExportRecovery(settledExport); err != nil {
		t.Fatal(err)
	}
	secondInput.HostExports = append([]string{}, againExports...)
	secondInput.HostExports[2] = settledExport
	secondPreview, err := second.execution.RecoverCrossHost(ctx, secondInput)
	if err != nil {
		t.Fatal("inspect after a second management loss", err)
	}
	secondInput.ExpectedState = secondPreview.StateHash
	secondApplied, err := second.execution.RecoverCrossHost(ctx, secondInput)
	if err != nil || !secondApplied.Applied {
		t.Fatal("prior replay-grant proofs prevented another recovery", err)
	}
	for _, input := range enrollments {
		if h, participant := secondApplied.Handovers[input.ServerId]; participant && h.Authority.Controller <= applied.Handovers[input.ServerId].Authority.Controller {
			t.Fatal("second recovery did not advance authority")
		}
	}
	var recoveredRestriction []byte
	var recoveredRestrictionState string
	if err = second.st.Pool.QueryRow(ctx, `SELECT command,state FROM execution_operations WHERE id=$1`, pendingRestriction.Id).Scan(&recoveredRestriction, &recoveredRestrictionState); err != nil {
		t.Fatal("standalone removal was lost from older-backup reconstruction", err)
	}
	var restoredRestriction api.ExecutionCommand
	if json.Unmarshal(recoveredRestriction, &restoredRestriction) != nil || restoredRestriction.ParentId != nil || restoredRestriction.PlanHash != pendingRestriction.PlanHash || restoredRestriction.Generation != pendingRestriction.Generation || !restoredRestriction.Deadline.Equal(pendingRestriction.Deadline) || recoveredRestrictionState != "running" {
		t.Fatal("standalone removal was rewritten or silently completed")
	}
	if err = second.st.Pool.QueryRow(ctx, `SELECT state FROM execution_operations WHERE id=$1`, completedRestriction.Id).Scan(&recoveredRestrictionState); err != nil || recoveredRestrictionState != "succeeded" {
		t.Fatal("completed receipt collided with the lower-UUID live removal", err)
	}
	if err = second.st.Pool.QueryRow(ctx, `SELECT state FROM execution_operations WHERE id=$1`, acknowledgedRestriction.Id).Scan(&recoveredRestrictionState); err != nil || recoveredRestrictionState != "succeeded" {
		t.Fatal("acknowledged terminal proof did not supersede stale pending metadata", err)
	}
	if err = second.st.Pool.QueryRow(ctx, `SELECT state FROM execution_operations WHERE id=$1`, unobserved.Id).Scan(&recoveredRestrictionState); err != nil || recoveredRestrictionState != "queued" {
		t.Fatal("unobserved dispatched removal bypassed the queued backlog", err)
	}
	if err = journals[0].TransferRecovery("https://second-recovery.example.test", make([]byte, 32), strings.Repeat("t", 43), secondApplied.Handovers[enrollments[0].ServerId]); err != nil {
		t.Fatal("pending standalone removal blocked guarded transfer", err)
	}
	retainedRestriction, err := journals[0].Record(pendingRestriction.Id)
	if err != nil || retainedRestriction.Acknowledged || retainedRestriction.Report.State != "running" || retainedRestriction.Command.PlanHash != pendingRestriction.PlanHash || retainedRestriction.Command.ControllerGeneration == nil || *retainedRestriction.Command.ControllerGeneration != secondApplied.Handovers[enrollments[0].ServerId].Authority.Controller {
		t.Fatal("guarded transfer discarded the pending removal", err)
	}

}

func recoveryFixtureRestriction(t *testing.T, ctx context.Context, st *store.Store, server uuid.UUID) api.ExecutionCommand {
	t.Helper()
	var raw []byte
	if err := st.Pool.QueryRow(ctx, `SELECT installed_manifest FROM network_members WHERE server_id=$1`, server).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var baseline hostnetwork.Manifest
	if json.Unmarshal(raw, &baseline) != nil {
		t.Fatal("invalid queued-removal baseline")
	}
	wanted := baseline
	wanted.Generation++
	wanted.Exports, wanted.Egress = nil, nil
	body := hostnetwork.Command{Action: "restrict", NetworkID: baseline.NetworkID, MembershipID: baseline.MembershipID, Baseline: &baseline, Manifest: &wanted}
	payload, _ := json.Marshal(body)
	message := json.RawMessage(payload)
	return api.ExecutionCommand{Id: uuid.New(), ServerId: server, Generation: 10000, Kind: "network", Action: "inspect", ResourceId: baseline.NetworkID, PlanHash: hostnetwork.Hash(body), Deadline: time.Now().Add(time.Hour), Network: &message}
}
