package agent

import (
	"archive/tar"
	"context"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/google/uuid"
)

const cacheEntryBytes int64 = 128 << 20
const cacheProjectBytes int64 = 256 << 20
const cacheTotalBytes int64 = 1 << 30
const cacheMaxAge = 7 * 24 * time.Hour
const cacheContainerDir = "/var/lib/buildkit/astilba-cache"

type buildCache struct{ root *os.Root }
type cacheEntry struct {
	name, scope string
	size        int64
	used        time.Time
}

// Execute's mutex and the exclusively opened journal serialize this store.
// No build container mounts this directory; only bounded archives cross Docker.
func openBuildCache(dir string) (*buildCache, error) {
	if err := os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
		return nil, err
	}
	i, err := os.Lstat(dir)
	if err != nil || !i.IsDir() || i.Mode().Perm()&0077 != 0 {
		return nil, errors.New("cache directory is not private")
	}
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	return &buildCache{r}, nil
}

func (s *buildCache) entries(now time.Time) ([]cacheEntry, error) {
	dir, err := s.root.Open(".")
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	files, err := dir.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	entries := []cacheEntry{}
	for _, f := range files {
		info, err := f.Info()
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return nil, errors.New("cache ownership differs")
		}
		name := f.Name()
		if strings.HasPrefix(name, ".pending-") {
			if _, err := uuid.Parse(strings.TrimPrefix(name, ".pending-")); err != nil {
				return nil, errors.New("unrecognized cache file")
			}
			if err := s.root.Remove(name); err != nil {
				return nil, err
			}
			continue
		}
		parts := strings.Split(name, ".")
		if len(parts) != 3 || !pipeline.ValidCacheID(parts[0]) || !pipeline.ValidCacheID(parts[1]) || parts[2] != "tar" {
			return nil, errors.New("unrecognized cache file")
		}
		if info.Size() > cacheEntryBytes || now.Sub(info.ModTime()) > cacheMaxAge {
			if err := s.root.Remove(name); err != nil {
				return nil, err
			}
			continue
		}
		entries = append(entries, cacheEntry{name, parts[0], info.Size(), info.ModTime()})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].used.Equal(entries[j].used) {
			return entries[i].name < entries[j].name
		}
		return entries[i].used.Before(entries[j].used)
	})
	return entries, nil
}

// Reserve the maximum temporary archive too, so interruption cannot exceed the
// retention ceiling. Eviction never touches Docker images or workload volumes.
func (s *buildCache) reserve(scope string, now time.Time) error {
	entries, err := s.entries(now)
	if err != nil {
		return err
	}
	var total, project int64
	var count int
	for _, e := range entries {
		total += e.size
		if e.scope == scope {
			project += e.size
			count++
		}
	}
	remaining := len(entries)
	for {
		projectFull := project+cacheEntryBytes > cacheProjectBytes || count >= 8
		if !projectFull && total+cacheEntryBytes <= cacheTotalBytes && remaining < 32 {
			return nil
		}
		oldest := -1
		for i, entry := range entries {
			if !projectFull || entry.scope == scope {
				oldest = i
				break
			}
		}
		if oldest < 0 {
			return errors.New("cannot reserve bounded cache storage")
		}
		e := entries[oldest]
		if err := s.root.Remove(e.name); err != nil {
			return err
		}
		total -= e.size
		remaining--
		if e.scope == scope {
			project -= e.size
			count--
		}
		entries = append(entries[:oldest], entries[oldest+1:]...)
	}
}

type cacheWriter struct {
	w io.Writer
	n int64
}

func (w *cacheWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > cacheEntryBytes-w.n {
		return 0, errors.New("cache archive exceeds its retention budget")
	}
	n, err := w.w.Write(p)
	w.n += int64(n)
	return n, err
}

