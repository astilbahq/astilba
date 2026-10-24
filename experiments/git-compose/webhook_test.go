// SPDX-License-Identifier: AGPL-3.0-only

package gitcompose

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func signPush(kind string, body []byte) http.Header {
	h := http.Header{}
	mac := hmac.New(sha256.New, []byte("fixture-webhook-secret"))
	mac.Write(body)
	sig := hex.EncodeToString(mac.Sum(nil))
	switch kind {
	case "github":
		h.Set("X-Hub-Signature-256", "sha256="+sig)
		h.Set("X-GitHub-Event", "push")
		h.Set("X-GitHub-Delivery", "delivery-one")
	case "gitlab":
		h.Set("X-Gitlab-Token", "fixture-webhook-secret")
		h.Set("X-Gitlab-Event", "Push Hook")
		h.Set("X-Gitlab-Event-UUID", "delivery-one")
	case "gitea":
		h.Set("X-Gitea-Signature", sig)
		h.Set("X-Gitea-Event", "push")
		h.Set("X-Gitea-Delivery", "delivery-one")
	}
	return h
}

func TestProviderPushAuthenticationAndReplay(t *testing.T) {
	for _, kind := range []string{"github", "gitlab", "gitea"} {
		t.Run(kind, func(t *testing.T) {
			body := []byte(`{"ref":"refs/heads/main","after":"1111111111111111111111111111111111111111","repository":{"id":42},"project":{"id":42}}`)
			r := PushReceiver{Kind: kind, Secret: "fixture-webhook-secret", Ref: "refs/heads/main", RepositoryID: 42}
			h := signPush(kind, body)
			bad := h.Clone()
			for _, key := range []string{"X-Hub-Signature-256", "X-Gitea-Signature", "X-Gitlab-Token"} {
				bad.Set(key, "forged")
			}
			if _, err := r.Verify(bad, body); err == nil {
				t.Fatal("forged authentication accepted")
			}
			for _, replacement := range []string{strings.Replace(string(body), "main", "other", 1), strings.ReplaceAll(string(body), `"id":42`, `"id":99`), strings.Replace(string(body), strings.Repeat("1", 40), strings.Repeat("0", 40), 1), "not JSON"} {
				data := []byte(replacement)
				if _, err := r.Verify(signPush(kind, data), data); err == nil {
					t.Fatal("wrong repository, ref, deletion or malformed event accepted")
				}
			}
			var accepted atomic.Int32
			var wg sync.WaitGroup
			for range 8 {
				wg.Go(func() {
					p, err := r.Verify(h, body)
					if err == nil && !p.Duplicate {
						accepted.Add(1)
					}
				})
			}
			wg.Wait()
			if accepted.Load() != 1 {
				t.Fatalf("delivery accepted %d times", accepted.Load())
			}
		})
	}
}
