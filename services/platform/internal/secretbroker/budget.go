package secretbroker

import (
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/time/rate"
)

// Budgets stop a bug or a flood from opening values without end. They are per process, and
// they are not a defence against code running inside management.
const (
	workspaceRate  = 20
	workspaceBurst = 100
	// A webhook is unauthenticated until its signature is checked, which needs its secret, so a
	// forged call cannot be told from a real one before the budget. The burst fits a provider's
	// own bursts, such as a pull request series, and the rate bounds a flood's records.
	webhookRate  = 5
	webhookBurst = 200
	// One agent that never acknowledges cannot starve its workspace's other servers.
	serverRate  = 4
	serverBurst = 20
	// maxBuckets bounds the memory a flood of distinct keys can take. Past it the buckets start
	// again full, which a flood can only reach by spreading itself thinner than any one bucket.
	maxBuckets = 10000
)

type bucket struct {
	key   string
	rate  rate.Limit
	burst int
}

// budgetKeys are the buckets a request draws from. A webhook draws only its connection's, so
// a flood of forged calls cannot spend what the workspace's own work needs. The installation's
// own values are budgeted per account, so one account cannot hold up everyone's sign-in. An
// agent's deliveries draw on their server's bucket first, which is below a fifth of the
// workspace's.
func budgetKeys(r Request) []bucket {
	switch {
	case r.Connection != nil:
		return []bucket{{"webhook:" + r.Connection.String(), webhookRate, webhookBurst}}
	case r.Workspace == uuid.Nil && r.Principal.Account != nil:
		return []bucket{{string(r.Purpose) + ":account:" + r.Principal.Account.String(), workspaceRate, workspaceBurst}}
	}
	workspace := bucket{string(r.Purpose) + ":" + r.Workspace.String(), workspaceRate, workspaceBurst}
	if r.Purpose == Deliver && r.Principal.Server != nil {
		return []bucket{{"deliver-server:" + r.Principal.Server.String(), serverRate, serverBurst}, workspace}
	}
	return []bucket{workspace}
}

type budgets struct {
	mu      sync.Mutex
	buckets map[string]*rate.Limiter
}

func newBudgets() *budgets { return &budgets{buckets: map[string]*rate.Limiter{}} }

// take spends one token from every bucket, or none when any is empty.
func (b *budgets) take(keys []bucket) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	reservations := make([]*rate.Reservation, 0, len(keys))
	for _, k := range keys {
		limiter := b.buckets[k.key]
		if limiter == nil {
			if len(b.buckets) >= maxBuckets {
				clear(b.buckets)
			}
			limiter = rate.NewLimiter(k.rate, k.burst)
			b.buckets[k.key] = limiter
		}
		r := limiter.ReserveN(now, 1)
		if !r.OK() || r.DelayFrom(now) > 0 {
			r.CancelAt(now)
			for _, taken := range reservations {
				taken.CancelAt(now)
			}
			return false
		}
		reservations = append(reservations, r)
	}
	return true
}
