package execution

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/astilbahq/astilba/services/platform/internal/githubapp"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
)

// repositoryMoved says why a pipeline stops when its connection no longer names the repository
// it was queued for.
const repositoryMoved = "the connection's repository is no longer the one this pipeline was queued for."

// finalProviderRefusal reports whether a Git provider's or GitHub's failure refuses the
// connection's repository for good, and if so why, to end a note with. Asking again gets the same
// answer until someone changes the connection, the repository or the App's installation, which
// the pipeline does not wait for: delivery would otherwise fail every poll by its runner until
// the pipeline's deadline. A rate limit, a minted token GitHub stopped accepting, management's own
// configuration refusing the App, a server error, a temporary redirect or a failed request
// passes, so none of them is final.
func finalProviderRefusal(err error) (string, bool) {
	if errors.Is(err, errGitHubTokenRejected) {
		return "", false
	}
	// Management's own configuration refusing the App is the operator's to restore.
	var unusable *appUnusable
	if errors.As(err, &unusable) {
		if unusable.operator != "" {
			return "", false
		}
		return "the connection's GitHub App authorization was removed, or no longer matches its repository. Connect the repository again, then start this again.", true
	}
	if githubapp.Revoked(err) {
		return "Astilba's GitHub App can no longer reach the repository: it was uninstalled, suspended or removed from the repository, or lost a permission Astilba needs. Install it on the repository again, then start this again.", true
	}
	if errors.Is(err, githubapp.ErrRepositoryChanged) || errors.Is(err, pipeline.ErrRepositoryChanged) {
		return repositoryMoved, true
	}
	var status int
	var provider *pipeline.ProviderError
	var app *githubapp.APIError
	switch {
	case errors.As(err, &provider) && !provider.Limited:
		status = provider.Status
		// GitHub not accepting a token the App made is errGitHubTokenRejected, so this is a stored
		// credential, which stays refused until someone replaces it.
		if status == http.StatusUnauthorized {
			return "the Git provider did not accept the connection's credential (HTTP 401). Replace it, then start this again.", true
		}
	// The App's own refusals under GitHub's rate limits are githubapp.RateLimited, never an
	// APIError. A 401 refuses the App's own key, which an operator's clock or configuration can
	// cause, and is not the repository's answer.
	case errors.As(err, &app):
		status = app.Status
		if status == http.StatusForbidden {
			return "GitHub refused Astilba's GitHub App access to the repository (HTTP 403), as it does once the App's installation is suspended, loses a permission Astilba needs, or is outside an organization's IP allow list. Check the App's installation, then start this again.", true
		}
	}
	switch status {
	case http.StatusMovedPermanently, http.StatusPermanentRedirect:
		return fmt.Sprintf("the Git provider says the repository moved (HTTP %d), as it does for a renamed or transferred one. Connect it under its new name, then start this again.", status), true
	case http.StatusForbidden:
		return "the Git provider refused the connection access to the repository (HTTP 403). Check what the connection's credential can read, then start this again.", true
	case http.StatusNotFound, http.StatusGone:
		return fmt.Sprintf("the Git provider has no repository by that name that the connection can see (HTTP %d): it was deleted, renamed or made private, or the connection lost access. Check the repository and the connection, then start this again.", status), true
	case http.StatusUnavailableForLegalReasons:
		return "the Git provider blocked access to the repository (HTTP 451). Start this again once it is available.", true
	}
	return "", false
}

// headRefusal is finalProviderRefusal for a question about a branch's head, which a branch the
// provider no longer has also answers for good. A report asks it, as a succeeded automatic build
// deploys only its branch's head, and refusing the report instead would fail it at every retry,
// holding the build, and the runner's later pipelines behind it, until its end.
func headRefusal(err error) (string, bool) {
	if errors.Is(err, pipeline.ErrBranchNotFound) {
		return "the Git provider no longer has the build's branch.", true
	}
	return finalProviderRefusal(err)
}
