package agent

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/agentidentity"
	"github.com/astilbahq/astilba/services/platform/internal/api"
)

// authAgent asks call to authenticate as this server: with an access token when the agent holds
// a key, otherwise with its legacy bearer.
const authAgent = "\x00agent"

// rejection is a response refusing a request.
type rejection struct {
	status int
	code   string
	// keys says the answer came from a controller that authenticates agent keys. A proxy's own
	// answer, or an older controller's, carries no such mark, and its code means nothing.
	keys bool
}

func (rejection) Error() string {
	return "management rejected request; check enrollment, credentials and operation status"
}

// refusedAs reports management's own refusal for one reason.
func refusedAs(err error, code string) bool {
	var r rejection
	return errors.As(err, &r) && r.keys && r.status == 401 && r.code == code
}

// errKeyNotCurrent says management holds none of the keys this agent has. Only an outstanding
// enrollment code can bring it back.
var errKeyNotCurrent = errors.New("management holds none of this agent's keys; enroll it again with a new code")

// tokenSource holds this process's session. One lock serves every goroutine, and enrollment,
// binding, rotation, refresh and session starts all run under it, so none of them can undo
// another's journal changes or race it at management.
type tokenSource struct {
	mu      sync.Mutex
	token   string
	refresh string
	renewAt time.Time
	expires time.Time
	// keys caches unsealed private keys by their sealed form, so systemd-creds runs once each.
	keys map[string]*ecdsa.PrivateKey
}

func (t *tokenSource) forget() {
	t.token, t.refresh, t.renewAt, t.expires = "", "", time.Time{}, time.Time{}
}

func (c *Client) unseal(ctx context.Context, sealed []byte) (*ecdsa.PrivateKey, error) {
	if k, ok := c.tokens.keys[string(sealed)]; ok {
		return k, nil
	}
	keys := c.keyStore()
	if keys == nil {
		return nil, errors.New("this agent holds a key but its key store is unavailable")
	}
	der, err := keys.Unseal(ctx, sealed)
	if err != nil {
		return nil, err
	}
	k, err := agentidentity.ParsePrivate(der)
	if err != nil {
		return nil, err
	}
	if c.tokens.keys == nil {
		c.tokens.keys = map[string]*ecdsa.PrivateKey{}
	}
	c.tokens.keys[string(sealed)] = k
	return k, nil
}

func (c *Client) newSealedKey(ctx context.Context) ([]byte, error) {
	keys := c.keyStore()
	if keys == nil {
		return nil, errors.New("this agent can't seal a key here")
	}
	k, err := agentidentity.GenerateKey()
	if err != nil {
		return nil, err
	}
	der, err := agentidentity.MarshalPrivate(k)
	if err != nil {
		return nil, err
	}
	return keys.Seal(ctx, der)
}

// bindable reports whether a legacy agent should replace its bearer with a key now.
func (c *Client) bindable(ctx context.Context, cfg Config) bool {
	return cfg.Enrolled && cfg.Key == nil && cfg.BindKey == nil && cfg.Credential != "" && cfg.Pending == "" &&
		c.keysSupported.Load() && c.keyStore() != nil
}

// authorization returns the credential for this server's next request.
func (c *Client) authorization(ctx context.Context) (string, error) {
	cfg, err := c.Journal.Config()
	if err != nil {
		return "", err
	}
	if cfg.Key == nil && cfg.BindKey == nil && !c.bindable(ctx, cfg) {
		return cfg.Credential, nil
	}
	c.tokens.mu.Lock()
	defer c.tokens.mu.Unlock()
	if c.tokens.token != "" && time.Now().Before(c.tokens.renewAt) {
		return c.tokens.token, nil
	}
	if c.tokens.refresh != "" {
		err := c.refreshToken(ctx)
		// A refresh that also rotated the key may end without a session; one then starts below.
		// One that failed without management refusing it keeps the token while it is valid.
		if c.tokens.token != "" && (err == nil || time.Now().Add(time.Minute).Before(c.tokens.expires)) {
			return c.tokens.token, nil
		}
		// Management didn't refuse the secret, so it is tried again rather than starting a new
		// session, which would end the terminal sessions attached through this one. Management
		// refuses a secret left unused for long, and the agent then starts a new session.
		if err != nil && c.tokens.refresh != "" {
			return "", err
		}
	}
	c.tokens.forget()
	if err = c.startSession(ctx); err != nil {
		return "", err
	}
	if c.tokens.token == "" {
		return "", errors.New("management started no session")
	}
	return c.tokens.token, nil
}

// invalidate drops a token management refused, so the next request starts a session.
func (c *Client) invalidate(token string) {
	c.tokens.mu.Lock()
	defer c.tokens.mu.Unlock()
	if c.tokens.token == token {
		c.tokens.forget()
	}
}

