package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// secretFields says where each secret setting lands in Config. A setting added to secretSettings
// without a row here fails TestEverySecretSettingIsCovered.
var secretFields = map[string]struct {
	get func(Config) string
	// hostedOnly settings are read only when hosted.
	hostedOnly bool
}{
	"ASTILBA_DATABASE_URL":              {get: func(c Config) string { return c.DatabaseURL }},
	"ASTILBA_DNS_CLOUDFLARE_TOKEN":      {get: func(c Config) string { return c.ApplicationDNS.Token }},
	"ASTILBA_SMTP_PASSWORD":             {get: func(c Config) string { return c.SMTP.Password }},
	"ASTILBA_STRIPE_SECRET":             {get: func(c Config) string { return c.Hosting.SecretKey }, hostedOnly: true},
	"ASTILBA_STRIPE_WEBHOOK_SECRET":     {get: func(c Config) string { return c.Hosting.WebhookSecret }, hostedOnly: true},
	"ASTILBA_GITHUB_APP_CLIENT_SECRET":  {get: func(c Config) string { return c.GitHubApp.ClientSecret }},
	"ASTILBA_GITHUB_APP_WEBHOOK_SECRET": {get: func(c Config) string { return c.GitHubApp.WebhookSecret }},
	"ASTILBA_GITHUB_APP_PRIVATE_KEY":    {get: func(c Config) string { return c.GitHubApp.PrivateKey }},
}

// clearSecrets unsets both forms of every secret setting, and the driver's passwords, so the
// test's own environment cannot leak in.
func clearSecrets(t *testing.T) {
	t.Helper()
	for _, s := range secretSettings {
		t.Setenv(s.Name, "")
		t.Setenv(s.File(), "")
	}
	for _, name := range libraryPasswords {
		t.Setenv(name, "")
	}
}

// secretFile writes an owner-only file and returns its path.
func secretFile(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// baseEnvironment is a configuration Load accepts, self-hosted or hosted, with no secret but the
// database URL, which is in its file when hosted.
func baseEnvironment(t *testing.T, hosted bool) {
	t.Helper()
	clearSecrets(t)
	t.Setenv("ASTILBA_KEYRING", "keyring.json")
	t.Setenv("ASTILBA_KEYRING_JSON", "")
	t.Setenv("ASTILBA_KEY_BACKEND", "")
	t.Setenv("ASTILBA_SMTP_MODE", "")
	t.Setenv("ASTILBA_STRIPE_PRICE", "")
	if !hosted {
		t.Setenv("ASTILBA_HOSTED", "false")
		t.Setenv("ASTILBA_DATABASE_URL", "postgres://example.invalid/platform")
		return
	}
	t.Setenv("ASTILBA_HOSTED", "true")
	t.Setenv("ASTILBA_HOSTED_COHORT_LIMIT", "10")
	t.Setenv("ASTILBA_HOSTED_SERVER_LIMIT", "10")
	t.Setenv("ASTILBA_HOSTED_SUPPORT", "support@example.test")
	t.Setenv("ASTILBA_SECRET_ACCESS_RETENTION_DAYS", "")
	t.Setenv("ASTILBA_DATABASE_URL_FILE", secretFile(t, "postgres://example.invalid/platform\n"))
}

func TestEverySecretSettingIsCovered(t *testing.T) {
	var names []string
	for _, s := range secretSettings {
		names = append(names, s.Name)
	}
	var covered []string
	for name := range secretFields {
		covered = append(covered, name)
	}
	slices.Sort(names)
	slices.Sort(covered)
	if !slices.Equal(names, covered) {
		t.Fatalf("secret settings %v, tested %v", names, covered)
	}
}

// Each secret setting: self-hosted takes either form, both forms are refused, and hosted refuses
// the environment form by name and takes the file.
func TestEachSecretSetting(t *testing.T) {
	for _, s := range secretSettings {
		field := secretFields[s.Name]
		value := "value-of-" + strings.ToLower(s.Name)
		if s.Multiline {
			value = "-----BEGIN TEST-----\n" + value + "\n-----END TEST-----"
		}
		// prepare sets everything the setting needs beside itself.
		prepare := func(t *testing.T, hosted bool) {
			baseEnvironment(t, hosted)
			t.Setenv(s.Name, "")
			t.Setenv(s.File(), "")
			if strings.HasPrefix(s.Name, "ASTILBA_STRIPE_") {
				// Stripe is all or nothing.
				t.Setenv("ASTILBA_STRIPE_PRICE", "price_test")
				for _, other := range []secretSetting{secretStripeSecret, secretStripeWebhookSecret} {
					if other != s {
						t.Setenv(other.File(), secretFile(t, "other"))
					}
				}
			}
		}
		t.Run(s.Name, func(t *testing.T) {
			if !field.hostedOnly {
				t.Run("self-hosted environment", func(t *testing.T) {
					prepare(t, false)
					t.Setenv(s.Name, value)
					if c, err := Load(); err != nil || field.get(c) != value {
						t.Fatalf("got %q, error %v", field.get(c), err)
					}
				})
				t.Run("self-hosted file", func(t *testing.T) {
					prepare(t, false)
					t.Setenv(s.Name, "")
					t.Setenv(s.File(), secretFile(t, value+"\n"))
					if c, err := Load(); err != nil || field.get(c) != value {
						t.Fatalf("got %q, error %v", field.get(c), err)
					}
				})
				t.Run("self-hosted both", func(t *testing.T) {
					prepare(t, false)
					t.Setenv(s.Name, value)
					t.Setenv(s.File(), secretFile(t, value))
					_, err := Load()
					if err == nil || !strings.Contains(err.Error(), s.Name+" or "+s.File()) {
						t.Fatalf("both forms: %v", err)
					}
				})
			}
			t.Run("hosted environment", func(t *testing.T) {
				prepare(t, true)
				t.Setenv(s.File(), "")
				t.Setenv(s.Name, value)
				_, err := Load()
				if err == nil || !strings.Contains(err.Error(), s.Name+" is refused when hosted") || !strings.Contains(err.Error(), s.File()) {
					t.Fatalf("hosted environment form: %v", err)
				}
				if strings.Contains(err.Error(), value) {
					t.Fatal("the error holds the value")
				}
			})
			t.Run("hosted file", func(t *testing.T) {
				prepare(t, true)
				t.Setenv(s.File(), secretFile(t, value+"\n"))
				if c, err := Load(); err != nil || field.get(c) != value {
					t.Fatalf("got %q, error %v", field.get(c), err)
				}
			})
		})
	}
}

// Stripe is used only when hosted, so a self-hosted installation does not read it.
func TestSelfHostedIgnoresStripe(t *testing.T) {
	baseEnvironment(t, false)
	t.Setenv("ASTILBA_STRIPE_SECRET", "sk_test_unused")
	t.Setenv("ASTILBA_STRIPE_WEBHOOK_SECRET_FILE", filepath.Join(t.TempDir(), "missing"))
	if c, err := Load(); err != nil || c.Hosting.SecretKey != "" || c.Hosting.WebhookSecret != "" {
		t.Fatalf("hosting %+v, error %v", c.Hosting, err)
	}
}

func TestHostedRequiresTheDatabaseURLFile(t *testing.T) {
	baseEnvironment(t, true)
	t.Setenv("ASTILBA_DATABASE_URL_FILE", "")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "ASTILBA_DATABASE_URL_FILE") {
		t.Fatalf("no database URL: %v", err)
	}
}

