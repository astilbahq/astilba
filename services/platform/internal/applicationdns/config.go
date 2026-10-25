package applicationdns

import (
	"errors"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"golang.org/x/net/publicsuffix"
	"net/url"
	"regexp"
)

type Config struct{ Base, Zone, Token string }

var cloudflareID = regexp.MustCompile(`^[a-f0-9]{32}$`)

func (c Config) Validate(origin string) error {
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
	appRoot, e := publicsuffix.EffectiveTLDPlusOne(c.Base)
	accountRoot, ae := publicsuffix.EffectiveTLDPlusOne(u.Hostname())
	if e != nil || ae == nil && appRoot == accountRoot {
		return errors.New("application and account domains must have separate registrable roots")
	}
	if (c.Zone == "") != (c.Token == "") || c.Zone != "" && (!cloudflareID.MatchString(c.Zone) || len(c.Token) > 4096) {
		return errors.New("configure both a Cloudflare DNS zone ID and its scoped API token, or neither")
	}
	return nil
}
