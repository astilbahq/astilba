package auth

import (
	"context"
	"github.com/alexedwards/argon2id"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
)

type Passwords struct {
	slots chan struct{}
	Dummy string
}

var passwordParams = argon2id.Params{Memory: 64 * 1024, Iterations: 3, Parallelism: 2, SaltLength: 16, KeyLength: 32}

func NewPasswords() (*Passwords, error) {
	p := &Passwords{slots: make(chan struct{}, 2)}
	var err error
	p.Dummy, err = p.Hash(context.Background(), "unused random-account comparison password")
	return p, err
}
func (p *Passwords) slot(ctx context.Context, fn func() error) error {
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
		return fn()
	case <-ctx.Done():
		return ctx.Err()
	default:
		return domain.RateLimited
	}
}
func (p *Passwords) Hash(ctx context.Context, password string) (string, error) {
	if err := domain.Password(password); err != nil {
		return "", err
	}
	var hash string
	err := p.slot(ctx, func() error { var err error; hash, err = argon2id.CreateHash(password, &passwordParams); return err })
	return hash, err
}
func (p *Passwords) Compare(ctx context.Context, password, hash string) error {
	if len(password) > 512 {
		return domain.Credentials
	}
	return p.slot(ctx, func() error {
		ok, err := argon2id.ComparePasswordAndHash(password, hash)
		if err != nil || !ok {
			return domain.Credentials
		}
		return nil
	})
}
