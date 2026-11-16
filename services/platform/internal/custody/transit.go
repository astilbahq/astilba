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

// transitData is an encrypt, decrypt or rewrap answer. A key is read as bytes, which the JSON codec
// decodes from base64, so management leaves no copy of it in a string that cannot be cleared. The
// HTTP and TLS layers' own connection buffers are outside its reach, as they are for every client.
type transitData struct {
	Ciphertext string `json:"ciphertext"`
	Plaintext  []byte `json:"plaintext"`
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
	encoded := base64.StdEncoding.AppendEncode(make([]byte, 0, base64.StdEncoding.EncodedLen(len(key))), key)
	body, err := privateJSON(member{"plaintext", encoded}, member{"context", []byte(transitContext(c))})
	clear(encoded)
	if err != nil {
		return nil, "", err
	}
	defer clear(body)
	var out transitData
	id, err := t.call(ctx, "/"+t.config.Mount+"/encrypt/"+TransitKey(class), body, &out, false)
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
	if len(out.Plaintext) == 0 {
		return Result{ProviderRequestID: id}, fmt.Errorf("%w: Transit returned no key", ErrUnavailable)
	}
	return Result{Key: out.Plaintext, ProviderRequestID: id}, nil
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

// call makes one request and, after an unavailable or throttled answer, one more attempt. Each
// request to the server in an attempt has its own deadline.
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

// attempt makes one request with a working token. Renewing or replacing the token has a deadline
// of its own, so the request keeps its whole one.
func (t *Transit) attempt(ctx context.Context, path string, body any, out any, unwrapping bool) (string, error) {
	token, err := t.currentToken(ctx)
	if err != nil {
		return "", err
	}
	id, status, err := t.sendWithin(ctx, path, token, body, out, unwrapping)
	if status != http.StatusForbidden {
		return id, err
	}
	// A 403 is the policy's refusal, or a token the server no longer accepts. Only a token the
	// server refuses to look up is replaced: signing in again for the policy's refusal would change
	// nothing, and would leave another token behind. A lookup the server could not answer says
	// neither, so the call is tried again as for any outage.
	switch lookup, lookupErr := t.lookup(ctx, token); {
	case lookupErr == nil:
		return id, err
	case lookup != http.StatusForbidden:
		if errors.Is(lookupErr, ErrUnavailable) || errors.Is(lookupErr, ErrThrottled) {
			return id, lookupErr
		}
		return id, err
	}
	if token, err = t.replaceToken(ctx, token); err != nil {
		return "", err
	}
	id, _, err = t.sendWithin(ctx, path, token, body, out, unwrapping)
	return id, err
}

// sendWithin is send with the per-attempt deadline.
func (t *Transit) sendWithin(ctx context.Context, path, token string, body any, out any, unwrapping bool) (string, int, error) {
	ctx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()
	return t.send(ctx, path, token, body, out, unwrapping)
}

// privateBody is a request body holding a key or a credential, in a buffer its maker owns and
// clears once the call is over. json.Marshal would leave a copy in the pooled buffer it encodes in.
// The HTTP and TLS layers still copy the body into their connection buffers, which no client clears.
type privateBody []byte

// member is one of a privateBody's string members.
type member struct {
	name  string
	value []byte
}

// privateJSON builds a JSON object of string members in one buffer, sized up front so appending
// never leaves a copy behind in one it outgrew. A value that is not valid UTF-8 is refused rather
// than sent altered.
func privateJSON(members ...member) (privateBody, error) {
	size := 2
	for _, m := range members {
		// Escaping takes at most six bytes for one.
		size += 6*len(m.name) + 6*len(m.value) + 6
	}
	out := make([]byte, 0, size)
	out = append(out, '{')
	for i, m := range members {
		if i > 0 {
			out = append(out, ',')
		}
		var err error
		out, _ = jsontext.AppendQuote(out, m.name)
		out = append(out, ':')
		if out, err = jsontext.AppendQuote(out, m.value); err != nil {
			clear(out)
			return nil, fmt.Errorf("%s is not valid UTF-8", m.name)
		}
	}
	return append(out, '}'), nil
}

// maxResponse bounds what is read from the server.
const maxResponse = 1 << 20

type transitResponse struct {
	RequestID string         `json:"request_id"`
	Data      jsontext.Value `json:"data"`
	Auth      *transitAuth   `json:"auth"`
	Errors    []string       `json:"errors"`
}

// transitStatus is what is read of sys/seal-status.
type transitStatus struct {
	ClusterName string `json:"cluster_name"`
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
	switch b := body.(type) {
	case nil:
	case privateBody:
		method, reader = http.MethodPost, bytes.NewReader(b)
	default:
		raw, err := json.Marshal(body)
		if err != nil {
			return "", 0, err
		}
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
	// The codec copies data out of the payload, so the copy is cleared too.
	defer clear(parsed.Data)
	if err = transitError(res.StatusCode, parsed.Errors, unwrapping); err != nil {
		return parsed.RequestID, res.StatusCode, err
	}
	switch v := out.(type) {
	case nil:
	case **transitAuth:
		*v = parsed.Auth
	case *transitStatus:
		// System status endpoints answer at the top level, not under data.
		if err = json.Unmarshal(payload, v); err != nil {
			return parsed.RequestID, res.StatusCode, fmt.Errorf("%w: Transit returned a malformed status", ErrUnavailable)
		}
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
