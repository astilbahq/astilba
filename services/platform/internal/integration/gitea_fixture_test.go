package integration

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json/v2"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

// giteaImage is the Gitea the Git provider experiment runs, so both check one release.
const giteaImage = "gitea/gitea:28.2.0@sha256:30777c91280d973fa02be48b1ade735bd6859e8f6f7af3b4444d519437852504"

// realGitea is a disposable Gitea with one private repository, owner/app. Its webhooks reach
// a receiver beside it on their own Docker network, which keeps what Gitea sent for the test
// to deliver, so nothing on the host needs to be reachable from a container.
type realGitea struct {
	t     *testing.T
	label string
	name  string
	base  string
	// admin sets the fixture up. write is the connection's API credential, with only the
	// repository access the documentation asks for, and read can only clone.
	admin, write, read string
	receiver           string
	delivered          map[string]bool
}

// giteaDelivery is one webhook delivery exactly as Gitea sent it.
type giteaDelivery struct {
	Headers http.Header
	Body    []byte
}

func dockerRun(t *testing.T, args ...string) string {
	t.Helper()
	return strings.TrimSpace(string(dockerOutput(t, args...)))
}

func dockerOutput(t *testing.T, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", append([]string{"--host", os.Getenv("PLAT_APP_DOCKER_HOST")}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("docker %s: %v: %s", args[0], err, stderr.String())
	}
	return out
}

// receiverScript keeps each delivery's headers, which CGI passes as HTTP_ variables, and its
// body, then answers as a receiver that accepted it.
const receiverScript = `mkdir -p /www/cgi-bin /deliveries && cat > /www/cgi-bin/hook <<'EOF'
#!/bin/sh
name=/deliveries/$(date +%s)-$$
head -c "$CONTENT_LENGTH" > "$name.body"
env | grep '^HTTP_X_GITEA_' > "$name.headers"
mv "$name.body" "$name.done"
printf 'Status: 204 No Content\r\n\r\n'
EOF
chmod +x /www/cgi-bin/hook && exec httpd -f -p 8080 -h /www`

