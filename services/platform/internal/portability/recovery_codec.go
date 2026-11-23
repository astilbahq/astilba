package portability

import (
	"bytes"
	"errors"
	"math/bits"

	"github.com/klauspost/compress/zstd"
)

// ErrRecoveryCodec reports a recovery base compressed in a way this release cannot read. Bases
// sealed by a newer release can't be read by an older one, so cross-host recovery has to run on
// the release that sealed them or a newer one.
var ErrRecoveryCodec = errors.New("recovery checkpoint uses a compression this release cannot read; run recovery with the release that sealed it or a newer one")

var zstdMagic = []byte{0x28, 0xb5, 0x2f, 0xfd}

// CompressRecovery compresses a recovery base for sealing. A base repeats each plan in several
// tables, further apart than deflate's 32 KiB window, so zstd with a window covering the whole
// base holds about 2.5 times the history gzip did in the same envelope. Each call has its own
// encoder, whose window is sized to the base, so memory follows the base and nothing outlives
// the call; lower-memory mode keeps the encoder's history to one window, which loses nothing when
// the window already covers the whole input. The result is decompressed again before it is
// returned, so a base the recovery reader can't read is refused when it is captured, not when a
// recovery needs it.
func CompressRecovery(raw []byte) ([]byte, error) {
	if len(raw) > MaxArchiveBytes {
		return nil, ErrArchive
	}
	window := max(zstd.MinWindowSize, 1<<bits.Len(uint(max(len(raw), 1)-1)))
	encoder, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBetterCompression), zstd.WithWindowSize(window), zstd.WithLowerEncoderMem(true), zstd.WithEncoderConcurrency(1))
	if err != nil {
		return nil, err
	}
	compressed := encoder.EncodeAll(raw, nil)
	if err := encoder.Close(); err != nil {
		clear(compressed)
		return nil, err
	}
	decoded, err := DecompressRecovery(compressed)
	equal := err == nil && bytes.Equal(decoded, raw)
	clear(decoded)
	if !equal {
		clear(compressed)
		return nil, ErrArchive
	}
	return compressed, nil
}

// DecompressRecovery reads a base sealed by this release (zstd). Earlier releases compressed bases
// with gzip, but only under version 1 descriptors, which recovery refuses before opening them.
// Bases are opened from management's own sealed envelopes, and their output is capped at
// MaxArchiveBytes.
func DecompressRecovery(compressed []byte) ([]byte, error) {
	switch {
	case bytes.HasPrefix(compressed, zstdMagic):
		decoder, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxWindow(MaxArchiveBytes), zstd.WithDecoderMaxMemory(MaxArchiveBytes+1))
		if err != nil {
			return nil, err
		}
		defer decoder.Close()
		out, err := decoder.DecodeAll(compressed, nil)
		if err != nil || len(out) > MaxArchiveBytes {
			clear(out)
			return nil, ErrArchive
		}
		return out, nil
	default:
		return nil, ErrRecoveryCodec
	}
}
