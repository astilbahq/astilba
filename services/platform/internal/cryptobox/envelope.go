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
	// ScopeEnvironment is one environment's own key, which its envelope's holder names. That key
	// is wrapped by its workspace's key, and destroying it when the environment is purged leaves
	// every copy of the environment's values unreadable.
	ScopeEnvironment = "environment"
)

// envelopeV2 is a value whose data key is wrapped under a scope's key at one generation, rather
// than under the installation keyring. Its data is sealed as in v1, so a value moved from v1 to
// v2 keeps the same data ciphertext. The workspace is never in the envelope: it comes from the
// identity, which the caller builds from the row.
type envelopeV2 struct {
	Version    int    `json:"version"`
	Scope      string `json:"scope"`
	Generation int64  `json:"generation"`
	// Holder names the environment whose key wraps the data key. Only the environment scope has
	// one, and the key's wrapping is bound to it.
	Holder *uuid.UUID `json:"holder,omitzero"`
	Key    []byte     `json:"key"`
	Data   []byte     `json:"data"`
}

// Header says what a sealed value's data key is wrapped under: the keyring key KeyID for
// version 1, or Scope's key at Generation for version 2, which for the environment scope is
// Holder's key.
type Header struct {
	Version    int
	KeyID      string
	Scope      string
	Generation int64
	Holder     uuid.UUID
}

// ReadHeader reads a sealed value's header without opening it. ErrKey is anything that is not a
// version 1 or 2 envelope this release opens.
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
		h := Header{Version: 2, Scope: env.Scope, Generation: env.Generation}
		if env.Holder != nil {
			h.Holder = *env.Holder
		}
		return h, nil
	}
	return Header{}, ErrKey
}

func decodeV2(blob []byte) (envelopeV2, error) {
	var env envelopeV2
	if len(blob) > maxBlob || json.Unmarshal(blob, &env, json.RejectUnknownMembers(true)) != nil || env.Version != 2 || env.Generation < 1 {
		return env, ErrKey
	}
	switch env.Scope {
	case ScopeWorkspace, ScopeInstallation, ScopeToken:
		if env.Holder == nil {
			return env, nil
		}
	case ScopeEnvironment:
		if env.Holder != nil && *env.Holder != uuid.Nil {
			return env, nil
		}
	}
	return env, ErrKey
}

// keyAAD binds a data key's wrapping to its value and to the key it is wrapped under: the scope
// and generation, and for an environment's key the environment, so a value cannot be passed off
// as another environment's by editing its header.
func keyAAD(identity string, t target) string {
	if t.scope == ScopeEnvironment {
		return identity + ":" + t.scope + ":" + t.holder.String() + ":" + strconv.FormatInt(t.generation, 10) + ":key"
	}
	return identity + ":" + t.scope + ":" + strconv.FormatInt(t.generation, 10) + ":key"
}

// target is the key a data key is wrapped under: a scope's key at a generation, and for the
// environment scope, the environment whose key it is.
type target struct {
	scope      string
	generation int64
	holder     uuid.UUID
}

func targetOf(env envelopeV2) target {
	t := target{scope: env.Scope, generation: env.Generation}
	if env.Holder != nil {
		t.holder = *env.Holder
	}
	return t
}

// SealUnder seals a value in a v2 envelope, with its data key wrapped under key, which is scope's
// key at generation. The environment scope seals with SealUnderEnvironment.
func SealUnder(key []byte, scope string, generation int64, value []byte, identity string) ([]byte, error) {
	if scope == ScopeEnvironment {
		return nil, ErrKey
	}
	return sealUnder(key, target{scope: scope, generation: generation}, value, identity)
}

// SealUnderEnvironment seals a value in a v2 envelope, with its data key wrapped under key, which
// is environment's key at generation.
func SealUnderEnvironment(key []byte, environment uuid.UUID, generation int64, value []byte, identity string) ([]byte, error) {
	return sealUnder(key, target{scope: ScopeEnvironment, generation: generation, holder: environment}, value, identity)
}