// The PostgreSQL driver reads these by itself, so hosting refuses them too.
func TestHostedRefusesDriverPasswords(t *testing.T) {
	for _, name := range libraryPasswords {
		t.Run(name, func(t *testing.T) {
			baseEnvironment(t, false)
			t.Setenv(name, "driver-password")
			if _, err := Load(); err != nil {
				t.Fatalf("self-hosted: %v", err)
			}
			baseEnvironment(t, true)
			t.Setenv(name, "driver-password")
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), name+" is refused when hosted") || strings.Contains(err.Error(), "driver-password") {
				t.Fatalf("hosted: %v", err)
			}
		})
	}
}

func TestSecretFileIsReadStrictly(t *testing.T) {
	const secret = "s3cret-value"
	for _, tc := range []struct {
		name, contents string
		mode           os.FileMode
		want           string
		refused        string
	}{
		{name: "no newline", contents: secret, mode: 0o600, want: secret},
		{name: "one newline", contents: secret + "\n", mode: 0o600, want: secret},
		{name: "one CRLF", contents: secret + "\r\n", mode: 0o400, want: secret},
		{name: "two newlines", contents: secret + "\n\n", mode: 0o600, refused: "single line"},
		{name: "a carriage return", contents: secret + "\r", mode: 0o600, refused: "single line"},
		{name: "two lines", contents: secret + "\nmore\n", mode: 0o600, refused: "single line"},
		{name: "a NUL", contents: secret + "\x00", mode: 0o600, refused: "single line"},
		{name: "leading and trailing spaces kept", contents: " " + secret + " \n", mode: 0o600, want: " " + secret + " "},
		{name: "empty", contents: "", mode: 0o600, refused: "empty file"},
		{name: "only a newline", contents: "\n", mode: 0o600, refused: "empty file"},
		{name: "group readable", contents: secret, mode: 0o640, refused: "only its owner"},
		{name: "others readable", contents: secret, mode: 0o604, refused: "only its owner"},
		{name: "group writable", contents: secret, mode: 0o620, refused: "only its owner"},
		{name: "too large", contents: strings.Repeat("a", maxSecretFileBytes+1), mode: 0o600, refused: "at most 64 KiB"},
		{name: "largest allowed", contents: strings.Repeat("a", maxSecretFileBytes), mode: 0o600, want: strings.Repeat("a", maxSecretFileBytes)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "smtp-password")
			if err := os.WriteFile(path, []byte(tc.contents), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, tc.mode); err != nil {
				t.Fatal(err)
			}
			clearSecrets(t)
			t.Setenv(secretSMTPPassword.File(), path)
			got, err := readSecret(secretSMTPPassword, false)
			checkRead(t, got, err, tc.want, tc.refused, path, secret)
		})
	}
}

