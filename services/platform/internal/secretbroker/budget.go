package secretbroker

import (
	"sync"

	"github.com/google/uuid"
	"golang.org/x/time/rate"
)

// Budgets stop a bug or a flood from opening values without end. They are per process, and
// they are not a defence against code running inside management.
const (
	workspaceRate  = 20
	workspaceBurst = 100
	// A webhook is unauthenticated until its signature is checked, which needs its secret.
	webhookRate  = 1
	webhookBurst = 30
	// maxBuckets bounds the memory a flood of distinct keys can take. Past it the buckets start
	// again full, which a flood can only reach by spreading itself thinner than any one bucket.
	maxBuckets = 10000
)

type bucket struct {
	key   string
	rate  rate.Limit
	burst int
}

// budgetKey is the one bucket a request draws from. A webhook draws only its connection's, so
// a flood of forged calls cannot spend what the workspace's own work needs. The installation's
// own values are budgeted per account, so one account cannot hold up everyone's sign-in.
func budgetKey(r Request) bucket {
	switch {
	case r.Connection != nil:
		return bucket{"webhook:" + r.Connection.String(), webhookRate, webhookBurst}
	case r.Workspace == uuid.Nil && r.Principal.Account != nil:
		return bucket{string(r.Purpose) + ":account:" + r.Principal.Account.String(), workspaceRate, workspaceBurst}
	}
	return bucket{string(r.Purpose) + ":" + r.Workspace.String(), workspaceRate, workspaceBurst}
}

type budgets struct {
	mu      sync.Mutex
	buckets map[string]*rate.Limiter
}

func newBudgets() *budgets { return &budgets{buckets: map[string]*rate.Limiter{}} }

func (b *budgets) take(k bucket) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	limiter := b.buckets[k.key]
	if limiter == nil {
		if len(b.buckets) >= maxBuckets {
			clear(b.buckets)
		}
		limiter = rate.NewLimiter(k.rate, k.burst)
		b.buckets[k.key] = limiter
	}
	return limiter.Allow()
}
