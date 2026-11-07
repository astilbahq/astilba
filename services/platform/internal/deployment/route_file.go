package deployment

import (
	"bytes"

	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

// RouteFile encodes a Traefik file provider configuration. The provider evaluates
// every file as a Go template before parsing it and has no setting to stop that, so
// the bytes must never open an action, whichever value they came from. JSON only
// holds "{{" inside a string, where an escaped brace decodes to the same value.
// Other output is unchanged, because the agent compares these bytes to detect drift.
func RouteFile(config any) ([]byte, error) {
	raw, err := stablejson.Marshal(config)
	if err != nil {
		return nil, err
	}
	return bytes.ReplaceAll(raw, []byte("{{"), []byte(`{\u007b`)), nil
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
