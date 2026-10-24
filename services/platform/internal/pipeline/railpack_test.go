package pipeline

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestRailpackConfigurationPreservesSourceAndOverridesCommands(t *testing.T) {
	raw := []byte(`{ // source comment
	  "provider":"shell", "steps":{"build":{"commands":["exit 21"],"variables":{"PUBLIC":"kept"}}},
	  "deploy":{"startCommand":"exit 22","variables":{"PUBLIC":"kept"}},
	}`)
	command := "printf '%s' \"it's built\""
	projected, provider, err := PrepareRailpackConfig(raw, &RailpackOptions{BuildCommand: command, StartCommand: "./start.sh"})
	if err != nil || provider != "shell" {
		t.Fatal("source config was not projected", err)
	}
	var cfg map[string]any
	_ = json.Unmarshal(projected, &cfg)
	build := cfg["steps"].(map[string]any)["build"].(map[string]any)
	commands := build["commands"].([]any)
	if len(commands) != 2 || commands[1].(map[string]any)["cmd"] != `sh -c 'printf '"'"'%s'"'"' "it'"'"'s built"'` || build["variables"].(map[string]any)["PUBLIC"] != "kept" || cfg["deploy"].(map[string]any)["startCommand"] != "./start.sh" {
		t.Fatal("command override lost precedence, quoting or unrelated configuration", string(projected))
	}
	if _, _, err := PrepareRailpackConfig([]byte(`{"provider":"python"}`), nil); err == nil {
		t.Fatal("unsupported effective provider accepted")
	}
}

func TestRailpackFreezeCapturesInputsAndRejectsMutableSources(t *testing.T) {
	inputs := RailpackFor("arm64")
	c := Command{Railpack: &inputs, Build: &BuildConfig{Arguments: map[string]string{"EMPTY": ""}, Secrets: map[string]uuid.UUID{"NPM_TOKEN": uuid.New()}}}
	plan := `{"secrets":["NPM_TOKEN"],"steps":[{"name":"packages:mise","inputs":[{"image":"` + strings.Split(RailpackBuilder, "@")[0] + `"}],"commands":[{"cmd":"mise install"},{"path":"/mise/shims"}],"assets":{"image":"unrelated source asset"}},{"name":"build","secrets":["*"],"commands":[{"image":"` + strings.Split(RailpackRuntime, "@")[0] + `","src":"/input","dest":"/output"}]}],"deploy":{"base":{"image":"` + strings.Split(RailpackRuntime, "@")[0] + `"},"startCommand":"node main.js"}}`
	info := []byte(`{"success":true,"railpackVersion":"0.43.0","detectedProviders":["node"],"resolvedPackages":{"node":{"resolvedVersion":"24.18.1"}}}`)
	frozen, err := FreezeRailpackPlan([]byte(plan), info, c, "")
	if err != nil {
		t.Fatal(err)
	}
	text := string(frozen.Plan)
	for _, value := range []string{RailpackBuilder, RailpackRuntime, `"EMPTY"`, `mise install 'node@24.18.1'`, `/mise/installs/node/24.18.1/bin`, `"image":"unrelated source asset"`} {
		if !strings.Contains(text, value) {
			t.Fatal("frozen input missing", value, text)
		}
	}
	if !frozen.Valid(inputs) {
		t.Fatal("prepared plan did not validate")
	}
	modified := *frozen
	modified.Plan = []byte(strings.Replace(string(frozen.Plan), "node main.js", "node other.js", 1))
	if modified.Valid(inputs) {
		t.Fatal("changed prepared plan accepted on replay")
	}
	for _, bad := range []string{
		strings.Replace(plan, strings.Split(RailpackRuntime, "@")[0], "example.test/custom:latest", 1),
		strings.Replace(plan, `"NPM_TOKEN"`, `"UNDECLARED"`, 1),
	} {
		if _, err := FreezeRailpackPlan([]byte(bad), info, c, ""); err == nil {
			t.Fatal("mutable copy image or undeclared secret accepted")
		}
	}
	if _, err := FreezeRailpackPlan([]byte(plan), info, c, "shell"); err != nil {
		t.Fatal("effective configured provider was ignored", err)
	}
}

func TestRailpackModeAdmissionAndCacheCompatibility(t *testing.T) {
	b := BuildConfig{ConnectionID: uuid.New(), RunnerID: uuid.New(), Branch: "main", Context: ".", Dockerfile: "Dockerfile", Architecture: "amd64"}
	c := Command{Build: &b}
	legacy := c.CacheKey()
	b.Builder = "dockerfile"
	if c.CacheKey() != legacy {
		t.Fatal("explicit Dockerfile invalidated legacy cache")
	}
	b.Builder, b.Dockerfile = "railpack", ""
	if b.Validate() == nil {
		t.Fatal("Railpack accepted insufficient default storage")
	}
	b.Budget = &BuildBudget{CPUs: 2, MemoryMiB: 6144, SourceMiB: 1024, StateMiB: 4096, Minutes: 30}
	if err := b.Validate(); err != nil {
		t.Fatal("Railpack required a Dockerfile", err)
	}
	inputs := RailpackFor("amd64")
	c.Railpack = &inputs
	key := c.CacheKey()
	if key == legacy {
		t.Fatal("Railpack reused Dockerfile cache")
	}
	inputs.Version = "changed"
	if c.CacheKey() == key {
		t.Fatal("planner version did not partition cache")
	}
	b.Arguments = map[string]string{"TOKEN": "public"}
	b.Secrets = map[string]uuid.UUID{"TOKEN": uuid.New()}
	if b.Validate() == nil {
		t.Fatal("secret collided with public planner input")
	}
}
