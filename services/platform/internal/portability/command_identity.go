package portability

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
)

// CommandIdentityVersion names the key list CommandIdentity hashes. A sealed recovery base can be
// read by a later release, so a release that adds a command key that identity must cover adds a
// new version with its own list and keeps every earlier one.
const CommandIdentityVersion = 2

// commandIdentityKeys are the top-level command keys identity version 2 covers: every key of the
// stored command except those that change after the command is created, which are
// controllerGeneration (raised by a controller transfer), recoveryCheckpoint (attached by
// coordination and recovery) and cancelRequested (set by a cancellation). A key a later release
// adds is not covered until a version that lists it.
var commandIdentityKeys = map[int][]string{
	2: {"action", "backup", "crosshost", "deadline", "environmentId", "generation", "id", "image", "kind", "network", "parentId", "pipeline", "planHash", "release", "resourceId", "serverId", "upgrade", "visual"},
}

var errCommandIdentity = errors.New("stored command cannot be identified")

// CommandIdentity hashes a stored command, as it is stored, not as a release's command type would
// encode it again: two releases that read the same row give the same identity, so evidence sealed
// by one can be checked by another. A key whose value is null counts as absent, and the deadline
// is compared as an instant.
func CommandIdentity(raw []byte, version int) (string, error) {
	keys, known := commandIdentityKeys[version]
	if !known {
		return "", errCommandIdentity
	}
	var command map[string]jsontext.Value
	if json.Unmarshal(raw, &command) != nil || command == nil {
		return "", errCommandIdentity
	}
	covered := map[string]jsontext.Value{}
	for key, value := range command {
		if !slices.Contains(keys, key) || string(bytes.TrimSpace(value)) == "null" {
			continue
		}
		if key == "deadline" {
			var text string
			if json.Unmarshal(value, &text) != nil {
				return "", errCommandIdentity
			}
			at, err := time.Parse(time.RFC3339Nano, text)
			if err != nil {
				return "", errCommandIdentity
			}
			value, _ = stablejson.Marshal(at.UTC().Format(time.RFC3339Nano))
		}
		covered[key] = jsontext.Value(stablejson.Normalize(value))
	}
	encoded, err := stablejson.Marshal(covered)
	if err != nil {
		return "", errCommandIdentity
	}
	h := sha256.Sum256(stablejson.Normalize(encoded))
	return fmt.Sprintf("v%d:%s", version, hex.EncodeToString(h[:])), nil
}
