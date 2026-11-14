package execution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"slices"
	"strconv"
	"sync"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
	"github.com/jackc/pgx/v5"
)

// claimTimeout bounds a release's claim, which runs on after its poll has ended.
const claimTimeout = 5 * time.Second

const (
	// releaseLease is how long a release waits for its acknowledgement before it is made
	// again, as a response that was lost would need. It matches the poll's own lease.
	releaseLease = 30 * time.Second
	// releaseAttempts bounds the releases one instance can have without acknowledging any.
	releaseAttempts = 5
)

const (
	upgradeRequiredNote = "This server's agent must be upgraded before it can receive this command's secrets."
	unacknowledgedNote  = "This command's secrets were released to the agent several times without acknowledgement. The agent must ask for them again."
)

// carriesValues reports whether a command's delivery hydrates stored values.
func carriesValues(c api.ExecutionCommand) bool {
	switch c.Kind {
	case "visual":
		// Removing a preview or a retired environment's unit needs no secret or registry access.
		var p deployment.Payload
		return c.Visual == nil || json.Unmarshal(*c.Visual, &p) != nil || p.Action != "preview-cleanup" && p.Action != retireAction
	case "crosshost", "backup", "schedule", "pipeline", "upgrade":
		return true
	}
	return false
}

// releaseDecision is what a poll does with a command that carries values.
type releaseDecision int

const (
	// sendCommand returns the command now.
	sendCommand releaseDecision = iota
	// waitForRelease waits, as an idle poll does, and looks again: its values may be on
	// their way to this instance.
	waitForRelease
	// skipCommand looks past the command: this agent cannot receive it, but may receive
	// what follows, such as the update that lets it.
	skipCommand
)

// release hydrates a command's values for the polling agent instance when, and only when, they
// must leave: once per dispatch epoch, again while unacknowledged after the lease up to a
// limit, and again whenever the instance asks. Otherwise nothing is opened or recorded, and
// the delivery's decisions are made without values: a stop, an expiry or a refusal still
// reaches the agent, and a command the instance holds comes without them.
func (s *Service) release(ctx context.Context, a Identity, epoch int64, c *api.ExecutionCommand) (releaseDecision, error) {
	key := db.SecretReleaseParams{WorkspaceID: a.Workspace, OperationID: c.Id, DispatchEpoch: epoch, AgentInstance: a.instance()}
	var row db.SecretReleaseRow
	reason, note := "", ""
	switch {
	case !a.AcknowledgesReleases:
		note = upgradeRequiredNote
	default:
		var err error
		row, err = s.Store.Queries.SecretRelease(ctx, key)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			reason = "first-release"
		case err != nil:
			return waitForRelease, err
		case row.AcknowledgedAt != nil:
		case row.Redelivery:
			reason = "redelivery"
		case row.Attempts >= releaseAttempts:
			note = unacknowledgedNote
		case row.CheckedAt.Sub(row.LastAttemptAt) >= releaseLease:
			reason = "unacknowledged"
		}
	}
	if reason != "" {
		return s.releaseValues(ctx, a, key, reason, c)
	}
	deliver, _, err := s.hydrate(ctx, a, nil, c)
	if err != nil || !deliver {
		return waitForRelease, err
	}
	if note != "" {
		if err = s.Store.Queries.AddOperationNote(ctx, db.AddOperationNoteParams{WorkspaceID: a.Workspace, ID: c.Id, Note: note}); err != nil {
			return waitForRelease, err
		}
	}
	switch {
	case a.AcknowledgesReleases && (row.AcknowledgedAt != nil || note != ""):
		// The instance holds the values, or has not answered for them: either way it gets the
		// command without them, and asks again if its journal lacks it.
		c.SecretRelease = &api.SecretRelease{Epoch: epoch, Digest: row.Digest, Values: false}
		return sendCommand, nil
	case c.CancelRequested:
		// A stop carries nothing.
		return sendCommand, nil
	case note != "":
		return skipCommand, nil
	}
	return waitForRelease, nil
}

