package agent

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"reflect"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/agentidentity"
	"github.com/astilbahq/astilba/services/platform/internal/api"
)

// hearFeatures reads what management says it reads. Every controller marks every agent response,
// whatever its status, so an extension is also forgotten after a controller is replaced or
// downgraded. A reverse proxy's own response lacks the mark and says nothing about management.
func (c *Client) hearFeatures(h http.Header) {
	if h.Get("Astilba-Agent-Features") == "" {
		return
	}
	c.buildControls.Store(h.Get("Astilba-Agent-Features") == "build-controls")
	c.buildCache.Store(h.Get("Astilba-Agent-Build-Cache") == "v1")
	c.buildNetwork.Store(h.Get("Astilba-Agent-Build-Network") == "v1")
	c.rootlessBuilds.Store(h.Get("Astilba-Agent-Rootless-Builds") == "v1")
	c.railpack.Store(h.Get("Astilba-Agent-Railpack") == "v1")
	c.safeReleases.Store(h.Get("Astilba-Agent-Safe-Releases") == "v1")
	c.serviceOutputs.Store(h.Get("Astilba-Agent-Service-Outputs") == "v1")
	c.previews.Store(h.Get("Astilba-Agent-Previews") == "v1")
	c.retirement.Store(h.Get("Astilba-Agent-Retirement") == "v1")
	c.schedules.Store(h.Get("Astilba-Agent-Schedules") == "v1")
	c.replicas.Store(h.Get("Astilba-Agent-Replicas") == "v1")
	c.crossHost.Store(h.Get("Astilba-Agent-Cross-Host") == "v1")
	c.maintenanceContinuation.Store(h.Get("Astilba-Agent-Maintenance-Continuation") == "v1")
	c.primaryIngress.Store(h.Get("Astilba-Agent-Primary-Ingress") == "v1")
	c.troubleshooting.Store(h.Get("Astilba-Agent-Troubleshooting") == "v2")
	c.machineRotation.Store(h.Get("Astilba-Agent-Machine-Rotation") == "v1")
	c.secretReleases.Store(h.Get("Astilba-Agent-Secret-Releases") == "v1")
	c.keysSupported.Store(h.Get(agentidentity.KeysHeader) == "v1")
	if c.Engine != nil {
		read := refusalsUnread
		if h.Get("Astilba-Agent-Network-Refusals") == "v1" {
			read = refusalsRead
		}
		c.Engine.networkRefusals.Store(read)
		reviews := refusalsUnread
		if h.Get("Astilba-Agent-Host-Changes") == "v1" {
			reviews = refusalsRead
		}
		c.Engine.hostChangeReviews.Store(reviews)
	}
	c.markNegotiated()
}

// errNotNegotiated says management has not yet answered this process.
var errNotNegotiated = errors.New("management has not yet said which agent capabilities it reads")

// errPredatesNegotiation says the controller has no challenge and marks nothing, as one older
// than negotiation does. It reads no extension.
var errPredatesNegotiation = fmt.Errorf("%w: the controller predates negotiation", errNotNegotiated)

func (c *Client) negotiatedSignal() chan struct{} {
	c.negotiatedInit.Do(func() { c.negotiatedNow = make(chan struct{}) })
	return c.negotiatedNow
}

func (c *Client) markNegotiated() {
	if c.negotiated.CompareAndSwap(false, true) {
		close(c.negotiatedSignal())
	}
}

// negotiate learns what management reads, if this process doesn't know yet. A challenge
// stores nothing at management and every controller marks its answer, whatever the status.
func (c *Client) negotiate(ctx context.Context) error {
	if c.negotiated.Load() {
		return nil
	}
	cfg, err := c.Journal.Config()
	if err != nil {
		return err
	}
	err = c.call(ctx, "POST", "/agent/v1/challenge", api.AgentChallengeRequest{ServerId: cfg.ServerID}, nil, "")
	if c.negotiated.Load() {
		return nil
	}
	if refused := (rejection{}); errors.As(err, &refused) && refused.status == http.StatusNotFound {
		return errPredatesNegotiation
	}
	if err != nil {
		return fmt.Errorf("%w: %w", errNotNegotiated, err)
	}
	return errNotNegotiated
}

