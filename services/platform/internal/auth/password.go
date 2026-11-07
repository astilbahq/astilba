package auth

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/alexedwards/argon2id"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
)

const (
	// Each running hash takes 64 MiB, so this bounds a replica's memory for password work.
	passwordSlots = 2
	// How many requests may wait in each class, and how long one may wait, before a
	// refusal tells the client to retry. The wait must suit one hash at a time on a small
	// host and must not hold a request longer than a person will watch a sign-in form.
	passwordQueue = 32
	passwordWait  = 5 * time.Second
	// How many requests one principal may have running or waiting. Several people can share
	// an address, so this is more than one; only one of them runs at a time.
	passwordPrincipalRequests = 4
)

// passwordShare is who a hash is done for. No principal runs two hashes at once, so one can
// never hold every slot. A proven request has shown something an anonymous client cannot
// choose, such as a live session or a valid link, and its own budgets bound it, so it goes
// ahead of anonymous requests.
type passwordShare struct {
	principal string
	proven    bool
}

type passwordWaiter struct {
	share passwordShare
	ready chan struct{}
}

// Passwords limits one replica's password hashing. Budgets, which PostgreSQL holds for all
// replicas, decide who may try; this only decides when an admitted attempt runs.
type Passwords struct {
	Dummy string

	mu       sync.Mutex
	running  map[string]struct{}
	requests map[string]int
	waiting  [2][]*passwordWaiter
}

var passwordParams = argon2id.Params{Memory: 64 * 1024, Iterations: 3, Parallelism: 2, SaltLength: 16, KeyLength: 32}

// dummyHash only has to cost what comparing a real hash costs, so one process needs one.
var dummyHash = sync.OnceValues(func() (string, error) {
	return newPasswordHash("unused random-account comparison password")
})

func NewPasswords() (*Passwords, error) {
	p := &Passwords{running: map[string]struct{}{}, requests: map[string]int{}}
	var err error
	p.Dummy, err = dummyHash()
	return p, err
}

func (s passwordShare) class() int {
	if s.proven {
		return 0
	}
	return 1
}

// dispatch starts the oldest waiter of the highest class whose principal has nothing running.
func (p *Passwords) dispatch() {
	for len(p.running) < passwordSlots {
		var next *passwordWaiter
		for class := range p.waiting {
			at := slices.IndexFunc(p.waiting[class], func(w *passwordWaiter) bool {
				_, busy := p.running[w.share.principal]
				return !busy
			})
			if at >= 0 {
				next = p.waiting[class][at]
				p.waiting[class] = slices.Delete(p.waiting[class], at, at+1)
				break
			}
		}
		if next == nil {
			return
		}
		p.running[next.share.principal] = struct{}{}
		close(next.ready)
	}
}

func (p *Passwords) leave(principal string) {
	if p.requests[principal]--; p.requests[principal] == 0 {
		delete(p.requests, principal)
	}
}

func (p *Passwords) release(share passwordShare) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.running, share.principal)
	p.leave(share.principal)
	p.dispatch()
}

// abandon reports whether the waiter left the queue. If it did not, it has already been given
// a slot and the caller must release that.
func (p *Passwords) abandon(w *passwordWaiter) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	class := w.share.class()
	at := slices.Index(p.waiting[class], w)
	if at < 0 {
		return false
	}
	p.waiting[class] = slices.Delete(p.waiting[class], at, at+1)
	p.leave(w.share.principal)
	return true
}

// acquire answers a client that has gone away the same as one refused for time, since nobody
// reads the answer and an unexpected error would log a server fault for something routine.
func (p *Passwords) acquire(ctx context.Context, share passwordShare) error {
	p.mu.Lock()
	class := share.class()
	if p.requests[share.principal] >= passwordPrincipalRequests || len(p.waiting[class]) >= passwordQueue {
		p.mu.Unlock()
		return domain.PasswordsBusy
	}
	waiter := &passwordWaiter{share, make(chan struct{})}
	p.requests[share.principal]++
	p.waiting[class] = append(p.waiting[class], waiter)
	p.dispatch()
	p.mu.Unlock()

	timer := time.NewTimer(passwordWait)
	defer timer.Stop()
	select {
	case <-waiter.ready:
		return nil
	case <-timer.C:
	case <-ctx.Done():
	}
	if !p.abandon(waiter) {
		p.release(share)
	}
	return domain.PasswordsBusy
}

// run calls fn once this share holds a slot. fn runs on the caller's goroutine, so a request
// that goes away while hashing keeps its slot until the hash ends.
func (p *Passwords) run(ctx context.Context, share passwordShare, fn func() error) error {
	if err := p.acquire(ctx, share); err != nil {
		return err
	}
	defer p.release(share)
	return fn()
}

func newPasswordHash(password string) (string, error) {
	return argon2id.CreateHash(password, &passwordParams)
}

func passwordMatches(password, hash string) error {
	if len(password) > 512 {
		return domain.Credentials
	}
	ok, err := argon2id.ComparePasswordAndHash(password, hash)
	if err != nil || !ok {
		return domain.Credentials
	}
	return nil
}

// Hash is for work outside a request, such as creating the first account from the command
// line. Requests go through Service.hashNewPassword, which applies budgets and shares.
func (p *Passwords) Hash(password string) (string, error) {
	if err := domain.Password(password); err != nil {
		return "", err
	}
	return newPasswordHash(password)
}
