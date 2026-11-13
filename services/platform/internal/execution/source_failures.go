package execution

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/jackc/pgx/v5"
)

// sourceFailure is a refresh's failure that belongs to one source, such as its runner being gone or
// a selected file missing from the commit. The refresh records it on that source and goes on with
// the others; it does not try that source at that commit again.
type sourceFailure struct{ reason string }

func (f *sourceFailure) Error() string { return f.reason }

// failureReasonLimit is the most a source's recorded reason holds, as the column allows.
const failureReasonLimit = 1000

func failedSource(reason string) *sourceFailure {
	reason = strings.ToValidUTF8(reason, "")
	for len(reason) > failureReasonLimit {
		reason = strings.ToValidUTF8(reason[:failureReasonLimit], "")
	}
	return &sourceFailure{reason: reason}
}

// absentSourceFiles is the selected paths the provider holds no file at.
func absentSourceFiles(paths []string, blobs map[string]string) []string {
	missing := []string{}
	for _, path := range paths {
		if _, found := blobs[path]; !found {
			missing = append(missing, path)
		}
	}
	return missing
}

// missingSourceFiles refuses an import whose selected files the commit doesn't hold, naming them.
// A file renamed or removed upstream needs the source bound again, since every later commit is
// missing it too. Paths are checked as the configuration allows, so naming them quotes no content.
func missingSourceFiles(commit string, missing []string) *domain.Error {
	quoted := make([]string, 0, 3)
	for _, path := range missing[:min(len(missing), 3)] {
		quoted = append(quoted, fmt.Sprintf("%q", path))
	}
	names := strings.Join(quoted, ", ")
	if more := len(missing) - len(quoted); more > 0 {
		names += fmt.Sprintf(" and %d more", more)
	}
	what := "the file " + names + ", which the source selects. If it was"
	if len(missing) > 1 {
		what = "the files " + names + ", which the source selects. If they were"
	}
	return &domain.Error{
		Code:    "invalid_input",
		Message: fmt.Sprintf("Commit %s doesn't have %s renamed or removed, bind the source again with the files the branch holds now.", shortCommit(commit), what),
		Fields:  []string{"configuration.paths"},
	}
}

func shortCommit(commit string) string { return commit[:min(len(commit), 12)] }

// runnerUnavailable reports whether selecting a runner failed because the configured runner, or
// its fallback, can't take the work: it was removed or revoked, isn't a build runner, or needs an
// upgrade. The configuration must change, or the runner, before the work can start.
func runnerUnavailable(err error) bool {
	var refused *domain.Error
	if errors.Is(err, pgx.ErrNoRows) {
		return true
	}
	if !errors.As(err, &refused) {
		return false
	}
	for _, field := range refused.Fields {
		if field == "runner" || strings.HasPrefix(field, "runner.") {
			return true
		}
	}
	return false
}

const runnerUnavailableReason = "The source's runner, or its fallback, was removed or revoked, or can no longer run imports. Bind the source again with another runner."

// importRefusal is the reason management gave for refusing to start an import.
func importRefusal(refused *domain.Error) string {
	if refused.Message == "Check the identified fields." {
		return "Astilba refused to start this import (" + strings.Join(refused.Fields, ", ") + ")."
	}
	return refused.Message
}

// reviewUnanswered is a provider's failure to say whether a review submitted from the draft
// merged. It wraps the provider's error, so a caller can tell a review that is gone from one the
// provider could not answer about now.
type reviewUnanswered struct{ err error }

func (e *reviewUnanswered) Error() string { return e.err.Error() }
func (e *reviewUnanswered) Unwrap() error { return e.err }

// gone reports whether the provider said the review does not exist, as when it was deleted or
// the repository no longer holds it. Asking again won't change that.
func (e *reviewUnanswered) gone() bool {
	var status *pipeline.ProviderError
	return errors.As(e.err, &status) && status.Status == http.StatusNotFound
}

const reviewGoneReason = "The Git provider no longer has a review that was submitted from this environment's draft, so Astilba can't tell whether the branch includes the draft's edits. Refresh and replace the saved draft with the branch's source, or bind the source again."