// checkRead also checks that a refusal names the setting, and holds neither the file's contents
// nor its path.
func checkRead(t *testing.T, got string, err error, want, refused, path, secret string) {
	t.Helper()
	if refused == "" {
		if err != nil || got != want {
			t.Fatalf("got %q, error %v", got, err)
		}
		return
	}
	if err == nil || !strings.Contains(err.Error(), refused) {
		t.Fatalf("want refusal %q, got %q, %v", refused, got, err)
	}
	if !strings.Contains(err.Error(), "ASTILBA_SMTP_PASSWORD") || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), path) {
		t.Fatalf("refusal says too much or too little: %v", err)
	}
}

func TestSecretFileMustBeARegularFile(t *testing.T) {
	dir := t.TempDir()
	private := filepath.Join(dir, "private")
	if err := os.WriteFile(private, []byte("linked-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Kubernetes secret volumes are symbolic links to the file, which is what is checked.
	link := filepath.Join(dir, "link")
	if err := os.Symlink(private, link); err != nil {
		t.Fatal(err)
	}
	got, err := readSecretFile(secretSMTPPassword, link)
	checkRead(t, got, err, "linked-secret", "", link, "linked-secret")

	subdirectory := filepath.Join(dir, "directory")
	if err := os.Mkdir(subdirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	got, err = readSecretFile(secretSMTPPassword, subdirectory)
	checkRead(t, got, err, "", "regular file", subdirectory, "linked-secret")

	missing := filepath.Join(dir, "missing")
	got, err = readSecretFile(secretSMTPPassword, missing)
	checkRead(t, got, err, "", "no such file", missing, "linked-secret")

	// A FIFO is refused at once, not waited on.
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := readSecretFile(secretSMTPPassword, fifo)
		done <- err
	}()
	select {
	case err := <-done:
		checkRead(t, "", err, "", "regular file", fifo, "linked-secret")
	case <-time.After(5 * time.Second):
		t.Fatal("reading a FIFO blocked")
	}
}

// A value with a line break is refused in either form, unless the setting is a PEM key.
func TestSecretValuesAreOneLine(t *testing.T) {
	baseEnvironment(t, false)
	t.Setenv("ASTILBA_SMTP_PASSWORD", "password\n")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "ASTILBA_SMTP_PASSWORD must be a single line") || strings.Contains(err.Error(), "password\n") {
		t.Fatalf("environment line break: %v", err)
	}
	baseEnvironment(t, false)
	key := "-----BEGIN TEST-----\nbody\n-----END TEST-----\n"
	t.Setenv("ASTILBA_GITHUB_APP_PRIVATE_KEY_FILE", secretFile(t, key+"\n"))
	if c, err := Load(); err != nil || c.GitHubApp.PrivateKey != key {
		t.Fatalf("PEM key %q, error %v", c.GitHubApp.PrivateKey, err)
	}
}

// docs/hosting.md lists every secret setting with its file form.
func TestSecretSettingsAreDocumented(t *testing.T) {
	page, err := os.ReadFile("../../../../docs/hosting.md")
	if err != nil {
		t.Fatal(err)
	}
	_, section, ok := strings.Cut(string(page), "\n### Secret settings\n")
	if !ok {
		t.Fatal("docs/hosting.md has no secret settings section")
	}
	if end := strings.Index(section, "\n#"); end > 0 {
		section = section[:end]
	}
	for _, s := range secretSettings {
		if !strings.Contains(section, "`"+s.Name+"`") || !strings.Contains(section, "`"+s.File()+"`") {
			t.Errorf("%s and %s are not in the secret settings section", s.Name, s.File())
		}
	}
	for _, name := range libraryPasswords {
		if !strings.Contains(section, "`"+name+"`") {
			t.Errorf("%s is not in the secret settings section", name)
		}
	}
}
