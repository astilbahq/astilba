package portability

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"

	"golang.org/x/crypto/argon2"
)

const MaxArchiveBytes = 32 << 20
const header = "ASTILBA1"

var ErrArchive = errors.New("archive, passphrase or supported archive version is invalid")
var derivations = make(chan struct{}, 2)

// The envelope has fixed KDF parameters. An untrusted archive cannot request
// arbitrary CPU or memory allocations, and each replica caps concurrent KDFs.
func key(ctx context.Context, passphrase string, salt []byte) ([]byte, error) {
	if len(passphrase) < 20 || len(passphrase) > 1024 {
		return nil, ErrArchive
	}
	select {
	case derivations <- struct{}{}:
		defer func() { <-derivations }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return argon2.IDKey([]byte(passphrase), salt, 3, 64*1024, 2, 32), nil
}
func aead(ctx context.Context, passphrase string, salt []byte) (cipher.AEAD, error) {
	k, e := key(ctx, passphrase, salt)
	if e != nil {
		return nil, e
	}
	defer clear(k)
	b, e := aes.NewCipher(k)
	if e != nil {
		return nil, ErrArchive
	}
	return cipher.NewGCM(b)
}
func Seal(ctx context.Context, passphrase string, plaintext []byte) ([]byte, error) {
	if len(plaintext) > MaxArchiveBytes {
		return nil, ErrArchive
	}
	prefix := make([]byte, len(header)+16+12)
	copy(prefix, header)
	if _, e := rand.Read(prefix[len(header):]); e != nil {
		return nil, e
	}
	a, e := aead(ctx, passphrase, prefix[len(header):len(header)+16])
	if e != nil {
		return nil, e
	}
	return a.Seal(prefix, prefix[len(header)+16:], plaintext, prefix[:len(header)+16]), nil
}
func Open(ctx context.Context, passphrase string, envelope []byte) ([]byte, error) {
	prefix := len(header) + 16 + 12
	if len(envelope) < prefix+16 || len(envelope) > MaxArchiveBytes+prefix+16 || !bytes.Equal(envelope[:len(header)], []byte(header)) {
		return nil, ErrArchive
	}
	a, e := aead(ctx, passphrase, envelope[len(header):len(header)+16])
	if e != nil {
		return nil, e
	}
	plaintext, e := a.Open(nil, envelope[len(header)+16:prefix], envelope[prefix:], envelope[:len(header)+16])
	if e != nil {
		return nil, ErrArchive
	}
	return plaintext, nil
}
