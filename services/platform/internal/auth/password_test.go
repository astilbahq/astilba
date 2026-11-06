package auth

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/domain"
)

func newGate() *Passwords {
	return &Passwords{running: map[string]struct{}{}, requests: map[string]int{}}
}

// held starts work that stays in its slot until the returned function is called.
func held(t *testing.T, p *Passwords, share passwordShare) (started <-chan struct{}, finish func() error) {
	t.Helper()
	begin, end, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- p.run(context.Background(), share, func() error {
			close(begin)
			<-end
			return nil
		})
	}()
	return begin, func() error {
		close(end)
		return <-done
	}
}

// finishAll ends work in any order, since waiting work cannot end before it has run.
func finishAll(t *testing.T, finish []func() error) {
	t.Helper()
	results := make(chan error, len(finish))
	for _, done := range finish {
		go func() { results <- done() }()
	}
	for range finish {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
}

func waitFor(t *testing.T, signal <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatal(what)
	}
}

func queued(p *Passwords) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.waiting[0]) + len(p.waiting[1])
}

func waitQueued(t *testing.T, p *Passwords, count int) {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); queued(p) != count; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("queue holds %d, want %d", queued(p), count)
		}
	}
}

func TestOnePrincipalCannotHoldEverySlot(t *testing.T) {
	p := newGate()
	attacker := passwordShare{principal: "attacker"}
	first, finishFirst := held(t, p, attacker)
	waitFor(t, first, "first hash did not start")
	second, finishSecond := held(t, p, attacker)
	waitQueued(t, p, 1)
	victim, finishVictim := held(t, p, passwordShare{principal: "victim"})
	waitFor(t, victim, "the other slot was not free for another principal")
	select {
	case <-second:
		t.Fatal("a principal ran two hashes at once")
	default:
	}
	if err := finishVictim(); err != nil {
		t.Fatal(err)
	}
	if err := finishFirst(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, second, "queued hash did not start after its principal's first ended")
	if err := finishSecond(); err != nil {
		t.Fatal(err)
	}
}

func TestPrincipalRequestsAreBounded(t *testing.T) {
	p := newGate()
	attacker := passwordShare{principal: "attacker"}
	var finish []func() error
	for range passwordPrincipalRequests {
		_, done := held(t, p, attacker)
		finish = append(finish, done)
	}
	waitQueued(t, p, passwordPrincipalRequests-1)
	err := p.run(context.Background(), attacker, func() error { t.Fatal("over-limit request ran"); return nil })
	if !errors.Is(err, domain.PasswordsBusy) {
		t.Fatal("request over the principal's limit was not refused at once", err)
	}
	finishAll(t, finish)
	if len(p.requests) != 0 || len(p.running) != 0 {
		t.Fatalf("gate state leaked: %+v", p)
	}
}

func TestProvenRequestsGoFirstAndHaveTheirOwnQueue(t *testing.T) {
	p := newGate()
	var finish []func() error
	for i := range passwordSlots {
		started, done := held(t, p, passwordShare{principal: fmt.Sprintf("running-%d", i)})
		waitFor(t, started, "running hash did not start")
		finish = append(finish, done)
	}
	for i := range passwordQueue {
		_, done := held(t, p, passwordShare{principal: fmt.Sprintf("anonymous-%d", i)})
		finish = append(finish, done)
	}
	waitQueued(t, p, passwordQueue)
	err := p.run(context.Background(), passwordShare{principal: "late"}, func() error { return nil })
	if !errors.Is(err, domain.PasswordsBusy) {
		t.Fatal("full anonymous queue accepted another request", err)
	}
	proven, finishProven := held(t, p, passwordShare{principal: "proven", proven: true})
	waitQueued(t, p, passwordQueue+1)
	if err := finish[0](); err != nil {
		t.Fatal(err)
	}
	waitFor(t, proven, "proven request did not overtake the anonymous queue")
	if err := finishProven(); err != nil {
		t.Fatal(err)
	}
	finishAll(t, finish[1:])
}

func TestAbandonedWaiterGivesBackItsPlace(t *testing.T) {
	p := newGate()
	var finish []func() error
	for i := range passwordSlots {
		started, done := held(t, p, passwordShare{principal: fmt.Sprintf("running-%d", i)})
		waitFor(t, started, "running hash did not start")
		finish = append(finish, done)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- p.run(ctx, passwordShare{principal: "leaver"}, func() error { return nil })
	}()
	waitQueued(t, p, 1)
	cancel()
	if err := <-result; !errors.Is(err, domain.PasswordsBusy) {
		t.Fatal("cancelled waiter", err)
	}
	if queued(p) != 0 || p.requests["leaver"] != 0 {
		t.Fatal("cancelled waiter kept its place")
	}
	finishAll(t, finish)
}

// A waiter that gives up just as a slot reaches it holds that slot and must hand it back. Losing
// one per race would end all sign-in on the replica after two.
func TestGivingUpWhileBeingDispatchedKeepsTheSlot(t *testing.T) {
	p := newGate()
	var racers sync.WaitGroup
	for i := range 400 {
		racers.Go(func() {
			ctx, cancel := context.WithCancel(context.Background())
			go cancel()
			_ = p.run(ctx, passwordShare{principal: fmt.Sprintf("principal-%d", i%8)}, func() error { return nil })
		})
	}
	racers.Wait()
	if len(p.running) != 0 || len(p.requests) != 0 || queued(p) != 0 {
		t.Fatalf("gate state leaked: %d running, %d requests, %d queued", len(p.running), len(p.requests), queued(p))
	}
}

// Hash capacity is what bounds a spray from many addresses, now nothing is installation-wide.
func TestHashesNeverExceedSlotsAcrossPrincipals(t *testing.T) {
	p := newGate()
	var running, most atomic.Int32
	var clients sync.WaitGroup
	for i := range 20 {
		clients.Go(func() {
			err := p.run(context.Background(), passwordShare{principal: fmt.Sprintf("address-%d", i)}, func() error {
				now := running.Add(1)
				defer running.Add(-1)
				for seen := most.Load(); now > seen && !most.CompareAndSwap(seen, now); seen = most.Load() {
				}
				time.Sleep(5 * time.Millisecond)
				return nil
			})
			if err != nil {
				t.Error(err)
			}
		})
	}
	clients.Wait()
	if got := most.Load(); got != passwordSlots {
		t.Fatalf("at most %d hashes ran at once, want %d", got, passwordSlots)
	}
}
