package execution

import (
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
)

// Only management's own configuration makes a command wait for the operator: no GitHub App, another
// App, or a provider its policy does not allow. A binding that is gone or no longer matches its
// connection is the connection's own refusal, which settles instead.
func TestOperatorRefusal(t *testing.T) {
	s := &Service{Policy: pipeline.Policy{Origins: []string{"https://git.example"}}}
	allowed := pipeline.Connection{Provider: "gitea", BaseURL: "https://git.example", Repository: "team/app"}
	removed := pipeline.Connection{Provider: "gitea", BaseURL: "https://other.example", Repository: "team/app"}
	for _, tc := range []struct {
		name string
		st   gitState
		want string
	}{
		{name: "allowed", st: gitState{connection: allowed}},
		{name: "origin removed", st: gitState{connection: removed}, want: providerPolicyNote},
		{name: "management without its App", st: gitState{connection: allowed, unusable: &appUnusable{err: domain.Invalid("githubApp.reauthorizationRequired"), operator: noGitHubAppNote}}, want: noGitHubAppNote},
		{name: "management with another App", st: gitState{connection: allowed, unusable: &appUnusable{err: domain.Forbidden, operator: otherGitHubAppNote}}, want: otherGitHubAppNote},
		{name: "binding gone", st: gitState{connection: allowed, unusable: &appUnusable{err: domain.Invalid("githubApp.reauthorizationRequired")}}},
		{name: "binding mismatched", st: gitState{connection: allowed, unusable: &appUnusable{err: domain.Forbidden}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := s.operatorRefusal(tc.st); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// An expiry ends its note with why delivery waited, never with the run's reason again, which the
// note starts with and the expiry puts in front itself.
func TestWaitedNotesLeaveTheReasonOut(t *testing.T) {
	const reason = "Imports commit abc again."
	if got := waitedNotes(reason + " " + providerPolicyNote + " " + gitHubBusyNote); got != gitHubBusyNote+" "+providerPolicyNote {
		t.Fatalf("got %q", got)
	}
	if got := waitedNotes(reason); got != "" {
		t.Fatalf("a run that never waited kept %q", got)
	}
}
