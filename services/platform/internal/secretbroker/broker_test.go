package secretbroker

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/custody"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type fakeQueries struct {
	members  map[uuid.UUID]string // account -> role in the one workspace
	servers  map[uuid.UUID]bool
	existing map[uuid.UUID]bool
	inserted []db.InsertSecretAccessParams
	outcomes []db.InsertSecretAccessOutcomeParams
	failNext error
}

func (f *fakeQueries) SecretAccessMember(_ context.Context, arg db.SecretAccessMemberParams) (string, error) {
	role, ok := f.members[arg.AccountID]
	if !ok {
		return "", pgx.ErrNoRows
	}
	return role, nil
}
func (f *fakeQueries) SecretAccessServer(_ context.Context, arg db.SecretAccessServerParams) (bool, error) {
	return f.servers[arg.ServerID], nil
}
func (f *fakeQueries) SecretAccessWorkspaceExists(_ context.Context, id uuid.UUID) (bool, error) {
	return f.existing[id], nil
}
func (f *fakeQueries) InsertSecretAccess(_ context.Context, arg db.InsertSecretAccessParams) error {
	if err := f.failNext; err != nil {
		f.failNext = nil
		return err
	}
	f.inserted = append(f.inserted, arg)
	return nil
}
func (f *fakeQueries) InsertSecretAccessOutcome(_ context.Context, arg db.InsertSecretAccessOutcomeParams) error {
	f.outcomes = append(f.outcomes, arg)
	return nil
}
func (f *fakeQueries) AbandonSecretAccess(context.Context, float64) (int64, error) { return 0, nil }

func setup(t *testing.T) (*Broker, *fakeQueries, uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	ring, err := cryptobox.New("test")
	if err != nil {
		t.Fatal(err)
	}
	file, err := custody.NewFile(ring)
	if err != nil {
		t.Fatal(err)
	}
	owner, member, server := uuid.New(), uuid.New(), uuid.New()
	q := &fakeQueries{members: map[uuid.UUID]string{owner: "owner", member: "member"}, servers: map[uuid.UUID]bool{server: true}, existing: map[uuid.UUID]bool{}}
	return New(q, file, nil), q, owner, member, server
}

func TestNothingOpensWithoutACommittedRecord(t *testing.T) {
	b, q, owner, _, _ := setup(t)
	ctx := context.Background()
	ws, version := uuid.New(), uuid.New()
	item := Item{Kind: SecretVersion, ID: version}
	q.failNext = errors.New("database down")
	if s, err := b.Begin(ctx, Request{Workspace: ws, Purpose: Seal, Principal: Account(owner), Items: []Item{item}}); err == nil || s != nil {
		t.Fatalf("Begin without a record: %v", err)
	}
	if len(q.inserted) != 0 {
		t.Fatal("a failed insert was counted")
	}
	s, err := b.Begin(ctx, Request{Workspace: ws, Purpose: Seal, Principal: Account(owner), Items: []Item{item}})
	if err != nil {
		t.Fatal(err)
	}
	if len(q.inserted) != 1 || q.inserted[0].ID != s.ID() || *q.inserted[0].WorkspaceID != ws || q.inserted[0].Purpose != "seal" || *q.inserted[0].AccountID != owner {
		t.Fatalf("record: %+v", q.inserted)
	}
	sealed, err := s.Seal(item, []byte("value"))
	if err != nil {
		t.Fatal(err)
	}
	s.Close(ctx, Used)
	s.Close(ctx, Failed)
	if len(q.outcomes) != 1 || q.outcomes[0].Outcome != "used" || q.outcomes[0].AccessID != s.ID() {
		t.Fatalf("outcomes: %+v", q.outcomes)
	}
	if _, err = s.Seal(item, []byte("value")); !errors.Is(err, ErrNeedKeys) {
		t.Fatalf("closed session sealed: %v", err)
	}
	// Every request records again: nothing is kept between them.
	inspect, err := b.Begin(ctx, Request{Workspace: ws, Purpose: Inspect, Principal: Account(owner), Items: []Item{item}})
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := inspect.Open(item, sealed); err != nil || string(plain) != "value" {
		t.Fatalf("open: %q %v", plain, err)
	}
	if len(q.inserted) != 2 {
		t.Fatalf("records: %d", len(q.inserted))
	}
}

