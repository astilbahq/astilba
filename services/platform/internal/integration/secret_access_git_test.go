package integration

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

// Git work records who opened a connection's stored access: an owner saving a preview policy
// asks the provider as themself, and a signed webhook is checked by the webhook job.
func TestGitAccessIsRecordedForWhoeverAsks(t *testing.T) {
	h := newHungPreview(t)
	if err := h.f.execution.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	h.b.call("PUT", h.path+"/preview-policy", map[string]any{"enabled": true, "generation": 0, "configuration": h.policy}, 200, nil)
	raw := []byte(`{"number":7,"repository":{"id":12}}`)
	mac := hmac.New(sha256.New, []byte(h.secret))
	mac.Write(raw)
	request, _ := http.NewRequest("POST", h.f.server.URL+"/hooks/git/"+h.connection.ID.String(), bytes.NewReader(raw))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Gitea-Event", "pull_request")
	request.Header.Set("X-Gitea-Delivery", uuid.NewString())
	request.Header.Set("X-Gitea-Signature", hex.EncodeToString(mac.Sum(nil)))
	response, err := h.f.server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 202 {
		t.Fatal("event refused", response.StatusCode)
	}
	rows, err := h.f.st.Pool.Query(t.Context(), `SELECT purpose,principal_kind,COALESCE(job,''),account_id IS NOT DISTINCT FROM $2,items::text FROM secret_access WHERE workspace_id=$1 AND items::text LIKE '%"git"%'`, h.ws, h.f.owner)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for rows.Next() {
		var purpose, principal, job, items string
		var owner bool
		if err = rows.Scan(&purpose, &principal, &job, &owner, &items); err != nil {
			t.Fatal(err)
		}
		seen[fmt.Sprintf("%s/%s/%s/%t", purpose, principal, job, owner)] = true
	}
	rows.Close()
	if !seen["operate/account//true"] || !seen["operate/system/webhook/false"] {
		t.Fatalf("git access records: %v", seen)
	}
}
