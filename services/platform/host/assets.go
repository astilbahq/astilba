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

//go:embed network-tools.sh
var networkTools string

//go:embed astilba-network.service
var networkUnit string

func NetworkPreparation() []byte {
	return []byte(strings.Replace(networkTools, "__ASTILBA_NETWORK_UNIT__", strings.TrimSpace(networkUnit), 1))
}

func NetworkService() []byte { return []byte(networkUnit) }
