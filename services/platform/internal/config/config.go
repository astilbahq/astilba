package config

import (
	"errors"
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
)

type Config struct {
	LogRetention                                      time.Duration
	DatabaseURL, Keyring, KeyringJSON, Listen, Origin string
	Development, Hosted                               bool
	ApplicationDNS                                    applicationdns.Config
	SMTP                                              mailer.Config
	Hosting                                           hosting.Config
	TrustedProxies                                    []netip.Prefix
}

func Load() (Config, error) {
	c := Config{DatabaseURL: os.Getenv("ASTILBA_DATABASE_URL"), Keyring: os.Getenv("ASTILBA_KEYRING"), KeyringJSON: os.Getenv("ASTILBA_KEYRING_JSON"), Listen: os.Getenv("ASTILBA_LISTEN"), Origin: os.Getenv("ASTILBA_ORIGIN"), Development: os.Getenv("ASTILBA_DEVELOPMENT") == "true", Hosted: os.Getenv("ASTILBA_HOSTED") == "true"}
	c.ApplicationDNS = applicationdns.Config{Base: os.Getenv("ASTILBA_APPLICATION_DOMAIN"), Zone: os.Getenv("ASTILBA_DNS_CLOUDFLARE_ZONE"), Token: os.Getenv("ASTILBA_DNS_CLOUDFLARE_TOKEN")}
	days := 30
	if raw := os.Getenv("ASTILBA_LOG_RETENTION_DAYS"); raw != "" {
		n, e := strconv.Atoi(raw)
		if e != nil || n < 1 || n > 3650 {
			return c, errors.New("ASTILBA_LOG_RETENTION_DAYS must be between 1 and 3650")
		}
		days = n
	}
	c.LogRetention = time.Duration(days) * 24 * time.Hour
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
	if c.Hosted {
		cohort, _ := strconv.Atoi(os.Getenv("ASTILBA_HOSTED_COHORT_LIMIT"))
		servers, _ := strconv.Atoi(os.Getenv("ASTILBA_HOSTED_SERVER_LIMIT"))
		c.Hosting = hosting.Config{CohortLimit: cohort, ServerLimit: servers, PriceID: os.Getenv("ASTILBA_STRIPE_PRICE"), SecretKey: os.Getenv("ASTILBA_STRIPE_SECRET"), WebhookSecret: os.Getenv("ASTILBA_STRIPE_WEBHOOK_SECRET"), Support: os.Getenv("ASTILBA_HOSTED_SUPPORT")}
		if e := c.Hosting.Validate(); e != nil {
			return c, e
		}
	}
	if c.DatabaseURL == "" || (c.Keyring == "" && c.KeyringJSON == "") || (c.Keyring != "" && c.KeyringJSON != "") {
		return c, errors.New("ASTILBA_DATABASE_URL and exactly one of ASTILBA_KEYRING or ASTILBA_KEYRING_JSON are required")
	}
	return c, nil
}
func (c Config) ValidateServer() error {
	if e := c.ApplicationDNS.Validate(c.Origin); e != nil {
		return e
	}
	u, e := url.Parse(c.Origin)
	if e != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("ASTILBA_ORIGIN must be an exact origin without a path")
	}
	host, _, e := net.SplitHostPort(c.Listen)
	if e != nil {
		return errors.New("ASTILBA_LISTEN must be host:port")
	}
	loopback := func(s string) bool { ip := net.ParseIP(s); return s == "localhost" || (ip != nil && ip.IsLoopback()) }
	if c.Development {
		if c.Hosted || !loopback(host) || !loopback(u.Hostname()) || u.Scheme != "http" {
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
