package custody

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

// fakeTransit answers as a Transit server does, with answers a test can change per path.
type fakeTransit struct {
	t      *testing.T
	mu     sync.Mutex
	calls  map[string]int
	bodies map[string][]map[string]any
	tokens map[string]bool
	issued int
	// answer, when set for a path, replaces the normal answer.
	answer map[string]func(w http.ResponseWriter)
	lease  int64
	keys   map[string]map[string]any
}

func newFakeTransit(t *testing.T) (*fakeTransit, *httptest.Server) {
	f := &fakeTransit{t: t, calls: map[string]int{}, bodies: map[string][]map[string]any{}, tokens: map[string]bool{}, answer: map[string]func(http.ResponseWriter){}, lease: 3600, keys: map[string]map[string]any{}}
	for _, class := range Classes {
		f.keys[TransitKey(class)] = map[string]any{"type": "aes256-gcm96", "derived": true, "exportable": false, "allow_plaintext_backup": false, "deletion_allowed": false}
	}
	s := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(s.Close)
	return f, s
}

func (f *fakeTransit) count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[path]
}

func (f *fakeTransit) set(path string, answer func(w http.ResponseWriter)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answer[path] = answer
}

func status(code int, message string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.WriteHeader(code)
		stablejson.Encode(w, map[string]any{"errors": []string{message}})
	}
}

func (f *fakeTransit) serve(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1")
	var body map[string]any
	raw, _ := io.ReadAll(r.Body)
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			f.t.Errorf("%s: %v", path, err)
		}
	}
	f.mu.Lock()
	f.calls[path]++
	f.bodies[path] = append(f.bodies[path], body)
	answer := f.answer[path]
	authorized := f.tokens[r.Header.Get("X-Vault-Token")]
	f.mu.Unlock()
	if answer != nil {
		answer(w)
		return
	}
	reply := func(data any, auth any) {
		stablejson.Encode(w, map[string]any{"request_id": uuid.NewString(), "data": data, "auth": auth})
	}
	if strings.HasPrefix(path, "/auth/approle/login") || strings.HasPrefix(path, "/auth/jwt/login") {
		f.mu.Lock()
		f.issued++
		token := "token-" + uuid.NewString()
		f.tokens[token] = true
		lease := f.lease
		f.mu.Unlock()
		reply(nil, map[string]any{"client_token": token, "lease_duration": lease, "renewable": true})
		return
	}
	if !authorized {
		status(http.StatusForbidden, "permission denied")(w)
		return
	}
	mount, op, key := splitTransitPath(path)
	switch {
	case path == "/auth/token/renew-self":
		f.mu.Lock()
		lease := f.lease
		f.mu.Unlock()
		reply(nil, map[string]any{"client_token": r.Header.Get("X-Vault-Token"), "lease_duration": lease, "renewable": true})
	case mount == DefaultTransitMount && op == "encrypt":
		reply(map[string]any{"ciphertext": "vault:v1:" + base64.StdEncoding.EncodeToString([]byte(key+"|"+body["context"].(string)+"|"+body["plaintext"].(string)))}, nil)
	case mount == DefaultTransitMount && (op == "decrypt" || op == "rewrap"):
		inner, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(body["ciphertext"].(string), "vault:v1:"))
		parts := strings.Split(string(inner), "|")
		if len(parts) != 3 || parts[0] != key || parts[1] != body["context"] {
			status(http.StatusBadRequest, "cipher: message authentication failed")(w)
			return
		}
		if op == "rewrap" {
			reply(map[string]any{"ciphertext": "vault:v2:" + strings.TrimPrefix(body["ciphertext"].(string), "vault:v1:")}, nil)
			return
		}
		reply(map[string]any{"plaintext": parts[2]}, nil)
	case mount == DefaultTransitMount && op == "keys":
		f.mu.Lock()
		config := f.keys[key]
		f.mu.Unlock()
		reply(config, nil)
	default:
		status(http.StatusNotFound, "unsupported path")(w)
	}
}

func splitTransitPath(path string) (mount, op, key string) {
	parts := strings.SplitN(strings.TrimPrefix(path, "/"), "/", 3)
	if len(parts) < 3 {
		return "", "", ""
	}
	return parts[0], parts[1], parts[2]
}

