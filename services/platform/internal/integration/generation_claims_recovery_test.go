package integration

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// claimRecovery is a one-unit interrupted root on server A, sealed the way management seals it,
// and an honest member B. Everything in a host export is what that host can write to it.
type claimRecovery struct {
	f             *fixture
	b             *browser
	ctx           context.Context
	ws, env, unit uuid.UUID
	network       uuid.UUID
	memberA       uuid.UUID
	a, peer       api.AgentEnrollment
	root          api.ExecutionCommand
	group, local  deployment.Plan
	unitPlan      deployment.DistributedUnit
	descriptor    deployment.RecoveryDescriptor
	envelope      []byte
	frameID       uuid.UUID
	// The peer's own unit, when the root also places the application on the peer.
	peerUnit uuid.UUID
	peerPlan deployment.DistributedUnit
	// former is the unit the root replaces, on the revoked peer, when the root is a replacement.
	former uuid.UUID
}

type claimShape int

const (
	claimOneUnit claimShape = iota
	claimWithParticipant
	claimAfterReplacement
)

const (
	claimEnvironmentGeneration = 2
	claimUpdateGeneration      = 3
)

// newClaimRecovery can take steps that add to what the sealed capsule records.
func newClaimRecovery(t *testing.T, networkGeneration int64, capture ...func(f *fixture, b *browser, ws, network uuid.UUID)) *claimRecovery {
	t.Helper()
	return buildClaimRecovery(t, networkGeneration, claimOneUnit, nil, capture...)
}

// newParticipantRecovery is a claim recovery whose root also runs the application on the peer, so
// the peer is a participant with a unit of its own.
func newParticipantRecovery(t *testing.T, capture ...func(f *fixture, b *browser, ws, network uuid.UUID)) *claimRecovery {
	t.Helper()
	return buildClaimRecovery(t, 1, claimWithParticipant, nil, capture...)
}

// newReplacementRecovery is a claim recovery whose root replaces the environment's primary on the
// peer, which the owner revoked: the peer's unit is detached, and the root deploys a new primary
// unit on A, as applying a replacement leaves things before its deployment finishes.
func newReplacementRecovery(t *testing.T) *claimRecovery {
	t.Helper()
	return buildClaimRecovery(t, 1, claimAfterReplacement, nil)
}

// newClaimRecoveryCaptured seals what snapshot returns in place of what CaptureRecovery would.
func newClaimRecoveryCaptured(t *testing.T, networkGeneration int64, snapshot func(context.Context, pgx.Tx, uuid.UUID) ([]byte, error), capture ...func(f *fixture, b *browser, ws, network uuid.UUID)) *claimRecovery {
	t.Helper()
	return buildClaimRecovery(t, networkGeneration, claimOneUnit, snapshot, capture...)
}

