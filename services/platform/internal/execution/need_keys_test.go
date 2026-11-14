package execution

import (
	"context"
	"errors"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
	"github.com/astilbahq/astilba/services/platform/internal/secretbroker/brokertest"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
)

// A pass without keys rolls back and asks for them; the keys begin between passes, serve every
// later pass, and are recorded once with how they ended.
func TestPassKeysBeginBetweenPasses(t *testing.T) {
	broker, records := brokertest.New(t)
	s := &Service{Store: &store.Store{Broker: broker}}
	ctx := context.Background()
	ws := uuid.New()
	keys := s.passKeys(secretbroker.Request{Workspace: ws, Purpose: secretbroker.Inspect, Principal: secretbroker.Account(uuid.New()), Kinds: []secretbroker.Kind{secretbroker.SecretVersion}})
	passes := 0
	err := withProviderCalls(ctx, func() error {
		passes++
		session, err := keys.get()
		if err != nil {
			return err
		}
		if session.Workspace() != ws {
			t.Fatal("keys for another workspace")
		}
		keys.use()
		return nil
	})
	if err != nil || passes != 2 || len(records.Accesses) != 1 {
		t.Fatalf("passes %d, records %d, %v", passes, len(records.Accesses), err)
	}
	// A provider call, which holds nothing, can begin them itself, and reuses live ones.
	if _, err = keys.ensure(ctx); err != nil || len(records.Accesses) != 1 {
		t.Fatalf("ensure began again: %d, %v", len(records.Accesses), err)
	}
	keys.close(ctx)
	if len(records.Outcomes) != 1 || records.Outcomes[0].Outcome != string(secretbroker.Used) {
		t.Fatalf("outcomes: %+v", records.Outcomes)
	}
	// A refused request is not asked for again on every pass.
	refused := s.passKeys(secretbroker.Request{Workspace: ws, Purpose: "spy", Principal: secretbroker.Account(uuid.New()), Kinds: []secretbroker.Kind{secretbroker.SecretVersion}})
	err = withProviderCalls(ctx, func() error {
		_, err := refused.get()
		return err
	})
	var call *providerCall
	if err == nil || errors.As(err, &call) {
		t.Fatalf("a refused request: %v", err)
	}
	var none *passKeys
	none.close(ctx)
}

// Calls a pass joins, such as the key requests of several build targets, begin one session
// between them, not one each.
func TestJoinedKeyRequestsBeginOnce(t *testing.T) {
	broker, records := brokertest.New(t)
	s := &Service{Store: &store.Store{Broker: broker}}
	ctx := context.Background()
	keys := s.passKeys(secretbroker.Request{Workspace: uuid.New(), Purpose: secretbroker.Inspect, Principal: secretbroker.Account(uuid.New()), Kinds: []secretbroker.Kind{secretbroker.SecretVersion}})
	var joined *providerCall
	for range 3 {
		_, err := keys.get()
		var call *providerCall
		if !errors.As(err, &call) {
			t.Fatal("no call for keys:", err)
		}
		joined = joined.and(call)
	}
	joined.fetch(ctx)
	if _, err := keys.get(); err != nil || len(records.Accesses) != 1 {
		t.Fatalf("joined requests began %d sessions: %v", len(records.Accesses), err)
	}
	keys.close(ctx)
}