func TestPurposeTable(t *testing.T) {
	b, _, owner, member, server := setup(t)
	ctx := context.Background()
	ws := uuid.New()
	secret := Item{Kind: SecretVersion, ID: uuid.New()}
	for _, test := range []struct {
		name string
		r    Request
		ok   bool
	}{
		{"member seals", Request{Workspace: ws, Purpose: Seal, Principal: Account(member), Items: []Item{secret}}, true},
		{"stranger seals", Request{Workspace: ws, Purpose: Seal, Principal: Account(uuid.New()), Items: []Item{secret}}, false},
		{"member exports", Request{Workspace: ws, Purpose: Export, Principal: Account(member), Kinds: []Kind{SecretVersion}}, false},
		{"owner exports", Request{Workspace: ws, Purpose: Export, Principal: Account(owner), Kinds: []Kind{SecretVersion}}, true},
		{"account delivers", Request{Workspace: ws, Purpose: Deliver, Principal: Account(owner), Items: []Item{secret}}, false},
		{"agent delivers", Request{Workspace: ws, Purpose: Deliver, Principal: Agent(server, "1"), Items: []Item{secret}}, true},
		{"unknown agent delivers", Request{Workspace: ws, Purpose: Deliver, Principal: Agent(uuid.New(), "1"), Items: []Item{secret}}, false},
		{"automation for its workspace", Request{Workspace: ws, Purpose: Seal, Principal: Automation(member, uuid.New(), ws), Items: []Item{secret}}, true},
		{"automation for another workspace", Request{Workspace: ws, Purpose: Seal, Principal: Automation(member, uuid.New(), uuid.New()), Items: []Item{secret}}, false},
		{"system job", Request{Workspace: ws, Purpose: Operate, Principal: System("alert-delivery"), Items: []Item{{Kind: AlertWebhook, ID: ws}}}, true},
		{"another workspace's webhook", Request{Workspace: ws, Purpose: Operate, Principal: System("alert-delivery"), Items: []Item{{Kind: AlertWebhook, ID: uuid.New()}}}, false},
		{"inspect a webhook", Request{Workspace: ws, Purpose: Inspect, Principal: Account(owner), Items: []Item{{Kind: AlertWebhook, ID: ws}}}, false},
		{"nothing asked for", Request{Workspace: ws, Purpose: Inspect, Principal: Account(owner)}, false},
		{"own second factor", Request{Purpose: Authenticate, Principal: Account(member), Items: []Item{{Kind: TOTP, ID: member}}}, true},
		{"another's second factor", Request{Purpose: Authenticate, Principal: Account(member), Items: []Item{{Kind: TOTP, ID: owner}}}, false},
		{"second factor in a workspace", Request{Workspace: ws, Purpose: Authenticate, Principal: Account(member), Items: []Item{{Kind: TOTP, ID: member}}}, false},
		{"workspace value at installation scope", Request{Purpose: Inspect, Principal: Account(member), Items: []Item{secret}}, false},
		{"new workspace", Request{Workspace: uuid.New(), Purpose: Seal, Principal: Account(uuid.New()), Items: []Item{secret}, NewWorkspace: true}, true},
		{"new workspace for export", Request{Workspace: uuid.New(), Purpose: Export, Principal: Account(owner), Kinds: []Kind{SecretVersion}, NewWorkspace: true}, false},
		{"operator rotates", Request{Purpose: Rotate, Principal: Operator("cli"), Items: []Item{{Kind: TOTP, ID: member}}}, false},
		{"row not yet chosen", Request{Workspace: ws, Purpose: Seal, Principal: Account(owner), Items: []Item{{Kind: BackupPolicy}}}, false},
		{"unknown purpose", Request{Workspace: ws, Purpose: "spy", Principal: Account(owner), Items: []Item{secret}}, false},
		{"malformed principal", Request{Workspace: ws, Purpose: Seal, Principal: Principal{Kind: AccountPrincipal}, Items: []Item{secret}}, false},
	} {
		s, err := b.Begin(ctx, test.r)
		if (err == nil) != test.ok {
			t.Errorf("%s: %v", test.name, err)
		}
		if err != nil && !errors.Is(err, domain.Forbidden) {
			t.Errorf("%s: %v, want forbidden", test.name, err)
		}
		if s != nil {
			s.Close(ctx, Used)
		}
	}
}

