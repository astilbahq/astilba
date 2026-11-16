package custody

import (
	"bytes"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"
)

// A refused request whose token the server could not look up says nothing about the token: it is
// tried again as for an outage, and nothing signs in again.
func TestTransitRetriesWhenTheLookupCannotBeAnswered(t *testing.T) {
	f, s := newFakeTransit(t)
	tr := newTestTransit(t, s.URL)
	ctx := t.Context()
	blob, _, err := tr.Wrap(ctx, ClassServe, bytes.Repeat([]byte{1}, 32), testContext)
	if err != nil {
		t.Fatal(err)
	}
	decrypt := "/" + DefaultTransitMount + "/decrypt/astilba-serve"
	f.set(decrypt, status(http.StatusForbidden, "permission denied"))
	f.set("/auth/token/lookup-self", status(http.StatusServiceUnavailable, "sealed"))
	if _, err = tr.Unwrap(ctx, ClassServe, blob, testContext); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if n := f.count(decrypt); n != 2 || f.issued != 1 {
		t.Fatalf("%d attempts and %d sign-ins, want 2 and 1", n, f.issued)
	}
}

// Sign-in credentials reach the server as they are in their files, escaped as JSON needs, and one
// that is not valid text is refused rather than sent altered.
func TestTransitSendsCredentialsExactly(t *testing.T) {
	f, s := newFakeTransit(t)
	tr, err := NewTransit(TransitConfig{Address: s.URL, Loopback: true, Auth: TransitAuth{Method: "approle", RoleIDFile: privateFile(t, "role", `role"\1`), SecretIDFile: privateFile(t, "secret", "secret-é")}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = tr.Wrap(t.Context(), ClassServe, []byte("k"), testContext); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	login := f.bodies["/auth/approle/login"][0]
	f.mu.Unlock()
	if login["role_id"] != `role"\1` || login["secret_id"] != "secret-é" {
		t.Fatalf("%v", login)
	}
	bad, err := NewTransit(TransitConfig{Address: s.URL, Loopback: true, Auth: TransitAuth{Method: "approle", RoleIDFile: privateFile(t, "role", "role"), SecretIDFile: privateFile(t, "secret", "secret-\xff")}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = bad.Wrap(t.Context(), ClassServe, []byte("k"), testContext); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	if f.count("/auth/approle/login") != 1 {
		t.Fatal("a credential that is not valid text was sent")
	}
}

// Calls queued behind a sign-in that fails spend their wait from their own deadline, so they fail
// soon after it rather than each signing in in turn.
func TestTransitQueuedSignInsFailTogether(t *testing.T) {
	f, s := newFakeTransit(t)
	tr := newTestTransit(t, s.URL)
	tr.timeout = 200 * time.Millisecond
	f.set("/auth/approle/login", func(w http.ResponseWriter) {
		time.Sleep(time.Second)
		status(http.StatusServiceUnavailable, "sealed")(w)
	})
	start := time.Now()
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, _, err := tr.Wrap(t.Context(), ClassServe, []byte("k"), testContext); !errors.Is(err, ErrUnavailable) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	// One after another, eight calls of two attempts would take 3.2 seconds.
	elapsed := time.Since(start)
	t.Logf("queued calls took %s", elapsed)
	if elapsed > 1500*time.Millisecond {
		t.Fatalf("queued calls took %s", elapsed)
	}
}
