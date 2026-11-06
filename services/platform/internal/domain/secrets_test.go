package domain

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/url"
	"strings"
	"testing"
	"time"
)

const (
	testKeyLine1 = "MIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQC7VJTUt9Us8cKj"
	testKeyLine2 = "MzEfYyjiWA4R4/M2bS1GB4t7NXp98C3SC6dVMvDuictGeurT8jNbvJZHtCSuYEvu"
	testKeyLast  = "NXp98C3SC6dV"
	testKey      = "-----BEGIN PRIVATE KEY-----\n" + testKeyLine1 + "\n" + testKeyLine2 + "\n" + testKeyLast + "\n-----END PRIVATE KEY-----\n"

	testCertificate = "-----BEGIN CERTIFICATE-----\nMIIDdzCCAl+gAwIBAgIEbF2Wqz7kYcYwDQYJKoZIhvcNAQELBQAwbDEQMA4GA1UE\nBhMCVVMxEDAOBgNVBAgTB1Vua25vd24xEDAOBgNVBAcTB1Vua25vd24xEDAOBgNV\n-----END CERTIFICATE-----\n"
	testDigest      = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
)

func TestSecretTextExposes(t *testing.T) {
	jsonEscapedKey := strings.ReplaceAll(testKey, "\n", `\n`)
	wrappedLine := testKeyLine1[:40] + "\n    " + testKeyLine1[40:]
	for _, c := range []struct {
		name, text, value string
		want              bool
	}{
		{"newline against a quoted value", `TOKEN="prod-key-123"`, "prod-key-123\n", true},
		{"space against a file name", "config/prod-key-123.env", "prod-key-123 ", true},
		{"LF against CRLF", "TOKEN=prod-key-123\r\n", "prod-key-123\n", true},
		{"leading whitespace", "TOKEN=prod-key-123", "\t prod-key-123", true},
		{"exact value without whitespace", "TOKEN=prod-key-123", "prod-key-123", true},
		{"absent value", "TOKEN=other", "prod-key-123\n", false},
		{"interior whitespace still counts", "abcd efgh", "abcd  efgh", false},
		{"at the floor", "abcdefghijkl", "abcdefghijkl\n", true},
		{"below the floor keeps ordinary text", "image: postgres:16", "postgres ", false},
		{"below the floor still matches as stored", "postgres \n", "postgres ", true},
		{"short value without whitespace", "compose.prod.yaml", "prod", true},
		{"only whitespace matches as stored", "a b", " ", true},
		{"only whitespace absent", "ab", " ", false},
		{"empty value", "anything", "", false},

		{"key in an indented YAML block", "key: |\n    " + strings.ReplaceAll(strings.TrimSpace(testKey), "\n", "\n    ") + "\n", testKey, true},
		{"key with CRLF line endings", strings.ReplaceAll(testKey, "\n", "\r\n"), testKey, true},
		{"key stored with CRLF against LF", testKey, strings.ReplaceAll(testKey, "\n", "\r\n"), true},
		{"key as a JSON string", `{"private_key": "` + jsonEscapedKey + `"}`, testKey, true},
		{"key stored JSON-escaped against real lines", testKey, jsonEscapedKey, true},
		{"key rewrapped", "-----BEGIN PRIVATE KEY-----\n" + testKeyLine1[:30] + "\n" + testKeyLine1[30:] + testKeyLine2 + testKeyLast + "\n-----END PRIVATE KEY-----", testKey, true},
		{"key body without its armour", "KEY=" + testKeyLine1 + testKeyLine2 + testKeyLast + "\n", testKey, true},
		{"key stored flattened with spaces against real lines", testKey, strings.ReplaceAll(testKey, "\n", " "), true},
		{"key stored beside a certificate", "KEY=" + testKeyLine1 + testKeyLine2 + testKeyLast + "\n", testCertificate + testKey, true},
		{"commented-out key", "# " + testKeyLine1 + "\n# " + testKeyLine2 + "\n# " + testKeyLast + "\n", testKey, true},
		{"key stored wrapped narrowly", "KEY=" + testKeyLine1 + testKeyLine2 + testKeyLast + "\n", "-----BEGIN PRIVATE KEY-----\n" + testKeyLine1[:20] + "\n" + testKeyLine1[20:] + "\n" + testKeyLine2 + "\n" + testKeyLast + "\n-----END PRIVATE KEY-----\n", true},
		{"encrypted key without its armour", "# " + testKeyLine1 + "\n# " + testKeyLine2 + "\n", "-----BEGIN RSA PRIVATE KEY-----\nProc-Type: 4,ENCRYPTED\nDEK-Info: AES-128-CBC,5F2A\n\n" + testKeyLine1 + "\n" + testKeyLine2 + "\n-----END RSA PRIVATE KEY-----\n", true},
		{"PGP key without its armour", testKeyLine1 + testKeyLine2 + "\n", "-----BEGIN PGP PRIVATE KEY BLOCK-----\nVersion: GnuPG v2\n\n" + testKeyLine1 + "\n" + testKeyLine2 + "\n=AbCd\n-----END PGP PRIVATE KEY BLOCK-----\n", true},
		{"wrapped base64 joined in a file", "KEY=" + testKeyLine1 + testKeyLine2 + "\n", testKeyLine1 + "\n" + testKeyLine2 + "\n", true},
		{"single-line key wrapped in a file", "key: >-\n    " + wrappedLine + "\n", testKeyLine1, true},
		{"multi-line value in an indented block", "config: |\n    host: db.internal\n    password: hunter2\n", "host: db.internal\npassword: hunter2\n", true},
		{"multi-line value URL encoded", "url=line-one-value%0Aline-two-value", "line-one-value\nline-two-value", true},
		{"multi-line value as a JSON string", `"{\n  \"user\": \"app\",\n  \"password\": \"hunter2\"\n}"`, "{\n  \"user\": \"app\",\n  \"password\": \"hunter2\"\n}\n", true},
		{"multi-line value with a backslash as a JSON string", `DOMAIN\\svc-account\npassword`, "DOMAIN\\svc-account\npassword", true},
		{"multi-line value with an escaped slash", `https:\/\/db.internal\npassword`, "https://db.internal\npassword", true},
		{"multi-line value continued with a backslash", "first-line\\\n  second-line", "first-line\nsecond-line", true},
		{"multi-line value with an escaped tab", `col-one\tcol-two\nsecond-line`, "col-one\tcol-two\nsecond-line", true},
		{"single-line value with an escaped tab", `col-one\tcol-two-three`, "col-one\tcol-two-three", true},
		{"single-line value with an escaped quote", `pa\"ss-word-123`, `pa"ss-word-123`, true},

		{"another key's armour", "-----BEGIN PRIVATE KEY-----\nb3RoZXI=\n-----END PRIVATE KEY-----\n", testKey, false},
		{"a config line shared with a stored file", "environment:\n  NODE_ENV=production\n", "NODE_ENV=production\nAPI_KEY=abcdef\n", false},
		{"a long ordinary line shared with a stored file", `"token_uri": "https://oauth2.example.com/token/endpoint",`, "{\n" + `"token_uri": "https://oauth2.example.com/token/endpoint",` + "\n" + `"key": "x"` + "\n}\n", false},
		{"a key's short last line", "# " + testKeyLast + "\n", testKey, false},
		{"one line of a key's body", "# " + testKeyLine2 + "\n", testKey, false},
		{"another key sharing a first line", testKeyLine1 + "\n", testKey, false},
		{"a short armoured body is not base64 of any length", "abcdefghijklmnop\n", "-----BEGIN PRIVATE KEY-----\nabcdefgh\nijklmnop\n-----END PRIVATE KEY-----\n", false},
		{"a long list of ordinary words", "environments: postgres redis nginx prometheus grafana\n", "postgres\nredis\nnginx\nprometheus\ngrafana\n", false},
		{"the public certificate stored beside a key", testCertificate, testCertificate + testKey, false},
		{"a path line in a multi-line value", "- db:/var/lib/postgresql/data/pgdata/main\n", "user\n/var/lib/postgresql/data/pgdata/main\npass", false},
		{"a digest line in a multi-line value", "image: postgres@sha256:" + testDigest + "\n", "user\n" + testDigest + "\npass", false},
		{"lines of a value as ordinary words", "# admin password is required", "admin\npassword", false},
		{"lines of a value run together", "# adminpassword is required", "admin\npassword", false},
		{"a backslash-n in a single-line value is not a line break", "password-123", `pass\nword-123`, false},
		{"multi-line value below the floor", "abcdef", "abc\ndef", false},
		{"lines of a value in another order", "second-line-of-it first-line-of-it", "first-line-of-it\nsecond-line-of-it", false},
		{"short single-line value split by a line break", "password\nreset", "passwordreset", false},

		{"URL encoded in a connection string", "postgres://app:p%40ss-word-123@db/app", "p@ss-word-123", true},
		{"URL encoded in a file name", "config/p%40ss-word-123.env", "p@ss-word-123", true},
		{"URL encoded with lower-case hex", "url=a%2fb%2fc-d%2fe-123", "a/b/c-d/e-123", true},
		{"URL encoded leaving some reserved characters", "url=p%40ss!word-123", "p@ss!word-123", true},
		{"every byte URL encoded", "%70%40%73%73", "p@ss", true},
		{"space as plus", "q=my+pass+phrase", "my pass phrase", true},
		{"space as %20", "q=my%20pass%20phrase", "my pass phrase", true},
		{"plus kept literal among encoding", "q=a+b%20c-d-e-f", "a+b c-d-e-f", true},
		{"encoded plus with a plus for a space", "q=a%2Bb+c-d-e-f", "a+b c-d-e-f", true},
		{"stored whitespace around an encoded value", "p%40ss-word-123", "p@ss-word-123\n", true},
		{"short encoded value", "postgres://app:p%40ss@db/app", "p@ss", true},
		{"other encoded text", "url=p%40ss-word-456", "p@ss-word-123", false},
		{"after a stray percent sign", "rate=100%ab%40c-word-12345;", "ab@c-word-12345", true},
		{"short value with whitespace is exact, not a query string", "TAGS=prod+staging", "prod ", false},
		{"short value with whitespace is exact, not encoded", "x=prod%20x", "prod ", false},
		{"only whitespace is exact in a file name", "c++/app.yaml", " ", false},
		{"percent sign without hex", "100%zz p@ss-word-12", "p@ss-word-123", false},
		{"percent sign at the end", "a%", "a%", true},
		{"truncated escape at the end", "p@ss%4", "p@ss@", false},
	} {
		if got := NewSecretText(c.text).Exposes(c.value); got != c.want {
			t.Errorf("%s: %q exposes %q = %v", c.name, c.text, c.value, got)
		}
	}
}