// awaitNegotiation asks until management has answered, or another request hears it first. A
// controller older than negotiation answers no challenge, and is heard when a poll returns.
func (c *Client) awaitNegotiation(ctx context.Context) bool {
	delay := time.Second
	for c.negotiate(ctx) != nil {
		t := time.NewTimer(delay + time.Duration(rand.IntN(500))*time.Millisecond)
		select {
		case <-ctx.Done():
			t.Stop()
			return false
		case <-c.negotiatedSignal():
		case <-t.C:
			delay = min(delay*2, 30*time.Second)
		}
		t.Stop()
	}
	return true
}

func (c *Client) advertisedCapabilities(v api.AgentCapabilities) api.AgentCapabilities {
	if !c.crossHost.Load() {
		v.CrossHost = nil
	}
	if !c.maintenanceContinuation.Load() || !c.crossHost.Load() || c.Journal == nil || c.Journal.formatVersion.Load() < localStateVersion {
		v.MaintenanceContinuation = nil
	}
	if !c.replicas.Load() {
		v.Replicas = nil
	}
	if !c.primaryIngress.Load() || !c.crossHost.Load() {
		v.PrimaryIngress = nil
	}
	if !c.troubleshooting.Load() {
		v.Troubleshooting, v.TroubleshootingEnabled, v.TroubleshootingUnavailable = nil, nil, nil
	}
	if !c.schedules.Load() {
		v.Schedules = nil
	}
	if !c.previews.Load() {
		v.Previews = nil
	}
	if !c.retirement.Load() {
		v.Retirement = nil
	}
	if !c.serviceOutputs.Load() {
		v.ServiceOutputs = nil
	}
	if !c.safeReleases.Load() {
		v.SafeReleases = nil
	}
	if !c.buildControls.Load() {
		v.BuildControls = nil
	}
	if !c.buildCache.Load() {
		v.BuildCache = nil
	}
	if !c.buildNetwork.Load() {
		v.BuildNetwork = nil
	}
	if !c.rootlessBuilds.Load() {
		v.RootlessBuilds = nil
	}
	if !c.railpack.Load() {
		v.RailpackVersion = nil
	}
	v.KeyProtection, v.KeyStoreProblem = nil, nil
	if c.keysSupported.Load() {
		v.KeyProtection, v.KeyStoreProblem = c.keyProtection()
	}
	if !c.machineRotation.Load() {
		v.MachineBoundRotation = nil
	}
	if !c.secretReleases.Load() {
		v.AcknowledgesReleases = nil
	}
	return v
}

// Heartbeat reports this host once.
func (c *Client) Heartbeat(ctx context.Context) (api.AgentStatus, error) {
	var status api.AgentStatus
	err := c.heartbeat(ctx, authAgent, &status)
	return status, err
}

// heartbeat reports this host, once this process knows what management reads. A response that
// changes what this controller reads, as a controller replaced since the last answer may, is
// followed at once by another report, so management doesn't wait an interval to learn it.
func (c *Client) heartbeat(ctx context.Context, credential string, status *api.AgentStatus) error {
	if err := c.negotiate(ctx); err != nil {
		return err
	}
	h := c.Observe(ctx)
	sent := c.advertisedCapabilities(h.Capabilities)
	if err := c.call(ctx, "POST", "/agent/v1/heartbeat", h, status, credential); err != nil {
		return err
	}
	if reflect.DeepEqual(sent, c.advertisedCapabilities(h.Capabilities)) {
		return nil
	}
	return c.call(ctx, "POST", "/agent/v1/heartbeat", h, status, credential)
}