func privateFile(t *testing.T, name, value string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(value+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func newTestTransit(t *testing.T, address string) *Transit {
	t.Helper()
	tr, err := NewTransit(TransitConfig{Address: address, Loopback: true, Timeout: 300 * time.Millisecond, Auth: TransitAuth{Method: "approle", RoleIDFile: privateFile(t, "role", "role-1"), SecretIDFile: privateFile(t, "secret", "secret-1")}})
	if err != nil {
		t.Fatal(err)
	}
	tr.retryDelay = func() time.Duration { return time.Millisecond }
	return tr
}

var testContext = Context{Installation: uuid.New(), Scope: ScopeWorkspace, Workspace: uuid.New(), Generation: 1, Purpose: "deliver"}

func TestTransitWrapsUnderTheClassKeyAndContext(t *testing.T) {
	f, s := newFakeTransit(t)
	tr := newTestTransit(t, s.URL)
	ctx := t.Context()
	key := bytes.Repeat([]byte{5}, 32)
	blob, id, err := tr.Wrap(ctx, ClassExport, key, testContext)
	if err != nil || id == "" || !bytes.HasPrefix(blob, []byte("vault:v1:")) {
		t.Fatalf("wrap: %q %q %v", blob, id, err)
	}
	sent := f.bodies["/"+DefaultTransitMount+"/encrypt/astilba-export"][0]
	if sent["context"] != base64.StdEncoding.EncodeToString([]byte(testContext.String())) || sent["plaintext"] != base64.StdEncoding.EncodeToString(key) {
		t.Fatalf("sent %v", sent)
	}
	got, err := tr.Unwrap(ctx, ClassExport, blob, testContext)
	if err != nil || !bytes.Equal(got.Key, key) || got.ProviderRequestID == "" {
		t.Fatalf("unwrap: %v %v", got, err)
	}
	if _, err = tr.Unwrap(ctx, ClassServe, blob, testContext); !errors.Is(err, ErrInvalid) {
		t.Fatalf("another class: %v", err)
	}
	// Something that is not a Transit ciphertext is refused without asking.
	before := f.count("/" + DefaultTransitMount + "/decrypt/astilba-export")
	if _, err = tr.Unwrap(ctx, ClassExport, []byte(`{"version":1}`), testContext); !errors.Is(err, ErrInvalid) || f.count("/"+DefaultTransitMount+"/decrypt/astilba-export") != before {
		t.Fatalf("a foreign blob: %v", err)
	}
	if _, _, err = tr.Wrap(ctx, Class("sign"), key, testContext); !errors.Is(err, ErrDenied) {
		t.Fatalf("an unknown class: %v", err)
	}
	if f.issued != 1 {
		t.Fatalf("%d sign-ins, want 1", f.issued)
	}
}

// Each kind of failure comes back as the error the broker acts on, and only the transient ones
// are tried again, once.
func TestTransitErrorTypes(t *testing.T) {
	decrypt := "/" + DefaultTransitMount + "/decrypt/astilba-serve"
	for _, c := range []struct {
		name     string
		answer   func(http.ResponseWriter)
		want     error
		attempts int
	}{
		{"throttled", status(http.StatusTooManyRequests, "rate limit quota exceeded"), ErrThrottled, 2},
		{"sealed", status(http.StatusServiceUnavailable, "Vault is sealed"), ErrUnavailable, 2},
		{"server error", status(http.StatusInternalServerError, "internal error"), ErrUnavailable, 2},
		{"standby redirect", func(w http.ResponseWriter) {
			w.Header().Set("Location", "http://198.51.100.1:8200/v1/x")
			w.WriteHeader(http.StatusTemporaryRedirect)
		}, ErrUnavailable, 2},
		{"tag mismatch", status(http.StatusBadRequest, "cipher: message authentication failed"), ErrInvalid, 1},
		{"malformed ciphertext", status(http.StatusBadRequest, "invalid ciphertext: version is too new"), ErrInvalid, 1},
		{"version too old", status(http.StatusBadRequest, "ciphertext or signature version is disallowed by policy (too old)"), ErrDenied, 1},
		{"missing key", status(http.StatusBadRequest, "encryption key not found"), ErrDenied, 1},
		{"no mount", status(http.StatusNotFound, "unsupported path"), ErrDenied, 1},
		{"garbage", func(w http.ResponseWriter) { io.WriteString(w, "<html>") }, ErrUnavailable, 2},
		{"slow", func(http.ResponseWriter) { time.Sleep(time.Second) }, ErrUnavailable, 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, s := newFakeTransit(t)
			tr := newTestTransit(t, s.URL)
			f.set(decrypt, c.answer)
			start := time.Now()
			_, err := tr.Unwrap(t.Context(), ClassServe, []byte("vault:v1:AAAA"), testContext)
			if !errors.Is(err, c.want) {
				t.Fatalf("got %v, want %v", err, c.want)
			}
			if n := f.count(decrypt); n != c.attempts {
				t.Fatalf("%d attempts, want %d", n, c.attempts)
			}
			if time.Since(start) > 2*time.Second {
				t.Fatalf("took %s", time.Since(start))
			}
		})
	}
	t.Run("unreachable", func(t *testing.T) {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		address := "http://" + l.Addr().String()
		l.Close()
		tr := newTestTransit(t, address)
		if _, err = tr.Unwrap(t.Context(), ClassServe, []byte("vault:v1:AAAA"), testContext); !errors.Is(err, ErrUnavailable) {
			t.Fatal(err)
		}
	})
}

// A refused token is replaced once by a new sign-in. A policy that refuses the new one too is the
// policy's answer, and is never tried again.
func TestTransitSignsInAgainOnceWhenRefused(t *testing.T) {
	f, s := newFakeTransit(t)
	tr := newTestTransit(t, s.URL)
	ctx := t.Context()
	blob, _, err := tr.Wrap(ctx, ClassServe, bytes.Repeat([]byte{1}, 32), testContext)
	if err != nil {
		t.Fatal(err)
	}
	// The token is revoked behind management's back.
	f.mu.Lock()
	clear(f.tokens)
	f.mu.Unlock()
	if _, err = tr.Unwrap(ctx, ClassServe, blob, testContext); err != nil {
		t.Fatal(err)
	}
	if f.issued != 2 {
		t.Fatalf("%d sign-ins, want 2", f.issued)
	}
	decrypt := "/" + DefaultTransitMount + "/decrypt/astilba-rotate"
	f.set(decrypt, status(http.StatusForbidden, "1 error occurred:\n\t* permission denied\n\n"))
	if _, err = tr.Unwrap(ctx, ClassRotate, blob, testContext); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	if n := f.count(decrypt); n != 2 || f.issued != 3 {
		t.Fatalf("%d attempts and %d sign-ins, want 2 and 3", n, f.issued)
	}
	// A refused sign-in is a refusal, not an outage.
	f.set("/auth/approle/login", status(http.StatusBadRequest, "invalid role or secret ID"))
	f.mu.Lock()
	clear(f.tokens)
	f.mu.Unlock()
	if _, err = tr.Unwrap(ctx, ClassServe, blob, testContext); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
}

// A token is renewed two thirds of the way through its lease, and replaced by a new sign-in once
// renewing stops giving it a useful lease. The credentials are read again at each sign-in.
func TestTransitRenewsAndReplacesItsToken(t *testing.T) {
	f, s := newFakeTransit(t)
	tr := newTestTransit(t, s.URL)
	now := time.Now()
	tr.now = func() time.Time { return now }
	ctx := t.Context()
	check := func() {
		t.Helper()
		if _, _, err := tr.Wrap(ctx, ClassServe, []byte("k"), testContext); err != nil {
			t.Fatal(err)
		}
	}
	check()
	now = now.Add(30 * time.Minute)
	check()
	if f.count("/auth/token/renew-self") != 0 || f.issued != 1 {
		t.Fatal("renewed before it was due")
	}
	now = now.Add(15 * time.Minute)
	check()
	if f.count("/auth/token/renew-self") != 1 || f.issued != 1 {
		t.Fatalf("%d renewals and %d sign-ins, want 1 and 1", f.count("/auth/token/renew-self"), f.issued)
	}
	// Near its maximum lifetime, renewing gives little: sign in instead, with the new secret ID.
	f.mu.Lock()
	f.lease = 60
	f.mu.Unlock()
	if err := os.WriteFile(tr.config.Auth.SecretIDFile, []byte("secret-2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	now = now.Add(45 * time.Minute)
	check()
	logins := f.bodies["/auth/approle/login"]
	if f.issued != 2 || logins[len(logins)-1]["secret_id"] != "secret-2" || logins[0]["role_id"] != "role-1" {
		t.Fatalf("%d sign-ins: %v", f.issued, logins)
	}
}

func TestTransitCheckRefusesAKeyThatCanLeave(t *testing.T) {
	f, s := newFakeTransit(t)
	tr := newTestTransit(t, s.URL)
	if err := tr.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, change := range []map[string]any{{"exportable": true}, {"allow_plaintext_backup": true}, {"deletion_allowed": true}, {"derived": false}, {"type": "chacha20-poly1305"}, {"convergent_encryption": true}} {
		f.mu.Lock()
		config := map[string]any{"type": "aes256-gcm96", "derived": true, "exportable": false, "allow_plaintext_backup": false, "deletion_allowed": false}
		for k, v := range change {
			config[k] = v
		}
		f.keys[TransitKey(ClassRotate)] = config
		f.mu.Unlock()
		if err := tr.Check(t.Context()); !errors.Is(err, ErrDenied) {
			t.Errorf("%v: %v", change, err)
		}
	}
}

func TestTransitConfiguration(t *testing.T) {
	secret := privateFile(t, "secret", "s")
	role := privateFile(t, "role", "r")
	approle := TransitAuth{Method: "approle", RoleIDFile: role, SecretIDFile: secret}
	for name, c := range map[string]TransitConfig{
		"plain HTTP":             {Address: "http://bao.internal:8200", Auth: approle},
		"plain HTTP, not dev":    {Address: "http://127.0.0.1:8200", Auth: approle},
		"plain HTTP, remote dev": {Address: "http://bao.internal:8200", Loopback: true, Auth: approle},
		"a path":                 {Address: "https://bao.internal:8200/v1", Auth: approle},
		"credentials":            {Address: "https://user:pass@bao.internal:8200", Auth: approle},
		"a query":                {Address: "https://bao.internal:8200?x=1", Auth: approle},
		"mount traversal":        {Address: "https://bao.internal:8200", Mount: "../sys", Auth: approle},
		"namespace traversal":    {Address: "https://bao.internal:8200", Namespace: "a/../b", Auth: approle},
		"no method":              {Address: "https://bao.internal:8200"},
		"approle without secret": {Address: "https://bao.internal:8200", Auth: TransitAuth{Method: "approle", RoleIDFile: role}},
		"jwt without role":       {Address: "https://bao.internal:8200", Auth: TransitAuth{Method: "jwt", JWTFile: secret}},
		"missing CA":             {Address: "https://bao.internal:8200", CAFile: "/nonexistent", Auth: approle},
	} {
		if _, err := NewTransit(c); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	for name, c := range map[string]TransitConfig{
		"https":        {Address: "https://bao.internal:8200", Auth: approle},
		"loopback dev": {Address: "http://127.0.0.1:8200", Loopback: true, Auth: approle},
		"jwt":          {Address: "https://bao.internal", Namespace: "admin/astilba", Auth: TransitAuth{Method: "jwt", JWTFile: secret, Role: "astilba"}},
	} {
		if _, err := NewTransit(c); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// A credential file others can read is refused, as the keyring is.
func TestTransitRefusesSharedCredentialFiles(t *testing.T) {
	_, s := newFakeTransit(t)
	tr := newTestTransit(t, s.URL)
	if err := os.Chmod(tr.config.Auth.SecretIDFile, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := tr.Wrap(t.Context(), ClassServe, []byte("k"), testContext); !errors.Is(err, ErrDenied) || !strings.Contains(err.Error(), "0600") {
		t.Fatal(err)
	}
}

func TestTransitJWTSignIn(t *testing.T) {
	f, s := newFakeTransit(t)
	tr, err := NewTransit(TransitConfig{Address: s.URL, Loopback: true, Auth: TransitAuth{Method: "jwt", JWTFile: privateFile(t, "jwt", "a.b.c"), Role: "astilba-serving"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = tr.Wrap(context.Background(), ClassServe, []byte("k"), testContext); err != nil {
		t.Fatal(err)
	}
	if got := f.bodies["/auth/jwt/login"]; len(got) != 1 || got[0]["jwt"] != "a.b.c" || got[0]["role"] != "astilba-serving" {
		t.Fatalf("%v", got)
	}
}

// The native rewrap is used when the backend has one, so an operator never needs to unwrap.
func TestRewrapUsesTheKeyServiceRewrap(t *testing.T) {
	f, s := newFakeTransit(t)
	tr := newTestTransit(t, s.URL)
	blob, _, err := tr.Wrap(t.Context(), ClassServe, []byte("k"), testContext)
	if err != nil {
		t.Fatal(err)
	}
	f.set("/"+DefaultTransitMount+"/decrypt/astilba-serve", status(http.StatusForbidden, "permission denied"))
	rewrapped, err := Rewrap(t.Context(), tr, ClassServe, blob, testContext)
	if err != nil || !bytes.HasPrefix(rewrapped, []byte("vault:v2:")) {
		t.Fatalf("%q %v", rewrapped, err)
	}
	if f.count("/"+DefaultTransitMount+"/decrypt/astilba-serve") != 0 {
		t.Fatal("rewrap unwrapped")
	}
}
