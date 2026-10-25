package integration

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	dbmigrate "github.com/astilbahq/astilba/services/platform/db"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/auth"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/httpapi"
	"github.com/astilbahq/astilba/services/platform/internal/mailer"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const postgresImage = "postgres:18.4-alpine3.23@sha256:996d0920e4ff9df1fc19dacb904492f3c1ec0ec1cc338f0ad7123be7731c5f5e"

var databaseURL string

func TestMain(m *testing.M) {
	flag.Parse()
	if os.Getenv("ASTILBA_P2_HELPER") == "1" {
		os.Exit(m.Run())
	}
	if testing.Short() {
		os.Exit(m.Run())
	}
	host := os.Getenv("PLAT_APP_DOCKER_HOST")
	if !strings.HasPrefix(host, "unix:///") {
		fmt.Fprintln(os.Stderr, "PLAT_APP_DOCKER_HOST must explicitly select a disposable local Unix socket")
		os.Exit(1)
	}
	run := func(args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		return exec.CommandContext(ctx, "docker", append([]string{"--host", host}, args...)...).CombinedOutput()
	}
	owner := uuid.NewString()
	password, _ := cryptobox.Token()
	name := "astilba-p1-test-" + owner
	out, e := run("run", "--detach", "--name", name, "--label", "com.astilba.p1-test="+owner, "--publish", "127.0.0.1::5432", "--env", "POSTGRES_PASSWORD="+password, postgresImage)
	if e != nil {
		fmt.Fprintln(os.Stderr, "cannot start disposable test database", string(out))
		os.Exit(1)
	}
	cleanup := func() {
		label, e := run("inspect", "--format", `{{ index .Config.Labels "com.astilba.p1-test" }}`, name)
		if e == nil && strings.TrimSpace(string(label)) == owner {
			_, _ = run("rm", "--force", "--volumes", name)
		}
	}
	port, e := run("port", name, "5432/tcp")
	if e != nil {
		cleanup()
		os.Exit(1)
	}
	databaseURL = "postgres://postgres:" + password + "@" + strings.TrimSpace(string(port)) + "/postgres?sslmode=disable"
	pool, e := pgxpool.New(context.Background(), databaseURL)
	if e != nil {
		cleanup()
		os.Exit(1)
	}
	ready := false
	for i := 0; i < 100; i++ {
		if pool.Ping(context.Background()) == nil {
			ready = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	pool.Close()
	if !ready {
		cleanup()
		fmt.Fprintln(os.Stderr, "test PostgreSQL did not become ready")
		os.Exit(1)
	}
	code := m.Run()
	cleanup()
	os.Exit(code)
}

type fixture struct {
	st        *store.Store
	auth      *auth.Service
	server    *httptest.Server
	mail      chan string
	password  string
	owner     uuid.UUID
	execution *execution.Service
	apiServer *httpapi.Server
}

func setup(t *testing.T) *fixture { return setupBootstrap(t, true) }
func setupBootstrap(t *testing.T, bootstrap bool) *fixture {
	t.Helper()
	if testing.Short() {
		t.Skip("disposable PostgreSQL integration requires verify")
	}
	ctx := context.Background()
	admin, e := pgxpool.New(ctx, databaseURL)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(admin.Close)
	name := "test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, e = admin.Exec(ctx, "CREATE DATABASE "+name); e != nil {
		t.Fatal(e)
	}
	u, _ := url.Parse(databaseURL)
	u.Path = "/" + name
	dsn := u.String()
	if e = dbmigrate.Up(ctx, dsn); e != nil {
		t.Fatal(e)
	}
	if e = dbmigrate.Check(ctx, dsn); e != nil {
		t.Fatal(e)
	}
	pool, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(pool.Close)
	keys, _ := cryptobox.New("test")
	st := store.New(pool, keys, false)
	passwords, e := auth.NewPasswords()
	if e != nil {
		t.Fatal(e)
	}
	password := "correct horse battery staple"
	hash, e := passwords.Hash(ctx, password)
	if e != nil {
		t.Fatal(e)
	}
	var owner uuid.UUID
	if bootstrap {
		owner, e = st.Bootstrap(ctx, "owner@example.test", "Owner", hash, "Test workspace")
		if e != nil {
			t.Fatal(e)
		}
	}

	mail, address := smtpFixture(t)
	sender, e := mailer.New(mailer.Config{Address: address, From: "Astilba <accounts@example.test>", Mode: "plain", Development: true})
	if e != nil {
		t.Fatal(e)
	}
	service := &auth.Service{Store: st, Passwords: passwords, Mail: sender, Now: time.Now}
	apiServer, cleanup := httpapi.New(st, service, "http://127.0.0.1", true, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(cleanup)
	handler, e := apiServer.Handler(http.NotFoundHandler())
	if e != nil {
		t.Fatal(e)
	}
	workerCtx, workerCancel := context.WithCancel(context.Background())
	if e = apiServer.Execution.Start(workerCtx); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		workerCancel()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if e := apiServer.Execution.Stop(ctx); e != nil {
			t.Error(e)
		}
	})
	server := httptest.NewServer(handler)
	apiServer.Origin = server.URL
	t.Cleanup(server.Close)
	return &fixture{st, service, server, mail, password, owner, apiServer.Execution, apiServer}
}
func smtpFixture(t *testing.T) (chan string, string) {
	t.Helper()
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = listener.Close() })
	messages := make(chan string, 10)
	go func() {
		for {
			conn, e := listener.Accept()
			if e != nil {
				return
			}
			go func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
				rw := bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))
				reply := func(s string) { _, _ = rw.WriteString(s + "\r\n"); _ = rw.Flush() }
				reply("220 test SMTP")
				for {
					line, e := rw.ReadString('\n')
					if e != nil {
						return
					}
					switch {
					case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
						reply("250 test")
					case strings.HasPrefix(line, "MAIL"), strings.HasPrefix(line, "RCPT"):
						reply("250 OK")
					case strings.HasPrefix(line, "DATA"):
						reply("354 send message")
						body, e := textproto.NewReader(rw.Reader).ReadDotBytes()
						if e != nil {
							return
						}
						messages <- string(body)
						reply("250 queued")
					case strings.HasPrefix(line, "QUIT"):
						reply("221 bye")
						return
					default:
						reply("500 invalid")
					}
				}
			}()
		}
	}()
	return messages, listener.Addr().String()
}

