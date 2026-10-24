// SPDX-License-Identifier: AGPL-3.0-only

package gitcompose

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
)

// PushReceiver validates hints only. Production must persist delivery identity
// and resolve the commit through its configured provider before deployment.
type PushReceiver struct {
	Kind, Secret, Ref string
	RepositoryID      int64
	mu                sync.Mutex
	seen              map[string]bool
}

type Push struct {
	Commit    string
	Duplicate bool
}

func (r *PushReceiver) Verify(headers http.Header, body []byte) (Push, error) {
	invalid := errors.New("invalid push delivery")
	if r.Secret == "" || r.RepositoryID <= 0 || len(body) > maxFileBytes {
		return Push{}, invalid
	}
	delivery := ""
	switch r.Kind {
	case "github", "gitea":
		prefix, signature, event, id := "", "X-Gitea-Signature", "X-Gitea-Event", "X-Gitea-Delivery"
		if r.Kind == "github" {
			prefix = "sha256="
			signature = "X-Hub-Signature-256"
			event = "X-GitHub-Event"
			id = "X-GitHub-Delivery"
		}
		if headers.Get(event) != "push" {
			return Push{}, invalid
		}
		value := headers.Get(signature)
		if !strings.HasPrefix(value, prefix) {
			return Push{}, invalid
		}
		actual, err := hex.DecodeString(strings.TrimPrefix(value, prefix))
		mac := hmac.New(sha256.New, []byte(r.Secret))
		mac.Write(body)
		if err != nil || !hmac.Equal(actual, mac.Sum(nil)) {
			return Push{}, invalid
		}
		delivery = headers.Get(id)
	case "gitlab":
		actual := sha256.Sum256([]byte(headers.Get("X-Gitlab-Token")))
		expected := sha256.Sum256([]byte(r.Secret))
		if headers.Get("X-Gitlab-Event") != "Push Hook" || subtle.ConstantTimeCompare(actual[:], expected[:]) != 1 {
			return Push{}, invalid
		}
		delivery = headers.Get("X-Gitlab-Event-UUID")
	default:
		return Push{}, invalid
	}
	if delivery == "" || len(delivery) > 200 {
		return Push{}, invalid
	}
	var payload struct {
		Ref, After string
		Repository struct{ ID int64 }
		Project    struct{ ID int64 }
	}
	if json.Unmarshal(body, &payload) != nil {
		return Push{}, invalid
	}
	id := payload.Repository.ID
	if r.Kind == "gitlab" {
		id = payload.Project.ID
	}
	if id != r.RepositoryID || payload.Ref != r.Ref || !validCommit(payload.After) || strings.Trim(payload.After, "0") == "" {
		return Push{}, invalid
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seen == nil {
		r.seen = map[string]bool{}
	}
	duplicate := r.seen[delivery]
	r.seen[delivery] = true
	return Push{Commit: payload.After, Duplicate: duplicate}, nil
}