// releaseValues hydrates a command's values and claims their release for the instance.
func (s *Service) releaseValues(ctx context.Context, a Identity, key db.SecretReleaseParams, reason string, c *api.ExecutionCommand) (releaseDecision, error) {
	keys := &deliveryKeys{passKeys: s.passKeys(secretbroker.Request{Workspace: a.Workspace, Purpose: secretbroker.Deliver, Principal: secretbroker.Agent(a.ID, a.instance()), Operation: &c.Id, Kinds: deliveryKinds, Reason: reason})}
	keys.retire = keys.hold
	outcome := secretbroker.Failed
	defer func() { keys.finish(ctx, outcome) }()
	deliver, carried, err := s.hydrate(ctx, a, keys, c)
	if err != nil || !deliver || !carried {
		if err == nil {
			outcome = secretbroker.UsedIf(keys.opened())
		}
		if err != nil || !deliver {
			return waitForRelease, err
		}
		return sendCommand, nil
	}
	digest := keys.digest(c.Id, key.DispatchEpoch)
	// A poll that has ended claims nothing: its response could not leave.
	if err := ctx.Err(); err != nil {
		return waitForRelease, err
	}
	// A poll that ends while the claim commits would not know whether it did, and a release
	// committed but not returned waits out its lease. So a claim once started finishes, briefly,
	// whatever becomes of the poll, and the poll then returns what it claimed.
	claimCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), claimTimeout)
	defer cancel()
	claimed, err := s.claimRelease(claimCtx, a, key, digest)
	if err != nil || !claimed {
		outcome = secretbroker.Refused
		return waitForRelease, err
	}
	outcome = secretbroker.Released
	c.SecretRelease = &api.SecretRelease{Epoch: key.DispatchEpoch, Digest: digest, Values: true}
	return sendCommand, nil
}

// hydrate fills a command's values for its kind, or with nil keys makes its delivery's
// decisions alone. It reports whether to deliver the command now, and whether it carries
// values: a pipeline may decide not to deliver yet, or to stop without credentials.
func (s *Service) hydrate(ctx context.Context, a Identity, keys valueKeys, c *api.ExecutionCommand) (deliver, carried bool, err error) {
	if c.Kind == "pipeline" {
		deliver, err = s.hydratePipeline(ctx, a, keys, c)
		return deliver, deliver && !c.CancelRequested, err
	}
	// Each kind opens its values in one pass, so the session begins between passes.
	err = withProviderCalls(ctx, func() error {
		switch c.Kind {
		case "upgrade":
			return s.hydrateUpgrade(ctx, keys, c)
		case "backup", "schedule":
			return s.hydrateBackup(ctx, keys, c)
		case "crosshost":
			return s.hydrateCrossHost(ctx, keys, c)
		case "visual":
			return s.hydrateVisual(ctx, keys, c)
		}
		return nil
	})
	return err == nil, true, err
}

// claimRelease decides under the release's lock, which another poll of the same instance may
// have taken first: the release goes ahead only if it still should.
func (s *Service) claimRelease(ctx context.Context, a Identity, key db.SecretReleaseParams, digest string) (bool, error) {
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)
	row, err := q.LockSecretRelease(ctx, db.LockSecretReleaseParams(key))
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		n, err := q.InsertSecretRelease(ctx, db.InsertSecretReleaseParams{OperationID: key.OperationID, DispatchEpoch: key.DispatchEpoch, AgentInstance: key.AgentInstance, WorkspaceID: a.Workspace, ServerID: a.ID, Digest: digest})
		if err != nil || n == 0 {
			return false, err
		}
	case err != nil:
		return false, err
	case !row.Redelivery && (row.AcknowledgedAt != nil || row.Attempts >= releaseAttempts || row.CheckedAt.Sub(row.LastAttemptAt) < releaseLease):
		return false, nil
	default:
		if err = q.RepeatSecretRelease(ctx, db.RepeatSecretReleaseParams{WorkspaceID: key.WorkspaceID, OperationID: key.OperationID, DispatchEpoch: key.DispatchEpoch, AgentInstance: key.AgentInstance, Digest: digest}); err != nil {
			return false, err
		}
	}
	// The release is committed before the response leaves: a failed response is still a
	// possible delivery, and its acknowledgement or the lease decides what happens next.
	return true, tx.Commit(ctx)
}

