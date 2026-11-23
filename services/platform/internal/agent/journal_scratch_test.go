package agent

import (
	"encoding/json/v2"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// PLAT-447. Opening the journal removes the scratch a process that died left behind, password files
// included, and nothing else: not a near miss of a scratch name, not a link with one, and not an
// entry of another type than its kind makes.
func TestOpenSweepsOnlyLeftoverScratch(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	j, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Made as the agent makes them, then left as a crash leaves them.
	scratch := []string{}
	for _, k := range scratchKinds {
		if k.file {
			f, err := j.scratchFile(k)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.WriteString("dump"); err != nil {
				t.Fatal(err)
			}
			f.Close()
			scratch = append(scratch, filepath.Base(f.Name()))
			continue
		}
		made, err := j.scratchDir(k)
		if err != nil {
			t.Fatal(err)
		}
		name := scratchPassword
		if k != scratchResticAccess {
			name = "staged"
		}
		if err = os.MkdirAll(filepath.Join(made, "nested"), 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(made, name), []byte(releaseCanary), 0600); err != nil {
			t.Fatal(err)
		}
		scratch = append(scratch, filepath.Base(made))
	}
	if err = j.Close(); err != nil {
		t.Fatal(err)
	}

	// Names the sweep must never touch.
	kept := []string{"restic-access", "restic-access-", "restic-access-12a", "restic-access-1-2", "Restic-access-1", "xrestic-access-1", "upgrade-1", "restore-stage", "backup-stage-1.tmp", "build-cache-v1", ".route-1"}
	for _, name := range kept {
		if err = os.Mkdir(filepath.Join(dir, name), 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, name, scratchPassword), []byte("kept"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Entries with scratch names that the agent did not make: a link to a directory outside, a
	// file where a directory belongs, and a directory where a file belongs.
	outside := t.TempDir()
	if err = os.WriteFile(filepath.Join(outside, scratchPassword), []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(outside, filepath.Join(dir, "restic-access-77")); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "backup-stage-78"), []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	// One name is the start of another, as a random suffix can make a name the start of any other.
	for _, name := range []string{"restore-verification-7", "restore-verification-79"} {
		if err = os.Mkdir(filepath.Join(dir, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	unexpected := []string{"backup-stage-78", "restic-access-77", "restore-verification-7", "restore-verification-79"}

	sweep := captureSweepNames(t)
	j, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	for _, name := range scratch {
		if _, err = os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatal("leftover scratch survived the open:", name, err)
		}
	}
	// The operator can tell what it removed from what it left, each named once.
	removed, left := sweep()
	slices.Sort(scratch)
	slices.Sort(removed)
	slices.Sort(left)
	if !slices.Equal(removed, scratch) {
		t.Fatal("the sweep logged it removed", removed, "not", scratch)
	}
	if !slices.Equal(left, unexpected) {
		t.Fatal("the sweep logged it left", left, "not", unexpected)
	}
	for _, name := range append(kept, unexpected...) {
		if _, err = os.Lstat(filepath.Join(dir, name)); err != nil {
			t.Fatal("the sweep removed an entry it did not make:", name, err)
		}
	}
	if raw, err := os.ReadFile(filepath.Join(outside, scratchPassword)); err != nil || string(raw) != "outside" {
		t.Fatal("the sweep followed a link out of the state directory", err)
	}
	for _, name := range []string{"journal.db", "keyring.json"} {
		if _, err = os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatal("the journal lost", name, err)
		}
	}
}

// captureSweepNames records what the scratch sweep logs. The function it returns gives the names
// the sweep logged as removed and as left, each list whole from its record, so a check never
// mistakes one name for the start of another.
func captureSweepNames(t *testing.T) func() (removed, left []string) {
	t.Helper()
	// captureTrail puts the default logger, and the log package's output it redirects, back.
	trail := captureTrail(t)
	slog.SetDefault(slog.New(slog.NewJSONHandler(trail, nil)))
	return func() (removed, left []string) {
		t.Helper()
		for line := range strings.Lines(trail.String()) {
			var record struct {
				Msg   string   `json:"msg"`
				Names []string `json:"names"`
			}
			if err := json.Unmarshal([]byte(line), &record); err != nil {
				t.Fatal(err)
			}
			switch {
			case strings.Contains(record.Msg, "removed scratch"):
				removed = append(removed, record.Names...)
			case strings.Contains(record.Msg, "did not make"):
				left = append(left, record.Names...)
			case record.Names != nil:
				t.Fatal("the sweep logged names in a record the test does not know:", record.Msg)
			}
		}
		return removed, left
	}
}

// Scratch in the state directory is made only through scratchDir and scratchFile, so the startup
// sweep's list and what the agent makes can't drift apart.
func TestScratchIsMadeOnlyThroughTheRegistry(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") || file == "journal_scratch.go" {
			continue
		}
		parsed, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(parsed, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			fn, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || fn.Sel.Name != "MkdirTemp" && fn.Sel.Name != "CreateTemp" {
				return true
			}
			if dir, ok := call.Args[0].(*ast.SelectorExpr); ok && dir.Sel.Name == "dir" {
				t.Errorf("%s makes scratch in the state directory outside the registry", fset.Position(call.Pos()))
			}
			return true
		})
	}
}

func TestScratchNamesMatchOnlyWhatTheRegistryMakes(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind scratchKind
		ok   bool
	}{
		{"restic-access-0", scratchResticAccess, true},
		{"restic-access-4294967295", scratchResticAccess, true},
		{"restore-verification-12", scratchRestoreVerification, true},
		{"upgrade-download-3", scratchUpgradeDownload, true},
		{"restic-access-", scratchKind{}, false},
		{"restic-access--1", scratchKind{}, false},
		{"restic-access-1/password", scratchKind{}, false},
		{"restore-stage-1 ", scratchKind{}, false},
		{"upgrade-7", scratchKind{}, false},
		{"journal.db", scratchKind{}, false},
	} {
		kind, ok := scratchName(tc.name)
		if ok != tc.ok || kind != tc.kind {
			t.Error(tc.name, kind, ok)
		}
	}
}
