package execution

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

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
	if !strings.Contains(one.Message, `the file "compose.yaml", which the source selects. If it was renamed`) || !strings.HasPrefix(one.Message, "Commit eeeeeeeeeeee ") {
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
