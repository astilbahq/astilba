package config

import (
	"errors"
	"fmt"
	"github.com/astilbahq/astilba/services/platform/internal/applicationdns"
	"github.com/astilbahq/astilba/services/platform/internal/hosting"
	"net"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/mailer"
	"github.com/google/uuid"
)

const (
	// defaultDatabasePoolSize is explained in docs/hosting.md#database-connections.
	defaultDatabasePoolSize = 12
	minDatabasePoolSize     = 4
	maxDatabasePoolSize     = 1000
)

type Config struct {
	LogRetention time.Duration
	// SecretAccessRetentionDays is how long secret access records are kept. `astilba migrate`
	// writes it; serving only reads it.
	SecretAccessRetentionDays            int32
	DatabasePoolSize                     int32
	DatabaseURL, Keyring, Listen, Origin string
	// KeyBackend is the key service keys are read from. Only the keyring file exists so far.
	KeyBackend string
	// InstallationID pins the installation the database must belong to, or is uuid.Nil when it is
	// not set, which hosting does not allow.
	InstallationID                       uuid.UUID
	Development, Hosted, Troubleshooting bool
	// AgentForkFencing clears a server's credential on proof that a second holder has its agent
	// key. Off by default: the proof then only raises the owner's alert.
	AgentForkFencing bool
	ApplicationDNS   applicationdns.Config
	SMTP             mailer.Config
	Hosting          hosting.Config
	TrustedProxies   []netip.Prefix
}

func Load() (Config, error) {
	c := Config{DatabaseURL: os.Getenv("ASTILBA_DATABASE_URL"), Keyring: os.Getenv("ASTILBA_KEYRING"), Listen: os.Getenv("ASTILBA_LISTEN"), Origin: os.Getenv("ASTILBA_ORIGIN"), Development: os.Getenv("ASTILBA_DEVELOPMENT") == "true", Hosted: os.Getenv("ASTILBA_HOSTED") == "true"}
	c.ApplicationDNS = applicationdns.Config{Base: os.Getenv("ASTILBA_APPLICATION_DOMAIN"), Zone: os.Getenv("ASTILBA_DNS_CLOUDFLARE_ZONE"), Token: os.Getenv("ASTILBA_DNS_CLOUDFLARE_TOKEN")}
	switch os.Getenv("ASTILBA_TROUBLESHOOTING") {
	case "":
	case "single-instance":
		c.Troubleshooting = true
	default:
		return c, errors.New("ASTILBA_TROUBLESHOOTING must be single-instance when set")
	}
	switch os.Getenv("ASTILBA_AGENT_FORK_FENCING") {
	case "", "false":
	case "true":
		c.AgentForkFencing = true
	default:
		return c, errors.New("ASTILBA_AGENT_FORK_FENCING must be true or false when set")
	}
	days := 30
	if raw := os.Getenv("ASTILBA_LOG_RETENTION_DAYS"); raw != "" {
		n, e := strconv.Atoi(raw)
		if e != nil || n < 1 || n > 3650 {
			return c, errors.New("ASTILBA_LOG_RETENTION_DAYS must be between 1 and 3650")
		}
		days = n
	}
	c.LogRetention = time.Duration(days) * 24 * time.Hour
	c.SecretAccessRetentionDays = 400
	if raw := os.Getenv("ASTILBA_SECRET_ACCESS_RETENTION_DAYS"); raw != "" {
		n, e := strconv.Atoi(raw)
		if e != nil || n < 30 || n > 3650 || c.Hosted && n != 400 {
			return c, errors.New("ASTILBA_SECRET_ACCESS_RETENTION_DAYS must be between 30 and 3650, and 400 when hosted")
		}
		c.SecretAccessRetentionDays = int32(n)
	}
	c.DatabasePoolSize = defaultDatabasePoolSize
	if raw := os.Getenv("ASTILBA_DATABASE_POOL_SIZE"); raw != "" {
		n, e := strconv.Atoi(raw)
		if e != nil || n < minDatabasePoolSize || n > maxDatabasePoolSize {
			return c, fmt.Errorf("ASTILBA_DATABASE_POOL_SIZE must be between %d and %d", minDatabasePoolSize, maxDatabasePoolSize)
		}
		c.DatabasePoolSize = int32(n)
	}
	if raw := os.Getenv("ASTILBA_TRUSTED_PROXIES"); raw != "" {
		for _, value := range strings.Split(raw, ",") {
			prefix, err := netip.ParsePrefix(strings.TrimSpace(value))
			if err != nil || prefix.Addr().Is4In6() {
				return c, errors.New("ASTILBA_TRUSTED_PROXIES must be comma-separated IPv4 or IPv6 CIDRs")
			}
			c.TrustedProxies = append(c.TrustedProxies, prefix.Masked())
		}
	}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8080"
	}
	c.SMTP = mailer.Config{Address: os.Getenv("ASTILBA_SMTP_ADDRESS"), Username: os.Getenv("ASTILBA_SMTP_USERNAME"), Password: os.Getenv("ASTILBA_SMTP_PASSWORD"), From: os.Getenv("ASTILBA_SMTP_FROM"), Mode: os.Getenv("ASTILBA_SMTP_MODE"), Development: c.Development}
	if c.SMTP.Mode == "console" {
		if !c.Development {
			return c, errors.New("console email is only available in local development")
		}
		if err := c.ValidateServer(); err != nil {
			return c, err
		}
	}
	if c.Hosted {
		cohort, _ := strconv.Atoi(os.Getenv("ASTILBA_HOSTED_COHORT_LIMIT"))
		// A server limit that is not a 32-bit integer stays zero, which Validate refuses.
		var servers int32
		if n, e := strconv.ParseInt(os.Getenv("ASTILBA_HOSTED_SERVER_LIMIT"), 10, 32); e == nil {
			servers = int32(n)
		}
		c.Hosting = hosting.Config{CohortLimit: cohort, ServerLimit: servers, PriceID: os.Getenv("ASTILBA_STRIPE_PRICE"), SecretKey: os.Getenv("ASTILBA_STRIPE_SECRET"), WebhookSecret: os.Getenv("ASTILBA_STRIPE_WEBHOOK_SECRET"), Support: os.Getenv("ASTILBA_HOSTED_SUPPORT")}
		if e := c.Hosting.Validate(); e != nil {
			return c, e
		}
	}
	if e := c.loadCustody(); e != nil {
		return c, e
	}
	if c.DatabaseURL == "" || c.Keyring == "" {
		return c, errors.New("ASTILBA_DATABASE_URL and ASTILBA_KEYRING are required")
	}
	return c, nil
}

