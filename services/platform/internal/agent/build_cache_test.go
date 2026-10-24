package agent

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCacheArchiveContainsOnlyBoundedOCILayout(t *testing.T) {
	archive := func(extra *tar.Header) []byte {
		var b bytes.Buffer
		w := tar.NewWriter(&b)
		for _, name := range []string{"cache-out", "cache-out/blobs", "cache-out/blobs/sha256", "cache-out/ingest"} {
			if err := w.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeDir, Mode: 0700}); err != nil {
				t.Fatal(err)
			}
		}
		for _, name := range []string{"cache-out/index.json", "cache-out/oci-layout", "cache-out/blobs/sha256/" + strings.Repeat("a", 64)} {
			if err := w.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0600, Size: 2}); err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write([]byte("{}"))
		}
		if err := w.WriteHeader(&tar.Header{Name: "cache-out/cache.lock", Typeflag: tar.TypeReg, Mode: 0600}); err != nil {
			t.Fatal(err)
		}
		if extra != nil {
			if err := w.WriteHeader(extra); err != nil {
				t.Fatal(err)
			}
		}
		_ = w.Close()
		return b.Bytes()
	}
	var out bytes.Buffer
	if err := writeCacheArchive(&out, bytes.NewReader(archive(nil))); err != nil {
		t.Fatal(err)
	}
	r := tar.NewReader(&out)
	for {
		h, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil || h.Name != "in" && !strings.HasPrefix(h.Name, "in/") || h.Typeflag != tar.TypeDir && h.Typeflag != tar.TypeReg {
			t.Fatal("unsafe imported archive", h, err)
		}
	}
	for _, h := range []*tar.Header{
		{Name: "cache-out/../escape", Typeflag: tar.TypeReg},
		{Name: "cache-out/link", Typeflag: tar.TypeSymlink, Linkname: "/workspace/auth"},
		{Name: "cache-out/blobs/sha256/" + strings.Repeat("b", 64), Typeflag: tar.TypeReg, Size: cacheEntryBytes + 1},
		{Name: "cache-out/index.json", Typeflag: tar.TypeReg},
	} {
		if err := writeCacheArchive(io.Discard, bytes.NewReader(archive(h))); err == nil {
			t.Fatal("accepted unsafe cache entry", h.Name)
		}
	}
}

func TestCacheRetentionReservesTemporaryStorageAndPreservesOwnership(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	s, err := openBuildCache(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.root.Close()
	now := time.Now()
	put := func(scope, key string, size int64, age time.Duration) string {
		t.Helper()
		name := scope + "." + key + ".tar"
		f, err := s.root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		if err = f.Truncate(size); err != nil {
			t.Fatal(err)
		}
		f.Close()
		if err = s.root.Chtimes(name, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
		return name
	}
	scope := strings.Repeat("a", 64)
	old := put(scope, fmt.Sprintf("%064x", 0), cacheEntryBytes, 2*time.Hour)
	put(scope, fmt.Sprintf("%064x", 1), cacheEntryBytes, time.Hour)
	for i := 2; i < 8; i++ {
		put(fmt.Sprintf("%064x", i), fmt.Sprintf("%064x", i), cacheEntryBytes, time.Duration(i)*time.Hour)
	}
	if err = s.reserve(scope, now); err != nil {
		t.Fatal(err)
	}
	if _, err = s.root.Stat(old); !os.IsNotExist(err) {
		t.Fatal("did not evict least-recent project archive")
	}
	entries, err := s.entries(now)
	if err != nil {
		t.Fatal(err)
	}
	var total, project int64
	for _, e := range entries {
		total += e.size
		if e.scope == scope {
			project += e.size
		}
	}
	if total+cacheEntryBytes > cacheTotalBytes || project+cacheEntryBytes > cacheProjectBytes {
		t.Fatal("temporary archive exceeds retention ceilings")
	}
	expired := put(scope, fmt.Sprintf("%064x", 9), 1, cacheMaxAge+time.Hour)
	pending := ".pending-" + uuid.NewString()
	if err = s.root.WriteFile(pending, []byte("interrupted"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.entries(now); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{expired, pending} {
		if _, err = s.root.Stat(name); !os.IsNotExist(err) {
			t.Fatal("did not clean expired or interrupted cache")
		}
	}
	if err = s.root.WriteFile("unowned", []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = s.reserve(scope, now); err == nil {
		t.Fatal("used a cache directory with unrecognized ownership")
	}
	if v, err := s.root.ReadFile("unowned"); err != nil || string(v) != "preserve" {
		t.Fatal("deleted an unrecognized file")
	}
}
