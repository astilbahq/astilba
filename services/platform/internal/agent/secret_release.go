package agent

import (
	"context"
	"errors"
	"os"
	"slices"

	"github.com/astilbahq/astilba/services/platform/internal/api"
)

// acceptReleased journals a polled command and settles its secret release. A command that
// brings its values is acknowledged once it is journaled, so management does not send them
// again; one that comes without them is accepted only when the journal already holds it, and
// otherwise the values it lost are asked for again.
func (c *Client) acceptReleased(ctx context.Context, command api.ExecutionCommand) error {
	release := command.SecretRelease
	if release != nil && !release.Values {
		if _, err := c.Journal.Record(command.Id); errors.Is(err, os.ErrNotExist) {
			notice := api.SecretReleaseNotice{Acknowledged: []api.SecretReleaseRef{}, Redeliver: []api.SecretRedelivery{{OperationId: command.Id, Epoch: release.Epoch, Reason: api.JournalMissing}}}
			return c.call(ctx, "POST", "/agent/v1/releases", notice, nil, authAgent)
		} else if err != nil {
			return err
		}
	}
	if _, err := c.acceptPolledCommand(command); err != nil {
		return err
	}
	if release != nil && release.Values {
		ref := api.SecretReleaseRef{OperationId: command.Id, Epoch: release.Epoch, Digest: release.Digest}
		if !slices.Contains(c.unacknowledged, ref) {
			c.unacknowledged = append(c.unacknowledged, ref)
		}
	}
	// The command is journaled and runs whatever happens to the acknowledgement, which is
	// sent again on the next poll if this one fails.
	_ = c.acknowledgeReleases(ctx)
	return nil
}

// acknowledgementBatch is the most releases one notice may acknowledge.
const acknowledgementBatch = 64

// acknowledgeReleases tells management which releases this instance has journaled. A batch
// management finds invalid is dropped rather than sent forever: its releases are made again
// after their lease, or the agent asks for them.
func (c *Client) acknowledgeReleases(ctx context.Context) error {
	for len(c.unacknowledged) > 0 {
		batch := c.unacknowledged[:min(len(c.unacknowledged), acknowledgementBatch)]
		notice := api.SecretReleaseNotice{Acknowledged: batch, Redeliver: []api.SecretRedelivery{}}
		err := c.call(ctx, "POST", "/agent/v1/releases", notice, nil, authAgent)
		var refused rejection
		// Only management's own refusal says the batch is invalid; a proxy's says nothing.
		if err != nil && (!errors.As(err, &refused) || !refused.keys || refused.status != 400) {
			return err
		}
		c.unacknowledged = c.unacknowledged[len(batch):]
	}
	return nil
}
