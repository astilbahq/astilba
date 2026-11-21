package secretbroker

import (
	"context"
	"encoding/json/v2"
	"errors"
	"slices"
	"strings"
	"testing"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/custody"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

func busy(err error) bool {
	var e *domain.Error
	return errors.As(err, &e) && e.Code == "temporarily_busy"
}

// New values are sealed under the workspace's active generation, never under the keyring.
func TestSealWritesV2UnderTheActiveGeneration(t *testing.T) {
	b, _, owner, _, _ := setup(t)
	ctx := context.Background()
	ws, item := uuid.New(), Item{Kind: SecretVersion, ID: uuid.New(), Environment: uuid.New()}
	s, err := b.Begin(ctx, Request{Workspace: ws, Purpose: Seal, Principal: Account(owner), Items: []Item{item}})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := s.Seal(ctx, fence{}, item, []byte("value"))
	if err != nil {
		t.Fatal(err)
	}
	header, err := cryptobox.ReadHeader(sealed)
	if err != nil || header.Version != 2 || header.Scope != cryptobox.ScopeWorkspace || header.Generation != 1 {
		t.Fatalf("header %+v %v", header, err)
	}
	inspect, err := b.Begin(ctx, Request{Workspace: ws, Purpose: Inspect, Principal: Account(owner), Items: []Item{item}})
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := inspect.Open(item, sealed); err != nil || string(plain) != "value" {
		t.Fatal(string(plain), err)
	}
}

// A generation that stops being sealable between Begin and the storing transaction is refused
// there, and the session ends so the caller begins one holding the new generation.
func TestSealIsFencedOnItsGeneration(t *testing.T) {
	b, _, owner, _, _ := setup(t)
	ctx := context.Background()
	item := Item{Kind: SecretVersion, ID: uuid.New(), Environment: uuid.New()}
	s, err := b.Begin(ctx, Request{Workspace: uuid.New(), Purpose: Seal, Principal: Account(owner), Items: []Item{item}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Seal(ctx, fence{unsealable: true}, item, []byte("value"))
	if !errors.Is(err, ErrNeedKeys) || !errors.Is(err, ErrKeysChanged) || !busy(err) || !SessionFailure(err) {
		t.Fatalf("unfenced seal: %v", err)
	}
	if s.Live() {
		t.Fatal("a session whose generation changed stayed live")
	}
}

// A holder without keys waits for them, and wakes the job that makes them; the refusal says nothing
// of other workspaces. A session that began before its holder was given a key begins again rather
// than wait.
func TestSealWaitsForKeys(t *testing.T) {
	b, q, owner, _, _ := setup(t)
	ctx := context.Background()
	ws, item := uuid.New(), Item{Kind: SecretVersion, ID: uuid.New(), Environment: uuid.New()}
	q.keyless[ws] = true
	s, err := b.Begin(ctx, Request{Workspace: ws, Purpose: Seal, Principal: Account(owner), Items: []Item{item}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Seal(ctx, fence{}, item, []byte("value"))
	if !errors.Is(err, ErrKeyPending) || !busy(err) || errors.Is(err, ErrNeedKeys) {
		t.Fatalf("keyless seal: %v", err)
	}
	select {
	case <-b.KeysWanted():
	default:
		t.Fatal("the key job was not woken")
	}
	if !s.Live() {
		t.Fatal("waiting for keys ended the session")
	}
	if _, err = s.Seal(ctx, fence{has: true}, item, []byte("value")); !errors.Is(err, ErrKeysChanged) || s.Live() {
		t.Fatalf("seal after keys arrived: %v", err)
	}
}

// A session that began before its holder had a key meets values sealed since as keys to begin
// again for. A session that held a generation meets any other as a value it cannot open, so an
// envelope naming a generation of its own counts for nothing.
func TestSessionsTellNewKeysFromForgedOnes(t *testing.T) {
	b, q, owner, _, _ := setup(t)
	ctx := context.Background()
	ws, item := uuid.New(), Item{Kind: SecretVersion, ID: uuid.New(), Environment: uuid.New()}
	q.keyless[ws] = true
	early, err := b.Begin(ctx, Request{Workspace: ws, Purpose: Inspect, Principal: Account(owner), Items: []Item{item}})
	if err != nil {
		t.Fatal(err)
	}
	delete(q.keyless, ws)
	seal, err := b.Begin(ctx, Request{Workspace: ws, Purpose: Seal, Principal: Account(owner), Items: []Item{item}})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := seal.Seal(ctx, fence{}, item, []byte("value"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = early.Open(item, sealed); !errors.Is(err, ErrNeedKeys) || early.Live() {
		t.Fatalf("early session: %v", err)
	}
	later, err := b.Begin(ctx, Request{Workspace: ws, Purpose: Inspect, Principal: Account(owner), Items: []Item{item}})
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := later.Open(item, sealed); err != nil || string(plain) != "value" {
		t.Fatal(string(plain), err)
	}
	var env map[string]any
	if err = json.Unmarshal(sealed, &env); err != nil {
		t.Fatal(err)
	}
	env["generation"] = 7
	forged, err := stablejson.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = later.Open(item, forged); !errors.Is(err, cryptobox.ErrKey) || !later.Live() {
		t.Fatalf("forged generation: %v", err)
	}
}

// A workspace whose only generation may no longer be sealed under refuses new values until a new
// generation replaces it: a seal session holds no key for it at all.
func TestUnsealableGenerationRefusesNewValues(t *testing.T) {
	b, q, owner, _, _ := setup(t)
	ctx := context.Background()
	ws, item := uuid.New(), Item{Kind: SecretVersion, ID: uuid.New(), Environment: uuid.New()}
	q.keys = map[string][]db.WorkspaceKeyWrappingsRow{ws.String() + ":seal": {{Generation: 1, State: "active", Sealable: false, Kcv: make([]byte, 32)}}}
	s, err := b.Begin(ctx, Request{Workspace: ws, Purpose: Seal, Principal: Account(owner), Items: []Item{item}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Seal(ctx, fence{has: true}, item, []byte("value")); !errors.Is(err, ErrNoSealableKey) || !s.Live() {
		t.Fatalf("seal under an unsealable generation: %v", err)
	}
}

// A seal session opens only checkpoints it sealed itself, to prove they read back: it holds only
// the generation it seals under, so it cannot be relied on to open anything older.
func TestSealSessionsOpenOnlyWhatTheySealed(t *testing.T) {
	b, _, owner, _, _ := setup(t)
	ctx := context.Background()
	ws := uuid.New()
	d := deployment.RecoveryDescriptor{Version: deployment.RecoveryVersion, WorkspaceID: ws, CheckpointID: uuid.New()}
	begin := func() *Session {
		s, err := b.Begin(ctx, Request{Workspace: ws, Purpose: Seal, Principal: Account(owner), Kinds: []Kind{RecoveryCheckpoint}})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	first, second := begin(), begin()
	sealed, err := first.Seal(ctx, fence{}, Checkpoint(d), []byte("checkpoint"))
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := first.Open(Checkpoint(d), sealed); err != nil || string(plain) != "checkpoint" {
		t.Fatal(string(plain), err)
	}
	if _, err = second.Open(Checkpoint(d), sealed); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("a seal session opened another's checkpoint: %v", err)
	}
}

// A host holds a checkpoint's descriptor beside its envelope, so the envelope opens only under the
// descriptor management sealed it with. A rewritten field, above all the checkpoint it follows, which
// alone links it into a chain, leaves it unopenable. Only the envelope's own digest and size, which
// sealing decides, are not bound.
func TestCheckpointsOpenOnlyUnderTheDescriptorTheyWereSealedWith(t *testing.T) {
	b, _, owner, _, _ := setup(t)
	ctx := context.Background()
	ws, id := uuid.New(), uuid.New()
	d := deployment.RecoveryDescriptor{Version: deployment.RecoveryVersion, WorkspaceID: ws, ParentID: uuid.New(), RootHash: strings.Repeat("a", 64), GroupID: uuid.New(), GroupHash: strings.Repeat("b", 64), BaseID: uuid.New(), CheckpointID: id, Sequence: 3, PreviousHash: strings.Repeat("c", 64), Participants: []uuid.UUID{uuid.New(), uuid.New()}}
	s, err := b.Begin(ctx, Request{Workspace: ws, Purpose: Seal, Principal: Account(owner), Kinds: []Kind{RecoveryCheckpoint}})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := s.Seal(ctx, fence{}, Checkpoint(d), []byte("continuation"))
	if err != nil {
		t.Fatal(err)
	}
	d.Digest, d.Bytes = deployment.RecoveryDigest(sealed), len(sealed)
	if plain, err := s.Open(Checkpoint(d), sealed); err != nil || string(plain) != "continuation" {
		t.Fatalf("the descriptor it was sealed with, with its digest and size = %q, %v", plain, err)
	}
	for name, rewrite := range map[string]func(*deployment.RecoveryDescriptor){
		"previous hash": func(d *deployment.RecoveryDescriptor) { d.PreviousHash = strings.Repeat("d", 64) },
		"sequence":      func(d *deployment.RecoveryDescriptor) { d.Sequence++ },
		"parent":        func(d *deployment.RecoveryDescriptor) { d.ParentID = uuid.New() },
		"root hash":     func(d *deployment.RecoveryDescriptor) { d.RootHash = strings.Repeat("e", 64) },
		"group":         func(d *deployment.RecoveryDescriptor) { d.GroupID = uuid.New() },
		"group hash":    func(d *deployment.RecoveryDescriptor) { d.GroupHash = strings.Repeat("e", 64) },
		"base":          func(d *deployment.RecoveryDescriptor) { d.BaseID = uuid.New() },
		"participants":  func(d *deployment.RecoveryDescriptor) { d.Participants = d.Participants[:1] },
		"participant order": func(d *deployment.RecoveryDescriptor) {
			d.Participants = []uuid.UUID{d.Participants[1], d.Participants[0]}
		},
	} {
		t.Run(name, func(t *testing.T) {
			rewritten := d
			rewritten.Participants = slices.Clone(d.Participants)
			rewrite(&rewritten)
			if plain, err := s.Open(Checkpoint(rewritten), sealed); err == nil {
				t.Fatalf("a checkpoint opened under a rewritten %s: %q", name, plain)
			}
		})
	}
}

// Version 1 descriptors bound neither the checkpoint they follow nor most of their root, so no
// checkpoint is sealed or opened under one.
func TestVersionOneCheckpointsAreRefused(t *testing.T) {
	b, _, owner, _, _ := setup(t)
	ctx := context.Background()
	ws := uuid.New()
	d := deployment.RecoveryDescriptor{Version: deployment.RecoveryVersion, WorkspaceID: ws, CheckpointID: uuid.New()}
	s, err := b.Begin(ctx, Request{Workspace: ws, Purpose: Seal, Principal: Account(owner), Kinds: []Kind{RecoveryCheckpoint}})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := s.Seal(ctx, fence{}, Checkpoint(d), []byte("checkpoint"))
	if err != nil {
		t.Fatal(err)
	}
	d.Version = 1
	if CheckpointIdentity(d) != "" {
		t.Fatal("a version 1 descriptor has an identity")
	}
	if _, err = s.Seal(ctx, fence{}, Checkpoint(d), []byte("checkpoint")); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("seal under a version 1 descriptor: %v", err)
	}
	if _, err = s.Open(Checkpoint(d), sealed); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("open under a version 1 descriptor: %v", err)
	}
}

// Rehome moves a stored value's data key onto the active generation and keeps its data, whether
// the value is under the keyring or a generation the session holds.
func TestRehomeKeepsTheData(t *testing.T) {
	b, _, _, _, _ := setup(t)
	ctx := context.Background()
	ws, item := uuid.New(), Item{Kind: SecretVersion, ID: uuid.New(), Environment: uuid.New()}
	v1, err := b.backend.Keyring().Seal([]byte("value"), item.identity(ws))
	if err != nil {
		t.Fatal(err)
	}
	s, err := b.Begin(ctx, Request{Workspace: ws, Purpose: Recover, Principal: Operator("test"), Kinds: RecoveryKinds})
	if err != nil {
		t.Fatal(err)
	}
	data := func(blob []byte) string {
		var env struct {
			Data []byte `json:"data"`
		}
		if err := json.Unmarshal(blob, &env); err != nil {
			t.Fatal(err)
		}
		return string(env.Data)
	}
	moved, err := s.Rehome(ctx, fence{}, item, v1)
	if err != nil {
		t.Fatal(err)
	}
	if header, _ := cryptobox.ReadHeader(moved); header.Version != 2 || header.Generation != 1 || data(moved) != data(v1) {
		t.Fatalf("moved %+v", header)
	}
	again, err := s.Rehome(ctx, fence{}, item, moved)
	if err != nil || data(again) != data(v1) {
		t.Fatal(err)
	}
	if plain, err := s.Open(item, again); err != nil || string(plain) != "value" {
		t.Fatal(string(plain), err)
	}
	other := Item{Kind: SecretVersion, ID: uuid.New(), Environment: uuid.New()}
	if _, err = s.Rehome(ctx, fence{}, other, moved); !errors.Is(err, cryptobox.ErrKey) {
		t.Fatalf("a value moved under another row's identity: %v", err)
	}
	if _, err = s.Rehome(ctx, fence{unsealable: true}, item, v1); !errors.Is(err, ErrKeysChanged) {
		t.Fatalf("unfenced rehome: %v", err)
	}
}

// Second factors are sealed under the installation key.
func TestSecondFactorsSealUnderTheInstallationKey(t *testing.T) {
	b, _, owner, _, _ := setup(t)
	ctx := context.Background()
	item := Item{Kind: TOTP, ID: owner}
	s, err := b.Begin(ctx, Request{Purpose: Authenticate, Principal: Account(owner), Items: []Item{item}})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := s.Seal(ctx, fence{}, item, []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	if header, _ := cryptobox.ReadHeader(sealed); header.Scope != string(custody.ScopeInstallation) || header.Generation != 1 {
		t.Fatalf("header %+v", header)
	}
	if plain, err := s.Open(item, sealed); err != nil || string(plain) != "secret" {
		t.Fatal(string(plain), err)
	}
}

// Recovery runs with the installation offline, where no key is made, so a recover session begun
// before its workspace had a key cannot open a v2 value, rather than waiting for a key.
func TestRecoverySessionsDoNotWaitForKeys(t *testing.T) {
	b, q, owner, _, _ := setup(t)
	ctx := context.Background()
	ws, item := uuid.New(), Item{Kind: SecretVersion, ID: uuid.New(), Environment: uuid.New()}
	seal, err := b.Begin(ctx, Request{Workspace: ws, Purpose: Seal, Principal: Account(owner), Items: []Item{item}})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := seal.Seal(ctx, fence{}, item, []byte("value"))
	if err != nil {
		t.Fatal(err)
	}
	q.keyless[ws] = true
	recover, err := b.Begin(ctx, Request{Workspace: ws, Purpose: Recover, Principal: Operator("test"), Kinds: RecoveryKinds})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = recover.Open(item, sealed); !errors.Is(err, ErrKeyNotInBackup) || !errors.Is(err, cryptobox.ErrKey) || SessionFailure(err) || !recover.Live() ||
		!strings.Contains(err.Error(), "holds no key for the workspace") || !strings.Contains(err.Error(), "after the workspace's key was made") {
		t.Fatalf("keyless recovery: %v", err)
	}
	// Nor can it restore a value: the operator makes the key first.
	if _, err = recover.Rehome(ctx, fence{}, item, sealed); !errors.Is(err, ErrKeyMissing) {
		t.Fatalf("keyless rehome: %v", err)
	}
	select {
	case <-b.KeysWanted():
		t.Fatal("an offline recovery woke the key job")
	default:
	}
}

// A recovery whose database has the workspace's key but not the key an environment's value is
// sealed under names that environment, and the remedy for it: accepting its values restored
// erased, which a newer workspace key would not give.
func TestRecoveryNamesTheEnvironmentWhoseKeyTheBackupLacks(t *testing.T) {
	b, _, _, _, _ := setup(t)
	ctx := context.Background()
	ws, item := uuid.New(), Item{Kind: SecretVersion, ID: uuid.New(), Environment: uuid.New()}
	sealed, err := cryptobox.SealUnderEnvironment(make([]byte, 32), item.Environment, 1, []byte("value"), item.identity(ws))
	if err != nil {
		t.Fatal(err)
	}
	recover, err := b.Begin(ctx, Request{Workspace: ws, Purpose: Recover, Principal: Operator("test"), Kinds: RecoveryKinds})
	if err != nil {
		t.Fatal(err)
	}
	_, err = recover.Open(item, sealed)
	if !errors.Is(err, ErrKeyNotInBackup) || !errors.Is(err, cryptobox.ErrKey) || SessionFailure(err) || !recover.Live() {
		t.Fatalf("recovery without the environment's key: %v", err)
	}
	for _, want := range []string{"environment " + item.Environment.String(), "--accept-erased-environments"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("%q does not say %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "key for the workspace") {
		t.Fatalf("%q blames the workspace's key", err)
	}
}
