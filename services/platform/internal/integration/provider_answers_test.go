package integration

import (
	"net/http"
	"strings"

	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
)

// Provider fixtures answer as the real provider does, so that a field only one provider names
// cannot pass for another's: a Gitea fixture that named a branch's commit sha hid that real
// Gitea names it id. The fixtures subtest of TestGiteaRunsTheGitPathAsFixturesDo checks the
// Gitea answers against a real Gitea.

func repositoryAnswer(kind string, id int64, private bool) map[string]any {
	visibility := "public"
	if private {
		visibility = "private"
	}
	switch kind {
	case "github":
		return map[string]any{"id": id, "full_name": "team/app", "private": private, "visibility": visibility}
	case "gitlab":
		return map[string]any{"id": id, "path_with_namespace": "team/app", "visibility": visibility}
	default:
		return map[string]any{"id": id, "full_name": "team/app", "private": private, "internal": false, "owner": map[string]any{"login": "team", "visibility": "public"}}
	}
}

func answerRepository(w http.ResponseWriter, kind string, id int64, private bool) {
	_ = stablejson.Encode(w, repositoryAnswer(kind, id, private))
}

// branchAnswer names the commit as sha on GitHub, and as id on GitLab and Gitea.
func branchAnswer(kind, commit string) map[string]any {
	key := "id"
	if kind == "github" {
		key = "sha"
	}
	return map[string]any{"name": "main", "commit": map[string]string{key: commit}}
}

func answerBranch(w http.ResponseWriter, kind, commit string) {
	_ = stablejson.Encode(w, branchAnswer(kind, commit))
}

// pullAnswer is a Gitea or GitHub pull request, from a head repository into the base one.
func pullAnswer(number int, state string, baseRepository int64, head string, headRepository int64) map[string]any {
	return map[string]any{"number": number, "state": state, "base": map[string]any{"ref": "main", "repo": map[string]any{"id": baseRepository}}, "head": map[string]any{"sha": head, "repo": map[string]any{"id": headRepository}}}
}

func answerPull(w http.ResponseWriter, number int, state string, baseRepository int64, head string, headRepository int64) {
	_ = stablejson.Encode(w, pullAnswer(number, state, baseRepository, head, headRepository))
}

// mergedAnswer is a Gitea or GitHub pull request, merged as commit when it names one.
func mergedAnswer(number int, commit string) map[string]any {
	answer := map[string]any{"number": number, "state": "open", "merged": false, "merge_commit_sha": nil}
	if commit != "" {
		answer["state"], answer["merged"], answer["merge_commit_sha"] = "closed", true, commit
	}
	return answer
}

// answerReceivePack answers a Git server's push check as it does for a credential that can only
// read, which is all a connection's clone credential may be.
func answerReceivePack(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasSuffix(r.URL.Path, ".git/info/refs") {
		return false
	}
	w.WriteHeader(http.StatusForbidden)
	return true
}
