package execution

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/jackc/pgx/v5"
)

// Only a review the provider says is gone stops a refresh trying a source; any other failure to
// answer is asked about again.
func TestOnlyAMissingReviewIsTerminal(t *testing.T) {
	for status, gone := range map[int]bool{http.StatusNotFound: true, http.StatusForbidden: false, http.StatusTooManyRequests: false, http.StatusBadGateway: false} {
		err := error(&reviewUnanswered{fmt.Errorf("asking: %w", &pipeline.ProviderError{Status: status})})
		var unanswered *reviewUnanswered
		if !errors.As(err, &unanswered) || unanswered.gone() != gone {
			t.Errorf("HTTP %d: gone=%t, want %t", status, unanswered.gone(), gone)
		}
	}
	if (&reviewUnanswered{errors.New("provider request failed")}).gone() {
		t.Error("a provider that didn't answer counts as a missing review")
	}
}

// A runner that is gone or can't take the work needs the configuration changed, while a database
// failure while choosing one is the refresh's to retry.
func TestRunnerUnavailableIsTheConfigurations(t *testing.T) {
	for _, err := range []error{pgx.ErrNoRows, domain.Invalid("runner"), domain.Invalid("runner.buildNetworkUpgradeRequired")} {
		if !runnerUnavailable(err) {
			t.Errorf("%v is not counted as an unavailable runner", err)
		}
	}
	for _, err := range []error{nil, errors.New("connection reset"), domain.Invalid("pipeline"), domain.Conflict} {
		if runnerUnavailable(err) {
			t.Errorf("%v is counted as an unavailable runner", err)
		}
	}
}

func TestMissingSourceFilesNamesAFewAndFitsItsColumn(t *testing.T) {
	commit := strings.Repeat("e", 40)
	one := missingSourceFiles(commit, []string{"compose.yaml"})
	if !strings.Contains(one.Message, `at "compose.yaml", which the source selects.`) || !strings.Contains(one.Message, "If it was renamed") || !strings.HasPrefix(one.Message, "Commit eeeeeeeeeeee ") {
		t.Fatal(one.Message)
	}
	long := []string{}
	for n := range 16 {
		long = append(long, fmt.Sprintf("%s-%d.yaml", strings.Repeat("d/", 115), n))
	}
	many := missingSourceFiles(commit, long)
	if !strings.Contains(many.Message, "and 13 more") || len(failedSource(many.Message).reason) > failureReasonLimit {
		t.Fatal(len(many.Message), many.Message)
	}
}

// Only a refusal of the source's own input fails it; one that passes by itself, such as its runner
// upgrading or the workspace's operation limit, waits.
func TestOnlyInvalidInputFailsAnImport(t *testing.T) {
	for _, refused := range []*domain.Error{domain.Conflict, domain.RateLimited, domain.EnvironmentInactive, sourceFilesUnavailable} {
		if err := importRefusal(refused); !errors.Is(err, errRefreshWaits) {
			t.Errorf("%s: %v, want a wait", refused.Code, err)
		}
	}
	for _, refused := range []*domain.Error{errCloneCanPush, missingSourceFiles(strings.Repeat("e", 40), []string{"a.yaml"})} {
		var failed *sourceFailure
		if err := importRefusal(refused); !errors.As(err, &failed) || failed.reason != refused.Message {
			t.Errorf("%s: %v, want the source to fail with its message", refused.Fields, err)
		}
	}
	var failed *sourceFailure
	if err := importRefusal(domain.Invalid("pipeline").(*domain.Error)); !errors.As(err, &failed) || !strings.Contains(failed.reason, "(pipeline)") {
		t.Errorf("a refusal without its own message: %v", err)
	}
}

// Only a refusal that names nothing but runner updates, such as a runner on other Railpack pins,
// makes an automatic build wait for its runner.
func TestOnlyARunnerUpdateMakesABuildWait(t *testing.T) {
	for _, refused := range []error{railpackRunner(api.AgentCapabilities{}, pipeline.RailpackFor("arm64")), domain.Invalid("runner.buildControlsUpgradeRequired"), domain.Invalid("runner.rootlessBuildsUpgradeRequired")} {
		if !runnerUpgradeRequired(refused) {
			t.Errorf("%v: not taken for an update", refused)
		}
	}
	both := &domain.Error{Code: "invalid_input", Fields: []string{"runner.railpackUpgradeRequired", "configuration.paths"}}
	for _, err := range []error{domain.Invalid("runner"), both, domain.Invalid("configuration.paths"), &domain.Error{Code: "invalid_input"}, errRefreshWaits, nil} {
		if runnerUpgradeRequired(err) {
			t.Errorf("%v: taken for an update", err)
		}
	}
}
