// SPDX-License-Identifier: AGPL-3.0-only

package gitcompose

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const GitLabImage = "gitlab/gitlab-ce:19.5.1-ce.0@sha256:bb79dc86267c557fd11e7feb0cd9dd506dd02e991ecdd6cae11ca7d7e5d36fc3"
const GiteaImage = "gitea/gitea:28.2.0@sha256:30777c91280d973fa02be48b1ade735bd6859e8f6f7af3b4444d519437852504"

func fixtureID() string {
	var data [8]byte
	if _, err := rand.Read(data[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(data[:])
}

func docker(t *testing.T, env map[string]string, args ...string) string {
	t.Helper()
	host := os.Getenv("PLAT7_DOCKER_HOST")
	if !strings.HasPrefix(host, "unix://") {
		t.Fatal("PLAT7_DOCKER_HOST must select a disposable local Docker socket")
	}
	cmd := exec.CommandContext(t.Context(), "docker", append([]string{"--host", host}, args...)...)
	cmd.Env = os.Environ()
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	var output boundedOutput
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		t.Fatalf("Docker %s failed: %v", args[0], err)
	}
	return strings.TrimSpace(output.String())
}

func waitFor(t *testing.T, timeout time.Duration, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		select {
		case <-t.Context().Done():
			t.Fatal("fixture cancelled")
		case <-time.After(time.Second):
		}
	}
	t.Fatal("fixture did not become ready within its deadline")
}

