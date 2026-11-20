package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencontainers/go-digest"
)

// Checked manifests survive the agent's restart in a file only the agent can read.
func TestCheckedManifestsPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), checkedManifestsFile)
	a, b := digest.FromString("a"), digest.FromString("b")
	checked := &checkedManifests{path: path}
	checked.add(a)
	checked.flush()
	restarted := &checkedManifests{path: path}
	if !restarted.has(a) || restarted.has(b) {
		t.Fatal("the checked manifests did not survive a restart")
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("the file is readable by others", err)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatal("a temporary file was left behind", entries)
	}
}

// A file the agent cannot read whole, or did not write, counts as no manifest checked.
func TestCheckedManifestsDistrustAnUnreadableFile(t *testing.T) {
	a := digest.FromString("a")
	for name, tc := range map[string]struct {
		content string
		mode    os.FileMode
	}{
		"another version":          {strings.Replace(checkedManifestsHeader, "v1", "v0", 1) + a.String() + "\n", 0600},
		"no header":                {a.String() + "\n", 0600},
		"a line that is no digest": {checkedManifestsHeader + a.String() + "\nnot a digest\n", 0600},
		"another algorithm":        {checkedManifestsHeader + a.String() + "\nsha512:" + strings.Repeat("ab", 64) + "\n", 0600},
		"a digest twice":           {checkedManifestsHeader + a.String() + "\n" + a.String() + "\n", 0600},
		"an empty line":            {checkedManifestsHeader + a.String() + "\n\n", 0600},
		"readable by others":       {checkedManifestsHeader + a.String() + "\n", 0644},
		"too long":                 {checkedManifestsHeader + a.String() + "\n" + manyDigests(layerCostEntries), 0600},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), checkedManifestsFile)
			if err := os.WriteFile(path, []byte(tc.content), tc.mode); err != nil {
				t.Fatal(err)
			}
			_ = os.Chmod(path, tc.mode)
			if (&checkedManifests{path: path}).has(a) {
				t.Fatal("trusted the file")
			}
		})
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "elsewhere")
	if err := os.WriteFile(target, []byte(checkedManifestsHeader+a.String()+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, checkedManifestsFile)
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if (&checkedManifests{path: link}).has(a) {
		t.Fatal("followed a link")
	}
}

// At most layerCostEntries manifests are kept, the oldest dropped first, in memory and on disk.
func TestCheckedManifestsAreBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), checkedManifestsFile)
	c := &checkedManifests{path: path}
	for i := range layerCostEntries + 5 {
		c.add(digest.FromString(fmt.Sprint(i)))
	}
	c.flush()
	restarted := &checkedManifests{path: path}
	if restarted.has(digest.FromString("0")) || !restarted.has(digest.FromString(fmt.Sprint(layerCostEntries+4))) || len(restarted.order) != layerCostEntries {
		t.Fatal("the cache is not bounded to its newest entries", len(restarted.order))
	}
}

func manyDigests(n int) string {
	var out strings.Builder
	for i := range n {
		out.WriteString(digest.FromString(fmt.Sprint("many", i)).String() + "\n")
	}
	return out.String()
}
