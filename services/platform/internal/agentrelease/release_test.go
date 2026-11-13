package agentrelease

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
)

func signedRelease(t *testing.T, data []byte) (api.AgentRelease, ed25519.PublicKey) {
	t.Helper()
	public, private, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	sum := sha256.Sum256(data)
	r := api.AgentRelease{Version: "1.4.0", MinimumVersion: "1.2.0", Commit: "0123456789abcdef0123456789abcdef01234567", Protocol: 1, Arch: "arm64", Size: int64(len(data)), Sha256: hex.EncodeToString(sum[:])}
	r.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(private, Message(r)))
	return r, public
}

// The signature covers every field, so none can be changed without the key.
func TestSignedArtifactBoundary(t *testing.T) {
	data := []byte("signed agent artifact")
	r, public := signedRelease(t, data)
	if e := Verify(r, public, bytes.NewReader(data)); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"bytes", "length", "architecture", "protocol", "version", "commit", "minimum version", "key"} {
		t.Run(name, func(t *testing.T) {
			altered := r
			body := append([]byte{}, data...)
			key := public
			switch name {
			case "bytes":
				body[0] ^= 1
			case "length":
				body = append(body, 1)
			case "architecture":
				altered.Arch = "amd64"
			case "protocol":
				altered.Protocol = 2
			case "version":
				altered.Version = "1.4.1"
			case "commit":
				altered.Commit = strings.Repeat("f", 40)
			case "minimum version":
				altered.MinimumVersion = "1.0.0"
			case "key":
				key, _, _ = ed25519.GenerateKey(rand.Reader)
			}
			if Verify(altered, key, bytes.NewReader(body)) == nil {
				t.Fatal("untrusted artifact accepted")
			}
		})
	}
}

// The bootstrap and operators verify this exact text with OpenSSL, so it is the format.
func TestMessageFormat(t *testing.T) {
	r, _ := signedRelease(t, []byte("x"))
	want := "astilba-agent-release-signature-v2\n1.4.0\n1\narm64\n1\n" + r.Sha256 + "\n0123456789abcdef0123456789abcdef01234567\n1.2.0\n"
	if got := string(Message(r)); got != want {
		t.Fatalf("message %q, want %q", got, want)
	}
}

// A release is refused before signing unless a host could act on every field.
func TestReleaseFields(t *testing.T) {
	good, _ := signedRelease(t, []byte("x"))
	if e := Check(good); e != nil {
		t.Fatal(e)
	}
	for name, change := range map[string]func(*api.AgentRelease){
		"free-form version":           func(r *api.AgentRelease) { r.Version = "fixture.2" },
		"build metadata":              func(r *api.AgentRelease) { r.Version = "1.4.0+linux" },
		"leading zero":                func(r *api.AgentRelease) { r.Version = "1.04.0" },
		"long version":                func(r *api.AgentRelease) { r.Version = "1.4.0-" + strings.Repeat("a", 75) },
		"short commit":                func(r *api.AgentRelease) { r.Commit = "0123456" },
		"uppercase commit":            func(r *api.AgentRelease) { r.Commit = strings.Repeat("A", 40) },
		"no minimum version":          func(r *api.AgentRelease) { r.MinimumVersion = "" },
		"minimum after version":       func(r *api.AgentRelease) { r.MinimumVersion = "1.4.1" },
		"minimum after a pre-release": func(r *api.AgentRelease) { r.Version, r.MinimumVersion = "1.4.0-rc.1", "1.4.0" },
	} {
		t.Run(name, func(t *testing.T) {
			r := good
			change(&r)
			if Check(r) == nil {
				t.Fatalf("accepted %+v", r)
			}
		})
	}
	pre := good
	pre.Version, pre.MinimumVersion = "1.4.0-rc.2", "1.4.0-rc.1"
	if e := Check(pre); e != nil {
		t.Fatalf("a pre-release with an earlier pre-release floor: %v", e)
	}
}

// An agent never installs a release older than the minimum version of the one it runs.
func TestPermitsBoundsRollback(t *testing.T) {
	r, _ := signedRelease(t, []byte("x"))
	for _, test := range []struct {
		floor, version string
		ok             bool
	}{
		{"1.2.0", "1.2.0", true},
		{"1.2.0", "1.10.0", true},
		{"1.2.0", "1.1.9", false},
		{"1.2.0", "1.2.0-rc.1", false},
		{"1.2.0-rc.1", "1.2.0-rc.2", true},
		{"", "1.2.0", false},
		{"development", "1.2.0", false},
	} {
		r.Version = test.version
		if e := Permits(test.floor, r); (e == nil) != test.ok {
			t.Errorf("floor %q, release %q: %v, want ok=%v", test.floor, test.version, e, test.ok)
		}
	}
}

// The bootstrap checks the fingerprint of the key OpenSSL reads from the served file. The agent
// must not read another key from the same file, so a release key file holds one PUBLIC KEY block
// and nothing else.
func TestPublicKeyIsOneBlock(t *testing.T) {
	public, _, _ := ed25519.GenerateKey(rand.Reader)
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	encode := func(block *pem.Block, key ed25519.PublicKey) []byte {
		der, err := x509.MarshalPKIXPublicKey(key)
		if err != nil {
			t.Fatal(err)
		}
		block.Bytes = der
		return pem.EncodeToMemory(block)
	}
	key := encode(&pem.Block{Type: "PUBLIC KEY"}, public)
	if k, e := PublicKey(key); e != nil || !k.Equal(public) {
		t.Fatalf("canonical key: %v", e)
	}
	if k, e := PublicKey(append(append([]byte("\n\n"), key...), "\r\n\n"...)); e != nil || !k.Equal(public) {
		t.Fatalf("key with surrounding whitespace: %v", e)
	}
	for name, data := range map[string][]byte{
		// OpenSSL skips a block of another type and reads the second; the agent once read the first.
		"another key in a block of another type first": append(encode(&pem.Block{Type: "OTHER KEY"}, other), key...),
		"another public key first":                     append(encode(&pem.Block{Type: "PUBLIC KEY"}, other), key...),
		"another public key after":                     append(append([]byte{}, key...), encode(&pem.Block{Type: "PUBLIC KEY"}, other)...),
		"trailing text":                                append(append([]byte{}, key...), "trailing"...),
		"leading text":                                 append([]byte("comment\n"), key...),
		"wrong block type":                             encode(&pem.Block{Type: "RSA PUBLIC KEY"}, public),
		"headers":                                      encode(&pem.Block{Type: "PUBLIC KEY", Headers: map[string]string{"Comment": "x"}}, public),
		"no PEM":                                       []byte("not a key"),
	} {
		t.Run(name, func(t *testing.T) {
			if k, e := PublicKey(data); e == nil {
				t.Fatalf("accepted %x from:\n%s", k, data)
			}
		})
	}
}
