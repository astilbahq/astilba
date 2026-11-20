package execution

import (
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
)

// A submission records a review link only on its connection's own site, which the dashboard then
// shows, and a connection without a usable base matches no link at all.
func TestReviewURLMatchesOnlyTheConnectionsSite(t *testing.T) {
	gitea := pipeline.Connection{Provider: "gitea", BaseURL: "https://git.example.test"}
	github := pipeline.Connection{Provider: "github", BaseURL: "https://api.github.com"}
	for _, test := range []struct {
		name       string
		connection pipeline.Connection
		review     string
		want       bool
	}{
		{"the connection's own site", gitea, "https://git.example.test/team/app/pulls/1", true},
		{"another site", gitea, "https://elsewhere.example.test/team/app/pulls/1", false},
		{"its site over plain HTTP", gitea, "http://git.example.test/team/app/pulls/1", false},
		{"its site with credentials", gitea, "https://user@git.example.test/team/app/pulls/1", false},
		{"GitHub's site", github, "https://github.com/team/app/pull/1", true},
		{"GitHub's API site", github, "https://api.github.com/team/app/pull/1", false},
		{"a link without a site, for a connection without a base", pipeline.Connection{Provider: "gitea"}, "/team/app/pulls/1", false},
		{"an empty link, for a connection without a base", pipeline.Connection{Provider: "gitlab"}, "", false},
		{"a connection whose base doesn't parse", pipeline.Connection{Provider: "gitea", BaseURL: "https://[::1"}, "https://git.example.test/team/app/pulls/1", false},
		{"a connection whose base has no site", pipeline.Connection{Provider: "gitea", BaseURL: "git.example.test"}, "/team/app/pulls/1", false},
		{"a GitLab connection's own site", pipeline.Connection{Provider: "gitlab", BaseURL: "https://gitlab.example.test:8443"}, "https://gitlab.example.test:8443/team/app/-/merge_requests/1", true},
	} {
		if got := reviewURLMatches(test.connection, test.review); got != test.want {
			t.Errorf("%s: got %v, want %v", test.name, got, test.want)
		}
	}
}