// accept holds a new session. Its lifetime counts from when the answer arrived, by this host's
// clock, so a host whose clock is wrong still renews on time.
func (c *Client) accept(out api.AgentToken) {
	now, lifetime := time.Now(), time.Duration(out.ExpiresIn)*time.Second
	c.tokens.token, c.tokens.refresh = out.Token, out.Refresh
	c.tokens.expires, c.tokens.renewAt = now.Add(lifetime), now.Add(min(10*time.Minute, lifetime*2/3))
}

// refreshToken renews the session. Management's own refusal ends it: the next request starts a new
// one, which management never takes for a fork. After any other failure the secret is kept and
// tried again, whatever the token's expiry; if management did renew it and the answer was lost,
// that try is refused.
func (c *Client) refreshToken(ctx context.Context) error {
	cfg, err := c.Journal.Config()
	if err != nil {
		return err
	}
	if cfg.Key == nil {
		c.tokens.forget()
		return errors.New("no key")
	}
	key, err := c.unseal(ctx, cfg.Key)
	if err != nil {
		return err
	}
	signature, err := agentidentity.Sign(key, agentidentity.RefreshMessage(cfg.ServerID, c.tokens.refresh))
	if err != nil {
		return err
	}
	var out api.AgentToken
	if err = c.call(ctx, "POST", "/agent/v1/token/refresh", api.AgentRefreshRequest{ServerId: cfg.ServerID, Refresh: c.tokens.refresh, Signature: signature}, &out, ""); err != nil {
		if refused := (rejection{}); errors.As(err, &refused) && refused.keys && refused.status == 401 {
			c.tokens.forget()
		}
		return err
	}
	c.accept(out)
	c.maybeRotate(ctx, out)
	return nil
}

// coldStart starts a session with one key.
func (c *Client) coldStart(ctx context.Context, cfg Config, sealed []byte) (api.AgentToken, error) {
	var out api.AgentToken
	key, err := c.unseal(ctx, sealed)
	if err != nil {
		return out, err
	}
	public, err := agentidentity.PublicKey(key)
	if err != nil {
		return out, err
	}
	for attempt := 0; ; attempt++ {
		var challenge api.AgentChallenge
		if err = c.call(ctx, "POST", "/agent/v1/challenge", api.AgentChallengeRequest{ServerId: cfg.ServerID}, &challenge, ""); err != nil {
			return out, err
		}
		counter, err := c.Journal.NextStartCounter()
		if err != nil {
			return out, err
		}
		signature, err := agentidentity.Sign(key, agentidentity.TokenMessage(cfg.ServerID, challenge.Nonce, counter))
		if err != nil {
			return out, err
		}
		err = c.call(ctx, "POST", "/agent/v1/token", api.AgentTokenRequest{ServerId: cfg.ServerID, PublicKey: public, Nonce: challenge.Nonce, StartCounter: counter, Signature: signature}, &out, "")
		if attempt < 2 && (refusedAs(err, agentidentity.NonceInvalid) || refusedAs(err, agentidentity.CounterStale)) {
			continue
		}
		return out, err
	}
}

// startSession starts a session with the newest key management may hold: a rotation's successor,
// then a bind's key, then the working key. It falls back only on management's own answer that a
// key isn't current, and it never presents a bearer once a bind has been sent, except to retry it.
func (c *Client) startSession(ctx context.Context) error {
	cfg, err := c.Journal.Config()
	if err != nil {
		return err
	}
	if c.bindable(ctx, cfg) {
		if err = c.bind(ctx); err != nil {
			return err
		}
		if cfg, err = c.Journal.Config(); err != nil {
			return err
		}
	}
	if cfg.PendingRotation != nil {
		out, err := c.coldStart(ctx, cfg, cfg.PendingRotation)
		if err == nil {
			if err = c.promote(func(next *Config) bool {
				if !slices.Equal(next.PendingRotation, cfg.PendingRotation) {
					return false
				}
				next.Key, next.PendingRotation = next.PendingRotation, nil
				return true
			}); err != nil {
				return err
			}
			c.accept(out)
			return nil
		}
		if !refusedAs(err, agentidentity.KeyNotCurrent) {
			return err
		}
	}
	if cfg.BindKey != nil {
		out, err := c.coldStart(ctx, cfg, cfg.BindKey)
		if refusedAs(err, agentidentity.KeyNotCurrent) {
			// The bind never committed: retry it, with the same key, then start the session.
			if err = c.sendBind(ctx, cfg); err != nil {
				return err
			}
			out, err = c.coldStart(ctx, cfg, cfg.BindKey)
		}
		if err != nil {
			return err
		}
		if err = c.promote(func(next *Config) bool {
			if !slices.Equal(next.BindKey, cfg.BindKey) {
				return false
			}
			next.Key, next.BindKey, next.Credential = next.BindKey, nil, ""
			return true
		}); err != nil {
			return err
		}
		if err = c.Journal.markKeyed(); err != nil {
			return err
		}
		c.accept(out)
		return nil
	}
	if cfg.Key == nil {
		return errKeyNotCurrent
	}
	out, err := c.coldStart(ctx, cfg, cfg.Key)
	if refusedAs(err, agentidentity.KeyNotCurrent) {
		c.needsEnrollment.Store(true)
		return errKeyNotCurrent
	}
	if err != nil {
		return err
	}
	c.accept(out)
	c.maybeRotate(ctx, out)
	return nil
}