func sealUnder(key []byte, t target, value []byte, identity string) ([]byte, error) {
	if !sealable(key, t) {
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
	return wrapUnder(key, t, dataKey, data, identity, maxBlob-maxKeyID)
}

func sealable(key []byte, t target) bool {
	if len(key) != 32 || t.generation < 1 {
		return false
	}
	switch t.scope {
	case ScopeWorkspace, ScopeInstallation, ScopeToken:
		return t.holder == uuid.Nil
	case ScopeEnvironment:
		return t.holder != uuid.Nil
	}
	return false
}

// wrapUnder wraps a value's data key under t's key, beside its sealed data.
func wrapUnder(key []byte, t target, dataKey, data []byte, identity string, limit int) ([]byte, error) {
	wrapped, err := seal(key, dataKey, keyAAD(identity, t))
	if err != nil {
		return nil, err
	}
	env := envelopeV2{Version: 2, Scope: t.scope, Generation: t.generation, Key: wrapped, Data: data}
	if t.scope == ScopeEnvironment {
		env.Holder = &t.holder
	}
	blob, err := stablejson.Marshal(env)
	if err != nil {
		return nil, err
	}
	if len(blob) > limit {
		return nil, ErrTooLarge
	}
	return blob, nil
}

// MoveUnder wraps a sealed value's data key again under key, scope's key at generation, and leaves
// its data as it is, so the value's content digest still matches. The value may be v1, opened with
// ring, or v2, opened with from, the key its header names. Its data is checked before it moves.
// The environment scope moves with MoveUnderEnvironment.
func MoveUnder(ring *Keyring, from, blob []byte, identity string, key []byte, scope string, generation int64) ([]byte, error) {
	if scope == ScopeEnvironment {
		return nil, ErrKey
	}
	return moveUnder(ring, from, blob, identity, key, target{scope: scope, generation: generation})
}

// MoveUnderEnvironment is MoveUnder onto environment's key at generation.
func MoveUnderEnvironment(ring *Keyring, from, blob []byte, identity string, key []byte, environment uuid.UUID, generation int64) ([]byte, error) {
	return moveUnder(ring, from, blob, identity, key, target{scope: ScopeEnvironment, generation: generation, holder: environment})
}

func moveUnder(ring *Keyring, from, blob []byte, identity string, key []byte, t target) ([]byte, error) {
	if !sealable(key, t) {
		return nil, ErrKey
	}
	header, err := ReadHeader(blob)
	if err != nil {
		return nil, err
	}
	var dataKey, data []byte
	if header.Version == 1 {
		if ring == nil {
			return nil, ErrKey
		}
		var env envelope
		env, dataKey, err = ring.unwrap(blob, identity)
		data = env.Data
	} else {
		var env envelopeV2
		if env, err = decodeV2(blob); err != nil || len(from) != 32 {
			return nil, ErrKey
		}
		dataKey, err = open(from, env.Key, keyAAD(identity, targetOf(env)))
		data = env.Data
	}
	if err != nil {
		return nil, err
	}
	defer clear(dataKey)
	plain, err := open(dataKey, data, identity+":data")
	clear(plain)
	if err != nil {
		return nil, err
	}
	return wrapUnder(key, t, dataKey, data, identity, maxBlob)
}

// OpenUnder opens a v2 envelope with key, which must be the key of the scope and generation its
// header names: ReadHeader says which.
func OpenUnder(key, blob []byte, identity string) ([]byte, error) {
	env, err := decodeV2(blob)
	if err != nil || len(key) != 32 {
		return nil, ErrKey
	}
	dataKey, err := open(key, env.Key, keyAAD(identity, targetOf(env)))
	if err != nil {
		return nil, err
	}
	defer clear(dataKey)
	return open(dataKey, env.Data, identity+":data")
}

// environmentKeyAAD binds an environment key's wrapping to its environment, its generation and
// the workspace key generation it is wrapped under.
func environmentKeyAAD(workspace, environment uuid.UUID, generation, workspaceGeneration int64) string {
	return "environment-key:v1:" + workspace.String() + ":" + environment.String() + ":" + strconv.FormatInt(generation, 10) + ":" + strconv.FormatInt(workspaceGeneration, 10)
}

// WrapEnvironmentKey wraps an environment's key at generation under its workspace's key at
// workspaceGeneration.
func WrapEnvironmentKey(workspaceKey []byte, workspace, environment uuid.UUID, generation, workspaceGeneration int64, environmentKey []byte) ([]byte, error) {
	if len(workspaceKey) != 32 || len(environmentKey) != 32 || generation < 1 || workspaceGeneration < 1 || workspace == uuid.Nil || environment == uuid.Nil {
		return nil, ErrKey
	}
	return seal(workspaceKey, environmentKey, environmentKeyAAD(workspace, environment, generation, workspaceGeneration))
}

// OpenEnvironmentKey opens what WrapEnvironmentKey wrapped.
func OpenEnvironmentKey(workspaceKey, wrapped []byte, workspace, environment uuid.UUID, generation, workspaceGeneration int64) ([]byte, error) {
	if len(workspaceKey) != 32 {
		return nil, ErrKey
	}
	key, err := open(workspaceKey, wrapped, environmentKeyAAD(workspace, environment, generation, workspaceGeneration))
	if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		clear(key)
		return nil, ErrKey
	}
	return key, nil
}
