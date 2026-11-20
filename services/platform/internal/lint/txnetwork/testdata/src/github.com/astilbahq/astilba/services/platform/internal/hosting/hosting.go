package hosting

type Stripe struct{ Key string }

func (s Stripe) Subscription(id string) (string, error) { return id, nil }
func (s *Stripe) Portal() (string, error)               { return "", nil }

// Subscription's methods only compute.
type Subscription struct{}

func (Subscription) Entitlement() string { return "" }