func (c *Client) promote(change func(*Config) bool) error {
	return c.Journal.UpdateConfig(func(cfg *Config) error {
		if !change(cfg) {
			return errors.New("agent identity changed while a session was starting")
		}
		return nil
	})
}

// bind replaces a legacy bearer with a key. The key is stored beside the bearer before the
// request, so a lost answer is retried with the same key.
func (c *Client) bind(ctx context.Context) error {
	sealed, err := c.newSealedKey(ctx)
	if err != nil {
		return err
	}
	if err = c.Journal.UpdateConfig(func(cfg *Config) error {
		if cfg.BindKey == nil {
			cfg.BindKey = sealed
		}
		return nil
	}); err != nil {
		return err
	}
	cfg, err := c.Journal.Config()
	if err != nil {
		return err
	}
	return c.sendBind(ctx, cfg)
}

func (c *Client) sendBind(ctx context.Context, cfg Config) error {
	key, err := c.unseal(ctx, cfg.BindKey)
	if err != nil {
		return err
	}
	public, err := agentidentity.PublicKey(key)
	if err != nil {
		return err
	}
	_, der, err := agentidentity.ParsePublicKey(public)
	if err != nil {
		return err
	}
	proof, err := agentidentity.Sign(key, agentidentity.BindMessage(cfg.ServerID, cfg.MachineID, agentidentity.KeyHash(der)))
	if err != nil {
		return err
	}
	return c.call(ctx, "POST", "/agent/v1/bind", api.AgentKeyBind{PublicKey: public, MachineId: cfg.MachineID, Proof: proof}, nil, cfg.Credential)
}

// maybeRotate rotates the key when management asks: with the successor already stored, if an
// earlier rotation never reached management, or with a new one. Management asks only while no
// terminal session would end. A failure leaves the successor stored, to be retried when management
// next asks. If the rotation reached management but no session started with the successor, the
// old key's session has ended and the next request starts one.
func (c *Client) maybeRotate(ctx context.Context, out api.AgentToken) {
	if !out.RotateKey {
		return
	}
	cfg, err := c.Journal.Config()
	if err != nil || cfg.Key == nil || c.keyStore() == nil {
		return
	}
	if cfg.PendingRotation != nil {
		_ = c.sendRotation(ctx, cfg, cfg.PendingRotation)
		return
	}
	sealed, err := c.newSealedKey(ctx)
	if err != nil {
		return
	}
	if err = c.Journal.UpdateConfig(func(next *Config) error {
		if next.PendingRotation == nil {
			next.PendingRotation = sealed
		}
		return nil
	}); err != nil {
		return
	}
	if cfg, err = c.Journal.Config(); err != nil {
		return
	}
	_ = c.sendRotation(ctx, cfg, cfg.PendingRotation)
}

// sendRotation registers a successor key, signed by both keys, then starts a session with it.
func (c *Client) sendRotation(ctx context.Context, cfg Config, sealed []byte) error {
	current, err := c.unseal(ctx, cfg.Key)
	if err != nil {
		return err
	}
	next, err := c.unseal(ctx, sealed)
	if err != nil {
		return err
	}
	public, err := agentidentity.PublicKey(next)
	if err != nil {
		return err
	}
	_, der, err := agentidentity.ParsePublicKey(public)
	if err != nil {
		return err
	}
	var challenge api.AgentChallenge
	if err = c.call(ctx, "POST", "/agent/v1/challenge", api.AgentChallengeRequest{ServerId: cfg.ServerID}, &challenge, ""); err != nil {
		return err
	}
	keyHash := agentidentity.KeyHash(der)
	oldSignature, err := agentidentity.Sign(current, agentidentity.KeysMessage(false, cfg.ServerID, challenge.Nonce, keyHash))
	if err != nil {
		return err
	}
	newSignature, err := agentidentity.Sign(next, agentidentity.KeysMessage(true, cfg.ServerID, challenge.Nonce, keyHash))
	if err != nil {
		return err
	}
	if err = c.call(ctx, "POST", "/agent/v1/keys", api.AgentKeyRotation{PublicKey: public, Nonce: challenge.Nonce, OldSignature: oldSignature, NewSignature: newSignature}, nil, c.tokens.token); err != nil {
		return err
	}
	// The old key's tokens ended with the rotation.
	c.tokens.forget()
	out, err := c.coldStart(ctx, cfg, sealed)
	if err != nil {
		return err
	}
	if err = c.promote(func(next *Config) bool {
		if !slices.Equal(next.PendingRotation, sealed) {
			return false
		}
		next.Key, next.PendingRotation = sealed, nil
		return true
	}); err != nil {
		return err
	}
	c.accept(out)
	return nil
}
