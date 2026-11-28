package pipeline

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
)

// CanPush asks the Git server whether the provider's credential may push to this repository over
// HTTPS, without pushing: Git asks for the receive-pack advertisement first, which GitHub, GitLab
// and Gitea give only to a credential that may write.
//
// A refusal makes the credential read-only only when the server says the credential has no write
// access. A server, or what stands in front of it, refuses the request for reasons that say
// nothing about the credential, as GitLab's ban after failed sign-ins, an IP allow list or an
// archived repository does, and a credential that can push once they pass must never reach a
// runner. Any other refusal is PushUnclear, and an answer that may pass, such as a rate limit or a
// server error, is a ProviderError.
func (p *Provider) CanPush(ctx context.Context) (bool, error) {
	base := GitURL(p.Connection)
	if p.Connection.Provider == "github" && p.base != p.Connection.BaseURL {
		base = p.base + "/" + p.Connection.Repository + ".git"
	}
	response, body, err := p.askAsGit(ctx, http.MethodGet, base+"/info/refs?service=git-receive-pack", true)
	if err != nil {
		return false, err
	}
	switch {
	case response.StatusCode >= 200 && response.StatusCode < 300:
		return true, nil
	// The server didn't accept the credential at all, so says nothing about what it could do.
	case response.StatusCode == http.StatusUnauthorized:
		return false, ErrCredentialRejected
	}
	if refusal := passingRefusal(response); refusal != nil {
		return false, refusal
	}
	if response.StatusCode == http.StatusForbidden {
		if read, err := p.refusesWrite(ctx, body); err != nil || read {
			return false, err
		}
	}
	return false, &PushUnclear{Status: response.StatusCode}
}

// ErrCredentialRejected is CanPush's answer when the Git server did not accept the credential.
var ErrCredentialRejected = errors.New("the Git server did not accept the credential")

// PushUnclear is CanPush's answer when the Git server refused to say whether the credential may
// push without saying it may not. Asking again gets the same answer until someone changes the
// credential, the repository or what blocks the request.
type PushUnclear struct{ Status int }

func (e *PushUnclear) Error() string {
	return fmt.Sprintf("the Git server did not say whether the credential can push (HTTP %d)", e.Status)
}

// GitHub's and GitLab's own refusals of a push by a credential without write access, answered
// as the whole body of a 403, as each server's source and real servers show. GitHub names the
// repository and who was refused, and GitLab a fine-grained token's missing permissions. GitLab
// answers "upload code" for a token or deploy token without push, and "push code" for a user
// whose role can't push.
var (
	gitHubNoWrite     = regexp.MustCompile(`^Permission to \S+ denied to \S+\.$`)
	gitLabFineGrained = regexp.MustCompile(`^Access denied: This operation requires a fine-grained .+ with the following (\S+ )?permissions: \[.+\]\.$`)
	noWrite           = map[string][]string{
		"github": {"Write access to repository not granted."},
		"gitlab": {"You are not allowed to upload code.", "You are not allowed to upload code for this project.", "You are not allowed to push code to this project."},
	}
)

// refusesWrite reports whether the server's 403, with body, says the credential has no write
// access. Gitea's refusal of a token without write:repository is a bare "Forbidden", as Go's own
// and so Traefik's is, so Gitea's API is asked instead.
func (p *Provider) refusesWrite(ctx context.Context, body []byte) (bool, error) {
	said := strings.TrimSpace(string(body))
	switch p.Connection.Provider {
	case "github":
		return slices.Contains(noWrite["github"], said) || gitHubNoWrite.MatchString(said), nil
	case "gitlab":
		return slices.Contains(noWrite["gitlab"], said) || gitLabFineGrained.MatchString(said), nil
	case "gitea":
		return p.giteaRefusesWrite(ctx)
	}
	return false, nil
}

// giteaNoBranch is a branch no repository has, as Git refuses a name with "..".
const giteaNoBranch = "astilba..push-check"

// giteaRefusesWrite asks Gitea's API, with the credential as Git presents it, to delete a branch
// no repository can have. Gitea refuses a write request by a token without write:repository
// before it looks for the repository or the branch, so its refusal is about the token's own
// grant, and a token with it only finds no such branch.
func (p *Provider) giteaRefusesWrite(ctx context.Context) (bool, error) {
	response, body, err := p.askAsGit(ctx, http.MethodDelete, p.base+p.apiPath()+"/branches/"+giteaNoBranch, false)
	if err != nil {
		return false, err
	}
	if response.StatusCode == http.StatusForbidden {
		var answer struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(body, &answer) == nil && strings.HasPrefix(answer.Message, "token does not have at least one of required scope(s)") && strings.Contains(answer.Message, "write:repository") {
			return true, nil
		}
	}
	if refusal := passingRefusal(response); refusal != nil {
		return false, refusal
	}
	return false, nil
}

// askAsGit sends a request with the credential as Git does, in Basic authentication, and as Git
// asks for a ref advertisement if advertisement is set. It returns the response with up to 4 KiB
// of its body, which is closed, and left for reading again, as a refusal's rate limit may be read
// from it.
func (p *Provider) askAsGit(ctx context.Context, method, target string, advertisement bool) (*http.Response, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		return nil, nil, errors.New("invalid Git request")
	}
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(p.access.Username+":"+p.access.Password)))
	if advertisement {
		req.Header.Set("Git-Protocol", "version=2")
	}
	response, err := p.client.Do(req)
	if err != nil {
		return nil, nil, errors.New("Git server request failed")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4096))
	if err != nil {
		return nil, nil, errors.New("Git server request failed")
	}
	response.Body = io.NopCloser(bytes.NewReader(body))
	return response, body, nil
}

// passingRefusal is the error for a refusal that may pass, as one under the server's rate limits,
// a server error or a timeout does, or nil for one asking again gets.
func passingRefusal(response *http.Response) *ProviderError {
	refusal := refused(response)
	if refusal.Limited || refusal.Status >= 500 || refusal.Status == http.StatusRequestTimeout {
		return refusal
	}
	return nil
}
