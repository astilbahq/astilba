package cryptobox

import (
	"encoding/json/v2"
	"strconv"

	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

// Scopes a v2 envelope's data key can be wrapped under. A workspace value's data key is wrapped
// under one generation of its workspace's key, a second factor's under the installation key's,
// and a self-issued token's under a token key's.
const (
	ScopeWorkspace    = "workspace"
	ScopeInstallation = "installation"
	ScopeToken        = "token"
	// scopeEnvironment is reserved for per-environment keys (PLAT-353), wrapped by the workspace
	// key and named by the envelope's holder. No reader opens it yet.
	scopeEnvironment = "environment"
)

// envelopeV2 is a value whose data key is wrapped under a scope's key at one generation, rather
// than under the installation keyring. Its data is sealed as in v1, so a value moved from v1 to
// v2 keeps the same data ciphertext. The workspace is never in the envelope: it comes from the
// identity, which the caller builds from the row.
type envelopeV2 struct {
	Version    int    `json:"version"`
	Scope      string `json:"scope"`
	Generation int64  `json:"generation"`
	// Holder is reserved for PLAT-353, where it names the environment whose key wraps the data
	// key. A reader refuses an envelope that sets it, until that reader exists.
	Holder *uuid.UUID `json:"holder,omitzero"`
	Key    []byte     `json:"key"`
	Data   []byte     `json:"data"`
}

// Header says what a sealed value's data key is wrapped under: the keyring key KeyID for
// version 1, or Scope's key at Generation for version 2.
type Header struct {
	Version    int
	KeyID      string
	Scope      string
	Generation int64
}

// ReadHeader reads a sealed value's header without opening it. ErrKey is anything that is not a
// version 1 or 2 envelope this release opens, including one using a field reserved for later.
func ReadHeader(blob []byte) (Header, error) {
	var probe struct {
		Version int `json:"version"`
	}
	if len(blob) > maxBlob || json.Unmarshal(blob, &probe) != nil {
		return Header{}, ErrKey
	}
	switch probe.Version {
	case 1:
		var env envelope
		if json.Unmarshal(blob, &env, json.RejectUnknownMembers(true)) != nil || !keyID.MatchString(env.KeyID) {
			return Header{}, ErrKey
		}
		return Header{Version: 1, KeyID: env.KeyID}, nil
	case 2:
		env, err := decodeV2(blob)
		if err != nil {
			return Header{}, err
		}
		return Header{Version: 2, Scope: env.Scope, Generation: env.Generation}, nil
	}
	return Header{}, ErrKey
}

func decodeV2(blob []byte) (envelopeV2, error) {
	var env envelopeV2
	if len(blob) > maxBlob || json.Unmarshal(blob, &env, json.RejectUnknownMembers(true)) != nil || env.Version != 2 || env.Generation < 1 || env.Holder != nil {
		return env, ErrKey
	}
	switch env.Scope {
	case ScopeWorkspace, ScopeInstallation, ScopeToken:
		return env, nil
	case scopeEnvironment:
		// Reserved until its reader exists.
		return env, ErrKey
	}
	return env, ErrKey
}

func keyAAD(identity, scope string, generation int64) string {
	return identity + ":" + scope + ":" + strconv.FormatInt(generation, 10) + ":key"
}

// SealUnder seals a value in a v2 envelope, with its data key wrapped under key, which is scope's
// key at generation.
func SealUnder(key []byte, scope string, generation int64, value []byte, identity string) ([]byte, error) {
	if len(key) != 32 || generation < 1 || scope != ScopeWorkspace && scope != ScopeInstallation && scope != ScopeToken {
		return nil, ErrKey
	}
	dataKey := make([]byte, 32)
	defer clear(dataKey)
	if err := randomKey(dataKey); err != nil {
		return nil, err
	}
	data, err := seal(dataKey, value, identity+":data")
	if err != nil {
		return nil, err
	}
	wrapped, err := seal(key, dataKey, keyAAD(identity, scope, generation))
	if err != nil {
		return nil, err
	}
	blob, err := stablejson.Marshal(envelopeV2{Version: 2, Scope: scope, Generation: generation, Key: wrapped, Data: data})
	if err != nil {
		return nil, err
	}
	if len(blob) > maxBlob-maxKeyID {
		return nil, ErrTooLarge
	}
	return blob, nil
}

// OpenUnder opens a v2 envelope with key, which must be the key of the scope and generation its
// header names: ReadHeader says which.
func OpenUnder(key, blob []byte, identity string) ([]byte, error) {
	env, err := decodeV2(blob)
	if err != nil || len(key) != 32 {
		return nil, ErrKey
	}
	dataKey, err := open(key, env.Key, keyAAD(identity, env.Scope, env.Generation))
	if err != nil {
		return nil, err
	}
	defer clear(dataKey)
	return open(dataKey, env.Data, identity+":data")
}
