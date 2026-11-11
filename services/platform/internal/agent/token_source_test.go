package agent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/agentidentity"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
)

// sessionController answers an agent's session requests as management would, with each refresh
// answered as the test says.
type sessionController struct {
	mu        sync.Mutex
	refresh   func() (status int, code string, keys bool)
	refreshes int
	starts    int
}

func (m *sessionController) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	token := func() {
		w.Header().Set("Astilba-Agent-Features", "none")
		w.Header().Set(agentidentity.KeysHeader, "v1")
		out := api.AgentToken{Token: agentidentity.TokenPrefix + strings.Repeat("t", 43), Refresh: strings.Repeat("r", 43), ExpiresIn: 900, Role: "deployment"}
		_ = stablejson.Encode(w, out)
	}
	switch r.URL.Path {
	case "/agent/v1/token/refresh":
		m.refreshes++
		status, code, keys := m.refresh()
		if status == 200 {
			token()
			return
		}
		if keys {
			w.Header().Set("Astilba-Agent-Features", "none")
			w.Header().Set(agentidentity.KeysHeader, "v1")
		}
		w.WriteHeader(status)
		_ = stablejson.Encode(w, map[string]string{"code": code})
	case "/agent/v1/challenge":
		w.Header().Set("Astilba-Agent-Features", "none")
		w.Header().Set(agentidentity.KeysHeader, "v1")
		_ = stablejson.Encode(w, api.AgentChallenge{Nonce: "nonce", ExpiresAt: time.Now().Add(time.Minute)})
	case "/agent/v1/token":
		m.starts++
		token()
	default:
		w.WriteHeader(404)
	}
}

func (m *sessionController) counts() (int, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.refreshes, m.starts
}

func keyedClient(t *testing.T) (*Client, *sessionController) {
	t.Helper()
	j, _ := testVisualJournal(t)
	m := &sessionController{}
	server := httptest.NewServer(m)
	t.Cleanup(server.Close)
	keys := InsecureKeyStore()
	key, err := agentidentity.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	der, err := agentidentity.MarshalPrivate(key)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := keys.Seal(context.Background(), der)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := j.Config()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Origin, cfg.Key, cfg.Enrolled = server.URL, sealed, true
	if err = j.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	c := NewClient(j, &Engine{Journal: j})
	c.HTTP, c.Keys = server.Client(), keys
	return c, m
}

func TestRefreshKeepsItsSecretUntilManagementRefusesIt(t *testing.T) {
	c, m := keyedClient(t)
	ctx := context.Background()
	// A session whose token has expired during an outage.
	c.tokens.token, c.tokens.refresh = agentidentity.TokenPrefix+strings.Repeat("o", 43), "first"
	c.tokens.renewAt, c.tokens.expires = time.Now().Add(-time.Minute), time.Now().Add(-time.Second)
	for _, failure := range []struct {
		status int
		keys   bool
	}{{503, true}, {502, false}, {401, false}} {
		m.refresh = func() (int, string, bool) { return failure.status, "unavailable", failure.keys }
		if _, err := c.authorization(ctx); err == nil {
			t.Fatal("an expired token was used", failure.status)
		}
		if c.tokens.refresh != "first" {
			t.Fatal("a failure management didn't answer dropped the refresh secret", failure.status, failure.keys)
		}
	}
	if _, starts := m.counts(); starts != 0 {
		t.Fatal("an outage started a new session, which ends attached terminal sessions")
	}
	m.refresh = func() (int, string, bool) { return 200, "", true }
	if token, err := c.authorization(ctx); err != nil || !strings.HasPrefix(token, agentidentity.TokenPrefix+"t") {
		t.Fatal("the kept secret didn't renew the session", err)
	}
	// Management's own refusal, such as a secret left unused too long, ends the session.
	c.tokens.renewAt = time.Now().Add(-time.Second)
	m.refresh = func() (int, string, bool) { return 401, agentidentity.RefreshReused, true }
	if _, err := c.authorization(ctx); err != nil {
		t.Fatal(err)
	}
	if refreshes, starts := m.counts(); starts != 1 || refreshes != 5 {
		t.Fatal("a refused secret didn't start a new session once", refreshes, starts)
	}
}

func TestUnavailableKeyStoreIsOpenedAgain(t *testing.T) {
	c := NewClient(nil, nil)
	opened := 0
	failure := errors.New("the TPM isn't ready")
	c.OpenKeys = func(context.Context) (KeyStore, error) {
		opened++
		if opened == 1 {
			return nil, failure
		}
		return InsecureKeyStore(), nil
	}
	ctx := context.Background()
	if c.OpenKeyStore(ctx) != nil || opened != 1 {
		t.Fatal("an unavailable store was reported open", opened)
	}
	// Requests only read the store, so none waits on opening it or caches a failure.
	if c.keyStore() != nil || opened != 1 {
		t.Fatal("a request opened the key store", opened)
	}
	if p, problem := c.keyProtection(); p == nil || *p != api.SealFailed || problem == nil || *problem != failure.Error() {
		t.Fatal("the agent didn't report why its key store is unavailable")
	}
	if c.OpenKeyStore(ctx) == nil || c.keyStore() == nil || opened != 2 {
		t.Fatal("the store wasn't opened again", opened)
	}
	if c.OpenKeyStore(ctx) == nil || opened != 2 {
		t.Fatal("an open store was opened again", opened)
	}
	if p, problem := c.keyProtection(); p == nil || *p != api.Unsealed || problem != nil {
		t.Fatal("the agent still reports a problem with a store that opened")
	}
	setup := NewClient(nil, nil)
	setup.OpenKeys = func(context.Context) (KeyStore, error) { return nil, errKeyStoreSetup }
	setup.OpenKeyStore(ctx)
	if p, _ := setup.keyProtection(); p == nil || *p != api.SetupRequired {
		t.Fatal("a host without systemd's host key wasn't reported as needing setup")
	}
}
