package execution

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// sourceFailure is a refresh's failure that belongs to one source, such as its runner being gone or
// a selected file missing from the commit. The refresh records it on that source and goes on with
// the others; it does not try that source at that commit again.
type sourceFailure struct{ reason string }

func (f *sourceFailure) Error() string { return f.reason }

// failureReasonLimit is the most a source's recorded reason holds, as the column allows.
const failureReasonLimit = 1000

// errRefreshWaits says a source or target can't start now for a reason that passes by itself, such
// as its runner enrolling again, upgrading or being moved, or the workspace's operation limit. The
// refresh rolls it back and looks again shortly, as for a busy environment.
var errRefreshWaits = errors.New("the work waits for its runner or the workspace")

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

// missingSourceFiles refuses an import whose selected files the provider has nothing to import at,
// naming them. The provider says the same of a path that holds a directory, a symlink on GitHub or
// a file too large for its answer, none of which a runner would import either. A file renamed or
// removed upstream needs the source bound again, since every later commit is missing it too. Paths
// are checked as the configuration allows, so naming them quotes no content.
func missingSourceFiles(commit string, missing []string) *domain.Error {
	quoted := make([]string, 0, 3)
	for _, path := range missing[:min(len(missing), 3)] {
		quoted = append(quoted, fmt.Sprintf("%q", path))
	}
	names := strings.Join(quoted, ", ")
	if more := len(missing) - len(quoted); more > 0 {
		names += fmt.Sprintf(" and %d more", more)
	}
	what := "a file Astilba can import at " + names + ", which the source selects. It may be missing, a directory, a symlink or too large. If it was"
	if len(missing) > 1 {
		what = "files Astilba can import at " + names + ", which the source selects. They may be missing, directories, symlinks or too large. If they were"
	}
	return &domain.Error{
		Code:    "invalid_input",
		Message: fmt.Sprintf("Commit %s doesn't have %s renamed or removed, bind the source again with the files the branch holds now.", shortCommit(commit), what),
		Fields:  []string{"configuration.paths"},
	}
}

func shortCommit(commit string) string { return commit[:min(len(commit), 12)] }

// runnerUnavailable reports whether selecting a runner failed because the configured runner, or
// its fallback, can't take the work: it has no credential, isn't a build runner, or needs an
// upgrade. The configuration must change, or the runner, before the work can start, unless
// runnerWaits finds it enrolling again.
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

// runnerWaits reports whether a runner selection that failed for want of the primary runner's
// credential is only waiting for it to finish enrolling again or moving to another management, or
// for an enrollment that finished since selectRunner looked. An unusable fallback never fails a
// selection, so the primary is the runner whose answer counts.
func runnerWaits(ctx context.Context, tx pgx.Tx, ws, primary uuid.UUID, err error) (bool, error) {
	if !errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	state, err := db.New(tx).RunnerCredential(ctx, db.RunnerCredentialParams{WorkspaceID: ws, ID: primary})
	return state.Enrolling || state.Enrolled, err
}

// sourceChangedSinceShown refuses binding a source again from a configuration that is no longer
// the source's, or a source that is gone, so another owner's change is never overwritten unseen.
var sourceChangedSinceShown = &domain.Error{Code: "conflict", Message: "The source changed since it was shown, for example because another owner bound it again or removed it. Look at it as it is now, then bind it again.", Fields: []string{"sourceHash"}}

// sourceImporting refuses binding a source again while an operation of its environment runs, since
// an environment runs one at a time.
var sourceImporting = &domain.Error{Code: "conflict", Message: "An import or another operation of this environment is running. Bind the source again once it finishes.", Fields: []string{"environment.operation"}}

const runnerUnavailableReason = "The source's runner was removed or revoked, has no credential and isn't enrolling, or can no longer run imports. Enroll the runner again, or bind the source again with another runner."

// importRefusal is the outcome of management refusing to start an import. Invalid input, such as a
// file the commit doesn't hold or a clone credential that can push, fails the source until its
// configuration changes. Any other refusal, such as its runner upgrading or the workspace's
// operation limit, passes by itself, so the import waits.
func importRefusal(refused *domain.Error) error {
	if refused.Code != "invalid_input" {
		return errRefreshWaits
	}
	if refused.Message == "Check the identified fields." {
		return failedSource("Astilba refused to start this import (" + strings.Join(refused.Fields, ", ") + ").")
	}
	return failedSource(refused.Message)
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