// The check and redaction read encoded text the same way, which must recognise what the
// standard encoders produce for any byte a value can hold.
func TestSecretFormsAreFoundForEveryEncoder(t *testing.T) {
	value := "a b+c/d:e@f?g&h=i%j!k'l(m)n*o;p,q$r~s-t_u.v\"w\\xé"
	for name, encoded := range map[string]string{
		"query":    url.QueryEscape(value),
		"path":     url.PathEscape(value),
		"userinfo": strings.TrimPrefix(url.UserPassword("", value).String(), ":"),
	} {
		if !NewSecretText("x=" + encoded + ";").Exposes(value) {
			t.Errorf("%s form %q was not found", name, encoded)
		}
	}
}

// A private key's body holds its public key's bytes, so whether a line of it appears in the
// public key or certificate depends on alignment. Those files must never be blocked, while
// the private key itself is found.
func TestSecretTextFindsKeysButNotTheirPublicFiles(t *testing.T) {
	encode := func(kind string, der []byte, err error) string {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return string(pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: der}))
	}
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	certificates := func(public, private any) []string {
		var out []string
		for length := 1; length <= 9; length++ {
			template := x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: strings.Repeat("a", length)}, NotAfter: time.Now().Add(time.Hour)}
			der, err := x509.CreateCertificate(rand.Reader, &template, &template, public, private)
			out = append(out, encode("CERTIFICATE", der, err))
		}
		return out
	}
	rsaSPKI, rsaSPKIErr := x509.MarshalPKIXPublicKey(&rsaKey.PublicKey)
	ecSPKI, ecSPKIErr := x509.MarshalPKIXPublicKey(&ecKey.PublicKey)
	rsaPKCS8, rsaPKCS8Err := x509.MarshalPKCS8PrivateKey(rsaKey)
	ecSEC1, ecSEC1Err := x509.MarshalECPrivateKey(ecKey)
	ecPKCS8, ecPKCS8Err := x509.MarshalPKCS8PrivateKey(ecKey)
	rsaPublic := append(certificates(&rsaKey.PublicKey, rsaKey), encode("PUBLIC KEY", rsaSPKI, rsaSPKIErr), encode("RSA PUBLIC KEY", x509.MarshalPKCS1PublicKey(&rsaKey.PublicKey), nil))
	ecPublic := append(certificates(&ecKey.PublicKey, ecKey), encode("PUBLIC KEY", ecSPKI, ecSPKIErr))
	for name, c := range map[string]struct {
		private string
		public  []string
	}{
		"RSA PKCS#1": {encode("RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(rsaKey), nil), rsaPublic},
		"RSA PKCS#8": {encode("PRIVATE KEY", rsaPKCS8, rsaPKCS8Err), rsaPublic},
		"EC SEC1":    {encode("EC PRIVATE KEY", ecSEC1, ecSEC1Err), ecPublic},
		"EC PKCS#8":  {encode("PRIVATE KEY", ecPKCS8, ecPKCS8Err), ecPublic},
	} {
		for _, public := range c.public {
			if NewSecretText(public).Exposes(c.private) {
				t.Errorf("%s blocked its own public file:\n%s", name, public)
			}
			if NewSecretText(public).Exposes(public + c.private) {
				t.Errorf("%s stored beside a public file blocked that file:\n%s", name, public)
			}
		}
		lines := strings.Split(strings.TrimSpace(c.private), "\n")
		body := strings.Join(lines[1:len(lines)-1], "")
		for form, text := range map[string]string{
			"indented":       "key: |\n  " + strings.ReplaceAll(c.private, "\n", "\n  "),
			"without armour": "KEY=" + body,
			"as stored":      c.private,
		} {
			if !NewSecretText(text).Exposes(c.public[0] + c.private) {
				t.Errorf("%s stored beside a certificate was not found %s", name, form)
			}
		}
	}
}
