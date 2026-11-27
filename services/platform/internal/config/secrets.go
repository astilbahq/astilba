package config

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
)

// A secretSetting is a credential that can be set in its environment variable or, preferably,
// in a private file that its _FILE variable names. Hosted installations accept only the file,
// so a leaked environment exposes none of them. See docs/hosting.md#secret-settings.
type secretSetting struct {
	Name string
	// Multiline allows line breaks inside the value, as a PEM key has. Any other value is one
	// line, and a line break in it is refused rather than sent to the provider.
	Multiline bool
	// HostedOnly settings are read only when hosted, the only time they are used.
	HostedOnly bool
	// field is where Load puts the value.
	field func(*Config) *string
}

func (s secretSetting) File() string { return s.Name + "_FILE" }

var (
	secretDatabaseURL         = secretSetting{Name: "ASTILBA_DATABASE_URL", field: func(c *Config) *string { return &c.DatabaseURL }}
	secretDNSCloudflareToken  = secretSetting{Name: "ASTILBA_DNS_CLOUDFLARE_TOKEN", field: func(c *Config) *string { return &c.ApplicationDNS.Token }}
	secretSMTPPassword        = secretSetting{Name: "ASTILBA_SMTP_PASSWORD", field: func(c *Config) *string { return &c.SMTP.Password }}
	secretStripeSecret        = secretSetting{Name: "ASTILBA_STRIPE_SECRET", HostedOnly: true, field: func(c *Config) *string { return &c.Hosting.SecretKey }}
	secretStripeWebhookSecret = secretSetting{Name: "ASTILBA_STRIPE_WEBHOOK_SECRET", HostedOnly: true, field: func(c *Config) *string { return &c.Hosting.WebhookSecret }}
	secretGitHubClientSecret  = secretSetting{Name: "ASTILBA_GITHUB_APP_CLIENT_SECRET", field: func(c *Config) *string { return &c.GitHubApp.ClientSecret }}
	secretGitHubWebhookSecret = secretSetting{Name: "ASTILBA_GITHUB_APP_WEBHOOK_SECRET", field: func(c *Config) *string { return &c.GitHubApp.WebhookSecret }}
	secretGitHubPrivateKey    = secretSetting{Name: "ASTILBA_GITHUB_APP_PRIVATE_KEY", Multiline: true, field: func(c *Config) *string { return &c.GitHubApp.PrivateKey }}
)

// secretSettings is every setting that holds a credential. Load reads each with readSecret, and
// no credential any other way.
var secretSettings = []secretSetting{
	secretDatabaseURL, secretDNSCloudflareToken, secretSMTPPassword, secretStripeSecret,
	secretStripeWebhookSecret, secretGitHubClientSecret, secretGitHubWebhookSecret, secretGitHubPrivateKey,
}

// libraryPasswords are environment variables that the PostgreSQL driver reads by itself. Hosted
// installations refuse them, since the database URL in ASTILBA_DATABASE_URL_FILE can hold the
// password instead.
var libraryPasswords = []string{"PGPASSWORD", "PGSSLPASSWORD"}

// maxSecretFileBytes bounds a secret file. The largest secret, a GitHub App PEM key, is far
// smaller.
const maxSecretFileBytes = 64 << 10

// readSecret returns a secret setting's value, or "" when neither form is set. An empty variable
// counts as unset, as Compose's ${NAME:-} gives. Errors name the setting, never its value or the
// file's contents.
func readSecret(s secretSetting, hosted bool) (string, error) {
	value, path := os.Getenv(s.Name), os.Getenv(s.File())
	switch {
	case value != "" && hosted:
		return "", fmt.Errorf("%s is refused when hosted: put the value in a private file and name it in %s", s.Name, s.File())
	case value != "" && path != "":
		return "", fmt.Errorf("set %s or %s, not both", s.Name, s.File())
	case path != "":
		raw, err := readSecretFile(s, path)
		if err != nil {
			return "", err
		}
		value = raw
	case value == "":
		return "", nil
	}
	if strings.ContainsRune(value, 0) {
		return "", fmt.Errorf("%s must not contain a NUL byte", s.Name)
	}
	if !s.Multiline && strings.ContainsAny(value, "\r\n") {
		return "", fmt.Errorf("%s must be a single line", s.Name)
	}
	return value, nil
}

// readSecretFile reads a secret file strictly. It must be a regular file, or a symbolic link to
// one, that only its owner can read or write, as the keyring and every other private file
// Astilba reads must be. A Compose file secret is the host's file, mounted as it is. Exactly one
// trailing newline, LF or CRLF, is removed, as editors and `echo` add one; anything left must not
// be empty.
func readSecretFile(s secretSetting, path string) (string, error) {
	// Stat first, so a FIFO or device is refused rather than blocking start-up on its open.
	if info, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("%s cannot be read: %w", s.File(), pathCause(err))
	} else if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s must name a regular file", s.File())
	}
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("%s cannot be read: %w", s.File(), pathCause(err))
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s must name a regular file", s.File())
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("%s must name a file only its owner can read (mode 0400 or 0600)", s.File())
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxSecretFileBytes+1))
	if err != nil {
		return "", fmt.Errorf("%s cannot be read: %w", s.File(), pathCause(err))
	}
	if len(raw) > maxSecretFileBytes {
		return "", fmt.Errorf("%s must name a file of at most %d KiB", s.File(), maxSecretFileBytes>>10)
	}
	value := string(raw)
	if trimmed, ok := strings.CutSuffix(value, "\n"); ok {
		value, _ = strings.CutSuffix(trimmed, "\r")
	}
	if value == "" {
		return "", fmt.Errorf("%s names an empty file", s.File())
	}
	return value, nil
}

// pathCause drops the path from a file error, leaving its cause, such as "permission denied".
func pathCause(err error) error {
	if pathErr, ok := errors.AsType[*fs.PathError](err); ok {
		return pathErr.Err
	}
	return err
}

// refuseLibraryPasswords stops a hosted start-up that would let the PostgreSQL driver take a
// password from the environment.
func refuseLibraryPasswords() error {
	for _, name := range libraryPasswords {
		if os.Getenv(name) != "" {
			return fmt.Errorf("%s is refused when hosted: put the password in the database URL that %s names", name, secretDatabaseURL.File())
		}
	}
	return nil
}
