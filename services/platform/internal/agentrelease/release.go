// Package agentrelease verifies the operator's pinned release key and exact agent artifacts.
package agentrelease

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"github.com/astilbahq/astilba/services/platform/internal/api"
)

const MaxSize int64 = 100 << 20

var versionPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,79}$`)
var hashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func Message(r api.AgentRelease) []byte {
	return []byte(fmt.Sprintf("astilba-agent-v1\n%s\n%d\n%s\n%d\n%s\n", r.Version, r.Protocol, r.Arch, r.Size, r.Sha256))
}
func PublicKey(data []byte) (ed25519.PublicKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("release public key must be PEM")
	}
	key, e := x509.ParsePKIXPublicKey(block.Bytes)
	if e != nil {
		return nil, e
	}
	k, ok := key.(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("release key must be Ed25519")
	}
	return k, nil
}
func Validate(r api.AgentRelease, key ed25519.PublicKey) error {
	if !versionPattern.MatchString(r.Version) || !hashPattern.MatchString(r.Sha256) || r.Protocol != 1 || (r.Arch != "amd64" && r.Arch != "arm64") || r.Size < 1 || r.Size > MaxSize {
		return errors.New("incompatible release manifest")
	}
	sig, e := base64.StdEncoding.DecodeString(r.Signature)
	if e != nil || len(key) != ed25519.PublicKeySize || !ed25519.Verify(key, Message(r), sig) {
		return errors.New("release signature is invalid")
	}
	return nil
}
func Verify(r api.AgentRelease, key ed25519.PublicKey, reader io.Reader) error {
	if e := Validate(r, key); e != nil {
		return e
	}
	h := sha256.New()
	n, e := io.Copy(h, io.LimitReader(reader, r.Size+1))
	if e != nil {
		return e
	}
	if n != r.Size || hex.EncodeToString(h.Sum(nil)) != r.Sha256 {
		return errors.New("artifact hash or size mismatch")
	}
	return nil
}
func Load(dir string, key ed25519.PublicKey) ([]api.AgentRelease, error) {
	out := []api.AgentRelease{}
	entries, e := os.ReadDir(dir)
	if e != nil {
		return nil, e
	}
	if len(entries) > 256 {
		return nil, errors.New("release catalog exceeds limit")
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, e := os.ReadFile(filepath.Join(dir, entry.Name()))
		if e != nil {
			return nil, e
		}
		if len(data) > 4096 {
			return nil, errors.New("manifest exceeds limit")
		}
		var r api.AgentRelease
		if e = json.Unmarshal(data, &r); e != nil {
			return nil, e
		}
		if e = Validate(r, key); e != nil {
			return nil, e
		}
		file, e := os.Open(filepath.Join(dir, r.Sha256))
		if e != nil {
			return nil, e
		}
		e = Verify(r, key, file)
		file.Close()
		if e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, nil
}
