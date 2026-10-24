package cryptobox

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
)

var ErrKey = errors.New("secret key is unavailable or ciphertext is invalid")
var keyID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

type Keyring struct {
	Active string            `json:"active"`
	Keys   map[string][]byte `json:"keys"`
}

type envelope struct {
	Version int    `json:"version"`
	KeyID   string `json:"keyId"`
	Key     []byte `json:"key"`
	Data    []byte `json:"data"`
}

func New(id string) (*Keyring, error) {
	if !keyID.MatchString(id) {
		return nil, ErrKey
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	return &Keyring{Active: id, Keys: map[string][]byte{id: key}}, nil
}

func Load(path string) (*Keyring, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot open keyring file")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 65536 {
		return nil, errors.New("keyring must be a private regular file (mode 0600)")
	}
	raw, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil {
		return nil, ErrKey
	}
	return Parse(raw)
}

// Parse accepts an injected runtime secret for hosts without persistent files.
func Parse(raw []byte) (*Keyring, error) {
	if len(raw) > 65536 {
		return nil, ErrKey
	}
	var ring Keyring
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&ring) != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, ErrKey
	}
	if err := ring.Validate(); err != nil {
		return nil, err
	}
	return &ring, nil
}

func (r *Keyring) Validate() error {
	if r == nil || !keyID.MatchString(r.Active) || len(r.Keys[r.Active]) != 32 || len(r.Keys) > 32 {
		return ErrKey
	}
	for id, key := range r.Keys {
		if !keyID.MatchString(id) || len(key) != 32 {
			return ErrKey
		}
	}
	return nil
}

func (r *Keyring) WriteNew(path string) error {
	if err := r.Validate(); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errors.New("cannot create keyring; choose a new private file")
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(r)
}

func aead(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrKey
	}
	return cipher.NewGCM(block)
}

func seal(key, plaintext []byte, aad string) ([]byte, error) {
	a, err := aead(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, a.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return a.Seal(nonce, nonce, plaintext, []byte(aad)), nil
}

func open(key, ciphertext []byte, aad string) ([]byte, error) {
	a, err := aead(key)
	if err != nil || len(ciphertext) < a.NonceSize() {
		return nil, ErrKey
	}
	plain, err := a.Open(nil, ciphertext[:a.NonceSize()], ciphertext[a.NonceSize():], []byte(aad))
	if err != nil {
		return nil, ErrKey
	}
	return plain, nil
}

func (r *Keyring) Seal(value []byte, identity string) ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	data, err := seal(key, value, identity+":data")
	if err != nil {
		return nil, err
	}
	wrapped, err := seal(r.Keys[r.Active], key, identity+":"+r.Active+":key")
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope{Version: 1, KeyID: r.Active, Key: wrapped, Data: data})
}

func (r *Keyring) unwrap(blob []byte, identity string) (envelope, []byte, error) {
	var env envelope
	if len(blob) > 1<<20 {
		return env, nil, ErrKey
	}
	d := json.NewDecoder(bytes.NewReader(blob))
	d.DisallowUnknownFields()
	if d.Decode(&env) != nil || env.Version != 1 || len(r.Keys[env.KeyID]) != 32 {
		return env, nil, ErrKey
	}
	key, err := open(r.Keys[env.KeyID], env.Key, identity+":"+env.KeyID+":key")
	return env, key, err
}

func (r *Keyring) Open(blob []byte, identity string) ([]byte, error) {
	env, key, err := r.unwrap(blob, identity)
	if err != nil {
		return nil, err
	}
	return open(key, env.Data, identity+":data")
}

func (r *Keyring) Rewrap(blob []byte, identity string) ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	env, key, err := r.unwrap(blob, identity)
	if err != nil {
		return nil, err
	}
	// Authenticate the data before preserving it under a different wrapping key.
	if _, err := open(key, env.Data, identity+":data"); err != nil {
		return nil, err
	}
	env.KeyID = r.Active
	env.Key, err = seal(r.Keys[r.Active], key, identity+":"+r.Active+":key")
	if err != nil {
		return nil, err
	}
	return json.Marshal(env)
}

func Token() (string, error) {
	var data [32]byte
	if _, err := io.ReadFull(rand.Reader, data[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data[:]), nil
}
