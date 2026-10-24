package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/moby/moby/client"
)

func captureTool(t *testing.T, stdout, stderr string, fail bool) string {
	t.Helper()
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	body := "#!/bin/sh\nif [ \"$1\" = version ]; then printf '%s\\n' 'restic " + backup.ResticVersion + " fixture'; exit 0; fi\n" +
		"printf '%s' " + quote(stdout) + "\nprintf '%s' " + quote(stderr) + " >&2\n"
	if fail {
		body += "exit 7\n"
	}
	file := filepath.Join(t.TempDir(), "capture-tool")
	if err := os.WriteFile(file, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestSubprocessCaptureStaysBounded(t *testing.T) {
	const limit = 64 << 10
	for _, stream := range []string{"stdout", "stderr"} {
		t.Run(stream, func(t *testing.T) {
			stdout, stderr := strings.Repeat("x", 2*limit), ""
			if stream == "stderr" {
				stdout, stderr = stderr, stdout
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, captureTool(t, stdout, stderr, false), "emit")
			out := &boundedOutput{limit: limit, stopOnLimit: true}
			cmd.Stdout, cmd.Stderr = out, out
			if err := cmd.Run(); err == nil || !out.truncated || out.b.Len() > limit {
				t.Fatalf("subprocess capture was not bounded: err=%v, truncated=%t, bytes=%d", err, out.truncated, out.b.Len())
			}
		})
	}
}

func TestToolCapturesRejectOverflowAndKeepCompleteDiagnostics(t *testing.T) {
	const limit = 1 << 20
	const secret = "capture-boundary-secret-canary"
	oversized := strings.Repeat("x", limit-4) + secret
	for _, tc := range []struct {
		name, tool, stdout, stderr, want string
		fail, wantErr                    bool
	}{
		{name: "source at limit ignores stderr", tool: "source", stdout: strings.Repeat("x", 4096), stderr: oversized, want: strings.Repeat("x", 4096)},
		{name: "source overflow", tool: "source", stdout: strings.Repeat("x", 4097), wantErr: true},
		{name: "build at limit", tool: "build", stdout: strings.Repeat("x", limit-len(secret)) + secret, want: strings.Repeat("x", 16384-len("[redacted]")) + "[redacted]"},
		{name: "failed build keeps redacted stdout and stderr", tool: "build", stdout: "stdout " + secret + "\n", stderr: "stderr " + secret, fail: true, wantErr: true, want: "stdout [redacted]\nstderr [redacted]"},
		{name: "build stdout overflow", tool: "build", stdout: oversized, wantErr: true},
		{name: "build stderr overflow", tool: "build", stderr: oversized, wantErr: true},
		{name: "build combined overflow", tool: "build", stdout: strings.Repeat("x", limit/2), stderr: strings.Repeat("x", limit/2-4) + secret, wantErr: true},
		{name: "repository overflow", tool: "repository", stdout: oversized, wantErr: true},
		{name: "database metadata at limit", tool: "data", stdout: strings.Repeat("x", 65536), want: strings.Repeat("x", 65536)},
		{name: "database metadata overflow", tool: "data", stdout: strings.Repeat("x", 65537), wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			docker, err := client.New(client.WithHost("unix:///capture-fixture.sock"), client.WithAPIVersion("1.54"))
			if err != nil {
				t.Fatal(err)
			}
			defer docker.Close()
			cli := captureTool(t, tc.stdout, tc.stderr, tc.fail)
			e := Engine{Journal: &Journal{dir: t.TempDir()}, Docker: docker, DockerCLI: cli, ResticCLI: cli}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			var got string
			switch tc.tool {
			case "source":
				var raw []byte
				raw, err = e.pipelineCLI(ctx, "fixture", nil, nil, 4096, "emit")
				got = string(raw)
			case "build":
				got, err = e.pipelineBuildLog(ctx, api.ExecutionCommand{}, pipeline.Command{BuildSecrets: map[string]string{"fixture": secret}}, []string{"emit"})
			case "repository":
				var raw []byte
				raw, err = e.restic(ctx, backup.Installation{Destination: backup.Destination{Kind: "filesystem", Root: t.TempDir()}}, e.Journal.dir, "emit")
				got = string(raw)
			case "data":
				var raw []byte
				raw, err = e.dataOutput(ctx, "fixture", "emit")
				got = string(raw)
			}
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("wrong captured outcome: err=%v, bytes=%d, wantErr=%t, wantBytes=%d", err, len(got), tc.wantErr, len(tc.want))
			}
		})
	}
}