func buildClaimRecovery(t *testing.T, networkGeneration int64, shape claimShape, snapshot func(context.Context, pgx.Tx, uuid.UUID) ([]byte, error), capture ...func(f *fixture, b *browser, ws, network uuid.UUID)) *claimRecovery {
	t.Helper()
	participant, replacement := shape == claimWithParticipant, shape == claimAfterReplacement
	ctx := context.Background()
	f := setup(t)
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	base := "/api/v1/workspaces/" + ws.String()
	var project api.Project
	b.call("POST", base+"/projects", map[string]string{"name": "Recovered"}, 201, &project)
	var environment api.Environment
	b.call("POST", base+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "Web"}, 201, &environment)
	env := environment.Id
	yes := true
	inputs := []api.AgentEnrollment{}
	for range 2 {
		_, in, _ := enrollment(t, f, b, ws, "deployment")
		in.Capabilities.Visual, in.Capabilities.CrossHost = &yes, &yes
		if _, err := f.execution.Enroll(ctx, in); err != nil {
			t.Fatal(err)
		}
		inputs = append(inputs, in)
	}
	a, member := inputs[0], inputs[1]
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := f.st.Pool.Exec(ctx, query, args...); err != nil {
			t.Fatal(query, err)
		}
	}
	image, app := execution.TestImage, uuid.New()
	spec := domain.Specification{Version: 1, Variables: []domain.Variable{}, Applications: []domain.Application{{ID: app, Name: "Web", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Health: &domain.HealthCheck{Port: 8080, Path: "/"}}}}
	var draft api.Revision
	b.call("PUT", base+"/projects/"+project.Id.String()+"/environments/"+env.String()+"/draft", map[string]any{"baseRevisionId": environment.DraftRevisionId, "specification": spec}, 200, &draft)
	unit := claimUUID(t, f, `SELECT id FROM deployment_units WHERE workspace_id=$1 AND environment_id=$2`, ws, env)
	host := a.ServerId
	if replacement {
		host = member.ServerId
	}
	exec(`UPDATE deployment_units SET server_id=$1 WHERE id=$2`, host, unit)
	// The source plan this root replaces, already applied.
	source := deployment.Plan{ID: uuid.New(), ServerID: host, EnvironmentID: env, UnitID: unit, RevisionID: draft.Id, Recipe: deployment.Recipe, ExpiresAt: time.Now().Add(time.Hour), Specification: spec, Images: map[string]string{app.String(): image}}.WithHash()
	body := deployment.Payload{Action: "deploy", Plan: source}
	applied := api.ExecutionCommand{Id: uuid.New(), ServerId: host, EnvironmentId: env, ResourceId: unit, Generation: 1, Kind: "visual", Action: "inspect", PlanHash: body.Digest(), Deadline: time.Now().Add(time.Hour), Visual: claimMessage(t, body)}
	exec(`INSERT INTO execution_operations(id,workspace_id,server_id,environment_id,generation,request_key,command,kind,state,report_sequence) VALUES($1,$2,$3,$4,1,$5,$6,'visual','succeeded',1)`, applied.Id, ws, host, env, uuid.New(), claimBytes(t, applied))
	exec(`INSERT INTO visual_requests(id,workspace_id,server_id,environment_id,unit_id,revision_id,operation_id,candidate) VALUES($1,$2,$3,$4,$5,$6,$7,'{}')`, source.ID, ws, host, env, unit, draft.Id, applied.Id)
	exec(`INSERT INTO visual_plans(id,workspace_id,environment_id,plan) VALUES($1,$2,$3,$4)`, source.ID, ws, env, claimBytes(t, source))
	exec(`UPDATE execution_operations SET runtime_outcome='applied',runtime_plan_id=$2 WHERE id=$1`, applied.Id, source.ID)
	exec(`INSERT INTO visual_runtime(workspace_id,environment_id,unit_id,server_id,plan_id,observation) VALUES($1,$2,$3,$4,$5,'{}')`, ws, env, unit, host, source.ID)
	exec(`UPDATE environments SET applied_revision_id=$2,runtime_operation_id=$3,execution_generation=$4 WHERE id=$1`, env, draft.Id, applied.Id, claimEnvironmentGeneration)
	exec(`UPDATE servers SET update_generation=$2 WHERE id=ANY($1)`, []uuid.UUID{a.ServerId, member.ServerId}, claimUpdateGeneration)
	network, memberA, memberB := uuid.New(), uuid.New(), uuid.New()
	exec(`INSERT INTO workspace_networks(id,workspace_id,prefix,generation) VALUES($1,$2,'10.249.0.0/24',$3)`, network, ws, networkGeneration)
	for _, row := range []struct {
		server, membership uuid.UUID
		address, key       string
	}{{a.ServerId, memberA, "10.249.0.1", claimKey("a")}, {member.ServerId, memberB, "10.249.0.2", claimKey("b")}} {
		exec(`INSERT INTO network_members(workspace_id,network_id,server_id,membership_id,address,endpoint,listen_port,cpu_millis,memory_mib,unbounded_inventory_hash,public_key,enabled,approved_controller,generation) VALUES($1,$2,$3,$4,$5,'192.0.2.1:51820',51820,100,64,$6,$7,true,0,$8)`, ws, network, row.server, row.membership, row.address, strings.Repeat("a", 64), row.key, networkGeneration)
	}
	var former uuid.UUID
	if replacement {
		// The owner revoked the primary and applied a replacement onto A, which detached its unit.
		exec(`UPDATE servers SET revoked=true,credential_hash=NULL WHERE id=$1`, member.ServerId)
		exec(`UPDATE network_members SET enabled=false WHERE server_id=$1`, member.ServerId)
		former, unit = unit, uuid.New()
		exec(`UPDATE deployment_units SET primary_unit=false,server_id=NULL WHERE id=$1`, former)
		exec(`INSERT INTO deployment_units(workspace_id,id,environment_id,server_id,primary_unit) VALUES($1,$2,$3,$4,true)`, ws, unit, env, a.ServerId)
	}
	coordinated := spec
	coordinated.Volumes = []domain.Volume{{ID: uuid.New(), Name: "Data"}}
	coordinated.Routes = []domain.Route{{ID: uuid.New(), TargetID: app, Protocol: "http", Domain: "test.example.test", Port: 8080, TLS: "off"}}
	coordinated.Placement = &domain.Placement{NetworkID: network, IngressServerID: a.ServerId, Applications: []domain.ApplicationPlacement{{ApplicationID: app, CPUMillis: 100, MemoryMiB: 64, Targets: []domain.PlacementTarget{{ServerID: a.ServerId, Count: 1}}}}}
	group := deployment.Plan{ID: uuid.New(), ServerID: a.ServerId, EnvironmentID: env, UnitID: unit, RevisionID: draft.Id, Recipe: deployment.CrossHostRecipe, ExpiresAt: time.Now().Add(time.Hour), Specification: coordinated, Images: map[string]string{}}
	local := group
	local.ID = uuid.New()
	local.Images = map[string]string{app.String(): image}
	primaryInstance := uuid.New()
	local.Local = &deployment.LocalPlacement{GroupID: group.ID, Primary: true, Network: hostnetwork.Manifest{Version: 1, NetworkID: network, MembershipID: memberA, ServerID: a.ServerId, Generation: 1, Prefix: "10.249.0.0/24", Address: "10.249.0.1", PublicKey: claimKey("a"), ListenPort: 51820}, Instances: map[string][]uuid.UUID{app.String(): {primaryInstance}}, Requests: map[string]deployment.ResourceRequest{app.String(): {CPUMillis: 100, MemoryMiB: 64}}}
	var peerUnit uuid.UUID
	var peerPlan deployment.DistributedUnit
	if participant {
		peerUnit = uuid.New()
		exec(`INSERT INTO deployment_units(workspace_id,id,environment_id,server_id,primary_unit) VALUES($1,$2,$3,$4,false)`, ws, peerUnit, env, member.ServerId)
		endpoint, peerInstance := uuid.New(), uuid.New()
		coordinated.Applications = []domain.Application{coordinated.Applications[0]}
		coordinated.Applications[0].Replicas = 2
		coordinated.Placement.Applications[0].Targets = []domain.PlacementTarget{{ServerID: a.ServerId, Count: 1}, {ServerID: member.ServerId, Count: 1}}
		group.Specification, local.Specification = coordinated, coordinated
		peerManifest := hostnetwork.Manifest{Version: 1, NetworkID: network, MembershipID: memberB, ServerID: member.ServerId, Generation: 1, Prefix: "10.249.0.0/24", Address: "10.249.0.2", PublicKey: claimKey("b"), ListenPort: 51820,
			Peers:   []hostnetwork.Peer{{ServerID: a.ServerId, MembershipID: memberA, Address: "10.249.0.1", PublicKey: claimKey("a"), Endpoint: "192.0.2.1:51820"}},
			Exports: []hostnetwork.Endpoint{{ID: endpoint, UnitID: peerUnit, ResourceID: app, InstanceID: peerInstance, Port: 20001, ContainerPort: 8080, Sources: []hostnetwork.Source{{ServerID: a.ServerId, UnitID: unit}}}}}
		local.Local.Network.Peers = []hostnetwork.Peer{{ServerID: member.ServerId, MembershipID: memberB, Address: "10.249.0.2", PublicKey: claimKey("b"), Endpoint: "192.0.2.1:51820"}}
		local.Local.Network.Egress = []hostnetwork.Egress{{Ingress: true, UnitID: unit, ServerID: member.ServerId, EndpointID: endpoint, Port: 20001}}
		local.Local.Backends = map[string][]deployment.PrivateEndpoint{app.String(): {
			{ServerID: a.ServerId, UnitID: unit, ResourceID: app, InstanceID: primaryInstance, Address: deployment.ServiceName(primaryInstance), Port: 8080, ContainerPort: 8080},
			{ServerID: member.ServerId, UnitID: peerUnit, ResourceID: app, InstanceID: peerInstance, Address: "10.249.0.2", Port: 20001, ContainerPort: 8080},
		}}
		peer := group
		peer.ID, peer.ServerID, peer.UnitID = uuid.New(), member.ServerId, peerUnit
		peer.Images = map[string]string{app.String(): image}
		peer.Local = &deployment.LocalPlacement{GroupID: group.ID, Network: peerManifest, Instances: map[string][]uuid.UUID{app.String(): {peerInstance}}, Requests: map[string]deployment.ResourceRequest{app.String(): {CPUMillis: 100, MemoryMiB: 64}}}
		peerPlan = deployment.DistributedUnit{Plan: peer.WithHash(), CPUMillis: 100, MemoryMiB: 64, InventoryHash: strings.Repeat("a", 64)}
	}
	local = local.WithHash()
	unitPlan := deployment.DistributedUnit{Plan: local, CPUMillis: 100, MemoryMiB: 64, InventoryHash: strings.Repeat("a", 64)}
	group.Distributed = &deployment.DistributedPlan{NetworkID: network, NetworkGeneration: 1, Units: []deployment.DistributedUnit{unitPlan}}
	if replacement {
		group.Replacement = &deployment.Replacement{Server: member.ServerId, AbandonedVolumes: []uuid.UUID{}}
		group.Distributed.Abandoned = []uuid.UUID{member.ServerId}
	}
	participants := []uuid.UUID{a.ServerId}
	if participant {
		group.Distributed.Units = append(group.Distributed.Units, peerPlan)
		participants = append(participants, member.ServerId)
		slices.SortFunc(participants, func(x, y uuid.UUID) int { return strings.Compare(x.String(), y.String()) })
	}
	group = group.WithHash()
	if err := group.Validate(true); err != nil {
		t.Fatal(err)
	}
	rootBody := struct {
		Action string          `json:"action"`
		Plan   deployment.Plan `json:"plan"`
	}{"deploy", group}
	root := api.ExecutionCommand{Id: uuid.New(), ServerId: a.ServerId, EnvironmentId: env, ResourceId: unit, Generation: claimEnvironmentGeneration, Kind: "crosshost", Action: "inspect", PlanHash: execution.Hash(rootBody), Deadline: time.Now().Add(time.Hour), Crosshost: claimMessage(t, rootBody)}
	exec(`INSERT INTO execution_operations(id,workspace_id,server_id,environment_id,generation,request_key,command,kind,state) VALUES($1,$2,$3,$4,$5,$6,$7,'crosshost','running')`, root.Id, ws, a.ServerId, env, root.Generation, uuid.New(), claimBytes(t, root))
	exec(`INSERT INTO visual_requests(id,workspace_id,server_id,environment_id,unit_id,revision_id,operation_id,candidate) VALUES($1,$2,$3,$4,$5,$6,$7,'{}')`, group.ID, ws, a.ServerId, env, unit, draft.Id, root.Id)
	exec(`INSERT INTO cross_host_operations(id,workspace_id,environment_id,plan_id,phase) VALUES($1,$2,$3,$4,'checkpoint')`, root.Id, ws, env, group.ID)
	for _, step := range capture {
		step(f, b, ws, network)
	}
	// Sealed as createRecoveryBase seals it: the keyring refuses a capsule under any other identity.
	id := uuid.New()
	descriptor := deployment.RecoveryDescriptor{Version: 1, WorkspaceID: ws, ParentID: root.Id, RootHash: root.PlanHash, GroupID: group.ID, GroupHash: group.Hash, BaseID: id, CheckpointID: id, Participants: participants}
	tx, err := f.st.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if snapshot == nil {
		snapshot = portability.CaptureRecovery
	}
	captured, err := snapshot(ctx, tx, ws)
	if err != nil {
		t.Fatal(err)
	}
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err = writer.Write(captured); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	envelope, err := f.st.Keys.Seal(compressed.Bytes(), fmt.Sprintf("cross-host-recovery:v1:%s:%s:%s:%d:%s", ws, root.Id, id, 0, root.PlanHash))
	if err != nil {
		t.Fatal(err)
	}
	descriptor.Digest, descriptor.Bytes = deployment.RecoveryDigest(envelope), len(envelope)
	if err = descriptor.Validate(); err != nil {
		t.Fatal(err)
	}
	if err = db.New(tx).InsertRecoveryBaseCheckpoint(ctx, db.InsertRecoveryBaseCheckpointParams{ID: id, WorkspaceID: ws, ParentID: root.Id, Descriptor: claimBytes(t, descriptor), Envelope: envelope}); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE execution_operations SET command=jsonb_set(command,'{recoveryCheckpoint}',$2::jsonb,true) WHERE id=$1`, root.Id, claimBytes(t, descriptor)); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = f.execution.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	return &claimRecovery{f: f, b: b, ctx: ctx, ws: ws, env: env, unit: unit, network: network, memberA: memberA, a: a, peer: member, root: root, group: group, local: local, unitPlan: unitPlan, descriptor: descriptor, envelope: envelope, frameID: uuid.NewSHA1(root.Id, []byte("crosshost:checkpoint:"+unit.String())), peerUnit: peerUnit, peerPlan: peerPlan, former: former}
}

func claimUUID(t *testing.T, f *fixture, query string, args ...any) uuid.UUID {
	t.Helper()
	var value uuid.UUID
	if err := f.st.Pool.QueryRow(context.Background(), query, args...).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

// claimExport says what server A's export claims. Zero values leave it as an honest host wrote it.
type claimExport struct {
	network, frame, standalone, receipt, controller, sequence int64
	retained                                                  []claimProof
}

// claimProof is an operation the host's journal still holds.
type claimProof struct {
	header api.ExecutionCommand
	report api.AgentReport
}

func (r *claimRecovery) exports(t *testing.T, claims claimExport) []string {
	t.Helper()
	proofs := []byte{}
	line := func(parent, id uuid.UUID, header api.ExecutionCommand, report api.AgentReport) {
		proofs = append(append(proofs, claimBytes(t, deployment.RecoveryProof{ParentID: parent, ID: id, Header: claimBytes(t, header), Report: claimBytes(t, report), Checkpoint: "accepted"})...), '\n')
	}
	if claims.frame != 0 || claims.sequence != 0 {
		frame, sequence := int64(claimEnvironmentGeneration+1), int64(1)
		if claims.frame != 0 {
			frame = claims.frame
		}
		if claims.sequence != 0 {
			sequence = claims.sequence
		}
		body := deployment.CrossHostCommand{ParentID: r.root.Id, GroupHash: r.group.Hash, Phase: "checkpoint", Unit: r.unitPlan}
		header := api.ExecutionCommand{Id: r.frameID, ParentId: &r.root.Id, ServerId: r.a.ServerId, EnvironmentId: r.env, ResourceId: r.unit, Kind: "crosshost", Action: "inspect", Generation: frame, PlanHash: body.Digest(), Deadline: time.Now().Add(time.Hour), RecoveryCheckpoint: claimMessage(t, r.descriptor)}
		line(r.root.Id, r.frameID, header, api.AgentReport{Id: r.frameID, Generation: frame, PlanHash: body.Digest(), Sequence: sequence, State: "running", Steps: []api.ExecutionStep{}})
	}
	if claims.standalone != 0 {
		removed := hostnetwork.Manifest{Version: 1, NetworkID: r.network, MembershipID: r.memberA, ServerID: r.a.ServerId, Generation: 3, Prefix: "10.249.0.0/24", Address: "10.249.0.1", PublicKey: claimKey("a"), ListenPort: 51820}
		baseline := removed
		baseline.Generation = 2
		restrict := hostnetwork.Command{Action: "restrict", NetworkID: r.network, MembershipID: r.memberA, Manifest: &removed, Baseline: &baseline}
		header := api.ExecutionCommand{Id: uuid.New(), ServerId: r.a.ServerId, ResourceId: r.network, Kind: "network", Action: "inspect", Generation: claims.standalone, PlanHash: hostnetwork.Hash(restrict), Deadline: time.Now().Add(time.Hour), Network: claimMessage(t, restrict)}
		line(uuid.Nil, header.Id, header, api.AgentReport{Id: header.Id, Generation: claims.standalone, PlanHash: header.PlanHash, Sequence: 1, State: "failed", Steps: []api.ExecutionStep{}})
	}
	if claims.receipt != 0 {
		removed := hostnetwork.Manifest{Version: 1, NetworkID: r.network, MembershipID: r.memberA, ServerID: r.a.ServerId, Generation: claims.receipt, Prefix: "10.249.0.0/24", Address: "10.249.0.1", PublicKey: claimKey("a"), ListenPort: 51820}
		baseline := removed
		baseline.Generation--
		restrict := hostnetwork.Command{Action: "restrict", NetworkID: r.network, MembershipID: r.memberA, Manifest: &removed, Baseline: &baseline}
		header := api.ExecutionCommand{Id: uuid.New(), ServerId: r.a.ServerId, ResourceId: r.network, Kind: "network", Action: "inspect", Generation: claimUpdateGeneration + 1, PlanHash: hostnetwork.Hash(restrict), Deadline: time.Now().Add(time.Hour), Network: claimMessage(t, restrict)}
		receipt := hostnetwork.Receipt{Identity: hostnetwork.Identity{MembershipID: r.memberA, PublicKey: claimKey("a")}, NetworkID: r.network, Generation: removed.Generation, Hash: hostnetwork.Hash(removed)}
		line(uuid.Nil, header.Id, header, api.AgentReport{Id: header.Id, Generation: header.Generation, PlanHash: header.PlanHash, Sequence: 1, State: "succeeded", Steps: []api.ExecutionStep{}, Network: claimMessage(t, receipt)})
	}
	for _, proof := range claims.retained {
		line(uuid.Nil, proof.header.Id, proof.header, proof.report)
	}
	networks := []hostnetwork.Manifest{}
	if claims.network != 0 {
		networks = append(networks, hostnetwork.Manifest{Version: 1, NetworkID: r.network, MembershipID: r.memberA, ServerID: r.a.ServerId, Generation: claims.network, Prefix: "10.249.0.0/24", Address: "10.249.0.1", PublicKey: claimKey("a"), ListenPort: 51820})
	}
	work := t.TempDir()
	write := func(dir, name string, body []byte) {
		t.Helper()
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	dirA, dirB := filepath.Join(work, "a"), filepath.Join(work, "b")
	sumA, sumB := sha256.Sum256(proofs), sha256.Sum256(nil)
	write(dirA, "host.json", claimBytes(t, deployment.RecoveryEvidence{Version: 1, ServerID: r.a.ServerId, MachineID: r.a.MachineId, Controller: claims.controller, Units: map[string]string{r.unit.String(): r.local.Hash}, Schedules: map[string]int64{}, Roots: []deployment.RecoveryRoot{{Descriptor: r.descriptor, EnvironmentID: r.env, UnitID: r.unit, Primary: true, UnitHash: r.local.Hash}}, ProofDigest: hex.EncodeToString(sumA[:]), Networks: networks}))
	write(dirA, "proofs.jsonl", proofs)
	write(dirA, r.descriptor.CheckpointID.String()+".capsule", r.envelope)
	write(dirA, r.descriptor.CheckpointID.String()+".json", claimBytes(t, r.descriptor))
	peerUnits, peerRoots := map[string]string{}, []deployment.RecoveryRoot{}
	if r.peerUnit != uuid.Nil {
		peerUnits[r.peerUnit.String()] = r.peerPlan.Plan.Hash
		peerRoots = append(peerRoots, deployment.RecoveryRoot{Descriptor: r.descriptor, EnvironmentID: r.env, UnitID: r.peerUnit, UnitHash: r.peerPlan.Plan.Hash})
	}
	write(dirB, "host.json", claimBytes(t, deployment.RecoveryEvidence{Version: 1, ServerID: r.peer.ServerId, MachineID: r.peer.MachineId, Units: peerUnits, Schedules: map[string]int64{}, Roots: peerRoots, ProofDigest: hex.EncodeToString(sumB[:]), Networks: []hostnetwork.Manifest{}}))
	write(dirB, "proofs.jsonl", nil)
	return []string{dirA, dirB}
}

func (r *claimRecovery) counters(t *testing.T) [4]int64 {
	t.Helper()
	return [4]int64{
		claimCounter(t, r.f, `SELECT generation FROM workspace_networks WHERE workspace_id=$1`, r.ws),
		claimCounter(t, r.f, `SELECT execution_generation FROM environments WHERE id=$1`, r.env),
		claimCounter(t, r.f, `SELECT update_generation FROM servers WHERE id=$1`, r.a.ServerId),
		claimCounter(t, r.f, `SELECT controller_generation FROM servers WHERE id=$1`, r.a.ServerId),
	}
}

func (r *claimRecovery) recoverAll(t *testing.T, exports []string) {
	t.Helper()
	in := execution.RecoveryImport{WorkspaceID: r.ws, OwnerID: r.f.owner, HostExports: exports}
	preview, err := r.f.execution.RecoverCrossHost(r.ctx, in)
	if err != nil {
		t.Fatal("preview: ", err)
	}
	in.ExpectedState = preview.StateHash
	applied, err := r.f.execution.RecoverCrossHost(r.ctx, in)
	if err != nil {
		t.Fatal("apply: ", err)
	}
	if !applied.Applied {
		t.Fatal("recovery did not apply")
	}
}

// The documented recovery has every participant re-enroll, so what must still work is revoking
// the host after that, with whatever the recovery left the counters holding.
func (r *claimRecovery) revokeAfterTransfer(t *testing.T) {
	t.Helper()
	credential := r.transfer(t, r.a)
	r.b.call("POST", "/api/v1/workspaces/"+r.ws.String()+"/servers/"+r.a.ServerId.String()+"/revoke", nil, 204, nil)
	if _, err := r.f.execution.Authenticate(r.ctx, r.a.ServerId, credential); err == nil {
		t.Fatal("the revoked credential still authenticates")
	}
}

// transfer re-enrolls a recovered host under the controller recovery staged for it, with the
// inventory it staged, as the documented recovery has every participant do. It returns the new
// credential.
func (r *claimRecovery) transfer(t *testing.T, host api.AgentEnrollment) string {
	t.Helper()
	var ticket api.TransferTicket
	r.b.call("POST", "/api/v1/workspaces/"+r.ws.String()+"/servers/"+host.ServerId.String()+"/transfer", nil, 201, &ticket)
	var staged []byte
	if err := r.f.st.Pool.QueryRow(r.ctx, `SELECT inventory FROM cross_host_recovery_servers WHERE server_id=$1`, host.ServerId).Scan(&staged); err != nil {
		t.Fatal(err)
	}
	var inventory api.ControllerInventory
	if err := json.Unmarshal(staged, &inventory); err != nil {
		t.Fatal(err)
	}
	credential, err := cryptobox.Token()
	if err != nil {
		t.Fatal(err)
	}
	generation := int64(ticket.Generation)
	input := host
	input.Token, input.Credential, input.ControllerGeneration, input.Inventory = ticket.Token, credential, &generation, &inventory
	if _, err = r.f.execution.Enroll(r.ctx, input); err != nil {
		t.Fatal(err)
	}
	if _, err = r.f.execution.Authenticate(r.ctx, host.ServerId, credential); err != nil {
		t.Fatal(err)
	}
	return credential
}

func TestRecoveryRefusesGenerationsNeverIssued(t *testing.T) {
	const issued = int64(1)
	for name, test := range map[string]struct {
		claims claimExport
		want   string
	}{
		"manifest at the bigint limit":     {claimExport{network: math.MaxInt64 - 1}, "claims a network generation"},
		"manifest past the allowance":      {claimExport{network: issued + exportAllowance + 1}, "claims a network generation"},
		"metadata frame":                   {claimExport{frame: math.MaxInt64}, "claims an environment generation"},
		"metadata frame at import's limit": {claimExport{frame: portability.MaxCounter}, "claims an environment generation"},
		"receipt in a proof":               {claimExport{receipt: issued + exportAllowance + 1}, "claims a network generation"},
		"standalone frame":                 {claimExport{standalone: math.MaxInt64}, "claims an update generation"},
		"controller":                       {claimExport{controller: math.MaxInt64 - 1}, "claims a controller generation"},
		"report sequence":                  {claimExport{sequence: portability.MaxCounter}, "host proof identity"},
	} {
		t.Run(name, func(t *testing.T) {
			r := newClaimRecovery(t, issued)
			before := r.counters(t)
			exports := r.exports(t, test.claims)
			in := execution.RecoveryImport{WorkspaceID: r.ws, OwnerID: r.f.owner, HostExports: exports}
			preview, err := r.f.execution.RecoverCrossHost(r.ctx, in)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("preview error = %v, want %q", err, test.want)
			}
			if test.want != "host proof identity" && !strings.Contains(err.Error(), r.a.ServerId.String()) {
				t.Fatalf("the error does not name the host: %v", err)
			}
			in.ExpectedState = preview.StateHash
			if _, err = r.f.execution.RecoverCrossHost(r.ctx, in); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("apply error = %v, want %q", err, test.want)
			}
			if after := r.counters(t); after != before {
				t.Fatalf("counters moved from %v to %v", before, after)
			}
		})
	}
}

// Recovery exists for a management database that is behind its hosts, so hosts ahead of every
// counter within the allowance must keep working, and so must a capsule that is itself past the
// limit an ordinary import accepts: it is management's own record.
func TestRecoveryKeepsRestoredGenerationHistory(t *testing.T) {
	t.Run("hosts ahead of the restored counters", func(t *testing.T) {
		r := newClaimRecovery(t, 7)
		network, frame, standalone := int64(7+exportAllowance), int64(claimEnvironmentGeneration+500), int64(claimUpdateGeneration+500)
		r.recoverAll(t, r.exports(t, claimExport{network: network, frame: frame, standalone: standalone, controller: 4}))
		if got, want := r.counters(t), [4]int64{network + 1, frame, standalone, 5}; got != want {
			t.Fatalf("counters = %v, want %v", got, want)
		}
		r.revokeAfterTransfer(t)
	})
	t.Run("receipt in a proof ahead of the restored counter", func(t *testing.T) {
		r := newClaimRecovery(t, 7)
		claimed := int64(7 + receiptMargin + 1000)
		r.recoverAll(t, r.exports(t, claimExport{receipt: claimed}))
		if got := r.counters(t)[0]; got <= claimed {
			t.Fatalf("network counter = %d, want it past the receipt at %d", got, claimed)
		}
	})
	for name, sealed := range map[string]int64{"sealed above the import limit": 1 << 60, "sealed at the bigint limit": math.MaxInt64} {
		t.Run(name, func(t *testing.T) {
			r := newClaimRecovery(t, sealed)
			r.recoverAll(t, r.exports(t, claimExport{network: sealed}))
			// Restoring the root's grants and adopting the manifest each advance the counter.
			if got := r.counters(t)[0]; got < sealed || sealed != math.MaxInt64 && got > sealed+2 {
				t.Fatalf("network counter = %d after sealing %d", got, sealed)
			}
			r.revokeAfterTransfer(t)
		})
	}
}

// The host's journal holds what it reported, so what management recorded for a claim it could not
// account for must not contradict that when recovery reads the host's export.
func TestUnaccountedReceiptDoesNotConflictWithItsHostsExport(t *testing.T) {
	r := newClaimRecovery(t, 7)
	identity, err := r.f.execution.Authenticate(r.ctx, r.a.ServerId, r.a.Credential)
	if err != nil {
		t.Fatal(err)
	}
	manifest := func(generation int64) hostnetwork.Manifest {
		return hostnetwork.Manifest{Version: 1, NetworkID: r.network, MembershipID: r.memberA, ServerID: r.a.ServerId, Generation: generation, Prefix: "10.249.0.0/24", Address: "10.249.0.1", PublicKey: claimKey("a"), ListenPort: 51820}
	}
	baseline, wanted := manifest(6), manifest(7)
	restrict := hostnetwork.Command{Action: "restrict", NetworkID: r.network, MembershipID: r.memberA, Baseline: &baseline, Manifest: &wanted}
	header := api.ExecutionCommand{Id: uuid.New(), ServerId: r.a.ServerId, ResourceId: r.network, Kind: "network", Action: "inspect", Generation: claimUpdateGeneration + 1, PlanHash: hostnetwork.Hash(restrict), Deadline: time.Now().Add(time.Hour), Network: claimMessage(t, restrict)}
	if _, err = r.f.st.Pool.Exec(r.ctx, `INSERT INTO execution_operations(id,workspace_id,server_id,generation,request_key,command,kind,state) VALUES($1,$2,$3,$4,$5,$6,'network','running')`, header.Id, r.ws, header.ServerId, header.Generation, uuid.New(), claimBytes(t, header)); err != nil {
		t.Fatal(err)
	}
	claimed := manifest(7 + receiptMargin + 1)
	receipt := hostnetwork.Receipt{Identity: hostnetwork.Identity{MembershipID: r.memberA, PublicKey: claimKey("a")}, NetworkID: r.network, Before: &claimed, Effective: &claimed, Generation: claimed.Generation, Hash: hostnetwork.Hash(claimed)}
	report := api.AgentReport{Id: header.Id, Generation: header.Generation, PlanHash: header.PlanHash, Sequence: 1, State: "succeeded", Steps: []api.ExecutionStep{}, Network: claimMessage(t, receipt)}
	if err = r.f.execution.Report(r.ctx, identity, report); err != nil {
		t.Fatal(err)
	}
	if got := r.counters(t)[0]; got != 7 {
		t.Fatalf("the unaccounted claim moved the counter to %d", got)
	}
	r.recoverAll(t, r.exports(t, claimExport{retained: []claimProof{{header, report}}}))
}