func startGitea(t *testing.T) *realGitea {
	t.Helper()
	if testing.Short() {
		t.Skip("a disposable Gitea requires verify")
	}
	g := &realGitea{t: t, label: uuid.NewString(), delivered: map[string]bool{}}
	name := "astilba-gitea-" + g.label
	g.receiver = name + "-hooks"
	// Cleanup is registered first, because a failed start can still leave what it created.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		docker := func(args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, "docker", append([]string{"--host", os.Getenv("PLAT_APP_DOCKER_HOST")}, args...)...).Output()
		}
		listed, err := docker("ps", "-aq", "--no-trunc", "--filter", "label=com.astilba.gitea-test="+g.label)
		if err != nil {
			t.Error("cannot list the Gitea fixture's containers")
		}
		for _, id := range strings.Fields(string(listed)) {
			if _, err = docker("rm", "--force", "--volumes", id); err != nil {
				t.Error("cannot remove a Gitea fixture container")
			}
		}
		if out, _ := docker("network", "ls", "-q", "--filter", "label=com.astilba.gitea-test="+g.label); len(bytes.TrimSpace(out)) > 0 {
			if _, err = docker("network", "rm", name); err != nil {
				t.Error("cannot remove the Gitea fixture's network")
			}
		}
	})
	label := "com.astilba.gitea-test=" + g.label
	dockerRun(t, "network", "create", "--label", label, name)
	dockerRun(t, "run", "--detach", "--name", g.receiver, "--label", label, "--network", name, "--network-alias", "receiver", pipelineTestImage, "sh", "-c", receiverScript)
	env := []string{"GITEA__security__INSTALL_LOCK=true", "GITEA__database__DB_TYPE=sqlite3", "GITEA__server__DISABLE_SSH=true", "GITEA__service__DISABLE_REGISTRATION=true", "GITEA__mailer__ENABLED=false",
		// Gitea refuses to deliver to a private address unless one is allowed by name.
		"GITEA__webhook__ALLOWED_HOST_LIST=private"}
	// A port free here, rather than one Docker picks: Colima forwards each profile's ports to
	// the same numbers on the host, so two profiles' choices can collide.
	// Another process can take the port before Docker binds it, so a refused start tries another.
	for attempt := 1; ; attempt++ {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := listener.Addr().(*net.TCPAddr).Port
		listener.Close()
		g.base = "http://127.0.0.1:" + strconv.Itoa(port)
		args := []string{"run", "--detach", "--name", name, "--label", label, "--network", name, "--publish", g.base[len("http://"):] + ":3000"}
		for _, e := range env {
			args = append(args, "--env", e)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
		out, err := exec.CommandContext(ctx, "docker", append([]string{"--host", os.Getenv("PLAT_APP_DOCKER_HOST")}, append(args, giteaImage)...)...).CombinedOutput()
		cancel()
		if err == nil {
			break
		}
		if attempt == 3 || !bytes.Contains(out, []byte("already")) {
			t.Fatalf("docker run: %v: %s", err, out)
		}
		// A container Docker created but could not start keeps the name; it may not exist.
		_ = exec.CommandContext(t.Context(), "docker", "--host", os.Getenv("PLAT_APP_DOCKER_HOST"), "rm", "--force", name).Run()
	}
	client := &http.Client{Timeout: 2 * time.Second}
	waitFor(t, "Gitea", 2*time.Minute, func() bool {
		res, err := client.Get(g.base + "/api/healthz")
		if err == nil {
			res.Body.Close()
		}
		return err == nil && res.StatusCode == 200
	})
	g.name = name
	g.admin = g.user("owner", true)
	g.write = g.token("owner", "write:repository")
	g.read = g.token("owner", "read:repository")
	g.call("POST", "/user/repos", map[string]any{"name": "app", "private": true, "default_branch": "main"}, 201, nil)
	return g
}

// user creates an account and returns a token that can also create repositories and organizations.
func (g *realGitea) user(name string, admin bool) string {
	g.t.Helper()
	password, _ := cryptobox.Token()
	args := []string{"exec", "--user", "git", g.name, "gitea", "admin", "user", "create", "--username", name, "--password", password, "--email", name + "@example.test", "--must-change-password=false"}
	if admin {
		args = append(args, "--admin")
	}
	dockerRun(g.t, args...)
	return g.token(name, "write:repository,write:user,write:organization")
}

func (g *realGitea) token(user, scopes string) string {
	g.t.Helper()
	return dockerRun(g.t, "exec", "--user", "git", g.name, "gitea", "admin", "user", "generate-access-token", "--username", user, "--token-name", strings.ReplaceAll(scopes, ":", "-"), "--scopes", scopes, "--raw")
}

// call asks Gitea's API as the repository's owner.
func (g *realGitea) call(method, path string, body any, status int, out any) []byte {
	g.t.Helper()
	return g.callAs(g.admin, method, path, body, status, out)
}

