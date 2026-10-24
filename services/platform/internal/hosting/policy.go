package hosting

import (
	"errors"
	"strings"
	"time"
)

const GracePeriod = 30 * 24 * time.Hour

// Config contains operator choices, not feature flags. Self-hosted installations
// expose the same product without subscription admission.
type Config struct {
	CohortLimit   int
	ServerLimit   int
	PriceID       string
	SecretKey     string
	WebhookSecret string
	Support       string
}

func (c Config) Validate() error {
	if c.CohortLimit < 1 || c.CohortLimit > 1000 || c.ServerLimit < 1 || c.ServerLimit > 100 || len(c.Support) > 1000 || strings.TrimSpace(c.Support) == "" {
		return errors.New("hosted cohort/server limits and support details must be configured")
	}
	if (c.SecretKey != "" || c.PriceID != "" || c.WebhookSecret != "") && (c.SecretKey == "" || c.PriceID == "" || c.WebhookSecret == "") {
		return errors.New("configure all Stripe settings or none")
	}
	return nil
}
func (c Config) BillingEnabled() bool {
	return c.SecretKey != "" && c.PriceID != "" && c.WebhookSecret != ""
}

type Entitlement struct {
	Status      string
	Until       *time.Time
	ServerLimit int
}

func (e Entitlement) Active(now time.Time) bool { return e.Until != nil && now.Before(*e.Until) }
func (e Entitlement) Recoverable(now time.Time) bool {
	return e.Until != nil && now.Before(e.Until.Add(GracePeriod))
}
