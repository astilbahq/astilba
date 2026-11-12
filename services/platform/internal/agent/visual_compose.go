package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
)

func (e *Engine) composeVersion(ctx context.Context) (string, error) {
	return composeVersion(ctx, e.DockerCLI)
}

func composeVersion(ctx context.Context, cli string) (string, error) {
	if cli == "" {
		cli = "docker"
	}
	out, err := exec.CommandContext(ctx, cli, "compose", "version", "--short").Output()
	version := strings.TrimPrefix(strings.TrimSpace(string(out)), "v")
	if err != nil || version != "5.6.1" {
		return version, errors.New("unsupported Compose version")
	}
	return version, nil
}

type boundedOutput struct {
	b           bytes.Buffer
	limit       int
	truncated   bool
	stopOnLimit bool
}

func (w *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := w.limit - w.b.Len()
	if remaining > 0 {
		w.b.Write(p[:min(remaining, n)])
	}
	if n > remaining {
		w.truncated = true
		if w.stopOnLimit {
			return max(remaining, 0), errors.New("output limit exceeded")
		}
	}
	return n, nil
}

func (e *Engine) compose(ctx context.Context, p deployment.Plan, body []byte, secrets map[string]string, args ...string) error {
	cli := e.DockerCLI
	if cli == "" {
		cli = "docker"
	}
	host := e.Docker.DaemonHost()
	if !strings.HasPrefix(host, "unix:///") {
		return errors.New("Compose requires the selected local Docker socket")
	}
	// Any run can start a service, so shells in what this plan makes elevated end first.
	if err := e.fenceTerminals(ctx, p); err != nil {
		return err
	}
	directory := e.Journal.dir
	if p.Specification.Compose != nil {
		directory = e.sourceDirectory(p)
	}
	project := deployment.UnitName(p.UnitID)
	if p.Specification.Compose == nil && len(args) > 0 {
		for _, app := range p.Specification.Applications {
			if args[len(args)-1] == deployment.ServiceName(app.ID) {
				project = composeApplicationProject(p, app.ID)
				args = args[:len(args)-1]
				for _, instance := range p.InstanceIDs(app.ID) {
					args = append(args, deployment.ServiceName(instance))
				}
				break
			}
		}
	}
	args = append([]string{"--host", host, "compose", "--ansi", "never", "--progress", "plain", "--env-file", "/dev/null", "--project-name", project, "--project-directory", directory, "--file", "-"}, args...)
	cmd := exec.CommandContext(ctx, cli, args...)
	cmd.Stdin = bytes.NewReader(body)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + e.Journal.dir, "DOCKER_CONFIG=" + e.Journal.dir + "/docker"}
	if p.Specification.Compose != nil {
		for _, id := range domain.SecretReferences(p.Specification) {
			if value, ok := secrets[id.String()]; ok {
				cmd.Env = append(cmd.Env, deployment.SecretEnvironmentName(id)+"="+value)
			}
		}
	}
	cmd.Dir = e.Journal.dir
	out := boundedOutput{limit: 1 << 20}
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		if out.truncated {
			return errors.New("Compose output exceeded its diagnostic limit; inspect the host")
		}
		return fmt.Errorf("Compose did not complete: %s", out.b.String())
	}
	return nil
}

func composeApplicationProject(p deployment.Plan, app uuid.UUID) string {
	isolated := p.Instances[app.String()] != uuid.Nil
	if p.Local != nil {
		_, isolated = p.Local.Requests[app.String()]
		ids := p.InstanceIDs(app)
		isolated = isolated || len(ids) > 0 && ids[0] != app
	}
	if isolated {
		return p.ContainerName(app)
	}
	return deployment.UnitName(p.UnitID)
}
