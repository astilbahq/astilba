package execution

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/githubapp"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
)

// A provider's answer ends a pipeline only when asking again gets it again: the repository is
// gone, moved or out of the connection's reach. A rate limit, even one GitHub answers with 403, a
// token GitHub stopped accepting, management's own App configuration or a server's failure
// passes.
func TestFinalProviderRefusal(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		final string
	}{
		{name: "not found", err: &pipeline.ProviderError{Status: 404}, final: "HTTP 404"},
		{name: "gone", err: &pipeline.ProviderError{Status: 410}, final: "HTTP 410"},
		{name: "forbidden", err: &pipeline.ProviderError{Status: 403}, final: "HTTP 403"},
		{name: "stored credential rejected", err: &pipeline.ProviderError{Status: 401}, final: "HTTP 401"},
		{name: "moved", err: &pipeline.ProviderError{Status: 301}, final: "HTTP 301"},
		{name: "permanent redirect", err: &pipeline.ProviderError{Status: 308}, final: "HTTP 308"},
		{name: "blocked", err: &pipeline.ProviderError{Status: 451}, final: "HTTP 451"},
		{name: "App installation answers not found", err: appFailure(&githubapp.APIError{Status: 404}), final: "HTTP 404"},
		{name: "App repository changed", err: appFailure(githubapp.ErrRepositoryChanged), final: repositoryMoved},
		{name: "repository changed", err: pipeline.ErrRepositoryChanged, final: repositoryMoved},
		{name: "App installation refuses access", err: appFailure(&githubapp.APIError{Status: 403}), final: "App's installation"},
		{name: "App binding gone", err: &appUnusable{err: domain.Invalid("githubApp.reauthorizationRequired")}, final: "GitHub App authorization"},
		{name: "App binding mismatched", err: &appUnusable{err: domain.Forbidden}, final: "GitHub App authorization"},
		{name: "management without its App", err: &appUnusable{err: domain.Invalid("githubApp.reauthorizationRequired"), operator: noGitHubAppNote}},
		{name: "management with another App", err: &appUnusable{err: domain.Forbidden, operator: otherGitHubAppNote}},
		{name: "rate limit as 403", err: &pipeline.ProviderError{Status: 403, Limited: true}},
		{name: "too many requests", err: &pipeline.ProviderError{Status: 429, Limited: true}},
		{name: "server error", err: &pipeline.ProviderError{Status: 502}},
		{name: "temporary redirect", err: &pipeline.ProviderError{Status: 302}},
		{name: "failed request", err: errors.New("provider request failed")},
		{name: "minted token rejected", err: fmt.Errorf("%w: %w", errGitHubTokenRejected, &pipeline.ProviderError{Status: 401})},
		{name: "App token rejected", err: appFailure(fmt.Errorf("%w: %w", githubapp.ErrTokenRejected, &githubapp.APIError{Status: 401}))},
		{name: "App rate limited", err: appFailure(&githubapp.RateLimited{Until: time.Now().Add(time.Minute)})},
		{name: "App key refused", err: &githubapp.APIError{Status: 401}},
		{name: "nothing", err: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			why, final := finalProviderRefusal(tc.err)
			if final != (tc.final != "") || !strings.Contains(why, tc.final) {
				t.Fatalf("final %v with %q, want %q", final, why, tc.final)
			}
		})
	}
}

// A branch the provider no longer has answers a question about its head for good, while what
// passes for the repository passes for its branch too.
func TestHeadRefusal(t *testing.T) {
	if why, final := headRefusal(fmt.Errorf("%w: %w", pipeline.ErrBranchNotFound, &pipeline.ProviderError{Status: 404})); !final || !strings.Contains(why, "branch") {
		t.Fatal("a missing branch passed", why)
	}
	if _, final := headRefusal(&pipeline.ProviderError{Status: 429, Limited: true}); final {
		t.Fatal("a rate limit ended the question")
	}
	if why, final := headRefusal(&pipeline.ProviderError{Status: 404}); !final || !strings.Contains(why, "no repository") {
		t.Fatal("a missing repository passed", why)
	}
}