func localProvider(t *testing.T, kind string) *Provider {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	name := "astilba-plat7-" + kind + "-" + fixtureID()
	args := []string{"run", "--detach", "--name", name, "--label", "com.astilba.fixture=plat7", "--memory", "6g"}
	env := map[string]string{}
	if kind == "gitlab" {
		config := fmt.Sprintf("external_url '%s'; nginx['listen_port']=80; puma['worker_processes']=0; sidekiq['concurrency']=4; prometheus_monitoring['enable']=false; gitlab_kas['enable']=false; registry['enable']=false; gitlab_rails['smtp_enable']=false;", base)
		env["GITLAB_OMNIBUS_CONFIG"] = config
		env["GITLAB_ROOT_PASSWORD"] = fixtureID() + fixtureID()
		args = append(args, "--shm-size", "256m", "--publish", fmt.Sprintf("127.0.0.1:%d:80", port), "--env", "GITLAB_OMNIBUS_CONFIG", "--env", "GITLAB_ROOT_PASSWORD", GitLabImage)
	} else {
		env = map[string]string{"GITEA__security__INSTALL_LOCK": "true", "GITEA__database__DB_TYPE": "sqlite3", "GITEA__server__ROOT_URL": base + "/", "GITEA__server__DISABLE_SSH": "true", "GITEA__service__DISABLE_REGISTRATION": "true", "GITEA__mailer__ENABLED": "false"}
		for key := range env {
			args = append(args, "--env", key)
		}
		args = append(args, "--publish", fmt.Sprintf("127.0.0.1:%d:3000", port), GiteaImage)
	}
	id := docker(t, env, args...)
	if len(id) != 64 {
		t.Fatal("Docker did not return a container identity")
	}
	t.Cleanup(func() {
		// Cleanup uses a fresh context because the test may have been cancelled.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "docker", "--host", os.Getenv("PLAT7_DOCKER_HOST"), "inspect", "--format", `{{index .Config.Labels "com.astilba.fixture"}}`, id)
		label, err := cmd.Output()
		if err != nil || strings.TrimSpace(string(label)) != "plat7" {
			t.Error("cannot establish container ownership for cleanup")
			return
		}
		if err := exec.CommandContext(ctx, "docker", "--host", os.Getenv("PLAT7_DOCKER_HOST"), "rm", "--force", "--volumes", id).Run(); err != nil {
			t.Error("fixture container cleanup failed")
			return
		}
		remaining, err := exec.CommandContext(ctx, "docker", "--host", os.Getenv("PLAT7_DOCKER_HOST"), "ps", "-aq", "--filter", "id="+id).Output()
		if err != nil || len(bytes.TrimSpace(remaining)) != 0 {
			t.Error("fixture container deletion was not confirmed")
		}
	})
	client := &http.Client{Timeout: 3 * time.Second}
	health := "/api/healthz"
	if kind == "gitlab" {
		health = "/users/sign_in"
	}
	t.Logf("waiting for pinned %s fixture", kind)
	waitFor(t, 10*time.Minute, func() bool {
		resp, err := client.Get(base + health)
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == 200
	})
	var token, login string
	if kind == "gitlab" {
		login = "oauth2"
		output := docker(t, nil, "exec", id, "gitlab-rails", "runner", `ApplicationSetting.current.update!(signup_enabled: false); u=User.find_by_username('root'); t=u.personal_access_tokens.create!(name:'plat7-bootstrap',scopes:['api','write_repository'],expires_at:Date.today+1); puts t.token`)
		for _, line := range strings.Split(output, "\n") {
			if strings.HasPrefix(line, "glpat-") {
				token = strings.TrimSpace(line)
			}
		}
	} else {
		login = "fixture"
		docker(t, nil, "exec", "--user", "git", id, "gitea", "--config", "/data/gitea/conf/app.ini", "admin", "user", "create", "--username", login, "--random-password", "--email", "fixture@example.invalid", "--admin", "--must-change-password=false")
		token = docker(t, nil, "exec", "--user", "git", id, "gitea", "--config", "/data/gitea/conf/app.ini", "admin", "user", "generate-access-token", "--username", login, "--token-name", "plat7", "--scopes", "write:repository,write:user", "--raw")
	}
	if len(token) < 20 {
		t.Fatal("fixture token was not created")
	}
	p, err := NewProvider(kind, base, login, token)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func createFixtureRepository(t *testing.T, p *Provider, owner string) (string, int64) {
	t.Helper()
	name := "plat7-fixture-" + fixtureID()
	path := "/api/v1/user/repos"
	body := map[string]any{"name": name, "private": true, "auto_init": false, "default_branch": "main", "description": "Disposable Astilba PLAT-7 fixture " + name}
	if p.kind == "gitlab" {
		path = "/api/v4/projects"
		body = map[string]any{"name": name, "path": name, "visibility": "private", "initialize_with_readme": false}
	}
	if p.kind == "github" {
		path = "/orgs/" + owner + "/repos"
	}
	var created struct {
		ID       int64
		FullName string `json:"full_name"`
		Path     string `json:"path_with_namespace"`
	}
	if err := p.request(t.Context(), "POST", path, body, &created); err != nil {
		t.Fatalf("create fixture repository: %v", err)
	}
	if created.ID <= 0 {
		t.Fatal("repository creation omitted identity")
	}
	repo := created.FullName
	if p.kind == "gitlab" {
		repo = created.Path
	}
	if !validRepository(repo) || !strings.HasSuffix(repo, "/"+name) {
		t.Fatal("unexpected created repository")
	}
	// Local repositories live entirely in the owned disposable container.
	// Its cleanup removes their state together, including delayed GitLab deletions.
	if p.kind == "github" {
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			id, err := p.RepositoryID(ctx, repo)
			if err != nil || id != created.ID {
				t.Error("cannot establish repository ownership for cleanup")
				return
			}
			if err := p.request(ctx, "DELETE", p.apiPath(repo), nil, nil); err != nil {
				t.Error("fixture repository cleanup failed")
				return
			}
			for ctx.Err() == nil {
				_, err := p.RepositoryID(ctx, repo)
				var response *apiError
				if errors.As(err, &response) && response.status == http.StatusNotFound {
					return
				}
				time.Sleep(time.Second)
			}
			t.Error("fixture repository deletion was not confirmed")
		})
	}
	return repo, created.ID
}

