// Package agentrelease verifies the operator's pinned release key and exact agent artifacts.
package agentrelease

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json/v2"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"golang.org/x/mod/semver"

	"github.com/astilbahq/astilba/services/platform/internal/api"
)

const MaxSize int64 = 100 << 20

// Domain is the first line of every signed release message. It keeps a release signature from
// being taken as a signature over anything else, and names the format.
const Domain = "astilba-agent-release-signature-v2"

// A version is a semantic version without build metadata, so that releases have an order that a
// minimum version can bound.
var versionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-(0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)(\.(0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*))*)?$`)
var hashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var commitPattern = regexp.MustCompile(`^[a-f0-9]{40}$`)

// ValidVersion reports whether v can name a release.
func ValidVersion(v string) bool { return len(v) <= 80 && versionPattern.MatchString(v) }

// CompareVersions orders two valid versions as semantic versioning does, returning -1, 0 or +1.
func CompareVersions(a, b string) int { return semver.Compare("v"+a, "v"+b) }

// Message is exactly what a release's signature covers, one LF-terminated field per line.
func Message(r api.AgentRelease) []byte {
	return []byte(fmt.Sprintf("%s\n%s\n%d\n%s\n%d\n%s\n%s\n%s\n", Domain, r.Version, r.Protocol, r.Arch, r.Size, r.Sha256, r.Commit, r.MinimumVersion))
}

// PublicKey reads a release key: one PEM PUBLIC KEY block holding an Ed25519 key, with only
// whitespace around it, so that no other tool can read a different key from the same file.
func PublicKey(data []byte) (ed25519.PublicKey, error) {
	block, rest := pem.Decode(data)
	if block == nil || bytes.Count(data, []byte("-----BEGIN")) != 1 || len(bytes.TrimSpace(data[:bytes.Index(data, []byte("-----BEGIN"))])) != 0 ||
		len(bytes.TrimSpace(rest)) != 0 || block.Type != "PUBLIC KEY" || len(block.Headers) != 0 {
		return nil, errors.New("release public key must be a single PEM PUBLIC KEY block and nothing else")
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

func Fingerprint(key ed25519.PublicKey) string {
	if len(key) != ed25519.PublicKeySize {
		return ""
	}
	raw, err := x509.MarshalPKIXPublicKey(key)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// Check validates a release's fields, without its signature, as release-manifest and
// release-sign do before anything is signed.
func Check(r api.AgentRelease) error {
	if !ValidVersion(r.Version) || !hashPattern.MatchString(r.Sha256) || !commitPattern.MatchString(r.Commit) || r.Protocol != 1 || (r.Arch != "amd64" && r.Arch != "arm64") || r.Size < 1 || r.Size > MaxSize {
		return errors.New("incompatible release manifest")
	}
	if !ValidVersion(r.MinimumVersion) || CompareVersions(r.MinimumVersion, r.Version) > 0 {
		return errors.New("a release's minimum version must be a version no later than its own")
	}
	return nil
}

func Validate(r api.AgentRelease, key ed25519.PublicKey) error {
	if e := Check(r); e != nil {
		return e
	}
	sig, e := base64.StdEncoding.DecodeString(r.Signature)
	if e != nil || len(key) != ed25519.PublicKeySize || !ed25519.Verify(key, Message(r), sig) {
		return errors.New("release signature is invalid")
	}
	return nil
}

// Permits reports whether an agent whose own release has minimum version floor may install r. r
// must have a minimum version at least as high: then r is never older than floor either, since a
// release's minimum version is no later than its version, and an agent's floor only ever rises,
// across any chain of updates. Rolling back among releases that share a floor stays possible.
// A compromised management can't move an agent below its floor, even one step at a time.
func Permits(floor string, r api.AgentRelease) error {
	if !ValidVersion(floor) {
		return errors.New("this agent has no valid minimum version, so it can't tell which releases it may install")
	}
	if CompareVersions(r.MinimumVersion, floor) < 0 {
		return fmt.Errorf("release %s allows going back to %s, below %s, the minimum version of the release this agent runs", r.Version, r.MinimumVersion, floor)
	}
	return nil
}

// Identity is what an agent build reports about itself with describe. Its fields are in the order
// describe writes them, which the bootstrap compares byte for byte.
type Identity struct {
	Arch   string `json:"Arch"`
	Commit string `json:"Commit"`
	// KeyAuth 1: this release authenticates with a key it holds, so an agent that holds one may
	// install it.
	KeyAuth        int    `json:"KeyAuth"`
	MinimumVersion string `json:"MinimumVersion"`
	Protocol       int    `json:"Protocol"`
	// StateVersion is the newest agent journal format this release reads. Releases without it
	// read formats up to the keyed one.
	StateVersion int    `json:"StateVersion"`
	Version      string `json:"Version"`
}

// Describes reports whether a build with this identity is the one release r was signed for.
func (i Identity) Describes(r api.AgentRelease) bool {
	return i.Version == r.Version && i.MinimumVersion == r.MinimumVersion && i.Commit == r.Commit && i.Protocol == int(r.Protocol) && i.Arch == string(r.Arch)
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

// ArtifactPath is where a release's artifact lives in its catalog directory. The file is named
// by the release's digest, which Validate requires to be 64 lowercase hex characters, so the
// path never leaves dir.
func ArtifactPath(dir string, r api.AgentRelease) string {
	return filepath.Join(dir, r.Sha256)
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
			return nil, fmt.Errorf("release manifest %s: %w", entry.Name(), e)
		}
		if e = Validate(r, key); e != nil {
			return nil, fmt.Errorf("release manifest %s: %w", entry.Name(), e)
		}
		file, e := os.Open(ArtifactPath(dir, r))
		if e != nil {
			return nil, e
		}
		e = Verify(r, key, file)
		file.Close()
		if e != nil {
			return nil, fmt.Errorf("release manifest %s: %w", entry.Name(), e)
		}
		out = append(out, r)
	}
	return out, nil
}
