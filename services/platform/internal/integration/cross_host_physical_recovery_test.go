package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/agent"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/astilbahq/astilba/services/platform/internal/upgrade"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
)

func TestNativeCrossHostDrainedRecovery(t *testing.T) {
	testNativePhysicalRecovery(t, false)
}

func TestNativeCrossHostUpgradeRecovery(t *testing.T) {
	testNativePhysicalRecovery(t, true)
}

func testNativePhysicalRecovery(t *testing.T, upgradeRecovery bool) {
	if testing.Short() || os.Getenv("ASTILBA_NATIVE_AGENT") != "1" {
		t.Skip("requires the explicitly prepared disposable native agent host")
	}
	ctx := context.Background()
	source := setup(t)
	b := source.browser(t)
	ws := b.login("owner@example.test", source.password).Workspaces[0].Id
	base := "/api/v1/workspaces/" + ws.String()
	var project api.Project
	b.call("POST", base+"/projects", map[string]string{"name": "Physical recovery"}, 201, &project)
	var environment api.Environment
	b.call("POST", base+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "Drain"}, 201, &environment)
	path := base + "/projects/" + project.Id.String() + "/environments/" + environment.Id.String()
	images, err := client.New(client.WithHost(os.Getenv("PLAT_APP_DOCKER_HOST")))
	if err != nil {
		t.Fatal(err)
	}
	defer images.Close()
	var hosts []*recoveryFixtureHost
	var inputs []api.AgentEnrollment
	var identities []execution.Identity
	var members []api.ManagedNetworkMemberInput
	var additionalImages []string
	if upgradeRecovery {
		additionalImages = []string{domain.Postgres17Image, domain.PostgresImage}
	}
	for i := range 2 {
		docker, namespace := recoveryFixtureDocker(t, ctx, images, additionalImages...)
		_, in, _ := enrollment(t, source, b, ws, "deployment")
		state := filepath.Join(t.TempDir(), "journal")
		if err := os.Mkdir(state, 0700); err != nil {
			t.Fatal(err)
		}
		if upgradeRecovery && i == 0 {
			if err := os.Mkdir(filepath.Join(state, "backups"), 0700); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(state, "network-enabled"), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(state, "docker"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("/opt/astilba-plat4/cli-plugins", filepath.Join(state, "docker", "cli-plugins")); err != nil {
			t.Fatal(err)
		}
		cli := filepath.Join(state, "docker-cli")
		config := strings.ReplaceAll(filepath.Join(state, "docker"), "'", "'\\''")
		if err := os.WriteFile(cli, []byte("#!/bin/sh\nexport DOCKER_CONFIG='"+config+"'\nexec /opt/astilba-plat4/bin/docker \"$@\"\n"), 0700); err != nil {
			t.Fatal(err)
		}
		journal, err := agent.Open(state)
		if err != nil {
			t.Fatal(err)
		}
		if err := journal.SetConfig(agent.Config{Origin: "https://source.example.test", ServerID: in.ServerId, MachineID: in.MachineId, Credential: in.Credential, Role: "deployment", Enrolled: true}); err != nil {
			journal.Close()
			t.Fatal(err)
		}
		host := newRecoveryFixtureHost(source.execution, execution.Identity{}, journal, docker, cli)
		host.namespace, host.state = namespace, state
		t.Cleanup(func() {
			if err := host.Close(); err != nil {
				t.Error(err)
			}
		})
		heartbeat, err := host.Preflight(ctx)
		if err != nil || heartbeat.Capabilities.CrossHost == nil || !*heartbeat.Capabilities.CrossHost || heartbeat.Preflight.CrossHostInventory == nil {
			t.Fatal("physical host preflight", err, heartbeat.Preflight.Notes)
		}
		in.Capabilities, in.Preflight = heartbeat.Capabilities, heartbeat.Preflight
		if _, err := source.execution.Enroll(ctx, in); err != nil {
			t.Fatal(err)
		}
		identity, err := source.execution.Authenticate(ctx, in.ServerId, in.Credential)
		if err != nil {
			t.Fatal(err)
		}
		host.engine.FetchCheckpoint = func(ctx context.Context, d deployment.RecoveryDescriptor) ([]byte, error) {
			return source.execution.RecoveryCheckpoint(ctx, identity, d.ParentID, d.CheckpointID)
		}
		hosts, inputs, identities = append(hosts, host), append(inputs, in), append(identities, identity)
		members = append(members, api.ManagedNetworkMemberInput{ServerId: in.ServerId, Endpoint: []string{"192.0.2.1:51820", "192.0.2.2:51820"}[i], ListenPort: 51820, CpuMillis: 1000, MemoryMiB: 1024, UnboundedInventoryHash: heartbeat.Preflight.CrossHostInventory.UnboundedInventoryHash})
	}
	linkRecoveryFixtureHosts(t, hosts[0].namespace, hosts[1].namespace)
	var group sync.WaitGroup
	workers, stopWorkers := context.WithCancel(ctx)
	defer func() { stopWorkers(); group.Wait() }()
	var holdPrimaryComplete atomic.Bool
	var crashCutover atomic.Bool
	interrupted := make(chan api.ExecutionCommand, 1)
	for i, host := range hosts {
		group.Go(func() {
			runPhysicalRecoveryHost(t, workers, source.execution, identities[i], host, func(c api.ExecutionCommand) bool {
				if i == 0 && upgradeRecovery && c.Kind == "upgrade" && c.Upgrade != nil {
					var command upgrade.Command
					if json.Unmarshal(*c.Upgrade, &command) == nil && command.Action == "cutover" {
						if crashCutover.CompareAndSwap(false, true) {
							_, err := host.executeAt(workers, c, "upgrade-database-effect")
							var exited *exec.ExitError
							if !errors.As(err, &exited) || exited.ExitCode() != 77 {
								t.Error("database-effect process did not exit at the fault point", err)
							} else {
								interrupted <- c
							}
						}
						return true
					}
				}
				return i == 0 && holdPrimaryComplete.Load() && physicalRecoveryPhase(c) == "complete"
			}, nil)
		})
	}
	wait := func(label string, check func() bool) {
		t.Helper()
		until := time.Now().Add(90 * time.Second)
		for time.Now().Before(until) {
			if check() {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		var diagnostic []byte
		_ = source.st.Pool.QueryRow(ctx, `SELECT jsonb_build_object('operations',(SELECT jsonb_agg(jsonb_build_object('kind',kind,'state',state,'phase',command->'crosshost'->>'phase','networkAction',command->'network'->>'action','wantedGeneration',command->'network'->'manifest'->'generation','note',note)) FROM execution_operations WHERE workspace_id=$1 AND state NOT IN ('succeeded','failed','cancelled')),'network',(SELECT jsonb_agg(jsonb_build_object('generation',generation,'ack',acknowledged_generation,'installedGeneration',installed_manifest->'generation')) FROM network_members WHERE workspace_id=$1))`, ws).Scan(&diagnostic)
		t.Fatal(label, string(diagnostic))
	}
	var network struct {
		ID         uuid.UUID `json:"id"`
		State      string    `json:"state"`
		Generation int64     `json:"generation"`
	}
	b.call("PUT", base+"/network", api.ConfigureManagedNetwork{Prefix: "10.248.0.0/24", Members: members}, 200, &network)
	wait("physical network convergence", func() bool { b.call("GET", base+"/network", nil, 200, &network); return network.State == "ready" })
	app, image := uuid.New(), execution.TestImage
	spec := domain.Specification{Version: 1, Variables: []domain.Variable{}, Applications: []domain.Application{{ID: app, Name: "Web", Replicas: 2, Command: []string{"sh", "-c", "mkdir -p /tmp/www; hostname > /tmp/www/index.html; exec httpd -f -p 8080 -h /tmp/www"}, Source: domain.ApplicationSource{Kind: "image", Image: &image}, Health: &domain.HealthCheck{Port: 8080, Path: "/"}}}, Routes: []domain.Route{{ID: uuid.New(), TargetID: app, Protocol: "http", Domain: "physical-recovery.example.test", Port: 8080, TLS: "off", AllowCIDRs: []string{}}}}
	pg, volume := uuid.New(), uuid.New()
	if upgradeRecovery {
		var secret api.SecretVersion
		b.call("POST", path+"/secrets", map[string]string{"name": "Database", "value": "physical-upgrade-fixture-canary"}, 201, &secret)
		pgImage := domain.Postgres17Image
		spec.Applications[0].DependsOn = []uuid.UUID{pg}
		spec.Applications[0].Health.Path = "/cgi-bin/health"
		spec.Applications[0].Command = []string{"sh", "-c", `mkdir -p /tmp/www/cgi-bin
cat > /tmp/www/cgi-bin/health <<'CGI'
#!/bin/sh
if ! nc -z -w 2 "$DB_HOST" "$DB_PORT"; then
  printf 'Status: 503\r\nContent-Type: text/plain\r\n\r\nDatabase unreachable'
  exit
fi
printf 'Content-Type: text/plain\r\n\r\n'
hostname
CGI
chmod +x /tmp/www/cgi-bin/health
exec httpd -f -p 8080 -h /tmp/www`}
		spec.Applications = append(spec.Applications, domain.Application{ID: pg, Name: "Database", Kind: "postgres", Source: domain.ApplicationSource{Kind: "image", Image: &pgImage}, Postgres: &domain.Postgres{Database: "app", Username: "app", PasswordSecretVersionID: secret.Id}, Mounts: []domain.VolumeMount{{VolumeID: volume, Target: domain.PostgresMount(pgImage)}}})
		spec.Volumes = []domain.Volume{{ID: volume, Name: "Database"}}
		spec.Bindings = []domain.Binding{{ID: uuid.New(), SourceID: pg, TargetID: app, Output: "HOST", Variable: "DB_HOST"}, {ID: uuid.New(), SourceID: pg, TargetID: app, Output: "PORT", Variable: "DB_PORT"}}
	}
	var revision api.Revision
	revision.Id = environment.DraftRevisionId
	deploy := func() (deployment.Plan, api.Operation) {
		t.Helper()
		for i, host := range hosts {
			heartbeat, err := host.Preflight(ctx)
			if err != nil || heartbeat.Preflight.CrossHostInventory == nil {
				t.Fatal("refresh physical capacity", err)
			}
			if _, err := source.execution.Heartbeat(ctx, identities[i], heartbeat); err != nil {
				t.Fatal(err)
			}
			members[i].UnboundedInventoryHash = heartbeat.Preflight.CrossHostInventory.UnboundedInventoryHash
		}
		if spec.Placement != nil {
			// The initial single-host applications and proxy change the actual
			// unbounded-workload inventory. Renew its explicit owner acknowledgement.
			b.call("GET", base+"/network", nil, 200, &network)
			b.call("PUT", base+"/network", api.ConfigureManagedNetwork{Prefix: "10.248.0.0/24", BaseGeneration: network.Generation, Members: members}, 200, &network)
			wait("refreshed physical network", func() bool { b.call("GET", base+"/network", nil, 200, &network); return network.State == "ready" })
		}
		b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": revision.Id, "specification": spec}, 200, &revision)
		var view deployment.Status
		b.call("POST", path+"/deployment-plans", api.VisualPlanRequest{BaseRevisionId: revision.Id, ServerId: inputs[0].ServerId, AllowHostChanges: true}, 201, &view)
		wait("physical plan", func() bool {
			b.call("GET", path+"/deployment-plans/"+view.ID.String(), nil, 200, &view)
			if view.State == "failed" || view.State == "needs_attention" {
				t.Fatal("physical plan failed", view)
			}
			return view.Plan != nil
		})
		var operation api.Operation
		b.call("POST", path+"/deployment-plans/"+view.ID.String()+"/apply", api.ApplyPlan{IdempotencyKey: uuid.New()}, 201, &operation)
		return *view.Plan, operation
	}
	complete := func(id uuid.UUID) bool {
		var state, note string
		if err := source.st.Pool.QueryRow(ctx, `SELECT state,note FROM execution_operations WHERE id=$1`, id).Scan(&state, &note); err != nil {
			t.Fatal(err)
		}
		if state == "failed" || state == "needs_attention" {
			t.Fatal("physical deployment failed", state, note)
		}
		return state == "succeeded"
	}
	_, first := deploy()
	wait("initial source", func() bool { return complete(first.Id) })
	targets := []domain.PlacementTarget{{ServerID: inputs[0].ServerId, Count: 1}, {ServerID: inputs[1].ServerId, Count: 1}}
	if !upgradeRecovery {
		targets = []domain.PlacementTarget{{ServerID: inputs[1].ServerId, Count: 2}}
	}
	spec.Placement = &domain.Placement{NetworkID: network.ID, IngressServerID: inputs[0].ServerId, Applications: []domain.ApplicationPlacement{{ApplicationID: app, CPUMillis: 200, MemoryMiB: 128, Targets: targets}}}
	applied, distributed := deploy()
	wait("distributed source", func() bool { return complete(distributed.Id) })
	wait("settled source network", func() bool {
		b.call("GET", base+"/network", nil, 200, &network)
		var idle bool
		if err := source.st.Pool.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM execution_operations WHERE workspace_id=$1 AND server_id IN ($2,$3) AND state NOT IN ('succeeded','failed','cancelled'))`, ws, inputs[0].ServerId, inputs[1].ServerId).Scan(&idle); err != nil {
			t.Fatal(err)
		}
		return idle && network.State == "ready"
	})
	var remoteUnit uuid.UUID
	for _, unit := range applied.ActiveUnits() {
		if unit.Plan.ServerID == inputs[1].ServerId {
			remoteUnit = unit.Plan.UnitID
		}
	}
	if remoteUnit == uuid.Nil {
		t.Fatal("missing remote source unit")
	}
	if !upgradeRecovery {
		var primary deployment.Plan
		for _, unit := range applied.ActiveUnits() {
			if unit.Plan.Local.Primary {
				primary = unit.Plan
			}
		}
		evidence, err := hosts[0].RecoveryEvidence()
		if err != nil || primary.ID == uuid.Nil || len(primary.LocalApplications()) != 0 || evidence.Units[primary.UnitID.String()] != primary.Hash {
			t.Fatal("ingress-only source lost its applied unit inventory", err)
		}
	}
	remoteContainers, err := hosts[1].engine.Docker.ContainerList(ctx, client.ContainerListOptions{Filters: client.Filters{}.Add("label", "com.astilba.server="+inputs[1].ServerId.String()).Add("label", "com.astilba.resource="+app.String())})
	expectedRemote := 1
	if !upgradeRecovery {
		expectedRemote = 2
	}
	if err != nil || len(remoteContainers.Items) != expectedRemote {
		t.Fatal("remote source container", err, remoteContainers)
	}
	remoteID := remoteContainers.Items[0].ID
	for _, remote := range remoteContainers.Items {
		assertPhysicalRemoteHTTP(t, ctx, hosts[0].namespace, spec.Applications[0].Health.Path, remote.ID)
	}
	if upgradeRecovery {
		physicalPGQuery(t, ctx, hosts[0], pg, "CREATE TABLE recovery_canary(value text PRIMARY KEY); INSERT INTO recovery_canary VALUES ('source-before-upgrade')")
	}
	tx, err := source.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	backupRaw, err := portability.CaptureRecovery(ctx, tx, ws)
	tx.Rollback(ctx)
	if err != nil {
		t.Fatal(err)
	}
	savedArchive, err := portability.DecodeRecovery(backupRaw)
	if err != nil {
		t.Fatal(err)
	}
	var target deployment.Plan
	var operation api.Operation
	var oldFrame api.ExecutionCommand
	var upgradeView upgrade.View
	if upgradeRecovery {
		var destination backup.Destination
		b.call("POST", base+"/backup-destinations", map[string]any{"name": "Physical fixture", "kind": "filesystem", "root": filepath.Join(hosts[0].state, "backups"), "endpoint": "", "bucket": "", "region": "", "access": map[string]string{"accessKey": "", "secretKey": "", "password": ""}}, 201, &destination)
		password := "physical-upgrade-repository-canary"
		var policy backup.Policy
		b.call("POST", path+"/backup-policies", api.BackupPolicyRequest{Name: "Recovery", ResourceId: pg, DestinationId: destination.ID, Method: "postgres-logical", Schedule: api.DailySchedule{Timezone: "UTC"}, Retain: 7, Enabled: true, Command: []string{}, Password: &password}, 201, &policy)
		var installation api.Operation
		b.call("POST", path+"/backup-policies/"+policy.ID.String()+"/commands", api.BackupCommandRequest{Action: "install", RequestKey: uuid.New(), Snapshot: ""}, 202, &installation)
		wait("physical policy installation", func() bool {
			if !complete(installation.Id) {
				return false
			}
			var installed bool
			if err := source.st.Pool.QueryRow(ctx, `SELECT COALESCE(status->>'generation' = configuration->>'generation',false) FROM backup_policies WHERE id=$1`, policy.ID).Scan(&installed); err != nil {
				t.Fatal(err)
			}
			return installed
		})
		b.call("POST", path+"/upgrades", api.UpgradeRequest{PolicyId: policy.ID, ResourceId: pg, BaseRevisionId: revision.Id, RetainDays: 7}, 201, &upgradeView)
		actions := path + "/upgrades/" + upgradeView.Plan.ID.String() + "/actions"
		b.call("POST", actions, api.UpgradeActionRequest{Action: "prepare", ExpectedPhase: "planned", RequestKey: uuid.New()}, 202, &operation)
		wait("physical upgrade preparation", func() bool {
			var ready bool
			if err := source.st.Pool.QueryRow(ctx, `SELECT h.phase='maintenance-owner' AND u.status->>'phase'='ready' FROM cross_host_operations h JOIN supported_upgrades u ON u.id=$2 WHERE h.id=$1`, operation.Id, upgradeView.Plan.ID).Scan(&ready); err != nil {
				t.Fatal(err)
			}
			return ready
		})
		b.call("POST", actions, api.UpgradeActionRequest{Action: "cutover", ExpectedPhase: "ready", RequestKey: uuid.New()}, 202, &operation)
		select {
		case oldFrame = <-interrupted:
		case <-time.After(90 * time.Second):
			t.Fatal("physical upgrade did not reach its database-effect crash")
		}
		target = upgradeView.Plan.TargetGroup()
		physicalPGQuery(t, ctx, hosts[0], pg, "INSERT INTO recovery_canary VALUES ('target-after-crash')")
		assertPhysicalWriters(t, ctx, hosts, applied, app, false)
	} else {
		holdPrimaryComplete.Store(true)
		spec.Placement.Applications[0].Targets = []domain.PlacementTarget{{ServerID: inputs[0].ServerId, Count: 2}}
		target, operation = deploy()
		wait("remote drain completion", func() bool {
			var done bool
			if err := source.st.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM execution_operations WHERE parent_id=$1 AND server_id=$2 AND command->'crosshost'->>'phase'='complete' AND state='succeeded')`, operation.Id, inputs[1].ServerId).Scan(&done); err != nil {
				t.Fatal(err)
			}
			return done
		})
		var oldRaw []byte
		if err := source.st.Pool.QueryRow(ctx, `SELECT command FROM execution_operations WHERE parent_id=$1 AND server_id=$2 AND command->'crosshost'->>'phase'='complete'`, operation.Id, inputs[0].ServerId).Scan(&oldRaw); err != nil || json.Unmarshal(oldRaw, &oldFrame) != nil {
			t.Fatal("held primary command", err)
		}
	}
	stopWorkers()
	group.Wait()
	if err := source.execution.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	source.server.Close()
	if !upgradeRecovery {
		evidence, err := hosts[1].RecoveryEvidence()
		if err != nil || evidence.ActiveParent != uuid.Nil {
			t.Fatal("drained host reservation retained", err, evidence.ActiveParent)
		}
		if _, exists := evidence.Units[remoteUnit.String()]; exists {
			t.Fatal("completed drain kept its unit inventory")
		}
		for _, remote := range remoteContainers.Items {
			oldPool, err := hosts[1].engine.Docker.ContainerInspect(ctx, remote.ID, client.ContainerInspectOptions{})
			if err != nil || oldPool.Container.State.Running {
				t.Fatal("drained pool was not stopped and retained", err)
			}
		}
	}
	var exports []string
	for _, host := range hosts {
		dir := filepath.Join(t.TempDir(), "export")
		if err := host.ExportRecovery(dir); err != nil {
			t.Fatal(err)
		}
		exports = append(exports, dir)
	}
	management := func(archive portability.Archive) (*fixture, uuid.UUID) {
		dst := setup(t)
		owner := dst.browser(t).login("owner@example.test", dst.password).User.Id
		dst.server.Close()
		if err := dst.execution.Stop(ctx); err != nil {
			t.Fatal(err)
		}
		dst.st.Keys = source.st.Keys
		if _, err := dst.st.Pool.Exec(ctx, `INSERT INTO workspaces(id,name) VALUES($1,'Restored backup')`, ws); err != nil {
			t.Fatal(err)
		}
		if _, err := dst.st.Pool.Exec(ctx, `INSERT INTO memberships(workspace_id,account_id,role) VALUES($1,$2,'owner')`, ws, owner); err != nil {
			t.Fatal(err)
		}
		tx, err := dst.st.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if err := portability.MergeRecovery(ctx, tx, archive, owner); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		return dst, owner
	}
	if upgradeRecovery {
		t.Run("conflicting_owner_approval", func(t *testing.T) {
			tx, err := source.st.Pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := portability.CaptureRecovery(ctx, tx, ws)
			tx.Rollback(ctx)
			if err != nil {
				t.Fatal(err)
			}
			archive, err := portability.DecodeRecovery(raw)
			if err != nil {
				t.Fatal(err)
			}
			dst, owner := management(archive)
			changed, err := dst.st.Pool.Exec(ctx, `UPDATE cross_host_approvals SET expected_phase='planned' WHERE workspace_id=$1 AND parent_id=$2 AND action='cutover'`, ws, operation.Id)
			if err != nil || changed.RowsAffected() != 1 {
				t.Fatal("conflicting approval fixture", err)
			}
			in := execution.RecoveryImport{WorkspaceID: ws, OwnerID: owner, HostExports: exports}
			preview, err := dst.execution.RecoverCrossHost(ctx, in)
			if err != nil {
				t.Fatal("inspect conflicting approval", err)
			}
			in.ExpectedState = preview.StateHash
			if _, err := dst.execution.RecoverCrossHost(ctx, in); err == nil || !strings.Contains(err.Error(), "owner approval identity conflicts") {
				t.Fatal("conflicting owner approval did not refuse recovery", err)
			}
			var controller int64
			var phase string
			if err := dst.st.Pool.QueryRow(ctx, `SELECT controller_generation FROM servers WHERE id=$1`, inputs[0].ServerId).Scan(&controller); err != nil || controller != 0 {
				t.Fatal("approval conflict changed controller authority", err, controller)
			}
			if err := dst.st.Pool.QueryRow(ctx, `SELECT expected_phase FROM cross_host_approvals WHERE workspace_id=$1 AND parent_id=$2 AND action='cutover'`, ws, operation.Id).Scan(&phase); err != nil || phase != "planned" {
				t.Fatal("failed recovery overwrote existing approval", err, phase)
			}
			in.ExpectedState = ""
			after, err := dst.execution.RecoverCrossHost(ctx, in)
			if err != nil || after.StateHash != preview.StateHash {
				t.Fatal("approval refusal committed partial recovery state", err)
			}
		})
	}
	var negativeOutcomes []string
	if !upgradeRecovery {
		negativeOutcomes = []string{"missing", "needs_attention"}
	}
	for _, outcome := range negativeOutcomes {
		t.Run(outcome+"_completion_proof", func(t *testing.T) {
			dst, owner := management(savedArchive)
			altered := alterRecoveryCompletion(t, exports[1], operation.Id, outcome)
			in := execution.RecoveryImport{WorkspaceID: ws, OwnerID: owner, HostExports: []string{exports[0], altered}}
			preview, err := dst.execution.RecoverCrossHost(ctx, in)
			if err != nil {
				t.Fatal("negative preview", err)
			}
			in.ExpectedState = preview.StateHash
			if _, err := dst.execution.RecoverCrossHost(ctx, in); err == nil || !strings.Contains(err.Error(), "incompatible source or target inventory") {
				t.Fatal("missing drained inventory did not fail at the frozen completion guard", err)
			}
			var controller int64
			if err := dst.st.Pool.QueryRow(ctx, `SELECT controller_generation FROM servers WHERE id=$1`, inputs[1].ServerId).Scan(&controller); err != nil || controller != 0 {
				t.Fatal("failed recovery changed authority", err, controller)
			}
		})
	}
	if !upgradeRecovery {
		t.Run("wrong_ingress_unit_hash", func(t *testing.T) {
			dst, owner := management(savedArchive)
			altered := copyRecoveryExport(t, exports[0])
			raw, err := os.ReadFile(filepath.Join(altered, "host.json"))
			if err != nil {
				t.Fatal(err)
			}
			var evidence deployment.RecoveryEvidence
			if json.Unmarshal(raw, &evidence) != nil || evidence.Units[applied.UnitID.String()] == "" {
				t.Fatal("missing ingress source hash")
			}
			evidence.Units[applied.UnitID.String()] = strings.Repeat("f", 64)
			raw, err = json.Marshal(evidence)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(altered, "host.json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			in := execution.RecoveryImport{WorkspaceID: ws, OwnerID: owner, HostExports: []string{altered, exports[1]}}
			preview, err := dst.execution.RecoverCrossHost(ctx, in)
			if err != nil {
				t.Fatal("inspect wrong ingress hash", err)
			}
			in.ExpectedState = preview.StateHash
			if _, err = dst.execution.RecoverCrossHost(ctx, in); err == nil || !strings.Contains(err.Error(), "incompatible source or target inventory") {
				t.Fatal("unrelated ingress inventory was accepted", err)
			}
			var controller int64
			if err = dst.st.Pool.QueryRow(ctx, `SELECT controller_generation FROM servers WHERE id=$1`, inputs[0].ServerId).Scan(&controller); err != nil || controller != 0 {
				t.Fatal("rejected ingress inventory changed authority", err, controller)
			}
		})
	}
	dst, owner := management(savedArchive)
	in := execution.RecoveryImport{WorkspaceID: ws, OwnerID: owner, HostExports: exports}
	preview, err := dst.execution.RecoverCrossHost(ctx, in)
	if err != nil {
		t.Fatal("inspect drained recovery", err)
	}
	in.ExpectedState = preview.StateHash
	result, err := dst.execution.RecoverCrossHost(ctx, in)
	if err != nil {
		t.Fatal("stage drained recovery", err)
	}
	current, err := execution.New(dst.st)
	if err != nil {
		t.Fatal(err)
	}
	if err := current.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer current.Stop(ctx)
	account, err := dst.st.Queries.AccountByID(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	recovered, stopRecovered := context.WithCancel(ctx)
	defer func() { stopRecovered(); group.Wait() }()
	var holdRemoteOpen atomic.Bool
	holdRemoteOpen.Store(true)
	var holdRemoteResume atomic.Bool
	holdRemoteResume.Store(upgradeRecovery)
	var interruptedSequence int64
	if upgradeRecovery {
		record, err := hosts[0].Record(oldFrame.Id)
		if err != nil {
			t.Fatal(err)
		}
		interruptedSequence = record.Report.Sequence
	}
	for i, host := range hosts {
		ticket, err := current.PrepareTransfer(ctx, store.Principal{Account: account, Recent: true, MFA: true}, ws, inputs[i].ServerId)
		if err != nil {
			t.Fatal(err)
		}
		host.mu.Lock()
		err = host.engine.Journal.TransferRecovery("https://recovered.example.test", make([]byte, 32), ticket.Token, result.Handovers[inputs[i].ServerId])
		inventory, inventoryErr := host.engine.Journal.Inventory()
		host.mu.Unlock()
		if err != nil || inventoryErr != nil {
			t.Fatal("transfer completed host", err, inventoryErr)
		}
		input := inputs[i]
		input.Inventory, input.ControllerGeneration, input.Token = &inventory, &ticket.Generation, ticket.Token
		input.Credential, err = cryptobox.Token()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := current.Enroll(ctx, input); err != nil {
			t.Fatal("enroll recovered host", i, err)
		}
		host.mu.Lock()
		cfg, cfgErr := host.engine.Journal.Config()
		if cfgErr == nil {
			cfg.Enrolled, cfg.Credential, cfg.EnrollmentToken = true, input.Credential, ""
			cfgErr = host.engine.Journal.SetConfig(cfg)
		}
		host.mu.Unlock()
		if cfgErr != nil {
			t.Fatal(cfgErr)
		}
		identity, err := current.Authenticate(ctx, input.ServerId, input.Credential)
		if err != nil {
			t.Fatal(err)
		}
		host.engine.FetchCheckpoint = func(ctx context.Context, d deployment.RecoveryDescriptor) ([]byte, error) {
			return current.RecoveryCheckpoint(ctx, identity, d.ParentID, d.CheckpointID)
		}
		group.Go(func() {
			runPhysicalRecoveryHost(t, recovered, current, identity, host, func(c api.ExecutionCommand) bool {
				return i == 1 && (holdRemoteOpen.Load() && physicalRecoveryPhase(c) == "recovery-open" || holdRemoteResume.Load() && physicalRecoveryPhase(c) == "resume-target")
			}, func(c api.ExecutionCommand, r api.AgentReport) bool {
				var status upgrade.Status
				return upgradeRecovery && c.Id == oldFrame.Id && r.State == "failed" && r.Upgrade != nil && json.Unmarshal(*r.Upgrade, &status) == nil && status.Phase == "attention" && status.WritesPossible
			})
		})
	}
	until := time.Now().Add(30 * time.Second)
	var primaryOpened bool
	for time.Now().Before(until) {
		var acknowledged bool
		if err := dst.st.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM execution_operations WHERE parent_id=$1 AND server_id=$2 AND command->'crosshost'->>'phase'='recovery-open' AND state='succeeded')`, operation.Id, inputs[0].ServerId).Scan(&acknowledged); err != nil {
			t.Fatal(err)
		}
		if acknowledged {
			primaryOpened = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !primaryOpened {
		t.Fatal("primary replay hold was not acknowledged")
	}
	if _, err := hosts[0].Execute(ctx, oldFrame); err == nil || !strings.Contains(err.Error(), "command belongs to another controller generation") {
		t.Fatal("prior controller command survived exact-root authority transfer")
	}
	var premature bool
	if err := dst.st.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM execution_operations WHERE parent_id=$1 AND server_id=$2 AND command->'crosshost'->>'phase'='complete' AND report_sequence>0)`, operation.Id, inputs[0].ServerId).Scan(&premature); err != nil || premature {
		t.Fatal("primary completion bypassed all-open acknowledgements", err)
	}
	if upgradeRecovery {
		record, err := hosts[0].Record(oldFrame.Id)
		if err != nil || record.Report.Sequence != interruptedSequence {
			t.Fatal("interrupted data effect replayed before all hosts opened", err)
		}
		assertPhysicalWriters(t, ctx, hosts, applied, app, false)
	}
	holdRemoteOpen.Store(false)
	if upgradeRecovery {
		waitRecovered := func(label string, check func() bool) {
			t.Helper()
			until := time.Now().Add(90 * time.Second)
			for time.Now().Before(until) {
				if check() {
					return
				}
				time.Sleep(50 * time.Millisecond)
			}
			var diagnostic []byte
			_ = dst.st.Pool.QueryRow(ctx, `SELECT jsonb_build_object('phase',h.phase,'state',o.state,'note',o.note,'upgrade',u.status) FROM cross_host_operations h JOIN execution_operations o ON o.id=h.id JOIN supported_upgrades u ON u.id=$2 WHERE h.id=$1`, operation.Id, upgradeView.Plan.ID).Scan(&diagnostic)
			t.Fatal(label, string(diagnostic))
		}
		waitRecovered("interrupted upgrade owner review", func() bool {
			var attention bool
			if err := dst.st.Pool.QueryRow(ctx, `SELECT h.phase='maintenance-owner' AND o.state='needs_attention' AND u.status->>'phase'='attention' AND (u.status->>'writesPossible')::boolean FROM cross_host_operations h JOIN execution_operations o ON o.id=h.id JOIN supported_upgrades u ON u.id=$2 WHERE h.id=$1`, operation.Id, upgradeView.Plan.ID).Scan(&attention); err != nil {
				t.Fatal(err)
			}
			return attention
		})
		assertPhysicalWriters(t, ctx, hosts, applied, app, false)
		principal := store.Principal{Account: account, Recent: true, MFA: true}
		if _, err := current.UpgradeAction(ctx, principal, ws, project.Id, environment.Id, upgradeView.Plan.ID, api.UpgradeActionRequest{Action: "return", ExpectedPhase: "attention", RequestKey: uuid.New()}); !errors.Is(err, domain.Conflict) {
			t.Fatal("possible target writes did not forbid returning to the source", err)
		}
		if _, err := current.UpgradeAction(ctx, principal, ws, project.Id, environment.Id, upgradeView.Plan.ID, api.UpgradeActionRequest{Action: "reconcile", ExpectedPhase: "attention", RequestKey: uuid.New()}); err != nil {
			t.Fatal("approve explicit target reconciliation", err)
		}
		waitRecovered("remote target resumption held", func() bool {
			var held bool
			if err := dst.st.Pool.QueryRow(ctx, `SELECT h.phase='maintenance-resume-target' AND EXISTS(SELECT 1 FROM execution_operations c WHERE c.parent_id=h.id AND c.server_id=$2 AND c.command->'crosshost'->>'phase'='resume-target' AND c.state='succeeded') AND EXISTS(SELECT 1 FROM execution_operations c WHERE c.parent_id=h.id AND c.server_id=$3 AND c.command->'crosshost'->>'phase'='resume-target' AND c.state='dispatched') FROM cross_host_operations h WHERE h.id=$1`, operation.Id, inputs[0].ServerId, inputs[1].ServerId).Scan(&held); err != nil {
				t.Fatal(err)
			}
			return held
		})
		var early uuid.UUID
		if err := dst.st.Pool.QueryRow(ctx, `SELECT applied_revision_id FROM environments WHERE id=$1`, environment.Id).Scan(&early); err != nil || early != applied.RevisionID {
			t.Fatal("primary-only resumption published the global upgrade target", err, early)
		}
		holdRemoteResume.Store(false)
	}
	until = time.Now().Add(60 * time.Second)
	var finished bool
	for time.Now().Before(until) {
		var state string
		if err := dst.st.Pool.QueryRow(ctx, `SELECT state FROM execution_operations WHERE id=$1`, operation.Id).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state == "succeeded" {
			finished = true
			break
		}
		if state == "failed" {
			t.Fatal("recovered operation failed")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !finished {
		t.Fatal("recovered operation did not complete")
	}
	var appliedRevision uuid.UUID
	var remainingUnits int
	if err := dst.st.Pool.QueryRow(ctx, `SELECT applied_revision_id,(SELECT count(*) FROM cross_host_runtime WHERE environment_id=$1) FROM environments WHERE id=$1`, environment.Id).Scan(&appliedRevision, &remainingUnits); err != nil || appliedRevision != target.RevisionID || remainingUnits != len(target.ActiveUnits()) {
		t.Fatal("completed recovery did not publish the exact target", err, appliedRevision, remainingUnits)
	}
	if upgradeRecovery {
		if got := physicalPGQuery(t, ctx, hosts[0], pg, "SELECT string_agg(value, ',' ORDER BY value) FROM recovery_canary"); got != "source-before-upgrade,target-after-crash" {
			t.Fatal("reconciliation lost source or post-crash target data", got)
		}
		if version := physicalPGQuery(t, ctx, hosts[0], pg, "SHOW server_version_num"); !strings.HasPrefix(version, "18") {
			t.Fatal("reconciliation returned to the old database", version)
		}
		if _, err := hosts[0].engine.Docker.VolumeInspect(ctx, deployment.VolumeName(volume), client.VolumeInspectOptions{}); err != nil {
			t.Fatal("upgrade recovery removed the source volume", err)
		}
		assertPhysicalWriters(t, ctx, hosts, target, app, true)
		assertPhysicalRemoteHTTP(t, ctx, hosts[0].namespace, "/cgi-bin/health", remoteID)
	}
}

func assertPhysicalRemoteHTTP(t *testing.T, ctx context.Context, namespace, path, containerID string) {
	t.Helper()
	served := false
	for range 20 {
		body, err := exec.CommandContext(ctx, "nsenter", "--net=/run/netns/"+namespace, "curl", "--fail", "--silent", "--show-error", "--max-time", "3", "-H", "Host: physical-recovery.example.test", "http://127.0.0.1"+path).CombinedOutput()
		if err != nil {
			t.Fatal("physical cross-host HTTP", err, string(body))
		}
		served = served || strings.TrimSpace(string(body)) == containerID[:12]
	}
	if !served {
		t.Fatal("ingress never reached the remote container over managed networking")
	}
}

func physicalPGQuery(t *testing.T, ctx context.Context, host *recoveryFixtureHost, resource uuid.UUID, sql string) string {
	t.Helper()
	result, err := exec.CommandContext(ctx, "/opt/astilba-plat4/bin/docker", "--host", host.engine.Docker.DaemonHost(), "exec", deployment.UnitName(resource), "psql", "-U", "app", "-d", "app", "-At", "-v", "ON_ERROR_STOP=1", "-c", sql).CombinedOutput()
	if err != nil {
		t.Fatal("physical application database query", err, string(result))
	}
	return strings.TrimSpace(string(result))
}

func assertPhysicalWriters(t *testing.T, ctx context.Context, hosts []*recoveryFixtureHost, plan deployment.Plan, app uuid.UUID, running bool) {
	t.Helper()
	for _, unit := range plan.ActiveUnits() {
		for _, host := range hosts {
			host.mu.Lock()
			cfg, err := host.engine.Journal.Config()
			host.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			if cfg.ServerID != unit.Plan.ServerID {
				continue
			}
			for _, id := range unit.Plan.InstanceIDs(app) {
				v, err := host.engine.Docker.ContainerInspect(ctx, deployment.UnitName(id), client.ContainerInspectOptions{})
				if err != nil || v.Container.State.Running != running {
					t.Fatal("physical writer running state", cfg.ServerID, id, running, err)
				}
			}
		}
	}
}

func physicalRecoveryPhase(c api.ExecutionCommand) string {
	if c.Crosshost == nil {
		return ""
	}
	var body deployment.CrossHostCommand
	_ = json.Unmarshal(*c.Crosshost, &body)
	return body.Phase
}

func runPhysicalRecoveryHost(t *testing.T, ctx context.Context, service *execution.Service, identity execution.Identity, host *recoveryFixtureHost, hold func(api.ExecutionCommand) bool, acceptFailure func(api.ExecutionCommand, api.AgentReport) bool) {
	t.Helper()
	last := time.Time{}
	for ctx.Err() == nil {
		if time.Since(last) > 15*time.Second {
			heartbeat, err := host.Preflight(ctx)
			if err == nil {
				_, err = service.Heartbeat(ctx, identity, heartbeat)
			}
			if err != nil {
				if ctx.Err() == nil {
					t.Error("physical heartbeat", err)
				}
				return
			}
			last = time.Now()
		}
		poll, cancel := context.WithTimeout(ctx, time.Second)
		command, err := service.Poll(poll, identity)
		pollErr := poll.Err()
		cancel()
		if err != nil {
			if waitForFixturePollLease(ctx, err) || ctx.Err() != nil || pollErr != nil || errors.Is(err, context.Canceled) {
				continue
			}
			t.Error(err)
			return
		}
		if command == nil {
			continue
		}
		if hold(*command) {
			time.Sleep(25 * time.Millisecond)
			continue
		}
		report, err := host.Execute(ctx, *command)
		if err != nil {
			if ctx.Err() == nil {
				t.Error("physical execution", err)
			}
			return
		}
		if (report.State == "failed" || report.State == "needs_attention") && (acceptFailure == nil || !acceptFailure(*command, report)) {
			t.Error("physical execution outcome", command.Kind, physicalRecoveryPhase(*command), report.State, report.Note)
			return
		}
		if err := service.Report(ctx, identity, report); err != nil {
			if ctx.Err() == nil {
				var invalid *domain.Error
				var fields []string
				if errors.As(err, &invalid) {
					fields = invalid.Fields
				}
				t.Error("physical report", command.Kind, physicalRecoveryPhase(*command), report.State, fields, err)
			}
			return
		}
		if err := host.Acknowledge(command.Id, report.Sequence); err != nil {
			t.Error(err)
			return
		}
		host.mu.Lock()
		schedules, err := host.engine.Journal.ScheduleReports()
		host.mu.Unlock()
		if err != nil {
			t.Error(err)
			return
		}
		for _, schedule := range schedules {
			raw, _ := json.Marshal(schedule.Status)
			var status backup.Status
			if err := json.Unmarshal(raw, &status); err != nil {
				t.Error(err)
				return
			}
			if err := service.ScheduleReport(ctx, identity, schedule.Sequence, status); err != nil {
				if ctx.Err() == nil {
					t.Error("physical schedule report", err)
				}
				return
			}
		}
	}
}

func linkRecoveryFixtureHosts(t *testing.T, first, second string) {
	t.Helper()
	owner := uuid.NewString()
	a, b := "ar"+strings.ReplaceAll(owner, "-", "")[:9], "br"+strings.ReplaceAll(owner, "-", "")[:9]
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("ip", args...).CombinedOutput(); err != nil {
			t.Fatal("physical fixture link", err, string(out))
		}
	}
	run("link", "add", a, "type", "veth", "peer", "name", b)
	aliasSet := false
	t.Cleanup(func() {
		for _, ns := range []string{"", first} {
			args := []string{"-j"}
			if ns != "" {
				args = append(args, "-n", ns)
			}
			args = append(args, "link", "show", a)
			out, err := exec.Command("ip", args...).Output()
			if err != nil {
				continue
			}
			var links []struct {
				Alias string `json:"ifalias"`
				Name  string `json:"ifname"`
				Peer  string `json:"link"`
			}
			if json.Unmarshal(out, &links) != nil || len(links) != 1 || links[0].Name != a || aliasSet && links[0].Alias != owner || !aliasSet && (links[0].Peer != b || links[0].Alias != "" && links[0].Alias != owner) {
				t.Error("physical fixture link ownership changed")
				continue
			}
			args = nil
			if ns != "" {
				args = append(args, "-n", ns)
			}
			args = append(args, "link", "delete", a)
			if out, err := exec.Command("ip", args...).CombinedOutput(); err != nil {
				t.Error("physical fixture link cleanup", err, string(out))
			}
		}
	})
	run("link", "set", a, "alias", owner)
	aliasSet = true
	run("link", "set", a, "netns", first)
	run("link", "set", b, "netns", second)
	for i, host := range []struct{ ns, name string }{{first, a}, {second, b}} {
		run("-n", host.ns, "address", "add", []string{"192.0.2.1/30", "192.0.2.2/30"}[i], "dev", host.name)
		run("-n", host.ns, "link", "set", host.name, "up")
	}
}

func copyRecoveryExport(t *testing.T, source string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "altered")
	if err := os.Mkdir(dst, 0700); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		raw, err := os.ReadFile(filepath.Join(source, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, entry.Name()), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dst
}

func alterRecoveryCompletion(t *testing.T, source string, parent uuid.UUID, outcome string) string {
	t.Helper()
	dst := copyRecoveryExport(t, source)
	raw, err := os.ReadFile(filepath.Join(dst, "proofs.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var changed bool
	var result bytes.Buffer
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		var proof deployment.RecoveryProof
		if err := json.Unmarshal(line, &proof); err != nil {
			t.Fatal(err)
		}
		var report api.AgentReport
		if err := json.Unmarshal(proof.Report, &report); err != nil {
			t.Fatal(err)
		}
		if proof.ParentID == parent && report.Crosshost != nil {
			var receipt deployment.CrossHostReceipt
			if err := json.Unmarshal(*report.Crosshost, &receipt); err != nil {
				t.Fatal(err)
			}
			if receipt.Phase == "complete" {
				changed = true
				if outcome == "missing" {
					continue
				}
				report.State = api.ExecutionState(outcome)
				proof.Report, _ = json.Marshal(report)
				line, _ = json.Marshal(proof)
			}
		}
		result.Write(line)
		result.WriteByte('\n')
	}
	if !changed {
		t.Fatal("missing actual complete proof to alter")
	}
	if err := os.WriteFile(filepath.Join(dst, "proofs.jsonl"), result.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(filepath.Join(dst, "host.json"))
	if err != nil {
		t.Fatal(err)
	}
	var evidence deployment.RecoveryEvidence
	if err := json.Unmarshal(raw, &evidence); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(result.Bytes())
	evidence.ProofDigest = hex.EncodeToString(digest[:])
	raw, _ = json.Marshal(evidence)
	if err := os.WriteFile(filepath.Join(dst, "host.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	return dst
}
