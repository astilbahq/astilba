package auth

import (
	"context"

	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
	"github.com/google/uuid"
)

// secondFactor gives out the keys to one account's own second factor, recorded first. It runs
// before the transaction that uses them.
func (s *Service) secondFactor(ctx context.Context, account uuid.UUID, purpose secretbroker.Purpose, kinds ...secretbroker.Kind) (*secretbroker.Session, error) {
	items := []secretbroker.Item{}
	for _, kind := range kinds {
		items = append(items, secretbroker.Item{Kind: kind, ID: account})
	}
	return s.Store.Broker.Begin(ctx, secretbroker.Request{Purpose: purpose, Principal: secretbroker.Account(account), Items: items})
}
