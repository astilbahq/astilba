package agent

import (
	"context"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
)

func gitEnvironment(p pipeline.Command) []string {
	credential := pipeline.Credential{}
	if p.Access != nil {
		credential = p.Access.Clone
		if p.Action == "submit" {
			credential = p.Access.API
		}
	}
	endpoint := pipeline.GitURL(p.Connection)
	environment := []string{"HOME=/workspace", "LC_ALL=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_ALLOW_PROTOCOL=https:http", "GIT_LITERAL_PATHSPECS=1", "GIT_INDEX_FILE=/workspace/index",
		"GIT_CONFIG_COUNT=5", "GIT_CONFIG_KEY_0=http." + endpoint + ".extraHeader", "GIT_CONFIG_VALUE_0=Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(credential.Username+":"+credential.Password)),
		"GIT_CONFIG_KEY_1=http.followRedirects", "GIT_CONFIG_VALUE_1=false", "GIT_CONFIG_KEY_2=core.hooksPath", "GIT_CONFIG_VALUE_2=/dev/null", "GIT_CONFIG_KEY_3=credential.helper", "GIT_CONFIG_VALUE_3=", "GIT_CONFIG_KEY_4=transfer.fsckObjects", "GIT_CONFIG_VALUE_4=true",
		"GIT_AUTHOR_NAME=Astilba", "GIT_AUTHOR_EMAIL=astilba@users.noreply.github.com", "GIT_COMMITTER_NAME=Astilba", "GIT_COMMITTER_EMAIL=astilba@users.noreply.github.com", "GIT_AUTHOR_DATE=" + p.SubmittedAt.Format(time.RFC3339), "GIT_COMMITTER_DATE=" + p.SubmittedAt.Format(time.RFC3339)}
	if credential.Password == "" {
		for i, v := range environment {
			if strings.HasPrefix(v, "GIT_CONFIG_VALUE_0=") {
				environment[i] = "GIT_CONFIG_VALUE_0="
			}
		}
	}
	return environment
}

func (e *Engine) pipelineGit(ctx context.Context, c api.ExecutionCommand, p pipeline.Command, input []byte, args ...string) ([]byte, error) {
	return e.pipelineCLI(ctx, pipelineName(c), gitEnvironment(p), input, 1<<20, append([]string{"git", "--git-dir=/workspace/repo"}, args...)...)
}
func (e *Engine) clonePipeline(ctx context.Context, c api.ExecutionCommand, p pipeline.Command) error {
	if _, err := e.pipelineCLI(ctx, pipelineName(c), nil, nil, 1024, "mkdir", "-p", "/workspace/repo"); err != nil {
		return err
	}
	if _, err := e.pipelineGit(ctx, c, p, nil, "init", "--bare", "--quiet"); err != nil {
		return err
	}
	if _, err := e.pipelineGit(ctx, c, p, nil, "remote", "add", "origin", pipeline.GitURL(p.Connection)); err != nil {
		return err
	}
	fetch := p.Commit
	if p.Preview != nil {
		fetch = pipeline.PullRequestRef(p.Connection.Provider, p.Preview.Number)
	}
	if _, err := e.pipelineGit(ctx, c, p, nil, "fetch", "--quiet", "--depth=1", "--no-tags", "origin", fetch); err != nil {
		return errors.New("exact-commit fetch failed or exceeded the source storage budget")
	}
	out, err := e.pipelineGit(ctx, c, p, nil, "rev-parse", "FETCH_HEAD")
	if err != nil || strings.TrimSpace(string(out)) != p.Commit {
		return errors.New("fetched commit differs from assigned source")
	}
	return nil
}
func (e *Engine) sourceFile(ctx context.Context, c api.ExecutionCommand, p pipeline.Command, name string) (composeproject.File, string, error) {
	file := composeproject.File{Path: name}
	if !composeproject.ValidPath(name) || strings.HasPrefix(name, ".git/") {
		return file, "", errors.New("invalid selected source file")
	}
	listing, err := e.pipelineGit(ctx, c, p, nil, "ls-tree", "-z", p.Commit, "--", name)
	if err != nil {
		return file, "", err
	}
	metadata, filename, ok := strings.Cut(strings.TrimSuffix(string(listing), "\x00"), "\t")
	parts := strings.Fields(metadata)
	if !ok || filename != name || len(parts) != 3 || (parts[0] != "100644" && parts[0] != "100755") || parts[1] != "blob" {
		return file, "", errors.New("source must be a regular tracked file; symlinks and submodules are unsupported")
	}
	size, err := e.pipelineGit(ctx, c, p, nil, "cat-file", "-s", p.Commit+":"+name)
	if err != nil {
		return file, "", err
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(size)))
	if err != nil || n > 65536 {
		return file, "", errors.New("selected source exceeds its file limit")
	}
	content, err := e.pipelineGit(ctx, c, p, nil, "show", p.Commit+":"+name)
	if err != nil {
		return file, "", err
	}
	file.Content = string(content)
	return file, parts[0], nil
}
func (e *Engine) importPipeline(ctx context.Context, c api.ExecutionCommand, p pipeline.Command) (pipeline.Result, error) {
	result := pipeline.Result{Commit: p.Commit, Files: []composeproject.File{}}
	for _, name := range p.Source.Paths {
		file, _, err := e.sourceFile(ctx, c, p, name)
		if err != nil {
			return result, err
		}
		result.Files = append(result.Files, file)
	}
	b := composeproject.Bundle{Namespace: p.UnitID, Entrypoints: p.Source.Entrypoints, Files: result.Files}
	return result, b.Validate()
}
func (e *Engine) remotePipelineHead(ctx context.Context, c api.ExecutionCommand, p pipeline.Command, branch string) (string, error) {
	out, err := e.pipelineGit(ctx, c, p, nil, "ls-remote", "origin", "refs/heads/"+branch)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return "", nil
	}
	if len(fields) != 2 || fields[1] != "refs/heads/"+branch || !pipeline.ValidCommit(fields[0]) {
		return "", errors.New("invalid remote branch")
	}
	return fields[0], nil
}
