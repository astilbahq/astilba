package custody

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// TransitPolicies are the shipped OpenBao and Vault policies, by name: the serving identity's and
// the operator's. Self-hosters apply these files; local development applies them through
// ProvisionTransitDev.
//
//go:embed policies/*.hcl
var transitPolicies embed.FS

// TransitPolicyNames are the shipped policies, which are also the AppRole names local development
// gives each identity.
var TransitPolicyNames = []string{"astilba-serving", "astilba-operator"}

// TransitPolicy returns a shipped policy's text.
func TransitPolicy(name string) (string, error) {
	raw, err := transitPolicies.ReadFile("policies/" + name + ".hcl")
	return string(raw), err
}

// TransitDevCredentials names the files ProvisionTransitDev writes for one identity.
type TransitDevCredentials struct {
	RoleIDFile, SecretIDFile string
}

// ProvisionTransitDev sets up a local OpenBao dev server the way the setup guide does by hand: the
// Transit mount, tuned so its audit device shows each request's context in clear, one key per
// class, the shipped policies, and an AppRole for each. It writes each role's ID and a new secret
// ID to private files in dir. It only talks to a loopback address, and is safe to run again.
//
// A production server is never set up this way: management would need a token that can change its
// own policies. The guide's commands are run there by an operator instead.
func ProvisionTransitDev(ctx context.Context, address, rootToken, dir string) (map[string]TransitDevCredentials, error) {
	server, err := newTransitServer(address, "", "", true)
	if err != nil {
		return nil, err
	}
	if !loopback(server.base.Hostname()) {
		return nil, errors.New("init-transit --dev sets up only a local OpenBao dev server, on a loopback address")
	}
	if rootToken == "" {
		return nil, errors.New("init-transit --dev needs the dev server's root token")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	call := func(path string, body any, out any) error {
		_, _, err := server.send(ctx, path, rootToken, body, out, false)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		return nil
	}
	var mounts map[string]any
	if err = call("/sys/mounts", nil, &mounts); err != nil {
		return nil, err
	}
	if _, ok := mounts[DefaultTransitMount+"/"]; !ok {
		if err = call("/sys/mounts/"+DefaultTransitMount, map[string]any{"type": "transit", "description": "Astilba key custody"}, nil); err != nil {
			return nil, err
		}
	}
	// The context names the installation, workspace, generation and purpose, none of them secret.
	// Keys' plaintext and ciphertext stay hashed in the audit log.
	if err = call("/sys/mounts/"+DefaultTransitMount+"/tune", map[string]any{"audit_non_hmac_request_keys": []string{"context"}}, nil); err != nil {
		return nil, err
	}
	for _, class := range Classes {
		// Creating a key that exists changes nothing, and Check refuses one made otherwise.
		if err = call("/"+DefaultTransitMount+"/keys/"+TransitKey(class), map[string]any{"type": "aes256-gcm96", "derived": true, "exportable": false, "allow_plaintext_backup": false}, nil); err != nil {
			return nil, err
		}
	}
	var auths map[string]any
	if err = call("/sys/auth", nil, &auths); err != nil {
		return nil, err
	}
	if _, ok := auths["approle/"]; !ok {
		if err = call("/sys/auth/approle", map[string]any{"type": "approle"}, nil); err != nil {
			return nil, err
		}
	}
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	out := map[string]TransitDevCredentials{}
	for _, name := range TransitPolicyNames {
		policy, err := TransitPolicy(name)
		if err != nil {
			return nil, err
		}
		if err = call("/sys/policies/acl/"+name, map[string]any{"policy": policy}, nil); err != nil {
			return nil, err
		}
		// Tokens last an hour and are renewed for up to a day; local development binds no
		// addresses, which the guide does.
		if err = call("/auth/approle/role/"+name, map[string]any{"token_policies": []string{name}, "token_ttl": "1h", "token_max_ttl": "24h", "secret_id_num_uses": 0, "token_no_default_policy": false}, nil); err != nil {
			return nil, err
		}
		var role struct {
			RoleID string `json:"role_id"`
		}
		if err = call("/auth/approle/role/"+name+"/role-id", nil, &role); err != nil {
			return nil, err
		}
		var secret struct {
			SecretID string `json:"secret_id"`
		}
		if err = call("/auth/approle/role/"+name+"/secret-id", map[string]any{}, &secret); err != nil {
			return nil, err
		}
		if role.RoleID == "" || secret.SecretID == "" {
			return nil, fmt.Errorf("OpenBao returned no credentials for %s", name)
		}
		creds := TransitDevCredentials{RoleIDFile: filepath.Join(dir, strings.TrimPrefix(name, "astilba-")+"-role-id"), SecretIDFile: filepath.Join(dir, strings.TrimPrefix(name, "astilba-")+"-secret-id")}
		if err = writePrivate(creds.RoleIDFile, role.RoleID); err != nil {
			return nil, err
		}
		if err = writePrivate(creds.SecretIDFile, secret.SecretID); err != nil {
			return nil, err
		}
		out[name] = creds
	}
	return out, nil
}

// writePrivate replaces path with value, readable only by its owner.
func writePrivate(path, value string) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0o600); err == nil {
		_, err = f.WriteString(value + "\n")
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
