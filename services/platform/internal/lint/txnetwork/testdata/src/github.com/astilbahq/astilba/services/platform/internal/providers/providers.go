package providers

import (
	"github.com/astilbahq/astilba/services/platform/internal/applicationdns"
	"github.com/astilbahq/astilba/services/platform/internal/hosting"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type service struct {
	stripe hosting.Stripe
	dns    *applicationdns.Cloudflare
	pool   *pgxpool.Pool
}

func scope(fn func(pgx.Tx) error) error { return nil }

// Stripe asked under the workspace lock, as the webhook once did.
func (s *service) stripeUnderScope() error {
	return scope(func(tx pgx.Tx) error {
		_, err := s.stripe.Subscription("sub_1") // want `contacts a Git provider, GitHub, Stripe`
		return err
	})
}

func (s *service) stripeThroughAPointer(tx pgx.Tx) error {
	_, err := (&s.stripe).Portal() // want `contacts a Git provider, GitHub, Stripe`
	return err
}

func (s *service) observe() (string, error) { return s.stripe.Subscription("sub_1") }

func (s *service) stripeThroughAHelper(tx pgx.Tx) error {
	_, err := s.observe() // want `contacts a Git provider, GitHub, Stripe`
	return err
}

// The fetch-then-apply shape: ask first, then decide under the lock.
func (s *service) stripeBeforeTheScope() error {
	seen, err := s.observe()
	if err != nil {
		return err
	}
	return scope(func(tx pgx.Tx) error {
		_ = seen
		return tx.Commit()
	})
}

func (s *service) entitlementIsLocal(tx pgx.Tx) string {
	return hosting.Subscription{}.Entitlement()
}

// The shape of the DNS defect: a session lock on a pinned connection held across the call.
func (s *service) dnsOnAPinnedConnection() error {
	conn, err := s.pool.Acquire()
	if err != nil {
		return err
	}
	defer conn.Release()
	if err = conn.Exec(); err != nil {
		return err
	}
	return s.dns.Reconcile("*.example") // want `contacts a Git provider, GitHub, Stripe, the DNS provider`
}

func (s *service) dnsWithAConnectionParameter(conn *pgxpool.Conn) error {
	return s.dns.Remove("*.example") // want `the DNS provider`
}

func (s *service) dnsCapturingAConnection(conn *pgxpool.Conn, run func(func() error) error) error {
	return run(func() error {
		_ = conn.Exec()
		return s.dns.Remove("*.example") // want `the DNS provider`
	})
}

func (s *service) dnsUnderTransaction(tx pgx.Tx) error {
	return s.dns.Reconcile("*.example") // want `the DNS provider`
}

// The lease shape: each statement takes a connection from the pool and returns it at once.
func (s *service) dnsOnThePool() error {
	if err := s.pool.Exec(); err != nil {
		return err
	}
	if err := s.dns.Reconcile("*.example"); err != nil {
		return err
	}
	return s.pool.Exec()
}

// A connection taken after the call holds nothing during it.
func (s *service) dnsBeforeAcquiring() error {
	if err := s.dns.Reconcile("*.example"); err != nil {
		return err
	}
	conn, err := s.pool.Acquire()
	if err != nil {
		return err
	}
	conn.Release()
	return nil
}
