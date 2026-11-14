package agent

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/opencontainers/go-digest"
)

// checkedManifestsHeader starts the file of checked manifests. Its version names the rules a
// manifest was checked by (leafBlobs), so a file from other rules is read as empty.
const checkedManifestsHeader = "astilba checked manifests v1\n"

// checkedManifestsFile is the file's name in the agent's state directory.
const checkedManifestsFile = "checked-manifests"

// checkedManifests are manifests checkSiblings found harmless, by digest, so a later load does
// not read them from the registry again: a manifest's digest names its content, so the verdict
// holds wherever the manifest comes from. They are kept in path, when it is set, across the
// agent's restarts. A file the agent cannot read or parse whole counts as empty, never as
// checked.
type checkedManifests struct {
	mu      sync.Mutex
	path    string
	loaded  bool
	digests map[digest.Digest]bool
	order   []digest.Digest
	dirty   bool
}

func (c *checkedManifests) has(d digest.Digest) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.load()
	return c.digests[d]
}

// add records d, dropping the oldest records beyond layerCostEntries. flush saves them.
func (c *checkedManifests) add(d digest.Digest) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.load()
	if c.digests[d] {
		return
	}
	c.digests[d] = true
	c.order = append(c.order, d)
	c.dirty = true
	for len(c.order) > layerCostEntries {
		delete(c.digests, c.order[0])
		c.order = c.order[1:]
	}
}

// flush saves what add recorded since. A failure to save leaves the records in memory only.
func (c *checkedManifests) flush() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dirty && c.save() == nil {
		c.dirty = false
	}
}

func (c *checkedManifests) load() {
	if c.loaded {
		return
	}
	c.loaded = true
	c.digests = map[digest.Digest]bool{}
	if c.path == "" {
		return
	}
	f, err := os.OpenFile(c.path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return
	}
	limit := int64(len(checkedManifestsHeader) + layerCostEntries*72)
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(data)) > limit {
		return
	}
	body, ok := bytes.CutPrefix(data, []byte(checkedManifestsHeader))
	if !ok {
		return
	}
	digests := map[digest.Digest]bool{}
	var order []digest.Digest
	for _, line := range strings.Split(strings.TrimSuffix(string(body), "\n"), "\n") {
		if line == "" && len(body) == 0 {
			break
		}
		d, err := digest.Parse(line)
		if err != nil || d.Algorithm() != digest.SHA256 || digests[d] {
			return
		}
		digests[d] = true
		order = append(order, d)
	}
	if len(order) > layerCostEntries {
		return
	}
	c.digests, c.order = digests, order
}

// save writes the records to path atomically, readable by the agent alone.
func (c *checkedManifests) save() error {
	if c.path == "" {
		return nil
	}
	var body strings.Builder
	body.WriteString(checkedManifestsHeader)
	for _, d := range c.order {
		body.WriteString(d.String() + "\n")
	}
	dir := filepath.Dir(c.path)
	f, err := os.CreateTemp(dir, ".checked-manifests-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err = f.Chmod(0600); err != nil {
		return err
	}
	if _, err = f.WriteString(body.String()); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), c.path); err != nil {
		return err
	}
	return syncLocalStateDirectory(dir)
}
