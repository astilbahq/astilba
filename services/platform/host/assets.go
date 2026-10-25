// Package host contains reviewable installation assets for supported hosts.
package host

import (
	_ "embed"
	"strings"
)

//go:embed bootstrap.sh.tmpl
var bootstrap string

//go:embed astilba-agent.service
var unit string

func Bootstrap() []byte {
	return []byte(strings.Replace(bootstrap, "__ASTILBA_SYSTEMD_UNIT__", strings.TrimSpace(unit), 1))
}
