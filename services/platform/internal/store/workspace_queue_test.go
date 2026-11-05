package store

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A lock taken beside the queue holds a connection while it waits, and one such path can
// starve every tenant. Offline recovery runs with no serving process to contend with, and
// a statement that skips locked rows never waits.
func TestWorkspaceLockHasOneEntryPoint(t *testing.T) {
	allowedCalls := map[string]bool{"internal/store/workspace_queue.go": true, "internal/store/recovery.go": true, "internal/execution/recovery_import.go": true}
	call := regexp.MustCompile(`LockWorkspaceByID\(`)
	statement := regexp.MustCompile(`(?s)\bworkspaces\b[^;"` + "`" + `]*?\bFOR\s+(?:NO\s+KEY\s+)?UPDATE\b(?:\s+OF\s+\w+)?(\s+SKIP\s+LOCKED)?`)
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		query := strings.HasPrefix(relative, "db/queries/") && strings.HasSuffix(relative, ".sql")
		code := strings.HasSuffix(relative, ".go") && !strings.HasSuffix(relative, "_test.go") && !strings.HasPrefix(relative, "db/generated/")
		if !query && !code {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		source := string(raw)
		if !allowedCalls[relative] && call.MatchString(source) {
			t.Errorf("%s locks the workspace row directly: use Store.BeginWorkspaceWrite", relative)
		}
		for _, match := range statement.FindAllStringSubmatchIndex(source, -1) {
			skipsLocked := match[2] >= 0
			entryPoint := query && strings.HasPrefix(source[strings.LastIndex(source[:match[0]], "-- name: "):], "-- name: LockWorkspaceByID ")
			if !skipsLocked && !entryPoint {
				t.Errorf("%s waits for the workspace row: use Store.BeginWorkspaceWrite", relative)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