// NoticeSecretReleases records what an agent instance holds, and what it has lost and asks
// for again.
func (s *Service) NoticeSecretReleases(ctx context.Context, a Identity, notice api.SecretReleaseNotice) error {
	q := s.Store.Queries
	for _, r := range notice.Acknowledged {
		if _, err := q.AcknowledgeSecretRelease(ctx, db.AcknowledgeSecretReleaseParams{WorkspaceID: a.Workspace, OperationID: r.OperationId, DispatchEpoch: r.Epoch, AgentInstance: a.instance(), Digest: r.Digest}); err != nil {
			return err
		}
	}
	for _, r := range notice.Redeliver {
		if _, err := q.RequestSecretRedelivery(ctx, db.RequestSecretRedeliveryParams{WorkspaceID: a.Workspace, OperationID: r.OperationId, DispatchEpoch: r.Epoch, AgentInstance: a.instance()}); err != nil {
			return err
		}
	}
	return nil
}

// agentKeys are the keys an agent's poll or report uses for the decisions it makes about the
// agent's own work, such as asking a Git provider: deliver, for the agent instance, asked for
// only when a decision opens something.
func (s *Service) agentKeys(a Identity) runKeys {
	keys := s.passKeys(secretbroker.Request{Workspace: a.Workspace, Purpose: secretbroker.Deliver, Principal: secretbroker.Agent(a.ID, a.instance()), Kinds: deliveryKinds, Reason: "decision"})
	return runKeys{operate: keys, inspect: keys}
}

// deliveryKinds are what a delivery may open: everything a command can carry, and the values
// the decisions made at delivery read.
var deliveryKinds = []secretbroker.Kind{secretbroker.SecretVersion, secretbroker.BuildSecret, secretbroker.Git, secretbroker.Registry, secretbroker.BackupDestination, secretbroker.BackupPolicy}

// deliveryKeys open values for one release, noting each value's ID and ciphertext for the
// release's digest. The digest names what was released without anything that would let a
// database dump test a guess at a value. The session begins when the first value is opened, so
// a release with nothing to open records nothing, and the release says how it ended.
type deliveryKeys struct {
	*passKeys
	mu      sync.Mutex
	items   map[string]bool
	retired []*secretbroker.Session
}

// hold keeps a session that ran out of time during the release, which records how it ended for
// every session it used.
func (k *deliveryKeys) hold(_ context.Context, session *secretbroker.Session, _ bool) {
	k.retired = append(k.retired, session)
}

func (k *deliveryKeys) open(item secretbroker.Item, cipher []byte) ([]byte, error) {
	return k.note(item, cipher)(k.passKeys.open(item, cipher))
}

func (k *deliveryKeys) openBetweenPasses(ctx context.Context, item secretbroker.Item, cipher []byte) ([]byte, error) {
	return k.note(item, cipher)(k.passKeys.openBetweenPasses(ctx, item, cipher))
}

func (k *deliveryKeys) note(item secretbroker.Item, cipher []byte) func([]byte, error) ([]byte, error) {
	return func(plain []byte, err error) ([]byte, error) {
		if err == nil {
			sum := sha256.Sum256(cipher)
			k.mu.Lock()
			if k.items == nil {
				k.items = map[string]bool{}
			}
			k.items[string(item.Kind)+":"+item.ID.String()+":"+hex.EncodeToString(sum[:])] = true
			k.mu.Unlock()
		}
		return plain, err
	}
}

// close leaves the sessions to the release, which records how they ended with finish.
func (k *deliveryKeys) close(context.Context) {}

func (k *deliveryKeys) finish(ctx context.Context, outcome secretbroker.Outcome) {
	for _, session := range append(k.retired, k.session) {
		if session != nil {
			session.Close(ctx, outcome)
		}
	}
}

func (k *deliveryKeys) opened() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.items) > 0
}

func (k *deliveryKeys) digest(operation [16]byte, epoch int64) string {
	k.mu.Lock()
	items := make([]string, 0, len(k.items))
	for item := range k.items {
		items = append(items, item)
	}
	k.mu.Unlock()
	slices.Sort(items)
	h := sha256.New()
	h.Write(operation[:])
	h.Write([]byte(strconv.FormatInt(epoch, 10)))
	for _, item := range items {
		h.Write([]byte("\x00" + item))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// openJSON opens an integration's value and decodes it.
func openJSON(keys valueKeys, item secretbroker.Item, cipher []byte, out any) error {
	plain, err := keys.open(item, cipher)
	if err != nil {
		return err
	}
	defer clear(plain)
	return json.Unmarshal(plain, out)
}
