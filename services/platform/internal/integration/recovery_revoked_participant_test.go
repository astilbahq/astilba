package integration

import (
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

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/google/uuid"
)

// revokePeer revokes the root's second participant, as the owner does in the restored database.
func (r *claimRecovery) revokePeer(t *testing.T) {
	t.Helper()
	r.revokeThird(t, r.peer)
}

// An interrupted root recovers without a participant the owner revoked: it needs nothing from that
// host, hands it nothing, and the work it was given ends as revocation ends it.
func TestRecoveryContinuesWithoutARevokedSecondary(t *testing.T) {
	t.Run("both participants answer", func(t *testing.T) {
		r := newParticipantRecovery(t)
		r.recoverAll(t, r.exports(t, claimExport{}))
		if count := claimCounter(t, r.f, `SELECT count(*) FROM cross_host_recovery_servers WHERE server_id=ANY($1)`, []any{r.a.ServerId, r.peer.ServerId}); count != 2 {
			t.Fatalf("%d participants staged for handover, want 2", count)
		}
	})
	t.Run("the secondary is revoked", func(t *testing.T) {
		r := newParticipantRecovery(t)
		r.revokePeer(t)
		exports := r.exports(t, claimExport{})
		in := execution.RecoveryImport{WorkspaceID: r.ws, OwnerID: r.f.owner, HostExports: exports[:1]}
		preview, err := r.f.execution.RecoverCrossHost(r.ctx, in)
		if err != nil {
			t.Fatal("preview without the revoked participant's export: ", err)
		}
		if _, handed := preview.Handovers[r.peer.ServerId]; handed {
			t.Fatal("recovery hands authority to a revoked participant")
		}
		in.ExpectedState = preview.StateHash
		for attempt := range 2 {
			applied, err := r.f.execution.RecoverCrossHost(r.ctx, in)
			if err != nil || !applied.Applied || applied.AlreadyApplied != (attempt == 1) {
				t.Fatalf("apply %d = %+v, %v", attempt, applied, err)
			}
		}
		// Lifting the revocation now would leave the root waiting on a host it handed nothing.
		r.b.call("POST", "/api/v1/workspaces/"+r.ws.String()+"/servers/"+r.peer.ServerId.String()+"/transfer", nil, 409, nil)
		if count := claimCounter(t, r.f, `SELECT count(*) FROM cross_host_recovery_servers WHERE server_id=$1`, r.peer.ServerId); count != 0 {
			t.Fatal("recovery staged the revoked participant")
		}
		if count := claimCounter(t, r.f, `SELECT count(*) FROM execution_operations WHERE parent_id=$1 AND server_id=$2 AND state NOT IN ('succeeded','failed','cancelled')`, r.root.Id, r.peer.ServerId); count != 0 {
			t.Fatalf("%d of the revoked participant's frames still wait on it", count)
		}
		// Once the remaining participant proves itself, the root opens without the revoked one.
		r.transfer(t, r.a)
		r.waitForRecoveryOpen(t, r.a.ServerId)
		if count := claimCounter(t, r.f, `SELECT count(*) FROM execution_operations WHERE parent_id=$1 AND server_id=$2 AND command->'crosshost'->>'phase'='recovery-open'`, r.root.Id, r.peer.ServerId); count != 0 {
			t.Fatal("the recovered root asked the revoked participant to open")
		}
	})
}

