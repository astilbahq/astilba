package agent

import (
	"context"
	"errors"
	"os"
	"slices"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/google/uuid"
)

// acceptReleased journals a polled command and settles its secret release. A command that
// brings its values is acknowledged once it is journaled, so management does not send them
// again; one that comes without them is accepted only when the journal already holds it, and
// otherwise the values it lost are asked for again. So are the values of a command retried after
// it gave them up while it waited for attention (awaitValues).
func (c *Client) acceptReleased(ctx context.Context, command api.ExecutionCommand) error {
	release := command.SecretRelease
	if release != nil && !release.Values {
		if _, err := c.Journal.Record(command.Id); errors.Is(err, os.ErrNotExist) {
			return c.requestRedelivery(ctx, command.Id, release.Epoch)
		} else if err != nil {
			return err
		}
	}
	record, err := c.acceptPolledCommand(command)
	if err != nil {
		return err
	}
	if release != nil && !release.Values && record.Values == valuesWanted && !record.Command.CancelRequested {
		// The journal holds the command but not its values: the redelivery a lost record asks
		// for. Management's release checks decide again whether they may go.
		return c.requestRedelivery(ctx, command.Id, release.Epoch)
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

// requestRedelivery tells management that this instance's journal does not hold a command's values,
// so its next poll releases them again if they may still go.
func (c *Client) requestRedelivery(ctx context.Context, id uuid.UUID, epoch int64) error {
	notice := api.SecretReleaseNotice{Acknowledged: []api.SecretReleaseRef{}, Redeliver: []api.SecretRedelivery{{OperationId: id, Epoch: epoch, Reason: api.JournalMissing}}}
	return c.call(ctx, "POST", "/agent/v1/releases", notice, nil, authAgent)
}

const (
	valuesWantedNote  = "Waiting for management to release this command's repository access again. The agent removed it while the command waited for attention."
	valuesExpiredNote = "Cancelled. The agent removed this command's repository access while it waited for attention, and management releases it only before the command's deadline. Start it again."
)

// awaitValues reports whether a command must wait for values it gave up while it waited for
// attention, and asks for them: acceptReleased then requests them from management on each delivery
// until one brings them. Past its deadline, management releases nothing, so the command is
// cancelled instead.
func (e *Engine) awaitValues(c api.ExecutionCommand, r Record) (bool, error) {
	switch {
	case r.Values == "":
		return false, nil
	case time.Now().After(c.Deadline):
		return true, e.finish(c.Id, "cancelled", valuesExpiredNote)
	case r.Values == valuesWanted:
		return true, nil
	}
	return true, e.Journal.Mutate(c.Id, func(r *Record) {
		r.Values = valuesWanted
		r.Report.Note = valuesWantedNote
	})
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
