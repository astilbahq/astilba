package agentrelease

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
)

func TestSignedArtifactBoundary(t *testing.T) {
	public, private, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	data := []byte("signed agent artifact")
	sum := sha256.Sum256(data)
	r := api.AgentRelease{Version: "test.1", Protocol: 1, Arch: "arm64", Size: int64(len(data)), Sha256: hex.EncodeToString(sum[:])}
	r.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(private, Message(r)))
	if e = Verify(r, public, bytes.NewReader(data)); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"bytes", "length", "architecture", "protocol", "version", "key"} {
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
				altered.Version = "other"
			case "key":
				key, _, _ = ed25519.GenerateKey(rand.Reader)
			}
			if Verify(altered, key, bytes.NewReader(body)) == nil {
				t.Fatal("untrusted artifact accepted")
			}
		})
	}
}
