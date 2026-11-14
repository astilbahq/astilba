package execution

import (
	"context"

	"github.com/google/uuid"

	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
)

// passKeys gives a multi-pass body its broker session. A pass that needs keys it does not hold
// returns the providerCall from get, which rolls its transaction back; the session begins
// between passes, with nothing held, and the next pass finds it. One session normally serves
// every pass, and one that runs out of time costs another Begin and another record.
type passKeys struct {
	service *Service
	request secretbroker.Request
	session *secretbroker.Session
	err     error
	used    bool
	// retire ends a session that ran out of time; by default it records whether it was used.
	retire func(ctx context.Context, session *secretbroker.Session, used bool)
}

func (s *Service) passKeys(r secretbroker.Request) *passKeys {
	return &passKeys{service: s, request: r}
}

func (k *passKeys) get() (*secretbroker.Session, error) {
	if k.err != nil {
		// The broker refused, which says nothing about the value or its connection.
		return nil, &keysUnavailable{k.err}
	}
	if k.session != nil && k.session.Live() {
		return k.session, nil
	}
	// Calls a pass joins may each ask for the session, so the call begins one only when none is
	// live by then.
	return nil, &providerCall{fetch: func(ctx context.Context) { _, _ = k.ensure(ctx) }}
}

// ensure is get for code that runs between passes, with nothing held, such as a provider call:
// it begins the session itself when it needs one.
func (k *passKeys) ensure(ctx context.Context) (*secretbroker.Session, error) {
	if k.err == nil && (k.session == nil || !k.session.Live()) {
		k.begin(ctx)
	}
	return k.session, k.err
}

func (k *passKeys) begin(ctx context.Context) {
	if k.session != nil {
		if k.retire != nil {
			k.retire(ctx, k.session, k.used)
		} else {
			k.session.Close(ctx, secretbroker.UsedIf(k.used))
		}
	}
	k.session, k.err = k.service.Store.Broker.Begin(ctx, k.request)
	k.used = false
}

// use marks the current session as having done its work.
func (k *passKeys) use() { k.used = true }

// close records how the last session ended. Call it once the passes are over.
func (k *passKeys) close(ctx context.Context) {
	if k != nil && k.session != nil {
		k.session.Close(ctx, secretbroker.UsedIf(k.used))
	}
}

// valueKeys opens stored values for a run: broker sessions for everything except the agent
// delivery path, whose legacy keys move to the broker with release records.
type valueKeys interface {
	// open is for a pass: without keys it returns the providerCall that begins them.
	open(item secretbroker.Item, cipher []byte) ([]byte, error)
	// openBetweenPasses is for code that holds nothing, such as a provider call.
	openBetweenPasses(ctx context.Context, item secretbroker.Item, cipher []byte) ([]byte, error)
	close(ctx context.Context)
}

func (k *passKeys) open(item secretbroker.Item, cipher []byte) ([]byte, error) {
	session, err := k.get()
	if err != nil {
		return nil, err
	}
	return k.opened(session, item, cipher)
}

func (k *passKeys) openBetweenPasses(ctx context.Context, item secretbroker.Item, cipher []byte) ([]byte, error) {
	session, err := k.ensure(ctx)
	if err != nil {
		return nil, &keysUnavailable{err}
	}
	return k.opened(session, item, cipher)
}

func (k *passKeys) opened(session *secretbroker.Session, item secretbroker.Item, cipher []byte) ([]byte, error) {
	plain, err := session.Open(item, cipher)
	if err == nil {
		k.use()
	}
	return plain, err
}

// keysUnavailable is a provider call's broker refusal. A pass that finds one as an answer gives
// its error back rather than keeping it as the connection's failure.
type keysUnavailable struct{ err error }

func (e *keysUnavailable) Error() string { return e.err.Error() }
func (e *keysUnavailable) Unwrap() error { return e.err }

// runKeys are the keys a run asking Git providers questions uses: operate for its
// connections' stored access, and inspect for the checks it makes before it plans work.
type runKeys struct {
	operate, inspect valueKeys
}

func (k runKeys) close(ctx context.Context) {
	k.operate.close(ctx)
	k.inspect.close(ctx)
}

// brokerKeys are runKeys for principal in ws.
func (s *Service) brokerKeys(principal secretbroker.Principal, ws uuid.UUID) runKeys {
	return runKeys{
		operate: s.passKeys(secretbroker.Request{Workspace: ws, Purpose: secretbroker.Operate, Principal: principal, Kinds: []secretbroker.Kind{secretbroker.Git}}),
		inspect: s.passKeys(secretbroker.Request{Workspace: ws, Purpose: secretbroker.Inspect, Principal: principal, Kinds: []secretbroker.Kind{secretbroker.SecretVersion, secretbroker.BuildSecret, secretbroker.Registry}}),
	}
}
