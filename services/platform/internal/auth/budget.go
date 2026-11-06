package auth

import (
	"context"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
)

const attemptWindow = 15 * time.Minute

// Source is where a request came from, at the two widths budgets group by. Key is an IPv4
// address or IPv6 /64. Network is the wider group one client can cheaply rotate within: the
// IPv6 /48, or for IPv4 the same address.
type Source struct {
	Key, Network string
}

func (from Source) principal() string { return "source:" + from.Network }

// budget caps attempts per window. Its scope decides who can refuse whom, because everyone who
// shares a budget is refused once any of them spends it. So nothing here is installation-wide.
// The one scope anyone can name is the account budget in loginAttempt, which bounds guessing
// and binds only browsers with no device token.
type budget struct {
	scope  string
	max    int32
	window time.Duration
}

func sourceBudgets(prefix string, from Source, max int32) []budget {
	budgets := []budget{{prefix + ":ip:" + from.Key, max, attemptWindow}}
	if from.Network != from.Key {
		budgets = append(budgets, budget{prefix + ":network:" + from.Network, 4 * max, attemptWindow})
	}
	return budgets
}

// refuseSpent refuses without writing, so a client whose budget is gone costs one read and
// takes no place in the queue for hashing.
func (s *Service) refuseSpent(ctx context.Context, budgets []budget) error {
	scopes := make([][]byte, len(budgets))
	for i, b := range budgets {
		scopes[i] = Hash(b.scope)
	}
	counts, err := s.Store.Queries.LiveAuthAttempts(ctx, scopes)
	if err != nil {
		return err
	}
	for _, count := range counts {
		for i, b := range budgets {
			if string(count.ScopeHash) == string(scopes[i]) && count.Attempts >= b.max {
				return domain.RateLimited
			}
		}
	}
	return nil
}

// charge spends one attempt from every budget, or from none if any is spent, so a refusal
// costs nothing. Budgets of the same kind are always listed in the same order, so concurrent
// charges take shared row locks in one order. Cleanup finishes first, so it holds none of them.
func (s *Service) charge(ctx context.Context, budgets []budget) error {
	if err := s.Store.Queries.PruneAuthAttempts(ctx); err != nil {
		return err
	}
	return s.Store.Transaction(ctx, func(q *db.Queries) error {
		for _, b := range budgets {
			if err := s.recordAttempt(ctx, q, b.scope, b.max, b.window); err != nil {
				return err
			}
		}
		return nil
	})
}

// refuseLink answers an invalid link. Only an attempt that will hash or that fails is charged,
// so a request refused for capacity with a valid link has cost nothing.
func (s *Service) refuseLink(ctx context.Context, budgets []budget) error {
	if err := s.charge(ctx, budgets); err != nil {
		return err
	}
	return domain.Credentials
}

// runCharged runs hashing work once it holds a slot, charging its budgets first. So an attempt
// refused for capacity has cost nothing, and replicas cannot exceed a budget together.
func (s *Service) runCharged(ctx context.Context, share passwordShare, budgets []budget, fn func() error) error {
	return s.Passwords.run(ctx, share, func() error {
		if err := s.charge(ctx, budgets); err != nil {
			return err
		}
		return fn()
	})
}

// withPassword is runCharged for a caller that has not checked its budgets yet. A spent budget
// refuses before the queue, so a client that has spent one takes no place in it.
func (s *Service) withPassword(ctx context.Context, share passwordShare, budgets []budget, fn func() error) error {
	if err := s.refuseSpent(ctx, budgets); err != nil {
		return err
	}
	return s.runCharged(ctx, share, budgets, fn)
}

func (s *Service) hashNewPassword(ctx context.Context, share passwordShare, budgets []budget, password string) (string, error) {
	var hash string
	err := s.runCharged(ctx, share, budgets, func() error {
		var err error
		hash, err = newPasswordHash(password)
		return err
	})
	return hash, err
}
