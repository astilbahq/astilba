package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
)

func TestPipelineLogsRedactBuildSecretsWithCredentialNames(t *testing.T) {
	journal, _ := testVisualJournal(t)
	cli := filepath.Join(t.TempDir(), "capture-output")
	if err := os.WriteFile(cli, []byte("#!/bin/sh\nprintf '%s' \"$FIXTURE_LOG\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	docker, err := client.New(client.WithHost("unix:///unused-fixture.sock"), client.WithAPIVersion("1.56"))
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close()
	engine := Engine{Journal: journal, Docker: docker, DockerCLI: cli}
	secrets := map[string]string{"clone": "build-clone-canary", "api": "build-api-canary", "push": "build-push-canary", "pull": "build-pull-canary"}
	command := pipeline.Command{
		BuildSecrets:   secrets,
		Access:         &pipeline.Access{Clone: pipeline.Credential{Password: "git-clone-canary"}, API: pipeline.Credential{Password: "git-api-canary"}},
		RegistryAccess: &pipeline.RegistryAccess{Push: pipeline.Credential{Password: "registry-push-canary"}, Pull: pipeline.Credential{Password: "registry-pull-canary"}},
	}
	values := []string{"git-clone-canary", "git-api-canary", "registry-push-canary", "registry-pull-canary"}
	for _, value := range secrets {
		values = append(values, value)
	}
	log := "visible build output\n" + strings.Join(values, "\n")
	got, err := engine.pipelineLog(context.Background(), api.ExecutionCommand{Id: uuid.New()}, command, []string{"FIXTURE_LOG=" + log}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "visible build output") {
		t.Fatal("redaction discarded nonsecret output")
	}
	for _, value := range values {
		if strings.Contains(got, value) {
			t.Fatal("build output retained a secret or credential value")
		}
	}
}
