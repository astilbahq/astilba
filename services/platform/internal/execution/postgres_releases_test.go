package execution

import (
	"errors"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/domain"
)

// An agent that does not know a release is no fault of the configuration, and an agent update
// clears it, so a refused preview or automatic change says so rather than "invalid".
func TestRefusalNamesTheAgentUpdate(t *testing.T) {
	agent := &domain.Error{Code: "invalid_input", Message: "Update the agent on host-2 before running PostgreSQL 18.7 there: its agent does not know that release.", Fields: []string{postgresReleasesField}}
	if got := refusal(agent); got != agent.Message {
		t.Fatalf("refusal: %q", got)
	}
	if got := refusal(domain.Invalid("applications")); got != "the configuration would be invalid." {
		t.Fatalf("an invalid configuration: %q", got)
	}
	if refusal(errors.New("transient")) != "" {
		t.Fatal("a transient error was taken as a refusal")
	}
}