func (g *realGitea) callAs(token, method, path string, body any, status int, out any) []byte {
	g.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, _ := stablejson.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	r, _ := http.NewRequestWithContext(g.t.Context(), method, g.base+"/api/v1"+path, reader)
	r.Header.Set("Authorization", "token "+token)
	r.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(r)
	if err != nil {
		g.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != status {
		g.t.Fatalf("Gitea %s %s: %d %s", method, path, res.StatusCode, raw)
	}
	if out != nil {
		if err = json.Unmarshal(raw, out); err != nil {
			g.t.Fatal(err)
		}
	}
	return raw
}

// commit writes files to main in one commit and returns it.
func (g *realGitea) commit(message string, files []composeproject.File, create bool) string {
	g.t.Helper()
	return g.commitTo(g.admin, "owner/app", "main", "", message, files, create)
}

// commitTo writes files in one commit to a repository's branch, starting it from another
// branch when from is set.
func (g *realGitea) commitTo(token, repository, branch, from, message string, files []composeproject.File, create bool) string {
	g.t.Helper()
	operation := "update"
	if create {
		operation = "create"
	}
	changes := []map[string]string{}
	for _, f := range files {
		changes = append(changes, map[string]string{"operation": operation, "path": f.Path, "content": base64.StdEncoding.EncodeToString([]byte(f.Content))})
	}
	var out struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	body := map[string]any{"branch": branch, "message": message, "files": changes}
	if from != "" {
		body["branch"], body["new_branch"] = from, branch
	}
	g.callAs(token, "POST", "/repos/"+repository+"/contents", body, 201, &out)
	return out.Commit.SHA
}

// files returns what a runner's clone holds at a commit: the files' bytes from Gitea itself,
// fetched with the read-only clone credential.
func (g *realGitea) files(commit string, paths []string) []composeproject.File {
	g.t.Helper()
	out := []composeproject.File{}
	for _, path := range paths {
		r, _ := http.NewRequestWithContext(g.t.Context(), "GET", g.base+"/api/v1/repos/owner/app/raw/"+path+"?ref="+commit, nil)
		r.Header.Set("Authorization", "token "+g.read)
		res, err := http.DefaultClient.Do(r)
		if err != nil {
			g.t.Fatal(err)
		}
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != 200 {
			g.t.Fatalf("Gitea has no %s at %s: %d", path, commit, res.StatusCode)
		}
		out = append(out, composeproject.File{Path: path, Content: string(raw)})
	}
	return out
}

// hook subscribes the receiver to the repository's pushes, signed with secret.
func (g *realGitea) hook(secret string) {
	g.t.Helper()
	g.call("POST", "/repos/owner/app/hooks", map[string]any{"type": "gitea", "active": true, "events": []string{"push"}, "config": map[string]string{"url": "http://receiver:8080/cgi-bin/hook", "content_type": "json", "secret": secret}}, 201, nil)
}

// nextDelivery waits for a delivery the test has not yet taken.
func (g *realGitea) nextDelivery() giteaDelivery {
	g.t.Helper()
	var name string
	// Each look starts a process in the receiver, so it looks a few times a second.
	for deadline := time.Now().Add(30 * time.Second); name == ""; time.Sleep(250 * time.Millisecond) {
		if time.Now().After(deadline) {
			g.t.Fatal("no Gitea webhook delivery arrived")
		}
		for _, file := range strings.Fields(dockerRun(g.t, "exec", g.receiver, "ls", "/deliveries")) {
			if base, done := strings.CutSuffix(file, ".done"); done && !g.delivered[base] {
				name = base
				break
			}
		}
	}
	g.delivered[name] = true
	d := giteaDelivery{Headers: http.Header{}, Body: dockerOutput(g.t, "exec", g.receiver, "cat", "/deliveries/"+name+".done")}
	for _, line := range strings.Split(dockerRun(g.t, "exec", g.receiver, "cat", "/deliveries/"+name+".headers"), "\n") {
		key, value, _ := strings.Cut(line, "=")
		// HTTP_X_GITEA_EVENT came from X-Gitea-Event.
		d.Headers.Set(strings.ReplaceAll(strings.TrimPrefix(key, "HTTP_"), "_", "-"), value)
	}
	return d
}

// deliver sends a delivery on to the management server's webhook for a connection.
func (d giteaDelivery) deliver(t *testing.T, f *fixture, connection uuid.UUID) int {
	t.Helper()
	r, _ := http.NewRequest("POST", f.server.URL+"/hooks/git/"+connection.String(), bytes.NewReader(d.Body))
	r.Header = d.Headers.Clone()
	r.Header.Set("Content-Type", "application/json")
	res, err := f.server.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}
