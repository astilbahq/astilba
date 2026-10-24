// SPDX-License-Identifier: AGPL-3.0-only

package gitcompose

import (
	"strings"
	"testing"
)

func TestManualSecretRemovalCannotExposeDiff(t *testing.T) {
	p, err := NewProvider("gitea", "http://127.0.0.1", "fixture", "unused")
	if err != nil {
		t.Fatal(err)
	}
	r := newGitRepository(p, t.TempDir(), "fixture/repo", "main")
	git := func(input []byte, args ...string) string {
		t.Helper()
		result, err := r.git(t.Context(), input, args...)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(result))
	}
	git(nil, "init", "--bare", "--quiet")
	original := "# synthetic-runtime-secret\nservices:\n  web:\n    image: alpine:3\n"
	blob := git([]byte(original), "hash-object", "-w", "--stdin")
	git(nil, "update-index", "--add", "--cacheinfo", "100644", blob, "compose.yaml")
	tree := git(nil, "write-tree")
	r.base = git([]byte("Seed\n"), "commit-tree", tree)
	r.Source = Bundle{Files: []SourceFile{{"compose.yaml", []byte(original)}}, Secrets: []string{"synthetic-runtime-secret"}}
	edited := Bundle{Files: []SourceFile{{"compose.yaml", []byte(strings.TrimPrefix(original, "# synthetic-runtime-secret\n"))}}}
	if draft, err := r.Prepare(t.Context(), edited); err == nil || draft.Diff != "" {
		t.Fatal("manual secret removal exposed a review diff")
	}
}
