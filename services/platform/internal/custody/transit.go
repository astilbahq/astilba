package custody

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// TransitConfig names an OpenBao or Vault Transit mount and how management signs in to it. The
// files are read again at each sign-in, so whatever issues them can replace them.
type TransitConfig struct {
	// Address is the server's origin, such as https://bao.internal:8200.
	Address string
	// Mount is where the Transit engine is mounted. It holds one key per class, named
	// astilba-<class>.
	Mount string
	// Namespace is sent as X-Vault-Namespace when set, as Vault Enterprise and HCP Vault need.
	Namespace string
	// CAFile is a PEM file of the only certificate authorities trusted for Address. Without it,
	// the system's are.
	CAFile string
	// Auth is how management signs in.
	Auth TransitAuth
	// Loopback allows plain HTTP to a loopback address, for local development only.
	Loopback bool
	// Timeout bounds each attempt of a call. It is 2 seconds when zero.
	Timeout time.Duration
}

// TransitAuth is an AppRole or a JWT role to sign in with.
type TransitAuth struct {
	// Method is "approle" or "jwt".
	Method string
	// Mount is the auth method's mount, "approle" or "jwt" when empty.
	Mount string
	// RoleIDFile and SecretIDFile hold the AppRole's role ID and secret ID.
	RoleIDFile, SecretIDFile string
	// JWTFile holds the JWT, such as a projected service account token, and Role the role it
	// signs in as.
	JWTFile, Role string
}

// DefaultTransitMount is the mount the shipped policies are written for.
const DefaultTransitMount = "astilba-transit"

// TransitKey is the name of a class's key in the mount.
func TransitKey(class Class) string { return "astilba-" + string(class) }

// Transit wraps keys with OpenBao or Vault Transit. Every class has its own key, derived per
// context, so the server's audit device records the installation, workspace, generation and
// purpose of every wrap and unwrap, outside management.
type Transit struct {
	transitServer
	config  TransitConfig
	timeout time.Duration
	now     func() time.Time
	// retryDelay is how long to wait before the one retry an unavailable or throttled call gets.
	retryDelay func() time.Duration

	signIn sync.Mutex // one sign-in at a time
	mu     sync.Mutex
	token  string
	// refresh is when the token should be renewed or replaced, and expires when it stops working.
	refresh, expires time.Time
	lease            time.Duration
	renewable        bool
}

