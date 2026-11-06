package deployment

import (
	"bytes"

	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
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