// loadCustody reads which key service to use and which installation to expect. A keyring in the
// environment is refused: anything that can read the environment, beside the database URL, could
// open every value. See docs/hosting.md#key-custody.
func (c *Config) loadCustody() error {
	if os.Getenv("ASTILBA_KEYRING_JSON") != "" {
		return errors.New("ASTILBA_KEYRING_JSON is no longer read: mount the keyring as a private file and name it in ASTILBA_KEYRING")
	}
	c.KeyBackend = os.Getenv("ASTILBA_KEY_BACKEND")
	switch c.KeyBackend {
	case "":
		c.KeyBackend = "file"
	case "file":
	default:
		return errors.New("ASTILBA_KEY_BACKEND must be file")
	}
	if raw := os.Getenv("ASTILBA_INSTALLATION_ID"); raw != "" {
		id, e := uuid.Parse(raw)
		if e != nil || id == uuid.Nil {
			return errors.New("ASTILBA_INSTALLATION_ID must be the installation's UUID, which `astilba custody status` shows")
		}
		c.InstallationID = id
	}
	return nil
}

// RequirePinnedInstallation refuses to use keys when hosted without ASTILBA_INSTALLATION_ID.
// Migrating and `custody status` do without it, since they are how an operator learns the ID.
func (c Config) RequirePinnedInstallation() error {
	if c.Hosted && c.InstallationID == uuid.Nil {
		return errors.New("ASTILBA_INSTALLATION_ID is required when hosted; `astilba custody status` shows it")
	}
	return nil
}

func loopbackHost(host string) bool {
	ip := net.ParseIP(host)
	return host == "localhost" || (ip != nil && ip.IsLoopback())
}

func (c Config) ValidateOrigin() error {
	u, e := url.Parse(c.Origin)
	if e != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("ASTILBA_ORIGIN must be an exact origin without a path")
	}
	if c.Development {
		if c.Hosted || !loopbackHost(u.Hostname()) || u.Scheme != "http" {
			return errors.New("development mode requires loopback HTTP and cannot be hosted")
		}
	} else if u.Scheme != "https" {
		return errors.New("HTTPS is required outside loopback development")
	}
	if strings.HasSuffix(c.Origin, "/") {
		return errors.New("origin cannot have a trailing slash")
	}
	return nil
}

func (c Config) ValidateServer() error {
	if e := c.ApplicationDNS.Validate(c.Origin, c.Hosted); e != nil {
		return e
	}
	if e := c.ValidateOrigin(); e != nil {
		return e
	}
	host, _, e := net.SplitHostPort(c.Listen)
	if e != nil {
		return errors.New("ASTILBA_LISTEN must be host:port")
	}
	if c.Development && !loopbackHost(host) {
		return errors.New("development mode requires loopback HTTP and cannot be hosted")
	}
	return nil
}
