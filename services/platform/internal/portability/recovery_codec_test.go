package portability

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// recoveryLikeBase is JSON shaped like a base: rows that repeat a large body with fresh IDs.
func recoveryLikeBase(t *testing.T, size int) []byte {
	t.Helper()
	body := bytes.Repeat([]byte(`{"name":"web","image":"registry.example.test/app@sha256:","ports":[8080],"health":{"path":"/"}},`), 160)
	var out bytes.Buffer
	out.WriteString(`[`)
	id := make([]byte, 16)
	for out.Len() < size {
		if _, err := rand.Read(id); err != nil {
			t.Fatal(err)
		}
		out.WriteString(`{"id":"` + hex.EncodeToString(id) + `","plan":[`)
		out.Write(body)
		out.WriteString(`{}]},`)
	}
	out.WriteString(`{}]`)
	return out.Bytes()
}

func TestRecoveryCodecRoundTripsBasesPastTheDefaultWindow(t *testing.T) {
	for _, size := range []int{2 << 10, 9 << 20} {
		raw := recoveryLikeBase(t, size)
		compressed, err := CompressRecovery(raw)
		if err != nil {
			t.Fatal(size, err)
		}
		if !bytes.HasPrefix(compressed, zstdMagic) {
			t.Fatal("a new base is not zstd")
		}
		out, err := DecompressRecovery(compressed)
		if err != nil || !bytes.Equal(out, raw) {
			t.Fatal("base did not round trip", size, err)
		}
	}
}

func TestRecoveryCodecReadsGzipBasesSealedBeforeIt(t *testing.T) {
	raw := recoveryLikeBase(t, 64<<10)
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	writer.Write(raw)
	writer.Close()
	out, err := DecompressRecovery(compressed.Bytes())
	if err != nil || !bytes.Equal(out, raw) {
		t.Fatal("a gzip base no longer reads", err)
	}
}

func TestRecoveryCodecNamesTheReleaseAnUnknownCodecNeeds(t *testing.T) {
	if _, err := DecompressRecovery([]byte(`{"version":6}`)); !errors.Is(err, ErrRecoveryCodec) {
		t.Fatal("an unknown codec was not reported as needing a newer release", err)
	}
}

func TestRecoveryCodecRefusesOutputPastTheArchiveLimit(t *testing.T) {
	if _, err := CompressRecovery(make([]byte, MaxArchiveBytes+1)); !errors.Is(err, ErrArchive) {
		t.Fatal("a base past the archive limit was compressed", err)
	}
	encoder, err := zstd.NewWriter(nil, zstd.WithWindowSize(MaxArchiveBytes), zstd.WithEncoderConcurrency(1))
	if err != nil {
		t.Fatal(err)
	}
	frame := encoder.EncodeAll(make([]byte, MaxArchiveBytes+1), nil)
	encoder.Close()
	if _, err := DecompressRecovery(frame); !errors.Is(err, ErrArchive) {
		t.Fatal("a frame decompressing past the archive limit was read", err)
	}
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	writer.Write(make([]byte, MaxArchiveBytes+1))
	writer.Close()
	if _, err := DecompressRecovery(compressed.Bytes()); !errors.Is(err, ErrArchive) {
		t.Fatal("a gzip base decompressing past the archive limit was read", err)
	}
}
