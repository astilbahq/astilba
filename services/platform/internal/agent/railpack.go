package agent

import (
	"archive/tar"
	"context"
	"errors"
	"io"
	"path"
	"path/filepath"
	"sort"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// The stopped helper exposes only the CLI from the same pinned frontend image.
// Its entrypoint never runs and it receives neither source nor credentials.
func (e *Engine) installRailpackPlanner(ctx context.Context, c api.ExecutionCommand, p pipeline.Command) error {
	if _, err := e.Docker.ImageInspect(ctx, p.Railpack.Frontend); err != nil {
		if err = e.pullImage(ctx, p.Railpack.Frontend); err != nil {
			return errors.New("cannot obtain the pinned Railpack planner")
		}
	}
	name := pipelineName(c) + "-planner"
	labels := map[string]string{labelServer: c.ServerId.String(), labelResource: c.Id.String(), labelEnvironment: c.EnvironmentId.String(), labelKind: "pipeline-planner-v1", "com.astilba.plan": c.PlanHash}
	_, err := e.Docker.ContainerCreate(ctx, client.ContainerCreateOptions{Name: name, Config: &container.Config{Image: p.Railpack.Frontend, Labels: labels}, HostConfig: &container.HostConfig{ReadonlyRootfs: true, NetworkMode: "none", CapDrop: []string{"ALL"}}})
	if err != nil {
		return errors.New("cannot create the owned Railpack planner helper")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = e.removePipelineContainer(cleanup, c, name, p.Railpack.Frontend, "pipeline-planner-v1")
	}()
	response, err := e.Docker.CopyFromContainer(ctx, name, client.CopyFromContainerOptions{SourcePath: "/railpack"})
	if err != nil {
		return errors.New("cannot read the pinned Railpack planner")
	}
	defer response.Content.Close()
	reader := tar.NewReader(io.LimitReader(response.Content, 64<<20+16384))
	header, err := reader.Next()
	if err != nil || header.Name != "railpack" || header.Typeflag != tar.TypeReg || header.Size < 1 || header.Size > 64<<20 {
		return errors.New("pinned Railpack planner archive has an unsupported layout")
	}
	cmd := e.pipelineExec(ctx, pipelineName(c), nil, "sh", "-ec", `umask 077; mkdir -p /workspace/planner; cat > /workspace/planner/railpack; chmod 500 /workspace/planner/railpack`)
	cmd.Stdin = reader
	if err = cmd.Run(); err != nil {
		return errors.New("cannot install the pinned planner within source storage limits")
	}
	if _, err = reader.Next(); err != io.EOF {
		return errors.New("pinned planner archive contains unexpected entries")
	}
	return nil
}

func (e *Engine) prepareRailpack(ctx context.Context, c api.ExecutionCommand, p pipeline.Command, contextDir string) (*pipeline.PreparedRailpack, string, error) {
	record, err := e.Journal.Record(c.Id)
	if err != nil {
		return nil, "", err
	}
	prepared := record.Railpack
	logs := ""
	if prepared == nil {
		if err = e.checkpoint(c.Id, "planning"); err != nil {
			return nil, "", err
		}
		configFile := p.Build.Arguments["RAILPACK_CONFIG_FILE"]
		optional := "required"
		if configFile == "" {
			configFile = "railpack.json"
			optional = "optional"
		}
		raw, err := e.pipelineCLI(ctx, pipelineName(c), nil, nil, 65537, "sh", "-ec", `if [ -f "$1" ]; then cat "$1"; elif [ "$2" = optional ]; then printf '{}'; else exit 1; fi`, "--", path.Join(contextDir, configFile), optional)
		if err != nil {
			return nil, "", errors.New("cannot read the selected Railpack configuration within its size limit")
		}
		config, provider, err := pipeline.PrepareRailpackConfig(raw, p.Build.Railpack)
		if err != nil {
			return nil, "", err
		}
		if err = e.installRailpackPlanner(ctx, c, p); err != nil {
			return nil, "", err
		}
		install := `set -e; umask 077; mkdir -p /workspace/planner/mise /workspace/planner/tmp /workspace/planner/home; ln -s /workspace/planner /tmp/railpack; wget -T 60 -qO /workspace/planner/mise.tgz "$1"; printf '%s  /workspace/planner/mise.tgz\n' "$2" | sha256sum -c -; tar -xzOf /workspace/planner/mise.tgz mise/bin/mise > /workspace/planner/mise/mise-2026.10.14; chmod 500 /workspace/planner/mise/mise-2026.10.14; rm /workspace/planner/mise.tgz`
		if _, err = e.pipelineCLI(ctx, pipelineName(c), nil, nil, 1024, "sh", "-ec", install, "--", p.Railpack.MiseArchive, p.Railpack.MiseSHA256); err != nil {
			return nil, "", errors.New("cannot install the checksum-pinned Railpack helper within source storage limits")
		}
		if _, err = e.pipelineCLI(ctx, pipelineName(c), nil, config, 1024, "sh", "-ec", `umask 077; cat > /workspace/planner/config.json`); err != nil {
			return nil, "", err
		}
		relative, err := filepath.Rel(contextDir, "/workspace/planner/config.json")
		if err != nil {
			return nil, "", err
		}
		args := []string{"/workspace/planner/railpack", "prepare", contextDir, "--hide-pretty-plan", "--error-missing-start", "--config-file", relative, "--plan-out", "/workspace/planner/railpack-plan.json", "--info-out", "/workspace/planner/info.json"}
		names := []string{}
		for name := range p.Build.Arguments {
			if name != "RAILPACK_CONFIG_FILE" {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		for _, name := range names {
			args = append(args, "--env", name+"="+p.Build.Arguments[name])
		}
		names = nil
		for name := range p.Build.Secrets {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			args = append(args, "--env", name+"=__ASTILBA_DECLARED_SECRET__")
		}
		logs, err = e.pipelineLog(ctx, c, p, []string{"HOME=/workspace/planner/home", "TMPDIR=/workspace/planner/tmp"}, args)
		if err != nil {
			return nil, logs, errors.New("Railpack planning failed; check the source configuration, start command and runner storage budget")
		}
		plan, err := e.pipelineCLI(ctx, pipelineName(c), nil, nil, pipeline.RailpackPlanLimit+1, "cat", "/workspace/planner/railpack-plan.json")
		if err != nil {
			return nil, logs, errors.New("Railpack plan exceeds supported limits")
		}
		info, err := e.pipelineCLI(ctx, pipelineName(c), nil, nil, pipeline.RailpackPlanLimit+1, "cat", "/workspace/planner/info.json")
		if err != nil {
			return nil, logs, errors.New("Railpack metadata exceeds supported limits")
		}
		prepared, err = pipeline.FreezeRailpackPlan(plan, info, p, provider)
		if err != nil {
			return nil, logs, err
		}
		if err = e.Journal.Mutate(c.Id, func(r *Record) { r.Railpack = prepared }); err != nil {
			return nil, logs, err
		}
		e.point("railpack-prepared")
	}
	if !prepared.Valid(*p.Railpack) {
		return nil, logs, errors.New("saved Railpack plan no longer matches the frozen inputs")
	}
	if _, err = e.pipelineCLI(ctx, pipelineName(c), nil, nil, 1024, "sh", "-ec", `mkdir -p /workspace/prepared; rm -rf /workspace/planner /tmp/railpack`); err != nil {
		return nil, logs, err
	}
	for name, data := range map[string][]byte{"railpack-plan.json": prepared.Plan, "source-policy.json": pipeline.RailpackSourcePolicy(*p.Railpack)} {
		if _, err = e.pipelineCLI(ctx, pipelineName(c), nil, data, 1024, "sh", "-ec", `umask 077; cat > "$1"`, "--", "/workspace/prepared/"+name); err != nil {
			return nil, logs, err
		}
	}
	return prepared, logs, nil
}
