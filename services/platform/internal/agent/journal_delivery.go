package agent

import "github.com/astilbahq/astilba/services/platform/internal/api"

// withoutDelivery is a command without what its delivery says about how it travelled rather than
// what it is: whether it was stopped, whether management's clock had passed its deadline, and
// whether it brought its values. The journal keeps the values it was first given.
func withoutDelivery(c api.ExecutionCommand) api.ExecutionCommand {
	c.CancelRequested, c.Expired, c.SecretRelease = false, nil, nil
	return c
}

// takeDelivery keeps what a later delivery of a journaled command says: a stop, management's
// verdict that its deadline has passed, and values the record asked for again. A stop or a passed
// deadline is never taken back.
func takeDelivery(r *Record, c api.ExecutionCommand) {
	r.Command.CancelRequested = r.Command.CancelRequested || c.CancelRequested
	if c.Expired != nil && *c.Expired {
		r.Command.Expired = c.Expired
	}
	takeReleasedValues(r, c)
}