var (
	pathSegment   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*(/[A-Za-z0-9][A-Za-z0-9_.-]*)*$`)
	transitPrefix = []byte("vault:v")
)

// NewTransit checks the configuration and prepares the client. It does not sign in: the first call
// does.
func NewTransit(config TransitConfig) (*Transit, error) {
	if config.Mount == "" {
		config.Mount = DefaultTransitMount
	}
	if !pathSegment.MatchString(config.Mount) {
		return nil, errors.New("the Transit mount must be a plain path")
	}
	// Each method takes only its own credentials, so a file meant for the other is never ignored.
	switch a := config.Auth; a.Method {
	case "approle":
		if a.RoleIDFile == "" || a.SecretIDFile == "" || a.JWTFile != "" || a.Role != "" {
			return nil, errors.New("AppRole sign-in needs a role ID file and a secret ID file, and no JWT file or role")
		}
	case "jwt":
		if a.JWTFile == "" || a.Role == "" || a.RoleIDFile != "" || a.SecretIDFile != "" {
			return nil, errors.New("JWT sign-in needs a token file and a role, and no AppRole files")
		}
	default:
		return nil, errors.New("the Transit sign-in method must be approle or jwt")
	}
	if config.Auth.Mount == "" {
		config.Auth.Mount = config.Auth.Method
	}
	if !pathSegment.MatchString(config.Auth.Mount) {
		return nil, errors.New("the Transit sign-in mount must be a plain path")
	}
	server, err := newTransitServer(config.Address, config.Namespace, config.CAFile, config.Loopback)
	if err != nil {
		return nil, err
	}
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	return &Transit{
		config: config, transitServer: server, timeout: timeout, now: time.Now,
		retryDelay: func() time.Duration { return 50*time.Millisecond + rand.N(200*time.Millisecond) },
	}, nil
}

// transitServer is where a Transit server is, and how it is reached.
type transitServer struct {
	base      *url.URL
	client    *http.Client
	namespace string
}

func newTransitServer(address, namespace, caFile string, loopbackHTTP bool) (transitServer, error) {
	base, err := url.Parse(address)
	if err != nil || base.Host == "" || base.User != nil || strings.Trim(base.Path, "/") != "" || base.RawQuery != "" || base.Fragment != "" {
		return transitServer{}, errors.New("the Transit address must be an origin, such as https://bao.internal:8200")
	}
	base.Path = ""
	switch {
	case base.Scheme == "https":
	case base.Scheme == "http" && loopbackHTTP && loopback(base.Hostname()):
	default:
		return transitServer{}, errors.New("the Transit address must use HTTPS; plain HTTP is allowed only to a loopback address in local development")
	}
	if namespace != "" && !pathSegment.MatchString(namespace) {
		return transitServer{}, errors.New("the Transit namespace must be a plain path")
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return transitServer{}, fmt.Errorf("cannot read the Transit CA file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return transitServer{}, errors.New("the Transit CA file holds no PEM certificate")
		}
		tlsConfig.RootCAs = pool
	}
	client := &http.Client{
		// No proxy from the environment: the token goes only to the configured server.
		Transport: &http.Transport{TLSClientConfig: tlsConfig, ForceAttemptHTTP2: true, MaxIdleConnsPerHost: 16, IdleConnTimeout: 90 * time.Second},
		// A standby redirects to the active node. Following it would send the token to whatever
		// the redirect names, so it counts as unavailable instead and the retry tries again.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return transitServer{base: base, client: client, namespace: namespace}, nil
}

func loopback(host string) bool {
	ip := net.ParseIP(host)
	return host == "localhost" || ip != nil && ip.IsLoopback()
}

func (t *Transit) ID() string { return "transit:primary" }

type transitData struct {
	Ciphertext string `json:"ciphertext"`
	Plaintext  string `json:"plaintext"`
}

func transitClass(class Class) error {
	if !slices.Contains(Classes, class) {
		return fmt.Errorf("%w: unknown key class %q", ErrDenied, class)
	}
	return nil
}

// Wrap encrypts key under the class's key, derived for c.
func (t *Transit) Wrap(ctx context.Context, class Class, key []byte, c Context) ([]byte, string, error) {
	if err := transitClass(class); err != nil {
		return nil, "", err
	}
	plaintext := base64.StdEncoding.EncodeToString(key)
	var out transitData
	id, err := t.call(ctx, "/"+t.config.Mount+"/encrypt/"+TransitKey(class), map[string]string{"plaintext": plaintext, "context": transitContext(c)}, &out, false)
	if err != nil {
		return nil, id, err
	}
	if !bytes.HasPrefix([]byte(out.Ciphertext), transitPrefix) {
		return nil, id, fmt.Errorf("%w: Transit returned no ciphertext", ErrUnavailable)
	}
	return []byte(out.Ciphertext), id, nil
}

// Unwrap decrypts a wrapping made by Wrap for the same class and context.
func (t *Transit) Unwrap(ctx context.Context, class Class, blob []byte, c Context) (Result, error) {
	if err := transitClass(class); err != nil {
		return Result{}, err
	}
	if !bytes.HasPrefix(blob, transitPrefix) {
		return Result{}, ErrInvalid
	}
	var out transitData
	id, err := t.call(ctx, "/"+t.config.Mount+"/decrypt/"+TransitKey(class), map[string]string{"ciphertext": string(blob), "context": transitContext(c)}, &out, true)
	if err != nil {
		return Result{ProviderRequestID: id}, err
	}
	key, err := base64.StdEncoding.DecodeString(out.Plaintext)
	if err != nil {
		return Result{ProviderRequestID: id}, fmt.Errorf("%w: Transit returned a malformed key", ErrUnavailable)
	}
	return Result{Key: key, ProviderRequestID: id}, nil
}

// OperatorUnwraps is the shipped operator policy: the operator decrypts only with the rotate
// key, and rewraps every other class's wrappings inside the key service.
func (t *Transit) OperatorUnwraps(class Class) bool { return class == ClassRotate }

// Rewrap wraps a wrapping again under the newest version of the class's key, inside the key
// service: the key never leaves it, so a principal that may not unwrap under the class can still
// move its wrappings onto a rotated key.
func (t *Transit) Rewrap(ctx context.Context, class Class, blob []byte, c Context) ([]byte, string, error) {
	if err := transitClass(class); err != nil {
		return nil, "", err
	}
	if !bytes.HasPrefix(blob, transitPrefix) {
		return nil, "", ErrInvalid
	}
	var out transitData
	id, err := t.call(ctx, "/"+t.config.Mount+"/rewrap/"+TransitKey(class), map[string]string{"ciphertext": string(blob), "context": transitContext(c)}, &out, true)
	if err != nil {
		return nil, id, err
	}
	if !bytes.HasPrefix([]byte(out.Ciphertext), transitPrefix) {
		return nil, id, fmt.Errorf("%w: Transit returned no ciphertext", ErrUnavailable)
	}
	return []byte(out.Ciphertext), id, nil
}

// transitKeyConfig is what Check reads of each class's key.
type transitKeyConfig struct {
	Type                 string `json:"type"`
	Derived              bool   `json:"derived"`
	Exportable           bool   `json:"exportable"`
	AllowPlaintextBackup bool   `json:"allow_plaintext_backup"`
	DeletionAllowed      bool   `json:"deletion_allowed"`
	ConvergentEncryption bool   `json:"convergent_encryption"`
}

// Check signs in, and refuses unless every class's key is one the key service keeps to itself:
// derived per context, never exported or backed up in plaintext, and never deleted. A key made by
// hand without these, or changed since, would quietly weaken every wrapping under it.
func (t *Transit) Check(ctx context.Context) error {
	for _, class := range Classes {
		var key transitKeyConfig
		if _, err := t.call(ctx, "/"+t.config.Mount+"/keys/"+TransitKey(class), nil, &key, false); err != nil {
			return fmt.Errorf("cannot read the Transit key %s: %w", TransitKey(class), err)
		}
		if key.Type != "aes256-gcm96" || !key.Derived || key.ConvergentEncryption || key.Exportable || key.AllowPlaintextBackup || key.DeletionAllowed {
			return fmt.Errorf("%w: the Transit key %s must be aes256-gcm96, derived, not convergent, not exportable, without plaintext backup, and not deletable", ErrDenied, TransitKey(class))
		}
	}
	return nil
}

// transitContext is what each key is derived from. It is not secret, and the audit device shows it.
func transitContext(c Context) string { return base64.StdEncoding.EncodeToString([]byte(c.String())) }

// call makes one request, giving it a deadline and, after an unavailable or throttled answer, one
// more attempt. A refused token is replaced by a new sign-in once.
func (t *Transit) call(ctx context.Context, path string, body any, out any, unwrapping bool) (string, error) {
	id, err := t.attempt(ctx, path, body, out, unwrapping)
	if errors.Is(err, ErrUnavailable) || errors.Is(err, ErrThrottled) {
		select {
		case <-ctx.Done():
			return id, err
		case <-time.After(t.retryDelay()):
		}
		id, err = t.attempt(ctx, path, body, out, unwrapping)
	}
	return id, err
}

func (t *Transit) attempt(ctx context.Context, path string, body any, out any, unwrapping bool) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()
	token, err := t.currentToken(ctx)
	if err != nil {
		return "", err
	}
	id, status, err := t.send(ctx, path, token, body, out, unwrapping)
	if status != http.StatusForbidden {
		return id, err
	}
	// The token may have expired or been revoked: sign in again, once, and try with the new one.
	// Another refusal is the policy's.
	if token, err = t.replaceToken(ctx, token); err != nil {
		return "", err
	}
	id, _, err = t.send(ctx, path, token, body, out, unwrapping)
	return id, err
}

// maxResponse bounds what is read from the server.
const maxResponse = 1 << 20

type transitResponse struct {
	RequestID string         `json:"request_id"`
	Data      jsontext.Value `json:"data"`
	Auth      *transitAuth   `json:"auth"`
	Errors    []string       `json:"errors"`
}

type transitAuth struct {
	ClientToken   string `json:"client_token"`
	LeaseDuration int64  `json:"lease_duration"`
	Renewable     bool   `json:"renewable"`
}

// send makes one HTTP request. It returns the provider's request ID, the HTTP status, and an error
// typed by what the answer means for the caller.
func (t transitServer) send(ctx context.Context, path, token string, body any, out any, unwrapping bool) (string, int, error) {
	method := http.MethodGet
	var reader io.Reader
	var raw []byte
	if body != nil {
		var err error
		if raw, err = json.Marshal(body); err != nil {
			return "", 0, err
		}
		defer clear(raw)
		method, reader = http.MethodPost, bytes.NewReader(raw)
	}
	u := *t.base
	u.Path = "/v1" + path
	req, err := http.NewRequestWithContext(ctx, method, u.String(), reader)
	if err != nil {
		return "", 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("X-Vault-Token", token)
	}
	if t.namespace != "" {
		req.Header.Set("X-Vault-Namespace", t.namespace)
	}
	// Ask for a 429 rather than a silent wait when the server is rate limiting.
	req.Header.Set("X-Vault-Request", "true")
	res, err := t.client.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("%w: %s", ErrUnavailable, transitNetworkError(ctx, err))
	}
	defer res.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(res.Body, maxResponse+1))
	if err != nil || len(payload) > maxResponse {
		return "", res.StatusCode, fmt.Errorf("%w: cannot read the Transit response", ErrUnavailable)
	}
	defer clear(payload)
	var parsed transitResponse
	if len(payload) > 0 {
		if err = json.Unmarshal(payload, &parsed); err != nil && res.StatusCode < 300 {
			return "", res.StatusCode, fmt.Errorf("%w: Transit answered with malformed JSON", ErrUnavailable)
		}
	}
	if err = transitError(res.StatusCode, parsed.Errors, unwrapping); err != nil {
		return parsed.RequestID, res.StatusCode, err
	}
	switch v := out.(type) {
	case nil:
	case **transitAuth:
		*v = parsed.Auth
	default:
		if len(parsed.Data) == 0 {
			return parsed.RequestID, res.StatusCode, fmt.Errorf("%w: Transit returned no data", ErrUnavailable)
		}
		if err = json.Unmarshal(parsed.Data, out); err != nil {
			return parsed.RequestID, res.StatusCode, fmt.Errorf("%w: Transit returned malformed data", ErrUnavailable)
		}
	}
	return parsed.RequestID, res.StatusCode, nil
}

// transitNetworkError says why a request got no answer, without the URL Go's errors repeat.
func transitNetworkError(ctx context.Context, err error) string {
	if ctx.Err() != nil {
		return "the Transit call ran out of time"
	}
	var u *url.Error
	if errors.As(err, &u) {
		err = u.Err
	}
	return "cannot reach Transit: " + err.Error()
}

// transitError types a response by its status. A 400 to decrypt or rewrap is the wrapping's fault
// when it names the ciphertext or its authentication, and the key's otherwise, such as a version
// below min_decryption_version.
func transitError(status int, messages []string, unwrapping bool) error {
	text := strings.Join(messages, "; ")
	if len(text) > 300 {
		text = text[:300]
	}
	switch {
	case status < 300:
		return nil
	case status == http.StatusTooManyRequests:
		return fmt.Errorf("%w: %s", ErrThrottled, text)
	case status >= 500 || status == http.StatusTemporaryRedirect || status == http.StatusPermanentRedirect:
		return fmt.Errorf("%w: Transit answered %d: %s", ErrUnavailable, status, text)
	case status == http.StatusBadRequest && unwrapping && (strings.Contains(text, "invalid ciphertext") || strings.Contains(text, "message authentication failed")):
		return fmt.Errorf("%w: %s", ErrInvalid, text)
	default:
		return fmt.Errorf("%w: Transit answered %d: %s", ErrDenied, status, text)
	}
}

// currentToken returns a token to use, renewing or replacing it first when it is due. While the
// token still works, one call refreshes it and the others go on using it, and a refresh the key
// service could not answer leaves it in use until it expires: the next call tries again.
func (t *Transit) currentToken(ctx context.Context) (string, error) {
	t.mu.Lock()
	token, refresh, expires := t.token, t.refresh, t.expires
	t.mu.Unlock()
	if token != "" && t.now().Before(refresh) {
		return token, nil
	}
	valid := token != "" && t.now().Before(expires)
	if !valid {
		t.signIn.Lock()
	} else if !t.signIn.TryLock() {
		return token, nil
	}
	defer t.signIn.Unlock()
	t.mu.Lock()
	token, refresh, expires, renewable := t.token, t.refresh, t.expires, t.renewable
	t.mu.Unlock()
	if token != "" && t.now().Before(refresh) {
		return token, nil
	}
	valid = token != "" && t.now().Before(expires)
	if valid && renewable && t.renew(ctx, token) {
		t.mu.Lock()
		defer t.mu.Unlock()
		return t.token, nil
	}
	fresh, err := t.login(ctx)
	// A refused sign-in is never papered over: its credentials need fixing.
	if err != nil && valid && (errors.Is(err, ErrUnavailable) || errors.Is(err, ErrThrottled)) {
		return token, nil
	}
	return fresh, err
}

// replaceToken signs in again after refused was refused, unless another call already has.
func (t *Transit) replaceToken(ctx context.Context, refused string) (string, error) {
	t.signIn.Lock()
	defer t.signIn.Unlock()
	t.mu.Lock()
	token := t.token
	t.mu.Unlock()
	if token != "" && token != refused {
		return token, nil
	}
	return t.login(ctx)
}

// renew extends the token, and reports whether it got a useful extension. A token near its maximum
// lifetime comes back with less than it had, and is replaced by a sign-in instead.
func (t *Transit) renew(ctx context.Context, token string) bool {
	var auth *transitAuth
	if _, _, err := t.send(ctx, "/auth/token/renew-self", token, map[string]string{}, &auth, false); err != nil || auth == nil {
		return false
	}
	t.mu.Lock()
	previous := t.lease
	t.mu.Unlock()
	if time.Duration(auth.LeaseDuration)*time.Second < previous/3 {
		return false
	}
	t.keep(token, auth)
	return true
}

// login signs in with the configured method, reading its credentials afresh. The caller holds
// signIn.
func (t *Transit) login(ctx context.Context) (string, error) {
	var body map[string]string
	switch t.config.Auth.Method {
	case "approle":
		role, err := readCredential(t.config.Auth.RoleIDFile)
		if err != nil {
			return "", err
		}
		secret, err := readCredential(t.config.Auth.SecretIDFile)
		if err != nil {
			return "", err
		}
		body = map[string]string{"role_id": role, "secret_id": secret}
	case "jwt":
		jwt, err := readCredential(t.config.Auth.JWTFile)
		if err != nil {
			return "", err
		}
		body = map[string]string{"role": t.config.Auth.Role, "jwt": jwt}
	}
	var auth *transitAuth
	_, status, err := t.send(ctx, "/auth/"+t.config.Auth.Mount+"/login", "", body, &auth, false)
	if err != nil {
		// A refused sign-in is never the server being away, so it is not retried as one.
		if status >= 400 && status < 500 && status != http.StatusTooManyRequests {
			return "", fmt.Errorf("%w: Transit refused the sign-in: %v", ErrDenied, err)
		}
		return "", err
	}
	if auth == nil || auth.ClientToken == "" {
		return "", fmt.Errorf("%w: Transit returned no token", ErrDenied)
	}
	t.keep(auth.ClientToken, auth)
	return auth.ClientToken, nil
}

// keep stores a token, due for renewal two thirds of the way through its lease. A token without a
// lease never expires; it is treated as a three-hour one, so it is looked at again after two.
func (t *Transit) keep(token string, auth *transitAuth) {
	lease := time.Duration(auth.LeaseDuration) * time.Second
	if lease <= 0 {
		lease = 3 * time.Hour
	}
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.token, t.renewable, t.lease = token, auth.Renewable, lease
	t.refresh, t.expires = now.Add(lease*2/3), now.Add(lease)
}

// readCredential reads a sign-in credential from a private file.
func readCredential(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 16384 {
		return "", fmt.Errorf("%w: %s must be a private regular file (mode 0600)", ErrDenied, path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("%w: cannot read %s", ErrDenied, path)
	}
	value := strings.TrimSpace(string(raw))
	clear(raw)
	if value == "" {
		return "", fmt.Errorf("%w: %s is empty", ErrDenied, path)
	}
	return value, nil
}
