package integration

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	dbmigrate "github.com/astilbahq/astilba/services/platform/db"
	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/custody"
	"github.com/astilbahq/astilba/services/platform/internal/custody/custodytest"
	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
)

// openBaoImage is the OpenBao the Transit backend is tested against, and the local stack runs.
const openBaoImage = "openbao/openbao:2.7.2@sha256:5046911075e137e154ce49ab051502f4ec7cf367d484ae4a4dce665d9b90561f"

// openBaoConfig adds a file audit device writing to standard output, so the test reads what the
// server recorded from the container's log. OpenBao creates audit devices only from configuration.
const openBaoConfig = `audit "file" "stdout" {
  options = {
    file_path = "stdout"
  }
}
`

// devOpenBao is a disposable OpenBao dev server, set up as `custody init-transit --dev` sets up the
// local stack's, with the shipped policies.
type devOpenBao struct {
	t         *testing.T
	address   string
	root      string
	container string
	creds     map[string]custody.TransitDevCredentials
}

func startOpenBao(t *testing.T) *devOpenBao {
	t.Helper()
	if testing.Short() {
		t.Skip("a disposable OpenBao requires verify")
	}
	label := uuid.NewString()
	name := "astilba-openbao-" + label
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		docker := func(args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, "docker", append([]string{"--host", os.Getenv("PLAT_APP_DOCKER_HOST")}, args...)...).Output()
		}
		listed, err := docker("ps", "-aq", "--no-trunc", "--filter", "label=com.astilba.openbao-test="+label)
		if err != nil {
			t.Error("cannot list the OpenBao fixture's containers")
		}
		for _, id := range strings.Fields(string(listed)) {
			if _, err = docker("rm", "--force", "--volumes", id); err != nil {
				t.Error("cannot remove an OpenBao fixture container")
			}
		}
	})
	secret := make([]byte, 16)
	if _, err := rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	root := hex.EncodeToString(secret)
	// Listening on the container's loopback only: the test reaches it through the Docker API.
	script := `printf '%s' "$ASTILBA_AUDIT" > /tmp/astilba-audit.hcl && exec bao server -dev -dev-listen-address=127.0.0.1:8200 -config=/tmp/astilba-audit.hcl`
	dockerRun(t, "run", "--detach", "--name", name, "--label", "com.astilba.openbao-test="+label, "--cap-add", "IPC_LOCK",
		"--env", "BAO_DEV_ROOT_TOKEN_ID="+root, "--env", "ASTILBA_AUDIT="+openBaoConfig, "--entrypoint", "sh", openBaoImage, "-c", script)
	docker, err := client.New(client.WithHost(os.Getenv("PLAT_APP_DOCKER_HOST")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { docker.Close() })
	forward, err := startDockerForward(docker, name, "127.0.0.1", "8200")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(forward.Close)
	o := &devOpenBao{t: t, address: "http://" + forward.Addr(), root: root, container: name}
	probe := &http.Client{Timeout: 2 * time.Second}
	waitFor(t, "OpenBao", 2*time.Minute, func() bool {
		res, err := probe.Get(o.address + "/v1/sys/health")
		if err == nil {
			res.Body.Close()
		}
		return err == nil && res.StatusCode == 200
	})
	tokenFile := filepath.Join(t.TempDir(), "root-token")
	if err = os.WriteFile(tokenFile, []byte(root), 0o600); err != nil {
		t.Fatal(err)
	}
	if o.creds, err = custody.ProvisionTransitDev(t.Context(), o.address, tokenFile, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	return o
}

// transit signs in as one of the shipped roles.
func (o *devOpenBao) transit(role string) *custody.Transit {
	o.t.Helper()
	creds, ok := o.creds[role]
	if !ok {
		o.t.Fatalf("no role %s", role)
	}
	t, err := custody.NewTransit(custody.TransitConfig{Address: o.address, Loopback: true, Auth: custody.TransitAuth{Method: "approle", RoleIDFile: creds.RoleIDFile, SecretIDFile: creds.SecretIDFile}})
	if err != nil {
		o.t.Fatal(err)
	}
	return t
}

// api calls OpenBao directly with token, and returns the status and the response's data.
func (o *devOpenBao) api(method, path, token string, body any) (int, map[string]any) {
	o.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := stablejson.Marshal(body)
		if err != nil {
			o.t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(o.t.Context(), method, o.address+"/v1/"+path, reader)
	if err != nil {
		o.t.Fatal(err)
	}
	req.Header.Set("X-Vault-Token", token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		o.t.Fatal(err)
	}
	defer res.Body.Close()
	var out struct {
		Data map[string]any `json:"data"`
		Auth struct {
			ClientToken string `json:"client_token"`
		} `json:"auth"`
	}
	raw, _ := io.ReadAll(res.Body)
	_ = json.Unmarshal(raw, &out)
	if out.Auth.ClientToken != "" {
		out.Data = map[string]any{"token": out.Auth.ClientToken}
	}
	return res.StatusCode, out.Data
}

// token signs in as a shipped role and returns its token.
func (o *devOpenBao) token(role string) string {
	o.t.Helper()
	read := func(path string) string {
		raw, err := os.ReadFile(path)
		if err != nil {
			o.t.Fatal(err)
		}
		return strings.TrimSpace(string(raw))
	}
	creds := o.creds[role]
	status, data := o.api("POST", "auth/approle/login", "", map[string]string{"role_id": read(creds.RoleIDFile), "secret_id": read(creds.SecretIDFile)})
	if status != 200 || data["token"] == nil {
		o.t.Fatalf("sign in as %s: %d", role, status)
	}
	return data["token"].(string)
}

// auditEntry is one line the server's audit device wrote.
type auditEntry struct {
	Type string `json:"type"`
	Auth struct {
		Policies []string `json:"policies"`
	} `json:"auth"`
	Request struct {
		ID   string            `json:"id"`
		Path string            `json:"path"`
		Data map[string]string `json:"data"`
	} `json:"request"`
	Response struct {
		Data map[string]any `json:"data"`
	} `json:"response"`
}

// audit reads every entry of one type, "request" or "response", the server's audit device wrote.
func (o *devOpenBao) audit(kind string) []auditEntry {
	o.t.Helper()
	logs := dockerOutput(o.t, "logs", o.container)
	var out []auditEntry
	lines := bufio.NewScanner(bytes.NewReader(logs))
	lines.Buffer(make([]byte, 1<<20), 1<<20)
	for lines.Scan() {
		var e auditEntry
		if !bytes.HasPrefix(lines.Bytes(), []byte(`{"time"`)) || json.Unmarshal(lines.Bytes(), &e) != nil || e.Type != kind {
			continue
		}
		out = append(out, e)
	}
	return out
}

// The Transit backend passes the conformance suite every backend passes, against a real OpenBao,
// signed in as a role that may wrap and unwrap under every class.
func TestOpenBaoConformance(t *testing.T) {
	o := startOpenBao(t)
	policy := ""
	for _, class := range custody.Classes {
		for _, op := range []string{"encrypt", "decrypt", "rewrap"} {
			policy += `path "astilba-transit/` + op + `/` + custody.TransitKey(class) + `" { capabilities = ["update"] }` + "\n"
		}
		policy += `path "astilba-transit/keys/` + custody.TransitKey(class) + `" { capabilities = ["read"] }` + "\n"
	}
	if status, _ := o.api("PUT", "sys/policies/acl/conformance", o.root, map[string]string{"policy": policy}); status != 204 {
		t.Fatalf("policy: %d", status)
	}
	if status, _ := o.api("POST", "auth/approle/role/conformance", o.root, map[string]any{"token_policies": []string{"conformance"}}); status != 204 {
		t.Fatalf("role: %d", status)
	}
	_, role := o.api("GET", "auth/approle/role/conformance/role-id", o.root, nil)
	_, secret := o.api("POST", "auth/approle/role/conformance/secret-id", o.root, map[string]string{})
	dir := t.TempDir()
	o.creds["conformance"] = custody.TransitDevCredentials{RoleIDFile: filepath.Join(dir, "role"), SecretIDFile: filepath.Join(dir, "secret")}
	for path, value := range map[string]any{o.creds["conformance"].RoleIDFile: role["role_id"], o.creds["conformance"].SecretIDFile: secret["secret_id"]} {
		if err := os.WriteFile(path, []byte(value.(string)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	custodytest.Conformance(t, o.transit("conformance"), custodytest.Options{RequestIDs: true, Tamper: func(blob []byte) []byte {
		// Change one byte of the sealed key, keeping the base64 and the version prefix.
		raw, err := base64.StdEncoding.DecodeString(string(blob[len("vault:v1:"):]))
		if err != nil {
			t.Fatal(err)
		}
		raw[len(raw)/2] ^= 1
		return append(blob[:len("vault:v1:")], base64.StdEncoding.EncodeToString(raw)...)
	}})
}

// The shipped policies: serving unwraps under serve, export and installation and only wraps under
// rotate; the operator unwraps only under rotate. Neither can take a key out of the key service,
// back it up, restore one, delete one, trim its versions, or change more of its settings than how
// old a version may still decrypt and how often it rotates.
func TestOpenBaoPolicies(t *testing.T) {
	o := startOpenBao(t)
	ctx := t.Context()
	c := custody.Context{Installation: uuid.New(), Scope: custody.ScopeWorkspace, Workspace: uuid.New(), Generation: 1, Purpose: "rotate"}
	key, _, err := custody.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	serving, operator := o.transit("astilba-serving"), o.transit("astilba-operator")
	if err = serving.Check(ctx); err != nil {
		t.Fatalf("serving reads key settings: %v", err)
	}
	if err = operator.Check(ctx); err != nil {
		t.Fatalf("the operator reads key settings: %v", err)
	}
	wrapped := map[custody.Class][]byte{}
	for _, class := range custody.Classes {
		// Both wrap under every class.
		for name, b := range map[string]*custody.Transit{"serving": serving, "operator": operator} {
			blob, _, err := b.Wrap(ctx, class, key, c)
			if err != nil {
				t.Fatalf("%s wraps under %s: %v", name, class, err)
			}
			wrapped[class] = blob
		}
	}
	for _, class := range custody.Classes {
		_, servingErr := serving.Unwrap(ctx, class, wrapped[class], c)
		_, operatorErr := operator.Unwrap(ctx, class, wrapped[class], c)
		if custody.MayUnwrap(serving, class, false) != (servingErr == nil) || !custody.MayUnwrap(serving, class, false) && !errors.Is(servingErr, custody.ErrDenied) {
			t.Errorf("serving unwraps under %s: %v", class, servingErr)
		}
		if custody.MayUnwrap(operator, class, true) != (operatorErr == nil) || !custody.MayUnwrap(operator, class, true) && !errors.Is(operatorErr, custody.ErrDenied) {
			t.Errorf("the operator unwraps under %s: %v", class, operatorErr)
		}
		// The operator moves wrappings onto a rotated key without unwrapping them.
		if _, _, err = custody.Rewrap(ctx, operator, class, wrapped[class], c); err != nil {
			t.Errorf("the operator rewraps under %s: %v", class, err)
		}
		if _, _, err = custody.Rewrap(ctx, serving, class, wrapped[class], c); !errors.Is(err, custody.ErrDenied) {
			t.Errorf("serving rewraps under %s: %v", class, err)
		}
	}
	tokens := map[string]string{"serving": o.token("astilba-serving"), "operator": o.token("astilba-operator")}
	k := "astilba-transit/keys/astilba-rotate"
	for _, refused := range []struct{ method, path string }{
		{"GET", "astilba-transit/export/encryption-key/astilba-rotate"},
		{"GET", "astilba-transit/export/encryption-key/astilba-serve/1"},
		{"GET", "astilba-transit/backup/astilba-rotate"},
		{"POST", "astilba-transit/restore"},
		{"POST", "astilba-transit/restore/astilba-rotate"},
		{"DELETE", k},
		{"POST", k + "/trim"},
		{"POST", "astilba-transit/keys/astilba-new"},
		{"POST", "astilba-transit/encrypt/astilba-new"},
		{"POST", "astilba-transit/datakey/plaintext/astilba-serve"},
		{"LIST", "astilba-transit/keys"},
		{"POST", "sys/mounts/astilba-transit/tune"},
		{"PUT", "sys/policies/acl/astilba-serving"},
	} {
		for who, token := range tokens {
			body := map[string]any{"backup": "x", "min_available_version": 1, "context": base64.StdEncoding.EncodeToString([]byte("x")), "plaintext": "AAAA"}
			if status, _ := o.api(refused.method, refused.path, token, body); status != http.StatusForbidden {
				t.Errorf("%s %s %s: %d", who, refused.method, refused.path, status)
			}
		}
	}
	// Serving changes no setting and rotates nothing.
	for _, path := range []string{k + "/config", k + "/rotate"} {
		if status, _ := o.api("POST", path, tokens["serving"], map[string]any{"min_decryption_version": 1}); status != http.StatusForbidden {
			t.Errorf("serving %s: %d", path, status)
		}
	}
	// The operator rotates, and changes only the two settings it is allowed.
	for _, change := range []map[string]any{{"deletion_allowed": true}, {"exportable": true}, {"allow_plaintext_backup": true}, {"min_encryption_version": 1}, {"min_decryption_version": 1, "deletion_allowed": true}} {
		if status, _ := o.api("POST", k+"/config", tokens["operator"], change); status != http.StatusForbidden {
			t.Errorf("the operator set %v: %d", change, status)
		}
	}
	if status, _ := o.api("POST", k+"/rotate", tokens["operator"], map[string]any{}); status != 200 && status != 204 {
		t.Errorf("the operator rotates: %d", status)
	}
	if status, _ := o.api("POST", k+"/config", tokens["operator"], map[string]any{"min_decryption_version": 1, "auto_rotate_period": "8760h"}); status != 200 && status != 204 {
		t.Errorf("the operator sets the allowed settings: %d", status)
	}
	// Whatever was asked, every key still cannot leave and cannot be deleted.
	if err = serving.Check(ctx); err != nil {
		t.Fatal(err)
	}
	// The audit log shows each request's context in clear, and nothing else: no mount lists a
	// key's plaintext among the request or response fields it leaves unhashed.
	if status, tune := o.api("GET", "sys/mounts/astilba-transit/tune", o.root, nil); status != 200 || fmt.Sprint(tune["audit_non_hmac_request_keys"]) != "[context]" || tune["audit_non_hmac_response_keys"] != nil {
		t.Errorf("the Transit mount's audit settings: %d %v", status, tune)
	}
	status, mounts := o.api("GET", "sys/mounts", o.root, nil)
	if status != 200 || len(mounts) == 0 {
		t.Fatalf("mounts: %d", status)
	}
	for path, mount := range mounts {
		config, _ := mount.(map[string]any)["config"].(map[string]any)
		for _, setting := range []string{"audit_non_hmac_request_keys", "audit_non_hmac_response_keys"} {
			if keys, _ := config[setting].([]any); slices.Contains(keys, any("plaintext")) {
				t.Errorf("%s leaves plaintext unhashed in its %s", path, setting)
			}
		}
	}
	// The listing shows the settings, so the loop above looked at them.
	if transit, _ := mounts["astilba-transit/"].(map[string]any); transit == nil || fmt.Sprint(transit["config"].(map[string]any)["audit_non_hmac_request_keys"]) != "[context]" {
		t.Errorf("the mount listing does not show the Transit mount's audit settings: %v", transit)
	}
}

// The broker, on Transit: every unwrap is in the key service's audit log with its context in
// clear, and the access record names the provider's request, so the two can be matched. An
// operator's command checks only what it may unwrap, and moves wrappings without unwrapping them.
func TestOpenBaoBrokerRecordsProviderRequests(t *testing.T) {
	o := startOpenBao(t)
	ctx := t.Context()
	pool := freshDatabase(t)
	serving, err := custody.WithKeyring(o.transit("astilba-serving"), testKeyring(t))
	if err != nil {
		t.Fatal(err)
	}
	b := secretbroker.New(db.New(pool), serving, nil)
	if err = b.Install(ctx, pool, uuid.Nil, false); err != nil {
		t.Fatal(err)
	}
	if more, err := b.InitKeys(ctx, func() bool { return true }); err != nil || more {
		t.Fatalf("keys: %v %v", more, err)
	}
	backends, err := db.New(pool).CustodyBackends(ctx)
	if err != nil || len(backends) != 1 || backends[0].BackendID != "transit:primary" {
		t.Fatalf("backends: %+v %v", backends, err)
	}
	account := uuid.New()
	s, err := b.Begin(ctx, secretbroker.Request{Purpose: secretbroker.Authenticate, Principal: secretbroker.Account(account), Items: []secretbroker.Item{{Kind: secretbroker.TOTP, ID: account}}})
	if err != nil {
		t.Fatal(err)
	}
	s.Close(ctx, secretbroker.Used)
	var raw []byte
	if err = pool.QueryRow(ctx, `SELECT provider_request_ids FROM secret_access_outcomes WHERE access_id=$1`, s.ID()).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var requests []string
	if err = json.Unmarshal(raw, &requests); err != nil || len(requests) != 1 {
		t.Fatalf("provider requests %s: %v", raw, err)
	}
	installation, err := db.New(pool).InstallationID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := custody.Context{Installation: installation, Scope: custody.ScopeInstallation, Generation: 1, Purpose: "authenticate"}
	var found bool
	for _, e := range o.audit("request") {
		if e.Request.Path == "astilba-transit/encrypt/astilba-installation" && strings.HasPrefix(e.Request.Data["plaintext"], "hmac-sha256:") == false {
			t.Fatalf("a key was written to the audit log: %s", e.Request.Path)
		}
		if e.Request.ID != requests[0] {
			continue
		}
		found = true
		context, err := base64.StdEncoding.DecodeString(e.Request.Data["context"])
		if err != nil || string(context) != want.String() || e.Request.Path != "astilba-transit/decrypt/astilba-installation" {
			t.Fatalf("audit: %s %q %v", e.Request.Path, context, err)
		}
		if !strings.HasPrefix(e.Request.Data["ciphertext"], "hmac-sha256:") {
			t.Fatal("the wrapping was written to the audit log in clear")
		}
	}
	if !found {
		t.Fatalf("request %s is not in the audit log", requests[0])
	}
	decrypts := 0
	for _, e := range o.audit("response") {
		if !strings.HasPrefix(e.Request.Path, "astilba-transit/decrypt/") {
			continue
		}
		decrypts++
		if plaintext, _ := e.Response.Data["plaintext"].(string); !strings.HasPrefix(plaintext, "hmac-sha256:") {
			t.Fatalf("a decrypt's response wrote the key to the audit log: %s", e.Request.Path)
		}
	}
	if decrypts == 0 {
		t.Fatal("no decrypt response in the audit log")
	}
	created := false
	events, err := db.New(pool).InstallationKeyEvents(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		var details map[string]string
		if e.Kind == "key-created" && json.Unmarshal(e.Details, &details) == nil && details["provider_requests"] != "" {
			created = len(strings.Split(details["provider_requests"], ",")) > 1
		}
	}
	if !created {
		t.Fatal("key creation recorded no provider requests")
	}

	// The operator's command signs in as the operator: it checks the rotate canary only, which
	// serving may not open, and rewraps every key wrapping inside the key service.
	op, err := custody.WithKeyring(o.transit("astilba-operator"), testKeyring(t))
	if err != nil {
		t.Fatal(err)
	}
	ob := secretbroker.New(db.New(pool), op, nil)
	if err = ob.Install(ctx, pool, uuid.Nil, true); err != nil {
		t.Fatal(err)
	}
	st := store.New(pool, ob, false)
	if err = st.Rewrap(ctx, "on-call", "PLAT-247 test"); err != nil {
		t.Fatal(err)
	}
	// Converting values onto workspace keys is the operator's too, and opens their keys under
	// rotate alone.
	if conversion, err := ob.Convert(ctx, "on-call", "PLAT-247 test"); err != nil || conversion.Waiting != "" {
		t.Fatalf("convert: %+v %v", conversion, err)
	}
	// What the operator rewrapped still opens for serving.
	if err = secretbroker.New(db.New(pool), serving, nil).Install(ctx, pool, uuid.Nil, false); err != nil {
		t.Fatal(err)
	}
	s, err = b.Begin(ctx, secretbroker.Request{Purpose: secretbroker.Authenticate, Principal: secretbroker.Account(account), Items: []secretbroker.Item{{Kind: secretbroker.TOTP, ID: account}}})
	if err != nil {
		t.Fatal(err)
	}
	s.Close(ctx, secretbroker.Used)
	// The operator unwrapped only under rotate, and rewrapped everything else in place. The
	// rotation's outcome, before the conversion's, names each rewrap's request.
	var rewraps []string
	for _, e := range o.audit("request") {
		if !slices.Contains(e.Auth.Policies, "astilba-operator") {
			continue
		}
		if strings.HasPrefix(e.Request.Path, "astilba-transit/decrypt/") && e.Request.Path != "astilba-transit/decrypt/astilba-rotate" {
			t.Errorf("the operator unwrapped %s", e.Request.Path)
		}
		if strings.HasPrefix(e.Request.Path, "astilba-transit/rewrap/") {
			rewraps = append(rewraps, e.Request.ID)
		}
	}
	// Four canaries, and the installation and token keys' wrappings: 3 and 2 purposes.
	if len(rewraps) != len(custody.Classes)+len(custody.Purposes(custody.ScopeInstallation))+len(custody.Purposes(custody.ScopeToken)) {
		t.Errorf("%d rewraps", len(rewraps))
	}
	if err = pool.QueryRow(ctx, `SELECT o.provider_request_ids FROM secret_access a JOIN secret_access_outcomes o ON o.access_id = a.id WHERE a.purpose='rotate' AND a.principal_kind='operator' ORDER BY a.at LIMIT 1`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &requests); err != nil || !slices.Equal(slices.Sorted(slices.Values(requests)), slices.Sorted(slices.Values(rewraps))) {
		t.Fatalf("the rotation names %q, the audit log %q: %v", requests, rewraps, err)
	}

	// An instance's token key unwraps are each named on the event after it, as in the audit log.
	tokens, err := b.Tokens(ctx, dbmigrate.CustodyStage)
	if err != nil {
		t.Fatal(err)
	}
	if events, err = db.New(pool).InstallationKeyEvents(ctx, 1000); err != nil {
		t.Fatal(err)
	}
	var opened []string
	for _, e := range events {
		var details map[string]string
		if e.Kind == "token-key-opened" && e.RunID == tokens.Instance() && json.Unmarshal(e.Details, &details) == nil {
			opened = append(opened, details["provider_requests"])
		}
	}
	if len(opened) != 1 {
		t.Fatalf("token keys opened: %q", opened)
	}
	found = false
	for _, e := range o.audit("request") {
		found = found || e.Request.ID == opened[0] && e.Request.Path == "astilba-transit/decrypt/astilba-installation"
	}
	if !found {
		t.Fatalf("token key request %q is not in the audit log", opened[0])
	}
}

// Each start checks every canary its identity may open on Transit: serving's three classes, and
// the operator's rotate. A canary that does not open refuses the start, and so does an operator's
// command given the serving identity's credentials, which the key service refuses.
func TestOpenBaoStartChecksWhatEachIdentityMayOpen(t *testing.T) {
	o := startOpenBao(t)
	ctx := t.Context()
	pool := freshDatabase(t)
	serving, err := custody.WithKeyring(o.transit("astilba-serving"), testKeyring(t))
	if err != nil {
		t.Fatal(err)
	}
	operator, err := custody.WithKeyring(o.transit("astilba-operator"), testKeyring(t))
	if err != nil {
		t.Fatal(err)
	}
	start := func(b custody.Legacy, asOperator bool) error {
		return secretbroker.New(db.New(pool), b, nil).Install(ctx, pool, uuid.Nil, asOperator)
	}
	if err = start(serving, false); err != nil {
		t.Fatal(err)
	}
	if err = start(operator, true); err != nil {
		t.Fatal(err)
	}
	refused := func(name, cause string, class custody.Class) {
		t.Helper()
		events, err := db.New(pool).InstallationKeyEvents(ctx, 100)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range events {
			if e.Kind != "start-refused" {
				continue
			}
			var details map[string]string
			if err = json.Unmarshal(e.Details, &details); err != nil || details["cause"] != cause || details["class"] != string(class) {
				t.Fatalf("%s: the refusal recorded %v %v", name, details, err)
			}
			return
		}
		t.Fatalf("%s: no refusal recorded", name)
	}
	if err = start(serving, true); !errors.Is(err, secretbroker.ErrBackendRefused) || !errors.Is(err, custody.ErrDenied) {
		t.Fatalf("an operator's command with serving's credentials: %v", err)
	}
	refused("serving's credentials", "backend-refused", custody.ClassRotate)
	// One class's canary replaced by another's no longer opens.
	for _, class := range custody.Classes {
		other := custody.ClassServe
		if class == other {
			other = custody.ClassExport
		}
		var original []byte
		if err = pool.QueryRow(ctx, `SELECT wrapped FROM installation_key_canaries WHERE class=$1`, string(class)).Scan(&original); err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, `UPDATE installation_key_canaries SET wrapped=(SELECT wrapped FROM installation_key_canaries WHERE class=$2) WHERE class=$1`, string(class), string(other)); err != nil {
			t.Fatal(err)
		}
		b, asOperator := serving, false
		if !custody.MayUnwrap(serving, class, false) {
			b, asOperator = operator, true
		}
		if err = start(b, asOperator); !errors.Is(err, secretbroker.ErrCanaryMismatch) {
			t.Fatalf("the %s canary does not open: %v", class, err)
		}
		refused(string(class), "canary", class)
		if _, err = pool.Exec(ctx, `UPDATE installation_key_canaries SET wrapped=$2 WHERE class=$1`, string(class), original); err != nil {
			t.Fatal(err)
		}
	}
	// Status checks what the identity it runs as may open, and says the rest is the other's.
	status := func(b custody.Legacy, asOperator bool) secretbroker.Status {
		t.Helper()
		s, err := secretbroker.New(db.New(pool), b, nil).Status(ctx, pool, uuid.Nil, asOperator)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	canaryPending := func(s secretbroker.Status) []string {
		var out []string
		for _, line := range s.Pending() {
			if strings.Contains(line, "canary") {
				out = append(out, line)
			}
		}
		return out
	}
	for _, asOperator := range []bool{false, true} {
		b := serving
		if asOperator {
			b = operator
		}
		s := status(b, asOperator)
		for _, class := range custody.Classes {
			if want := custody.MayUnwrap(b, class, asOperator); (s.Canaries[class] == "ok") != want {
				t.Errorf("status as the operator %v: %s canary %q", asOperator, class, s.Canaries[class])
			}
		}
		if pending := canaryPending(s); len(pending) != 0 {
			t.Errorf("status as the operator %v: %v", asOperator, pending)
		}
	}
	// A rotate canary under a version the key no longer decrypts is the operator's to see.
	k := "astilba-transit/keys/astilba-rotate"
	if code, _ := o.api("POST", k+"/rotate", o.root, map[string]any{}); code != 200 && code != 204 {
		t.Fatalf("rotate: %d", code)
	}
	if code, _ := o.api("POST", k+"/config", o.root, map[string]any{"min_decryption_version": 2}); code != 200 && code != 204 {
		t.Fatalf("config: %d", code)
	}
	if pending := canaryPending(status(operator, true)); len(pending) != 1 || !strings.Contains(pending[0], "rotate canary does not open") {
		t.Errorf("an operator's status with a rotate canary too old to open: %v", pending)
	}
}

// testKeyring is the keyring values sealed before per-workspace keys would open under; a new
// installation on Transit has none, but every instance still loads one.
func testKeyring(t *testing.T) *cryptobox.Keyring {
	t.Helper()
	ring, err := cryptobox.New("test")
	if err != nil {
		t.Fatal(err)
	}
	return ring
}