func seedRepository(t *testing.T, p *Provider, repo string) {
	t.Helper()
	r := newGitRepository(p, t.TempDir(), repo, "main")
	if _, err := r.git(t.Context(), nil, "init", "--bare", "--quiet"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.git(t.Context(), nil, "remote", "add", "origin", p.gitURL(repo)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.git(t.Context(), nil, "read-tree", "--empty"); err != nil {
		t.Fatal(err)
	}
	for path, data := range map[string]string{"compose.yaml": baseSource, "compose.override.yaml": overrideSource, "README.md": "Unrelated source must remain byte-for-byte identical.\n"} {
		blob, err := r.git(t.Context(), []byte(data), "hash-object", "-w", "--stdin")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.git(t.Context(), nil, "update-index", "--add", "--cacheinfo", "100644", strings.TrimSpace(string(blob)), path); err != nil {
			t.Fatal(err)
		}
	}
	tree, err := r.git(t.Context(), nil, "write-tree")
	if err != nil {
		t.Fatal(err)
	}
	commit, err := r.git(t.Context(), []byte("Seed disposable fixture\n"), "commit-tree", strings.TrimSpace(string(tree)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.git(t.Context(), nil, "push", "--quiet", "--force-with-lease=refs/heads/main:", "origin", strings.TrimSpace(string(commit))+":refs/heads/main"); err != nil {
		t.Fatal(err)
	}
}

func mergeFixtureReview(t *testing.T, p *Provider, repo string, publication Publication) {
	t.Helper()
	path := fmt.Sprintf("%s/pulls/%d", p.apiPath(repo), publication.Review.Number)
	if p.kind == "gitlab" {
		path = fmt.Sprintf("%s/merge_requests/%d", p.apiPath(repo), publication.Review.Number)
	}
	// Mergeability is calculated asynchronously by each provider.
	waitFor(t, time.Minute, func() bool {
		var result struct {
			Mergeable bool
			Status    string `json:"detailed_merge_status"`
		}
		if err := p.request(t.Context(), "GET", path, nil, &result); err != nil {
			t.Fatal(err)
		}
		return result.Mergeable || result.Status == "mergeable"
	})
	method := "PUT"
	body := map[string]any{"sha": publication.Commit, "merge_method": "merge"}
	if p.kind == "gitlab" {
		body = map[string]any{"sha": publication.Commit}
	} else if p.kind == "gitea" {
		method = "POST"
		body = map[string]any{"head_commit_id": publication.Commit, "do": "merge"}
	}
	if err := p.request(t.Context(), method, path+"/merge", body, nil); err != nil {
		t.Fatal(err)
	}
}

func exerciseProvider(t *testing.T, p *Provider, owner string) {
	repo, _ := createFixtureRepository(t, p, owner)
	t.Logf("%s: created owned fixture repository", p.kind)
	seedRepository(t, p, repo)
	if p.kind == "gitlab" {
		// Runtime requests use project-scoped credentials; bootstrap credentials
		// only provision the disposable repository and token.
		var scoped struct{ Token string }
		if err := p.request(t.Context(), "POST", p.apiPath(repo)+"/access_tokens", map[string]any{"name": "plat7-runtime", "scopes": []string{"api", "write_repository"}, "access_level": 40, "expires_at": time.Now().Add(24 * time.Hour).Format("2006-01-02")}, &scoped); err != nil {
			t.Fatal(err)
		}
		var err error
		p, err = NewProvider(p.kind, p.base.String(), p.login, scoped.Token)
		if err != nil {
			t.Fatal(err)
		}
		// Newly provisioned project-token access can initially return 404.
		waitFor(t, 30*time.Second, func() bool {
			_, err := p.RepositoryID(t.Context(), repo)
			var response *apiError
			if errors.As(err, &response) && response.status == http.StatusNotFound {
				return false
			}
			if err != nil {
				t.Fatal(err)
			}
			return true
		})
	}
	bad, _ := NewProvider(p.kind, p.base.String(), p.login, "intentionally-invalid-token")
	if _, err := bad.RepositoryID(t.Context(), repo); err == nil {
		t.Fatal("invalid credentials accepted")
	}
	r, err := Open(t.Context(), p, t.TempDir(), repo, "main", []string{"compose.yaml", "compose.override.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	r.Source.Environment = sampleBundle().Environment
	r.Source.Secrets = sampleBundle().Secrets
	edited, err := EditImage(t.Context(), r.Source, "web", "registry.example/web:new")
	if err != nil {
		t.Fatal(err)
	}
	edited, err = EditImage(t.Context(), edited, "worker", "registry.example/worker:new")
	if err != nil {
		t.Fatal(err)
	}
	draft, err := r.Prepare(t.Context(), edited)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(draft.Diff, "synthetic-runtime-secret") || strings.Count(draft.Diff, "diff --git") != 2 {
		t.Fatal("diff leaked a secret or lost a file edit")
	}
	published, err := r.Publish(t.Context(), draft, "fixture/image-update")
	if err != nil {
		t.Fatal(err)
	}
	if published.Review.Number <= 0 || published.Review.URL == "" {
		t.Fatal("provider did not create a review request")
	}
	head, err := p.ReviewHead(t.Context(), repo, published.Review)
	if err != nil || head != published.Commit {
		t.Fatal("review request does not contain the selected commit")
	}
	base, err := r.remoteHead(t.Context(), "main")
	if err != nil || base != r.base {
		t.Fatal("review creation changed the base branch")
	}
	if _, err := r.Publish(t.Context(), draft, "fixture/image-update"); err == nil {
		t.Fatal("existing review branch was overwritten")
	}
	headAgain, err := r.remoteHead(t.Context(), "fixture/image-update")
	if err != nil || headAgain != head {
		t.Fatal("branch collision changed remote source")
	}
	contents, err := r.git(t.Context(), nil, "show", draft.commit+":README.md")
	if err != nil || string(contents) != "Unrelated source must remain byte-for-byte identical.\n" {
		t.Fatal("unrelated repository source changed")
	}
	mergeFixtureReview(t, p, repo, published)
	waitFor(t, time.Minute, func() bool {
		current, err := r.remoteHead(t.Context(), "main")
		return err == nil && current != r.base
	})
	if _, err := r.Publish(t.Context(), draft, "fixture/stale-draft"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale draft did not report conflict: %v", err)
	}
	fresh, err := Open(t.Context(), p, t.TempDir(), repo, "main", []string{"compose.yaml", "compose.override.yaml"})
	if err != nil || fresh.base == r.base {
		t.Fatal("reimport did not observe the authoritative source transition")
	}
	for i, file := range fresh.Source.Files {
		if !bytes.Equal(file.Data, edited.Files[i].Data) {
			t.Fatal("merged source differs from the exact edited bytes")
		}
		if strings.Contains(string(file.Data), "synthetic-runtime-secret") {
			t.Fatal("runtime secret entered Git")
		}
	}
	t.Logf("%s: authenticated import, two-file review, unchanged base, create-only branch and stale-draft rejection passed", p.kind)
}

func TestLocalProviders(t *testing.T) {
	if os.Getenv("PLAT7_PROVIDER_TEST") != "1" {
		t.Skip("set PLAT7_PROVIDER_TEST=1 and PLAT7_DOCKER_HOST for real provider tests")
	}
	for _, kind := range []string{"gitea", "gitlab"} {
		t.Run(kind, func(t *testing.T) { exerciseProvider(t, localProvider(t, kind), "") })
	}
}

func TestHostedGitHub(t *testing.T) {
	if os.Getenv("PLAT7_GITHUB_TOKEN") == "" {
		t.Skip("set PLAT7_GITHUB_TOKEN and PLAT7_GITHUB_OWNER for the disposable hosted test")
	}
	owner := os.Getenv("PLAT7_GITHUB_OWNER")
	if owner == "" || strings.Contains(owner, "/") {
		t.Fatal("set an explicit disposable repository owner")
	}
	p, err := NewProvider("github", "https://api.github.com", "x-access-token", os.Getenv("PLAT7_GITHUB_TOKEN"))
	if err != nil {
		t.Fatal(err)
	}
	exerciseProvider(t, p, owner)
}