func TestSessionOpensOnlyWhatItWasGiven(t *testing.T) {
	b, _, owner, _, _ := setup(t)
	ctx := context.Background()
	ws, other := uuid.New(), uuid.New()
	mine, theirs := Item{Kind: SecretVersion, ID: uuid.New()}, Item{Kind: SecretVersion, ID: uuid.New()}
	seal, err := b.Begin(ctx, Request{Workspace: ws, Purpose: Seal, Principal: Account(owner), Items: []Item{mine, theirs}})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := seal.Seal(mine, []byte("v"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = seal.Open(mine, sealed); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("seal session opened a secret: %v", err)
	}
	inspect, err := b.Begin(ctx, Request{Workspace: ws, Purpose: Inspect, Principal: Account(owner), Items: []Item{theirs}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = inspect.Open(mine, sealed); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("undeclared item opened: %v", err)
	}
	scan, err := b.Begin(ctx, Request{Workspace: ws, Purpose: Inspect, Principal: Account(owner), Kinds: []Kind{SecretVersion}})
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := scan.Open(mine, sealed); err != nil || string(plain) != "v" {
		t.Fatalf("scan: %v", err)
	}
	// The same row ID in another workspace is another value: this ciphertext does not open there.
	moved, err := b.Begin(ctx, Request{Workspace: other, Purpose: Inspect, Principal: Account(owner), Kinds: []Kind{SecretVersion}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = moved.Open(mine, sealed); err == nil {
		t.Fatal("a value opened in another workspace")
	}
	// A checkpoint names its own workspace.
	d := deployment.RecoveryDescriptor{WorkspaceID: other, CheckpointID: uuid.New()}
	cp, err := b.Begin(ctx, Request{Workspace: ws, Purpose: Seal, Principal: Account(owner), Kinds: []Kind{RecoveryCheckpoint}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = cp.Seal(Checkpoint(d), []byte("x")); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("another workspace's checkpoint sealed: %v", err)
	}
}

func TestSessionEndsAtItsLimit(t *testing.T) {
	b, _, owner, _, _ := setup(t)
	now := time.Now()
	b.now = func() time.Time { return now }
	ctx := context.Background()
	item := Item{Kind: SecretVersion, ID: uuid.New()}
	s, err := b.Begin(ctx, Request{Workspace: uuid.New(), Purpose: Seal, Principal: Account(owner), NewWorkspace: true, Items: []Item{item}})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(defaultLimit)
	if _, err = s.Seal(item, []byte("v")); !errors.Is(err, ErrNeedKeys) {
		t.Fatalf("expired session: %v", err)
	}
}

func TestBudgets(t *testing.T) {
	b, q, owner, _, _ := setup(t)
	ctx := context.Background()
	ws := uuid.New()
	item := Item{Kind: SecretVersion, ID: uuid.New()}
	for range workspaceBurst {
		if _, err := b.Begin(ctx, Request{Workspace: ws, Purpose: Seal, Principal: Account(owner), Items: []Item{item}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := b.Begin(ctx, Request{Workspace: ws, Purpose: Seal, Principal: Account(owner), Items: []Item{item}}); !errors.Is(err, domain.RateLimited) {
		t.Fatalf("over budget: %v", err)
	}
	if len(q.inserted) != workspaceBurst {
		t.Fatalf("a refused request was recorded: %d", len(q.inserted))
	}
	// Another purpose in the same workspace has its own budget.
	if _, err := b.Begin(ctx, Request{Workspace: ws, Purpose: Inspect, Principal: Account(owner), Items: []Item{item}}); err != nil {
		t.Fatal(err)
	}
	// Forged webhook calls spend only their connection's budget, never the workspace's.
	hooked := uuid.New()
	connection := uuid.New()
	hook := Request{Workspace: hooked, Purpose: Operate, Principal: System("webhook"), Items: []Item{{Kind: Git, ID: connection}}, Connection: &connection}
	// More than the workspace's whole burst, so a shared bucket would be empty, and more than
	// the connection's own.
	for range max(workspaceBurst, webhookBurst) + 1 {
		_, _ = b.Begin(ctx, hook)
	}
	if _, err := b.Begin(ctx, hook); !errors.Is(err, domain.RateLimited) {
		t.Fatalf("webhook over budget: %v", err)
	}
	if _, err := b.Begin(ctx, Request{Workspace: hooked, Purpose: Operate, Principal: System("alert-delivery"), Items: []Item{{Kind: AlertWebhook, ID: hooked}}}); err != nil {
		t.Fatalf("webhooks spent the workspace's budget: %v", err)
	}
	// Each account has its own budget for its second factor.
	for range workspaceBurst + 1 {
		_, _ = b.Begin(ctx, Request{Purpose: Authenticate, Principal: Account(owner), Items: []Item{{Kind: TOTP, ID: owner}}})
	}
	other := uuid.New()
	if _, err := b.Begin(ctx, Request{Purpose: Authenticate, Principal: Account(other), Items: []Item{{Kind: TOTP, ID: other}}}); err != nil {
		t.Fatalf("one account's checks held up another's: %v", err)
	}
}

// One agent that never acknowledges spends its server's budget, not its workspace's.
func TestDeliveryBudgetIsPerServer(t *testing.T) {
	b, q, _, _, server := setup(t)
	ctx := context.Background()
	ws, other := uuid.New(), uuid.New()
	q.servers[other] = true
	secret := Item{Kind: SecretVersion, ID: uuid.New()}
	for range serverBurst {
		if _, err := b.Begin(ctx, Request{Workspace: ws, Purpose: Deliver, Principal: Agent(server, "1"), Items: []Item{secret}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := b.Begin(ctx, Request{Workspace: ws, Purpose: Deliver, Principal: Agent(server, "1"), Items: []Item{secret}}); !errors.Is(err, domain.RateLimited) {
		t.Fatalf("server over budget: %v", err)
	}
	if _, err := b.Begin(ctx, Request{Workspace: ws, Purpose: Deliver, Principal: Agent(other, "1"), Items: []Item{secret}}); err != nil {
		t.Fatalf("one server held up another: %v", err)
	}
	if serverRate*5 > workspaceRate || serverBurst*5 > workspaceBurst {
		t.Fatal("a server's delivery budget must stay within a fifth of its workspace's")
	}
}

func TestNewWorkspaceMustBeNew(t *testing.T) {
	b, q, _, _, _ := setup(t)
	taken := uuid.New()
	q.existing[taken] = true
	_, err := b.Begin(context.Background(), Request{Workspace: taken, Purpose: Seal, Principal: Account(uuid.New()), Kinds: []Kind{SecretVersion}, NewWorkspace: true})
	if !errors.Is(err, domain.Conflict) || len(q.inserted) != 0 {
		t.Fatalf("an import named an existing workspace: %v, %d records", err, len(q.inserted))
	}
}

func TestCancelledBeginEndsItsRecord(t *testing.T) {
	b, q, owner, _, _ := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := b.Begin(ctx, Request{Workspace: uuid.New(), Purpose: Inspect, Principal: Account(owner), Kinds: []Kind{SecretVersion}}); err == nil {
		t.Fatal("a cancelled request was given keys")
	}
	if len(q.inserted) != 1 || len(q.outcomes) != 1 || q.outcomes[0].Outcome != "refused" {
		t.Fatalf("records %d, outcomes %+v", len(q.inserted), q.outcomes)
	}
}

func TestTokensRecordOnceAndStayApart(t *testing.T) {
	b, q, _, _, _ := setup(t)
	tokens, err := b.Tokens(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(q.inserted) != 1 || q.inserted[0].Purpose != "token" || q.inserted[0].WorkspaceID != nil || len(q.outcomes) != 1 || q.outcomes[0].Outcome != "released" {
		t.Fatalf("records: %+v %+v", q.inserted, q.outcomes)
	}
	if _, err = tokens.Open(TokenKind{}, nil); !errors.Is(err, ErrNotAllowed) {
		t.Fatal("a token kind outside the package was accepted")
	}
	sealed, err := tokens.Seal(DeviceToken, []byte("t"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tokens.Open(ResetMail, sealed); err == nil {
		t.Fatal("a device token opened as reset mail")
	}
	if plain, err := tokens.Open(DeviceToken, sealed); err != nil || !bytes.Equal(plain, []byte("t")) {
		t.Fatal(err)
	}
}
