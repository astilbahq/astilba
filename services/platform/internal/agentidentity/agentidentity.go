// Package agentidentity holds what an agent and management must agree on byte for byte: the
// agent key's encoding and every message the agent signs with it.
package agentidentity

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// TokenPrefix marks an access token, so a legacy bearer is never mistaken for one.
const TokenPrefix = "at1_"

// KeysHeader is the response header by which management says it authenticates agent keys.
const KeysHeader = "Astilba-Agent-Keys"

// Refusal codes: why management refused an agent's credential. An agent acts on one only when
// the response also carries KeysHeader, because a proxy or an older controller sends neither.
const (
	KeyNotCurrent   = "key_not_current"
	NonceInvalid    = "nonce_invalid"
	CounterStale    = "counter_stale"
	RefreshReused   = "refresh_reused"
	CodeNotAccepted = "code_not_accepted"
)

var encoding = base64.RawURLEncoding

// GenerateKey makes a new agent key.
func GenerateKey() (*ecdsa.PrivateKey, error) { return ecdsa.GenerateKey(elliptic.P256(), rand.Reader) }

// PublicKey returns a key's public half as management receives it.
func PublicKey(k *ecdsa.PrivateKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
	if err != nil {
		return "", err
	}
	return encoding.EncodeToString(der), nil
}

// ParsePublicKey reads an encoded public key and returns its DER, refusing anything but P-256.
func ParsePublicKey(encoded string) (*ecdsa.PublicKey, []byte, error) {
	der, err := encoding.DecodeString(encoded)
	if err != nil || len(der) > 200 {
		return nil, nil, errors.New("invalid public key")
	}
	parsed, err := x509.ParsePKIXPublicKey(der)
	key, ok := parsed.(*ecdsa.PublicKey)
	if err != nil || !ok || key.Curve != elliptic.P256() {
		return nil, nil, errors.New("invalid public key")
	}
	return key, der, nil
}

// KeyHash identifies a key: the hash of its DER, which is what management keeps as the server's
// current credential.
func KeyHash(der []byte) []byte {
	sum := sha256.Sum256(der)
	return sum[:]
}

// Hex is the form in which hashes appear inside signed messages.
func Hex(b []byte) string { return hex.EncodeToString(b) }

// HashHex hashes a secret for a signed message, so the message never carries the secret itself.
func HashHex(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func message(label string, fields ...string) []byte {
	return []byte(label + "\x00" + strings.Join(fields, "\x00"))
}

// TokenMessage is signed to start a session.
func TokenMessage(server uuid.UUID, nonce string, counter int64) []byte {
	return message("astilba agent token v1", server.String(), nonce, strconv.FormatInt(counter, 10))
}

// RefreshMessage is signed to renew a session's token.
func RefreshMessage(server uuid.UUID, refresh string) []byte {
	return message("astilba agent refresh v1", server.String(), HashHex(refresh))
}

// EnrollMessage binds a key to one enrollment code and the machine that sends it.
func EnrollMessage(server uuid.UUID, code string, machine uuid.UUID) []byte {
	return message("astilba agent enroll v1", server.String(), HashHex(code), machine.String())
}

// BindMessage binds a key to the server that held a bearer credential until now.
func BindMessage(server, machine uuid.UUID, keyHash []byte) []byte {
	return message("astilba agent bind v1", server.String(), machine.String(), Hex(keyHash))
}

// KeysMessage is signed by both the current key and its successor.
func KeysMessage(newKey bool, server uuid.UUID, epoch int64, nonce string, keyHash []byte) []byte {
	label := "astilba agent keys v1"
	if newKey {
		label = "astilba agent keys new v1"
	}
	return message(label, server.String(), strconv.FormatInt(epoch, 10), nonce, Hex(keyHash))
}

// Sign signs a message with an agent key.
func Sign(k *ecdsa.PrivateKey, msg []byte) (string, error) {
	sum := sha256.Sum256(msg)
	sig, err := ecdsa.SignASN1(rand.Reader, k, sum[:])
	if err != nil {
		return "", err
	}
	return encoding.EncodeToString(sig), nil
}

// Verify checks a signature made with Sign.
func Verify(k *ecdsa.PublicKey, msg []byte, signature string) bool {
	sig, err := encoding.DecodeString(signature)
	if err != nil || len(sig) > 80 {
		return false
	}
	sum := sha256.Sum256(msg)
	return ecdsa.VerifyASN1(k, sum[:], sig)
}

// MarshalPrivate and ParsePrivate move a key in and out of sealed storage.
func MarshalPrivate(k *ecdsa.PrivateKey) ([]byte, error) { return x509.MarshalPKCS8PrivateKey(k) }

func ParsePrivate(der []byte) (*ecdsa.PrivateKey, error) {
	parsed, err := x509.ParsePKCS8PrivateKey(der)
	key, ok := parsed.(*ecdsa.PrivateKey)
	if err != nil || !ok || key.Curve != elliptic.P256() {
		return nil, errors.New("invalid agent key")
	}
	return key, nil
}
