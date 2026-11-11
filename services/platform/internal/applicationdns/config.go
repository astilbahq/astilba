package applicationdns

import (
	"errors"
	"net/url"
	"regexp"
	"strings"

	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/publicsuffix"
)

type Config struct{ Base, Zone, Token string }

var cloudflareID = regexp.MustCompile(`^[a-f0-9]{32}$`)

// Isolated reports whether the Public Suffix List puts every generated name's site
// below the base, so browsers keep one server's applications from setting cookies
// for, or counting as same-site with, another's. That holds when the base is itself
// a public suffix, listed or matched by a wildcard, or when a "*.base" rule makes each
// server's name one. See docs/hosting.md#generated-application-names.
func (c Config) Isolated() bool {
	if c.Base == "" {
		return false
	}
	// Any child stands for every server's name: no rule picks out one server ID.
	suffix, listed := publicsuffix.PublicSuffix("s." + c.Base)
	return listed && (suffix == c.Base || strings.HasSuffix(suffix, "."+c.Base))
}

func (c Config) Validate(origin string, hosted bool) error {
	if c.Base == "" {
		if c.Zone != "" || c.Token != "" {
			return errors.New("application DNS credentials require ASTILBA_APPLICATION_DOMAIN")
		}
		return nil
	}
	if !domain.ValidApplicationDomain(c.Base) {
		return errors.New("ASTILBA_APPLICATION_DOMAIN must be a lowercase DNS name of at most 165 characters")
	}
	u, e := url.Parse(origin)
	if e != nil {
		return errors.New("invalid management origin")
	}
	// Neither domain may share a site with the other. An origin without a registrable
	// domain, such as a loopback address in development, has no site to share.
	host := strings.ToLower(u.Hostname())
	accountRoot, ae := publicsuffix.RegistrableDomain(host)
	if host == c.Base || strings.HasSuffix(host, "."+c.Base) || ae == nil && (c.Base == accountRoot || strings.HasSuffix(c.Base, "."+accountRoot)) {
		return errors.New("application and account domains must have separate registrable roots")
	}
	if hosted && !c.Isolated() {
		return errors.New("hosted installations need ASTILBA_APPLICATION_DOMAIN on the Public Suffix List (" + publicsuffix.Version() + "), or left empty; see docs/hosting.md#generated-application-names")
	}
	if (c.Zone == "") != (c.Token == "") || c.Zone != "" && (!cloudflareID.MatchString(c.Zone) || len(c.Token) > 4096) {
		return errors.New("configure both a Cloudflare DNS zone ID and its scoped API token, or neither")
	}
	return nil
}
