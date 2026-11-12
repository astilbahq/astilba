package agent

import (
	"context"
	"encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/agentidentity"
	"github.com/astilbahq/astilba/services/platform/internal/api"
)

// A command that brings its values is acknowledged once journaled. One that comes without them
// is accepted only when the journal already holds it; otherwise the agent asks for them again
// and journals nothing.
func TestReleasesAreAcknowledgedOrAskedForAgain(t *testing.T) {
	j, plan := testVisualJournal(t)
	var mu sync.Mutex
	var notices []api.SecretReleaseNotice
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/agent/v1/releases" || r.Method != "POST" || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(400)
			return
		}
		var notice api.SecretReleaseNotice
		if err := json.UnmarshalRead(r.Body, &notice); err != nil {
			t.Error(err)
		}
		mu.Lock()
		notices = append(notices, notice)
		mu.Unlock()
		w.WriteHeader(204)
	}))
	defer server.Close()
	cfg, err := j.Config()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Origin, cfg.Enrolled, cfg.Credential = server.URL, true, plan.ServerID.String()+".credential"
	if err = j.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	c := &Client{Journal: j, HTTP: server.Client()}
	ctx := context.Background()

	released := visualCommand(plan, "inspect", 1)
	released.SecretRelease = &api.SecretRelease{Epoch: 0, Digest: strings.Repeat("a", 64), Values: true}
	if err = c.acceptReleased(ctx, released); err != nil {
		t.Fatal(err)
	}
	if len(notices) != 1 || len(notices[0].Acknowledged) != 1 || notices[0].Acknowledged[0].OperationId != released.Id || notices[0].Acknowledged[0].Digest != released.SecretRelease.Digest {
		t.Fatalf("acknowledgement: %+v", notices)
	}

	held := released
	held.SecretRelease = &api.SecretRelease{Epoch: 0, Digest: released.SecretRelease.Digest, Values: false}
	if err = c.acceptReleased(ctx, held); err != nil || len(notices) != 1 {
		t.Fatalf("a held release: %v, %+v", err, notices)
	}

	lost := visualCommand(plan, "inspect", 2)
	lost.SecretRelease = &api.SecretRelease{Epoch: 3, Digest: strings.Repeat("b", 64), Values: false}
	if err = c.acceptReleased(ctx, lost); err != nil {
		t.Fatal(err)
	}
	if len(notices) != 2 || len(notices[1].Redeliver) != 1 || notices[1].Redeliver[0].OperationId != lost.Id || notices[1].Redeliver[0].Epoch != 3 || notices[1].Redeliver[0].Reason != api.JournalMissing {
		t.Fatalf("re-delivery request: %+v", notices)
	}
	if _, err = j.Record(lost.Id); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a command without its values was journaled: %v", err)
	}
}

// A journaled release whose acknowledgement fails still runs, and is acknowledged again later.
func TestAFailedAcknowledgementIsSentAgain(t *testing.T) {
	j, plan := testVisualJournal(t)
	var mu sync.Mutex
	var attempts []api.SecretReleaseNotice
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var notice api.SecretReleaseNotice
		if err := json.UnmarshalRead(r.Body, &notice); err != nil {
			t.Error(err)
		}
		mu.Lock()
		attempts = append(attempts, notice)
		first := len(attempts) == 1
		mu.Unlock()
		if first {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(204)
	}))
	defer server.Close()
	cfg, err := j.Config()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Origin, cfg.Enrolled, cfg.Credential = server.URL, true, plan.ServerID.String()+".credential"
	if err = j.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	c := &Client{Journal: j, HTTP: server.Client()}
	ctx := context.Background()
	released := visualCommand(plan, "inspect", 1)
	released.SecretRelease = &api.SecretRelease{Epoch: 0, Digest: strings.Repeat("a", 64), Values: true}
	if err = c.acceptReleased(ctx, released); err != nil {
		t.Fatal("a journaled command failed with its acknowledgement:", err)
	}
	if _, err = j.Record(released.Id); err != nil {
		t.Fatal(err)
	}
	if err = c.acknowledgeReleases(ctx); err != nil {
		t.Fatal(err)
	}
	if err = c.acknowledgeReleases(ctx); err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 2 || len(attempts[1].Acknowledged) != 1 || attempts[1].Acknowledged[0].OperationId != released.Id {
		t.Fatalf("acknowledgements: %+v", attempts)
	}
}

// Acknowledgements go in batches the endpoint accepts, once each, and a batch it finds invalid
// is dropped rather than sent forever.
func TestAcknowledgementsAreBatchedAndBounded(t *testing.T) {
	j, plan := testVisualJournal(t)
	var mu sync.Mutex
	var sizes []int
	refuse, proxy := true, true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var notice api.SecretReleaseNotice
		if err := json.UnmarshalRead(r.Body, &notice); err != nil {
			t.Error(err)
		}
		mu.Lock()
		defer mu.Unlock()
		sizes = append(sizes, len(notice.Acknowledged))
		if !proxy {
			w.Header().Set(agentidentity.KeysHeader, "v1")
		}
		if refuse || len(notice.Acknowledged) > 64 {
			w.WriteHeader(400)
			return
		}
		w.WriteHeader(204)
	}))
	defer server.Close()
	cfg, err := j.Config()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Origin, cfg.Enrolled, cfg.Credential = server.URL, true, plan.ServerID.String()+".credential"
	if err = j.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	c := &Client{Journal: j, HTTP: server.Client()}
	ctx := context.Background()
	released := visualCommand(plan, "inspect", 1)
	released.SecretRelease = &api.SecretRelease{Epoch: 0, Digest: strings.Repeat("a", 64), Values: true}
	if err = c.acceptReleased(ctx, released); err != nil {
		t.Fatal(err)
	}
	if len(c.unacknowledged) != 1 {
		t.Fatal("a proxy's refusal dropped the batch", c.unacknowledged)
	}
	proxy = false
	if err = c.acknowledgeReleases(ctx); err != nil {
		t.Fatal(err)
	}
	if len(c.unacknowledged) != 0 {
		t.Fatal("an invalid batch was kept", c.unacknowledged)
	}
	refuse = false
	// Seventy journaled releases, one of them the command delivered again: sent once each.
	for i := range 70 {
		c.unacknowledged = append(c.unacknowledged, api.SecretReleaseRef{OperationId: released.Id, Epoch: int64(i), Digest: released.SecretRelease.Digest})
	}
	if err = c.acceptReleased(ctx, released); err != nil {
		t.Fatal(err)
	}
	if len(c.unacknowledged) != 0 || len(sizes) != 4 || sizes[2] != 64 || sizes[3] != 6 {
		t.Fatalf("batches %v, left %d", sizes, len(c.unacknowledged))
	}
}
