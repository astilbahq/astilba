package secretbroker

import (
	"sync"

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

type budgets struct {
	mu      sync.Mutex
	buckets map[string]*rate.Limiter
}

func newBudgets() *budgets { return &budgets{buckets: map[string]*rate.Limiter{}} }

func (b *budgets) take(key string, r rate.Limit, burst int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	bucket := b.buckets[key]
	if bucket == nil {
		if len(b.buckets) >= maxBuckets {
			clear(b.buckets)
		}
		bucket = rate.NewLimiter(r, burst)
		b.buckets[key] = bucket
	}
	return bucket.Allow()
}