// Canonicalize the OCI layout without extracting anything on the host. Reject
// links, devices, traversal and oversized entries before Docker sees an import.
func writeCacheArchive(dst io.Writer, src io.Reader) error {
	in := tar.NewReader(io.LimitReader(src, cacheEntryBytes+1))
	out := tar.NewWriter(&cacheWriter{w: dst})
	seen := map[string]bool{}
	for count := 0; ; count++ {
		h, err := in.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		name := strings.TrimSuffix(h.Name, "/")
		if count >= 4096 || path.Clean(name) != name || seen[name] || h.Size < 0 || h.Size > cacheEntryBytes {
			return errors.New("invalid cache archive")
		}
		seen[name] = true
		// BuildKit's local content store leaves this empty working directory.
		if name == "cache-out/ingest" && h.Typeflag == tar.TypeDir && h.Size == 0 {
			continue
		}
		if name == "cache-out/cache.lock" && (h.Typeflag == tar.TypeReg || h.Typeflag == tar.TypeRegA) && h.Size == 0 {
			continue
		}
		rel := strings.TrimPrefix(name, "cache-out/")
		directory := name == "cache-out" || name != rel && (rel == "blobs" || rel == "blobs/sha256")
		file := rel == "oci-layout" || rel == "index.json" || strings.HasPrefix(rel, "blobs/sha256/") && pipeline.ValidCacheID(strings.TrimPrefix(rel, "blobs/sha256/"))
		if directory && h.Typeflag == tar.TypeDir {
			err = out.WriteHeader(&tar.Header{Name: path.Join("in", strings.TrimPrefix(rel, "cache-out")), Typeflag: tar.TypeDir, Mode: 0700})
		} else if name != rel && file && (h.Typeflag == tar.TypeReg || h.Typeflag == tar.TypeRegA) {
			err = out.WriteHeader(&tar.Header{Name: path.Join("in", rel), Typeflag: tar.TypeReg, Mode: 0600, Size: h.Size})
			if err == nil {
				_, err = io.CopyN(out, in, h.Size)
			}
		} else {
			return errors.New("cache contains an unexpected path or file type")
		}
		if err != nil {
			return err
		}
	}
	if !seen["cache-out/index.json"] || !seen["cache-out/oci-layout"] {
		return errors.New("cache has no OCI index")
	}
	return out.Close()
}

func (e *Engine) importBuildCache(ctx context.Context, c api.ExecutionCommand, p pipeline.Command, s *buildCache) bool {
	if _, err := s.entries(time.Now()); err != nil {
		return false
	}
	name := p.CacheScope + "." + p.CacheKey() + ".tar"
	f, err := s.root.Open(name)
	if err != nil {
		return false
	}
	defer f.Close()
	if _, err = e.pipelineCLI(ctx, pipelineName(c), nil, nil, 1024, "mkdir", "-p", cacheContainerDir); err != nil {
		return false
	}
	cmd := e.pipelineExec(ctx, pipelineName(c), nil, "tar", "-xf", "-", "-C", cacheContainerDir)
	cmd.Stdin = f
	err = cmd.Run()
	if err == nil {
		_ = s.root.Chtimes(name, time.Now(), time.Now())
	}
	return err == nil
}

func (e *Engine) retainBuildCache(ctx context.Context, c api.ExecutionCommand, p pipeline.Command, s *buildCache) bool {
	if e.cancelled(c.Id) || ctx.Err() != nil || freeBytes(filepath.Join(e.Journal.dir, "build-cache-v1")) < (4<<30)+cacheEntryBytes {
		return false
	}
	if err := s.reserve(p.CacheScope, time.Now()); err != nil {
		return false
	}
	name := ".pending-" + uuid.NewString()
	f, err := s.root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return false
	}
	defer s.root.Remove(name)
	cmd := e.pipelineExec(ctx, pipelineName(c), nil, "tar", "-cf", "-", "-C", cacheContainerDir, "cache-out")
	stream, err := cmd.StdoutPipe()
	if err != nil {
		f.Close()
		return false
	}
	if err = cmd.Start(); err != nil {
		f.Close()
		return false
	}
	limited := &io.LimitedReader{R: stream, N: cacheEntryBytes + 1}
	err = writeCacheArchive(f, limited)
	if err == nil {
		_, err = io.Copy(io.Discard, limited)
	}
	if err != nil || limited.N == 0 {
		_ = cmd.Process.Kill()
		err = errors.New("cache export exceeded its archive budget or layout")
	}
	waitErr := cmd.Wait()
	closeErr := f.Close()
	if err != nil || waitErr != nil || closeErr != nil || e.cancelled(c.Id) || ctx.Err() != nil {
		return false
	}
	return s.root.Rename(name, p.CacheScope+"."+p.CacheKey()+".tar") == nil
}