// waitForRecoveryOpen runs management until the recovered root asks server to open.
func (r *claimRecovery) waitForRecoveryOpen(t *testing.T, server uuid.UUID) {
	t.Helper()
	if err := r.f.execution.Start(r.ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.f.execution.Stop(r.ctx) })
	deadline := time.Now().Add(30 * time.Second)
	for claimCounter(t, r.f, `SELECT count(*) FROM execution_operations WHERE parent_id=$1 AND server_id=$2 AND command->'crosshost'->>'phase'='recovery-open'`, r.root.Id, server) == 0 {
		if time.Now().After(deadline) {
			var note string
			_ = r.f.st.Pool.QueryRow(r.ctx, `SELECT COALESCE(note,'') FROM execution_operations WHERE id=$1`, r.root.Id).Scan(&note)
			t.Fatalf("the recovered root never asked %s to open: %s", server, note)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// A revoked participant's export can't steer recovery or stop it: a success it claims for its own
// frame is not taken, and a network generation it claims beyond anything issued refuses nothing.
func TestRecoveryIgnoresWhatARevokedParticipantClaims(t *testing.T) {
	r := newParticipantRecovery(t)
	r.revokePeer(t)
	exports := r.exports(t, claimExport{})
	frame := uuid.NewSHA1(r.root.Id, []byte("crosshost:checkpoint:"+r.peerUnit.String()))
	body := deployment.CrossHostCommand{ParentID: r.root.Id, GroupHash: r.group.Hash, Phase: "checkpoint", Unit: r.peerPlan}
	header := api.ExecutionCommand{Id: frame, ParentId: &r.root.Id, ServerId: r.peer.ServerId, EnvironmentId: r.env, ResourceId: r.peerUnit, Kind: "crosshost", Action: "inspect", Generation: claimEnvironmentGeneration + 1, PlanHash: body.Digest(), Deadline: time.Now().Add(time.Hour), RecoveryCheckpoint: claimMessage(t, r.descriptor)}
	report := api.AgentReport{Id: frame, Generation: header.Generation, PlanHash: header.PlanHash, Sequence: 2, State: "succeeded", Steps: []api.ExecutionStep{}}
	proofs := append(claimBytes(t, deployment.RecoveryProof{ParentID: r.root.Id, ID: frame, Header: claimBytes(t, header), Report: claimBytes(t, report), Checkpoint: "accepted"}), '\n')
	digest := sha256.Sum256(proofs)
	claimed := hostnetwork.Manifest{Version: 1, NetworkID: r.network, MembershipID: uuid.New(), ServerID: r.peer.ServerId, Generation: math.MaxInt64 - 1, Prefix: "10.249.0.0/24", Address: "10.249.0.2", PublicKey: claimKey("b"), ListenPort: 51820}
	evidence := deployment.RecoveryEvidence{Version: 1, ServerID: r.peer.ServerId, MachineID: r.peer.MachineId, Controller: math.MaxInt64 - 1, Units: map[string]string{r.peerUnit.String(): r.peerPlan.Plan.Hash}, Schedules: map[string]int64{}, Roots: []deployment.RecoveryRoot{{Descriptor: r.descriptor, EnvironmentID: r.env, UnitID: r.peerUnit, UnitHash: r.peerPlan.Plan.Hash}}, ProofDigest: hex.EncodeToString(digest[:]), Networks: []hostnetwork.Manifest{claimed}}
	for name, raw := range map[string][]byte{"host.json": claimBytes(t, evidence), "proofs.jsonl": proofs} {
		if err := os.WriteFile(filepath.Join(exports[1], name), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	r.recoverAll(t, exports)
	if count := claimCounter(t, r.f, `SELECT count(*) FROM execution_operations WHERE id=$1 AND state='succeeded'`, frame); count != 0 {
		t.Fatal("recovery took the revoked participant's word that its frame succeeded")
	}
	if count := claimCounter(t, r.f, `SELECT count(*) FROM cross_host_recovery_servers WHERE server_id=$1`, r.peer.ServerId); count != 0 {
		t.Fatal("recovery staged the revoked participant from its own export")
	}
}

// A revoked host's optional export may still list what its revocation released, such as previews,
// which management no longer expects. That decides nothing, so it doesn't stop recovery.
func TestRecoveryIgnoresWhatARevokedHostStillRuns(t *testing.T) {
	r, third := revokedRecovery(t)
	r.revokeThird(t, third)
	dir := thirdExport(t, third, third.MachineId, nil)
	digest := sha256.Sum256(nil)
	evidence := deployment.RecoveryEvidence{Version: 1, ServerID: third.ServerId, MachineID: third.MachineId, Units: map[string]string{uuid.NewString(): strings.Repeat("e", 64)}, Schedules: map[string]int64{}, Roots: []deployment.RecoveryRoot{}, ProofDigest: hex.EncodeToString(digest[:])}
	if err := os.WriteFile(filepath.Join(dir, "host.json"), claimBytes(t, evidence), 0o600); err != nil {
		t.Fatal(err)
	}
	r.recoverAll(t, append(r.exports(t, claimExport{}), dir))
}

// An interrupted root whose primary the owner revoked comes back from the checkpoints management
// sealed for it, since that host can't be asked for its chain, and opens for the remaining
// participant alone, which then ends it as revoking a primary does.
func TestRecoveryRestoresARevokedPrimarysRootFromManagement(t *testing.T) {
	r := newParticipantRecovery(t)
	r.revokeThird(t, r.a)
	exports := r.exports(t, claimExport{})
	// The revoked primary's export decides nothing, even when it is malformed, and management's own
	// chain is used ahead of its capsule, which here no longer opens.
	if err := os.WriteFile(filepath.Join(exports[0], "host.json"), []byte(`{"version":9,"serverId":"`+r.a.ServerId.String()+`","machineId":"`+r.a.MachineId.String()+`","proofDigest":"short"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(exports[0], r.descriptor.CheckpointID.String()+".capsule"), []byte("replayed"), 0o600); err != nil {
		t.Fatal(err)
	}
	in := execution.RecoveryImport{WorkspaceID: r.ws, OwnerID: r.f.owner, HostExports: exports}
	preview, err := r.f.execution.RecoverCrossHost(r.ctx, in)
	if err != nil {
		t.Fatal("preview without the revoked primary's export: ", err)
	}
	if _, handed := preview.Handovers[r.a.ServerId]; handed || len(preview.Handovers) != 1 {
		t.Fatalf("handovers %v, want only the remaining participant", preview.Handovers)
	}
	in.ExpectedState = preview.StateHash
	for attempt := range 2 {
		applied, err := r.f.execution.RecoverCrossHost(r.ctx, in)
		if err != nil || !applied.Applied || applied.AlreadyApplied != (attempt == 1) {
			t.Fatalf("apply %d = %+v, %v", attempt, applied, err)
		}
	}
	if count := claimCounter(t, r.f, `SELECT count(*) FROM execution_operations WHERE parent_id=$1 AND server_id=$2 AND state NOT IN ('succeeded','failed','cancelled')`, r.root.Id, r.a.ServerId); count != 0 {
		t.Fatalf("%d of the revoked primary's frames still wait on it", count)
	}
	r.transfer(t, r.peer)
	r.waitForRecoveryOpen(t, r.peer.ServerId)
	if count := claimCounter(t, r.f, `SELECT count(*) FROM execution_operations WHERE parent_id=$1 AND server_id=$2 AND command->'crosshost'->>'phase'='recovery-open'`, r.root.Id, r.a.ServerId); count != 0 {
		t.Fatal("the recovered root asked the revoked primary to open")
	}
}

// forgetCheckpoints leaves the restored management without the root's sealed checkpoints, as a
// backup taken before the root started has none.
func (r *claimRecovery) forgetCheckpoints(t *testing.T) {
	t.Helper()
	for _, statement := range []string{
		`DELETE FROM cross_host_checkpoints WHERE parent_id=$1`,
		`UPDATE execution_operations SET command=command-'recoveryCheckpoint' WHERE id=$1`,
	} {
		if _, err := r.f.st.Pool.Exec(r.ctx, statement, r.root.Id); err != nil {
			t.Fatal(statement, err)
		}
	}
}

// When management kept none of a root's checkpoints, a revoked primary's sealed capsules are the only
// copy. The host can only replay them, so they bring the root back.
func TestRecoveryUsesARevokedPrimarysCapsulesManagementLacks(t *testing.T) {
	r := newParticipantRecovery(t)
	r.revokeThird(t, r.a)
	r.forgetCheckpoints(t)
	r.recoverAll(t, r.exports(t, claimExport{}))
	if count := claimCounter(t, r.f, `SELECT count(*) FROM execution_operations WHERE parent_id=$1 AND server_id=$2 AND state NOT IN ('succeeded','failed','cancelled')`, r.root.Id, r.a.ServerId); count != 0 {
		t.Fatalf("%d of the revoked primary's frames still wait on it", count)
	}
	r.transfer(t, r.peer)
	r.waitForRecoveryOpen(t, r.peer.ServerId)
	if count := claimCounter(t, r.f, `SELECT count(*) FROM execution_operations WHERE parent_id=$1 AND server_id=$2 AND command->'crosshost'->>'phase'='recovery-open'`, r.root.Id, r.a.ServerId); count != 0 {
		t.Fatal("the recovered root asked the revoked primary to open")
	}
}

// An interrupted root whose revoked primary is newer than anything management kept, and whose
// capsules nobody supplies, can't be brought back, so recovery says why and what to do rather than
// fail on a missing capsule.
func TestRecoveryExplainsALostRevokedPrimaryRoot(t *testing.T) {
	r := newParticipantRecovery(t)
	r.revokeThird(t, r.a)
	r.forgetCheckpoints(t)
	peer := r.exports(t, claimExport{})[1]
	var evidence deployment.RecoveryEvidence
	raw, err := os.ReadFile(filepath.Join(peer, "host.json"))
	if err != nil || json.Unmarshal(raw, &evidence) != nil {
		t.Fatal("peer export", err)
	}
	// The peer is still reserved for the root, which is what leaves it stuck.
	evidence.ActiveParent = r.root.Id
	if err = os.WriteFile(filepath.Join(peer, "host.json"), claimBytes(t, evidence), 0o600); err != nil {
		t.Fatal(err)
	}
	in := execution.RecoveryImport{WorkspaceID: r.ws, OwnerID: r.f.owner, HostExports: []string{peer}}
	if _, err := r.f.execution.RecoverCrossHost(r.ctx, in); err == nil || !strings.Contains(err.Error(), "Revoke those servers too") {
		t.Fatalf("recovery of a root nothing holds = %v", err)
	}
}

// An interrupted replacement recovers from the new primary alone: the revoked former primary is no
// participant and is asked nothing, its unit stays detached even though the checkpoint holds the
// plan it last applied, and the new unit keeps the primary role.
func TestRecoveryResumesAnInterruptedReplacement(t *testing.T) {
	r := newReplacementRecovery(t)
	r.recoverAll(t, r.exports(t, claimExport{})[:1])
	var formerServer *uuid.UUID
	var formerPrimary, primary bool
	if err := r.f.st.Pool.QueryRow(r.ctx, `SELECT (SELECT server_id FROM deployment_units WHERE id=$1),(SELECT primary_unit FROM deployment_units WHERE id=$1),(SELECT primary_unit FROM deployment_units WHERE id=$2 AND server_id=$3)`, r.former, r.unit, r.a.ServerId).Scan(&formerServer, &formerPrimary, &primary); err != nil {
		t.Fatal(err)
	}
	if formerServer != nil || formerPrimary || !primary {
		t.Fatalf("former unit on %v primary %v, new unit primary %v", formerServer, formerPrimary, primary)
	}
	if count := claimCounter(t, r.f, `SELECT count(*) FROM cross_host_recovery_servers WHERE server_id=$1`, r.peer.ServerId); count != 0 {
		t.Fatal("recovery staged the revoked former primary")
	}
	r.transfer(t, r.a)
	r.waitForRecoveryOpen(t, r.a.ServerId)
}

// continuationDeadline is when the children of a test-sealed checkpoint expire.
var continuationDeadline = time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)

// continuation seals the checkpoint after previous with key, as management seals a phase it starts:
// one child of that phase for every unit of the root.
func (r *claimRecovery) continuation(t *testing.T, key *cryptobox.Keyring, previous deployment.RecoveryDescriptor, phase string) (deployment.RecoveryDescriptor, []byte) {
	t.Helper()
	children := []map[string]any{}
	for index, unit := range r.group.Distributed.Units {
		body := deployment.CrossHostCommand{ParentID: r.root.Id, GroupHash: r.group.Hash, Phase: phase, Unit: unit}
		header := api.ExecutionCommand{Id: uuid.NewSHA1(r.root.Id, []byte("crosshost:"+phase+":"+unit.Plan.UnitID.String())), ParentId: &r.root.Id, ServerId: unit.Plan.ServerID, EnvironmentId: r.env, ResourceId: unit.Plan.UnitID, Kind: "crosshost", Action: "inspect", Generation: claimEnvironmentGeneration + 10*(previous.Sequence+1) + int64(index), PlanHash: body.Digest(), Deadline: continuationDeadline}
		children = append(children, map[string]any{"header": header, "unitId": unit.Plan.UnitID})
	}
	raw := claimBytes(t, map[string]any{"phase": phase, "cancelRequested": false, "children": children, "approvals": []any{}})
	d := previous
	d.Sequence++
	d.CheckpointID, d.PreviousHash = uuid.New(), previous.Digest
	envelope, err := key.Seal(raw, fmt.Sprintf("cross-host-recovery:v1:%s:%s:%s:%d:%s", d.WorkspaceID, d.ParentID, d.CheckpointID, d.Sequence, d.RootHash))
	if err != nil {
		t.Fatal(err)
	}
	d.Digest, d.Bytes = deployment.RecoveryDigest(envelope), len(envelope)
	return d, envelope
}

// supplyCapsule puts a sealed checkpoint in an export, as the host retained it.
func supplyCapsule(t *testing.T, dir string, d deployment.RecoveryDescriptor, envelope []byte) {
	t.Helper()
	for name, raw := range map[string][]byte{d.CheckpointID.String() + ".capsule": envelope, d.CheckpointID.String() + ".json": claimBytes(t, d)} {
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// Beyond the checkpoints management kept, a revoked primary's copies extend the root only as far as
// management itself sealed them, one after another: the host can replay that progress, but can't
// invent it, skip a step, reorder it, choose between continuations, or hold the root back below
// what management kept.
func TestRecoveryExtendsARevokedPrimarysRootOnlyAsManagementSealedIt(t *testing.T) {
	for name, test := range map[string]struct {
		supply func(t *testing.T, r *claimRecovery, exports []string)
		phases []string
	}{
		"management's checkpoints alone": {func(*testing.T, *claimRecovery, []string) {}, nil},
		"the checkpoint management sealed next": {func(t *testing.T, r *claimRecovery, exports []string) {
			d, envelope := r.continuation(t, keysOf(r.f.st), r.descriptor, "reserve")
			supplyCapsule(t, exports[0], d, envelope)
		}, []string{"reserve"}},
		"two checkpoints in turn": {func(t *testing.T, r *claimRecovery, exports []string) {
			d, envelope := r.continuation(t, keysOf(r.f.st), r.descriptor, "reserve")
			supplyCapsule(t, exports[0], d, envelope)
			d, envelope = r.continuation(t, keysOf(r.f.st), d, "prepare")
			supplyCapsule(t, exports[0], d, envelope)
		}, []string{"reserve", "prepare"}},
		"a checkpoint resealed under another identity": {func(t *testing.T, r *claimRecovery, exports []string) {
			d, _ := r.continuation(t, keysOf(r.f.st), r.descriptor, "reserve")
			forged := d
			forged.CheckpointID = uuid.New()
			_, envelope := r.continuation(t, keysOf(r.f.st), r.descriptor, "reserve")
			forged.Digest, forged.Bytes = deployment.RecoveryDigest(envelope), len(envelope)
			supplyCapsule(t, exports[0], forged, envelope)
		}, nil},
		"bytes management never sealed": {func(t *testing.T, r *claimRecovery, exports []string) {
			d, _ := r.continuation(t, keysOf(r.f.st), r.descriptor, "reserve")
			envelope := []byte("made up by the revoked host")
			d.Digest, d.Bytes = deployment.RecoveryDigest(envelope), len(envelope)
			supplyCapsule(t, exports[0], d, envelope)
		}, nil},
		"a gap": {func(t *testing.T, r *claimRecovery, exports []string) {
			d, _ := r.continuation(t, keysOf(r.f.st), r.descriptor, "reserve")
			d, envelope := r.continuation(t, keysOf(r.f.st), d, "prepare")
			supplyCapsule(t, exports[0], d, envelope)
		}, nil},
		"a checkpoint that follows another": {func(t *testing.T, r *claimRecovery, exports []string) {
			elsewhere := r.descriptor
			elsewhere.Digest = strings.Repeat("0", 64)
			d, envelope := r.continuation(t, keysOf(r.f.st), elsewhere, "reserve")
			supplyCapsule(t, exports[0], d, envelope)
		}, nil},
		"a copy from another revoked server": {func(t *testing.T, r *claimRecovery, exports []string) {
			r.revokePeer(t)
			d, envelope := r.continuation(t, keysOf(r.f.st), r.descriptor, "reserve")
			supplyCapsule(t, exports[1], d, envelope)
		}, nil},
		"another revoked server's forgery beside the primary's copy": {func(t *testing.T, r *claimRecovery, exports []string) {
			r.revokePeer(t)
			forged, _ := r.continuation(t, keysOf(r.f.st), r.descriptor, "abort")
			junk := []byte("made up by another revoked host")
			forged.Digest, forged.Bytes = deployment.RecoveryDigest(junk), len(junk)
			supplyCapsule(t, exports[1], forged, junk)
			d, envelope := r.continuation(t, keysOf(r.f.st), r.descriptor, "reserve")
			supplyCapsule(t, exports[0], d, envelope)
		}, []string{"reserve"}},
		"two continuations": {func(t *testing.T, r *claimRecovery, exports []string) {
			for _, phase := range []string{"reserve", "abort"} {
				d, envelope := r.continuation(t, keysOf(r.f.st), r.descriptor, phase)
				supplyCapsule(t, exports[0], d, envelope)
			}
		}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			r := newParticipantRecovery(t)
			r.revokeThird(t, r.a)
			exports := r.exports(t, claimExport{})
			test.supply(t, r, exports)
			r.recoverAll(t, exports)
			for _, phase := range []string{"reserve", "prepare", "abort"} {
				child := uuid.NewSHA1(r.root.Id, []byte("crosshost:"+phase+":"+r.peerUnit.String()))
				want := int64(0)
				if slices.Contains(test.phases, phase) {
					want = 1
				}
				if count := claimCounter(t, r.f, `SELECT count(*) FROM execution_operations WHERE id=$1`, child); count != want {
					t.Fatalf("%s child restored %d times, want %d", phase, count, want)
				}
			}
		})
	}
}

// A healthy participant may already have acted on progress the revoked primary holds beyond the
// checkpoints management kept. Recovery takes that progress from the primary's sealed copy, so the
// participant's proof of it fits, where management's chain alone has no place for it.
func TestRecoveryFitsAParticipantsProofOfProgressBeyondTheBackup(t *testing.T) {
	r := newParticipantRecovery(t)
	r.revokeThird(t, r.a)
	exports := r.exports(t, claimExport{})
	next, envelope := r.continuation(t, keysOf(r.f.st), r.descriptor, "reserve")
	child := uuid.NewSHA1(r.root.Id, []byte("crosshost:reserve:"+r.peerUnit.String()))
	body := deployment.CrossHostCommand{ParentID: r.root.Id, GroupHash: r.group.Hash, Phase: "reserve", Unit: r.peerPlan}
	header := api.ExecutionCommand{Id: child, ParentId: &r.root.Id, ServerId: r.peer.ServerId, EnvironmentId: r.env, ResourceId: r.peerUnit, Kind: "crosshost", Action: "inspect", Generation: claimEnvironmentGeneration + 10 + 1, PlanHash: body.Digest(), Deadline: continuationDeadline, RecoveryCheckpoint: claimMessage(t, next)}
	report := api.AgentReport{Id: child, Generation: header.Generation, PlanHash: header.PlanHash, Sequence: 1, State: "running", Steps: []api.ExecutionStep{}}
	proofs := append(claimBytes(t, deployment.RecoveryProof{ParentID: r.root.Id, ID: child, Header: claimBytes(t, header), Report: claimBytes(t, report), Checkpoint: "accepted"}), '\n')
	var evidence deployment.RecoveryEvidence
	raw, err := os.ReadFile(filepath.Join(exports[1], "host.json"))
	if err != nil || json.Unmarshal(raw, &evidence) != nil {
		t.Fatal("peer export", err)
	}
	digest := sha256.Sum256(proofs)
	evidence.ProofDigest = hex.EncodeToString(digest[:])
	for name, body := range map[string][]byte{"host.json": claimBytes(t, evidence), "proofs.jsonl": proofs} {
		if err = os.WriteFile(filepath.Join(exports[1], name), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	in := execution.RecoveryImport{WorkspaceID: r.ws, OwnerID: r.f.owner, HostExports: exports}
	preview, err := r.f.execution.RecoverCrossHost(r.ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	in.ExpectedState = preview.StateHash
	if _, err = r.f.execution.RecoverCrossHost(r.ctx, in); err == nil || !strings.Contains(err.Error(), "frozen continuation") {
		t.Fatalf("management's chain alone with a proof of progress it never recorded = %v", err)
	}
	supplyCapsule(t, exports[0], next, envelope)
	r.recoverAll(t, exports)
	if count := claimCounter(t, r.f, `SELECT count(*) FROM execution_operations WHERE id=$1 AND report_sequence=1`, child); count != 1 {
		t.Fatal("the participant's proof that it took up its reserve didn't fit the recovered root")
	}
}
