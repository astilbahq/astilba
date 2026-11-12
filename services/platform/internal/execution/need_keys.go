package execution

import (
	"context"

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
}

func (s *Service) passKeys(r secretbroker.Request) *passKeys { return &passKeys{service: s, request: r} }

func (k *passKeys) get() (*secretbroker.Session, error) {
	if k.err != nil {
		return nil, k.err
	}
	if k.session != nil && k.session.Live() {
		return k.session, nil
	}
	return nil, &providerCall{fetch: func(ctx context.Context) {
		if k.session != nil {
			k.session.Close(ctx, secretbroker.UsedIf(k.used))
		}
		k.session, k.err = k.service.Store.Broker.Begin(ctx, k.request)
		k.used = false
	}}
}

// use marks the current session as having done its work.
func (k *passKeys) use() { k.used = true }

// close records how the last session ended. Call it once the passes are over.
func (k *passKeys) close(ctx context.Context) {
	if k.session != nil {
		k.session.Close(ctx, secretbroker.UsedIf(k.used))
	}
}
