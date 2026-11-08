package deployment

import (
	"bytes"
	"fmt"
	"unicode/utf8"

	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

// RouteFile encodes a Traefik file provider configuration. The provider evaluates
// every file as a Go template before parsing it and has no setting to stop that, so
// the bytes must never open an action, whichever value they came from. JSON only
// holds "{{" inside a string, where an escaped brace decodes to the same value.
//
// The provider then reads the file as YAML, which refuses some characters JSON leaves
// raw and reads U+0085 as a line break. A refused character makes the proxy reject the
// whole file, every environment's routes with it, so each such character is written as
// an escape, which YAML decodes to the same character as JSON does. A route value then
// reaches the proxy as reviewed, and if the proxy refuses it, it refuses that route.
//
// Other output is unchanged, because the agent compares these bytes to detect drift.
func RouteFile(config any) ([]byte, error) {
	raw, err := stablejson.Marshal(config)
	if err != nil {
		return nil, err
	}
	raw = bytes.ReplaceAll(raw, []byte("{{"), []byte(`{\u007b`))
	if bytes.IndexFunc(raw, yamlEscaped) < 0 {
		return raw, nil
	}
	// Valid UTF-8 outside a string is ASCII punctuation, so every match is in a string.
	out := make([]byte, 0, len(raw)+64)
	for _, r := range string(raw) {
		if yamlEscaped(r) {
			out = fmt.Appendf(out, `\u%04x`, r)
		} else {
			out = utf8.AppendRune(out, r)
		}
	}
	return out, nil
}

// yamlEscaped is a character that the YAML 1.1 reader the proxy uses refuses, or reads
// as a line break and so folds to a space inside a quoted string. stablejson already
// escapes characters below U+0020 and the other line breaks, U+2028 and U+2029, and
// valid UTF-8 holds no surrogates.
func yamlEscaped(r rune) bool {
	return r >= 0x7f && r <= 0x9f || r == 0xfffe || r == 0xffff
}

// BackendHost is the name a proxy dials to reach a service in unit. The proxy shares
// one resolver across every routed unit's network, and any unit can declare a bare name
// on its own, so Docker DNS could answer a bare name from another unit. Docker DNS looks
// up the name.network form only on that network, which only the unit's services join.
// It tries the whole string as a name first, on every network, so composeproject.Load
// must also keep units from declaring it.
func BackendHost(unit uuid.UUID, service string) string {
	return service + "." + NetworkName(unit)
}

// RouteConfig renders routes whose backends resolve only on their own unit's network.
func RouteConfig(sets map[string]RouteSet) ([]byte, error) {
	return routeConfig(sets, BackendHost)
}

// BareRouteConfig renders the file agents wrote before backends were qualified. A host
// keeps comparing that file to detect drift until it has converted its routes, which
// the agent does as soon as it can.
func BareRouteConfig(sets map[string]RouteSet) ([]byte, error) {
	return routeConfig(sets, func(_ uuid.UUID, service string) string { return service })
}
