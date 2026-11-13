package agent

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/agentidentity"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/randtoken"
)

// enrollCode exchanges one enrollment code. Whatever was first sent with a code, a key or a legacy
// bearer, is sent with every retry of it.
func (c *Client) enrollCode(ctx context.Context, code string) (api.AgentStatus, api.AgentCapabilities, bool, error) {
	var status api.AgentStatus
	var sent api.AgentCapabilities
	c.tokens.mu.Lock()
	defer c.tokens.mu.Unlock()
	cfg, err := c.Journal.Config()
	if err != nil {
		return status, sent, false, err
	}
	// Learn what this controller reads, keys included, before choosing what to send: an enrollment
	// replaces the capabilities management holds. Only a controller older than negotiation, which
	// has no challenge to answer, is sent one without hearing first.
	if err = c.negotiate(ctx); err != nil && !errors.Is(err, errPredatesNegotiation) {
		return status, sent, false, err
	}
	codeHash := agentidentity.HashHex(code)
	if cfg.Enrolled && cfg.EnrolledCode == codeHash {
		// Sending it with a new key would look, to management, like a second holder of the code.
		return status, sent, false, errors.New("this agent is already enrolled with this code; to enroll it again, create a new enrollment code")
	}
	entry, ok := cfg.Enrollments[codeHash]
	if !ok {
		keys := c.keyStore()
		if len(cfg.Enrollments) >= 4 {
			return status, sent, false, errors.New("four enrollment codes are already outstanding; use one of them, or remove the state directory and enroll this host as a new server")
		}
		entry = Enrollment{FirstSent: time.Now()}
		switch {
		case !cfg.Enrolled && cfg.Credential != "" && cfg.EnrollmentToken == code:
			// An earlier agent sent its credential with this code.
			entry.Credential = cfg.Credential
		case c.keysSupported.Load() && keys != nil:
			if entry.Key, err = c.newSealedKey(ctx); err != nil {
				return status, sent, false, err
			}
			entry.Keyed = true
		default:
			if entry.Credential, err = randtoken.New(); err != nil {
				return status, sent, false, err
			}
		}
		if keys != nil {
			if entry.SealedCode, err = keys.Seal(ctx, []byte(code)); err != nil {
				return status, sent, false, err
			}
		} else {
			entry.Code = code
		}
		if err = c.Journal.UpdateConfig(func(next *Config) error {
			if next.Enrollments == nil {
				next.Enrollments = map[string]Enrollment{}
			}
			if _, exists := next.Enrollments[codeHash]; !exists {
				next.Enrollments[codeHash] = entry
			}
			next.EnrollmentToken = ""
			return nil
		}); err != nil {
			return status, sent, false, err
		}
		if cfg, err = c.Journal.Config(); err != nil {
			return status, sent, false, err
		}
		entry = cfg.Enrollments[codeHash]
	}
	h := c.Observe(ctx)
	enrollment := api.AgentEnrollment{ServerId: cfg.ServerID, MachineId: cfg.MachineID, Token: code, Capabilities: h.Capabilities, Preflight: h.Preflight}
	if entry.Keyed {
		key, err := c.unseal(ctx, entry.Key)
		if err != nil {
			return status, sent, false, err
		}
		public, err := agentidentity.PublicKey(key)
		if err != nil {
			return status, sent, false, err
		}
		proof, err := agentidentity.Sign(key, agentidentity.EnrollMessage(cfg.ServerID, code, cfg.MachineID))
		if err != nil {
			return status, sent, false, err
		}
		enrollment.PublicKey, enrollment.Proof = &public, &proof
	} else {
		credential := entry.Credential
		enrollment.Credential = &credential
	}
	if cfg.ControllerGeneration > 0 {
		inventory, err := c.Journal.Inventory()
		if err != nil {
			return status, sent, false, err
		}
		enrollment.ControllerGeneration = &cfg.ControllerGeneration
		enrollment.Inventory = &inventory
	}
	sent = c.advertisedCapabilities(h.Capabilities)
	if err = c.call(ctx, "POST", "/agent/v1/enroll", enrollment, &status, ""); err != nil {
		if refusedAs(err, agentidentity.CodeNotAccepted) {
			if e := c.Journal.UpdateConfig(func(next *Config) error {
				delete(next.Enrollments, codeHash)
				return nil
			}); e != nil {
				return status, sent, false, errors.Join(err, e)
			}
			return status, sent, false, errors.New("management refused this enrollment code, which may have expired or already been used")
		}
		return status, sent, false, err
	}
	if enrollment.Inventory != nil {
		if err = c.Journal.acknowledgePreviewProofs(enrollment.Inventory.PreviewCleanups); err != nil {
			return status, sent, false, err
		}
	}
	if err = c.Journal.UpdateConfig(func(next *Config) error {
		if entry.Keyed {
			next.Key, next.Credential = entry.Key, ""
		} else {
			next.Key, next.Credential = nil, entry.Credential
		}
		next.PendingRotation, next.BindKey, next.Pending = nil, nil, ""
		next.Enrollments, next.EnrollmentToken, next.EnrolledCode = nil, "", codeHash
		next.PreviewOnly = status.PreviewOnly != nil && *status.PreviewOnly
		next.Role = status.Role
		next.Enrolled = true
		return nil
	}); err != nil {
		return status, sent, false, err
	}
	c.tokens.forget()
	c.needsEnrollment.Store(false)
	if entry.Keyed {
		if err = c.Journal.markKeyed(); err != nil {
			return status, sent, false, err
		}
	}
	return status, sent, true, nil
}

// retryEnrollments retries the outstanding codes, newest first.
func (c *Client) retryEnrollments(ctx context.Context) (api.AgentStatus, api.AgentCapabilities, bool, error) {
	// A code is sent with a key only if the store has opened, and whatever is first sent with a code
	// is sent with every retry of it. So nothing is retried until the store has been tried at
	// least once, and codes sealed by it aren't passed over while it opens.
	if !c.keyStoreSettled() {
		return api.AgentStatus{}, api.AgentCapabilities{}, false, errors.New("the agent's key store is still opening")
	}
	cfg, err := c.Journal.Config()
	if err != nil {
		return api.AgentStatus{}, api.AgentCapabilities{}, false, err
	}
	codes := []string{}
	if cfg.EnrollmentToken != "" {
		codes = append(codes, cfg.EnrollmentToken)
	}
	entries := make([]Enrollment, 0, len(cfg.Enrollments))
	for _, e := range cfg.Enrollments {
		entries = append(entries, e)
	}
	slices.SortFunc(entries, func(a, b Enrollment) int { return b.FirstSent.Compare(a.FirstSent) })
	for _, e := range entries {
		code := e.Code
		if e.SealedCode != nil {
			keys := c.keyStore()
			if keys == nil {
				continue
			}
			plain, err := keys.Unseal(ctx, e.SealedCode)
			if err != nil {
				return api.AgentStatus{}, api.AgentCapabilities{}, false, err
			}
			code = string(plain)
		}
		codes = append(codes, code)
	}
	if len(codes) == 0 {
		return api.AgentStatus{}, api.AgentCapabilities{}, false, errors.New("enroll this agent with a new code")
	}
	var last error
	for _, code := range codes {
		status, sent, ok, err := c.enrollCode(ctx, strings.TrimSpace(code))
		if ok {
			return status, sent, true, nil
		}
		last = err
	}
	return api.AgentStatus{}, api.AgentCapabilities{}, false, last
}
