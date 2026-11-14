package integration

import (
	"context"
	"net/http"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
)

// Test agents acknowledge secret releases, as every current agent does, unless a test says
// otherwise by setting the capability itself.
func acknowledging(in api.AgentEnrollment) api.AgentEnrollment {
	in.Capabilities = acknowledgingCapabilities(in.Capabilities)
	return in
}

func acknowledgingHeartbeat(in api.AgentHeartbeat) api.AgentHeartbeat {
	in.Capabilities = acknowledgingCapabilities(in.Capabilities)
	return in
}

func acknowledgingCapabilities(c api.AgentCapabilities) api.AgentCapabilities {
	if c.AcknowledgesReleases == nil {
		yes := true
		c.AcknowledgesReleases = &yes
	}
	return c
}

// acknowledgeRelease tells management the agent journaled a command's released values.
func acknowledgeRelease(ctx context.Context, s *execution.Service, a execution.Identity, c *api.ExecutionCommand) error {
	if c == nil || c.SecretRelease == nil || !c.SecretRelease.Values {
		return nil
	}
	return s.NoticeSecretReleases(ctx, a, api.SecretReleaseNotice{Acknowledged: []api.SecretReleaseRef{{OperationId: c.Id, Epoch: c.SecretRelease.Epoch, Digest: c.SecretRelease.Digest}}, Redeliver: []api.SecretRedelivery{}})
}

// pollReleased polls as an agent that journals what it receives and acknowledges it. The
// acknowledgement is a request of its own, which a short poll's deadline does not cut off.
func pollReleased(s *execution.Service, ctx context.Context, a execution.Identity) (*api.ExecutionCommand, error) {
	c, err := s.Poll(ctx, a)
	if err == nil {
		err = acknowledgeRelease(context.WithoutCancel(ctx), s, a, c)
	}
	return c, err
}

func pollAgentReleased(s *execution.Service, ctx context.Context, a execution.Identity) (api.AgentPoll, error) {
	p, err := s.PollAgent(ctx, a)
	if err == nil {
		err = acknowledgeRelease(context.WithoutCancel(ctx), s, a, p.Command)
	}
	return p, err
}

// releaseCapable says, in an agent's enrollment or heartbeat, what an agent built from this code
// says: it acknowledges releases.
func releaseCapable(body any) any {
	switch in := body.(type) {
	case api.AgentEnrollment:
		return acknowledging(in)
	case api.AgentHeartbeat:
		return acknowledgingHeartbeat(in)
	}
	return body
}

// acknowledgePolled acknowledges the values a poll brought, as a real agent does once it has
// journaled them.
func acknowledgePolled(t *testing.T, c *http.Client, origin, path, token string, out any) {
	t.Helper()
	poll, ok := out.(*api.AgentPoll)
	if !ok || path != "/agent/v1/poll" || poll.Command == nil || poll.Command.SecretRelease == nil || !poll.Command.SecretRelease.Values || t.Failed() {
		return
	}
	r := poll.Command.SecretRelease
	agentRequest(t, c, origin, "/agent/v1/releases", token, api.SecretReleaseNotice{Acknowledged: []api.SecretReleaseRef{{OperationId: poll.Command.Id, Epoch: r.Epoch, Digest: r.Digest}}, Redeliver: []api.SecretRedelivery{}}, 204, nil)
}