type browser struct {
	t      *testing.T
	f      *fixture
	client *http.Client
	csrf   string
}

func (f *fixture) browser(t *testing.T) *browser {
	jar, _ := cookiejar.New(nil)
	b := &browser{t, f, &http.Client{Jar: jar}, ""}
	var session api.Session
	b.call("GET", "/api/v1/session", nil, 200, &session)
	b.csrf = session.CsrfToken
	return b
}
func (b *browser) call(method, path string, body any, status int, out any) []byte {
	b.t.Helper()
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	request, e := http.NewRequest(method, b.f.server.URL+path, bytes.NewReader(raw))
	if e != nil {
		b.t.Fatal(e)
	}
	request.Header.Set("Origin", b.f.server.URL)
	request.Header.Set("X-CSRF-Token", b.csrf)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, e := b.client.Do(request)
	if e != nil {
		b.t.Fatal(e)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	if response.StatusCode != status {
		b.t.Fatalf("%s %s: status %d want %d: %s", method, path, response.StatusCode, status, data)
	}
	if out != nil {
		if e = json.Unmarshal(data, out); e != nil {
			b.t.Fatal(e)
		}
	}
	return data
}
func (b *browser) login(email, password string) api.Session {
	var v api.Session
	b.call("POST", "/api/v1/auth/login", map[string]string{"email": email, "password": password}, 200, &v)
	b.csrf = v.CsrfToken
	return v
}
func (b *browser) session() api.Session {
	var v api.Session
	b.call("GET", "/api/v1/session", nil, 200, &v)
	b.csrf = v.CsrfToken
	return v
}
func tokenFromMail(t *testing.T, messages chan string) string {
	t.Helper()
	select {
	case m := <-messages:
		fields := strings.Fields(m)
		return fields[len(fields)-1]
	case <-time.After(2 * time.Second):
		t.Fatal("email missing")
		return ""
	}
}

func tokenFromLinkMail(t *testing.T, f *fixture, path string) string {
	t.Helper()
	link, err := url.Parse(tokenFromMail(t, f.mail))
	if err != nil || link.Scheme+"://"+link.Host != f.apiServer.Origin || link.Path != path || link.Query().Get("token") == "" {
		t.Fatal("email must contain a link to the configured origin and expected page with its token")
	}
	return link.Query().Get("token")
}
