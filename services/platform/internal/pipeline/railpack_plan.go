package pipeline

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/distribution/reference"
	"github.com/tailscale/hujson"
)

const RailpackPlanLimit = 128 << 10

func railpackPlanHash(plan json.RawMessage, inputs RailpackInputs) string {
	return Hash(struct {
		Plan   json.RawMessage
		Inputs RailpackInputs
		Policy json.RawMessage
	}{plan, inputs, RailpackSourcePolicy(inputs)})
}

var toolComponent = regexp.MustCompile(`^[A-Za-z0-9_.+-]{1,80}$`)

func supportedRailpackProvider(p string) bool { return p == "node" || p == "golang" || p == "shell" }

func shellLiteral(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

func configObject(m map[string]any, key string) (map[string]any, error) {
	if m[key] == nil {
		m[key] = map[string]any{}
	}
	v, ok := m[key].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("Railpack %s must be an object", key)
	}
	return v, nil
}

// Project a source configuration without rewriting its tracked file. Platform
// commands take precedence over repository commands, unlike upstream CLI flags.
func PrepareRailpackConfig(raw []byte, options *RailpackOptions) ([]byte, string, error) {
	if len(raw) > 65536 {
		return nil, "", errors.New("Railpack configuration exceeds 64 KiB")
	}
	clean, err := hujson.Standardize(raw)
	if err != nil {
		return nil, "", errors.New("Railpack configuration must be valid JSON or JSON with comments")
	}
	var cfg map[string]any
	if json.Unmarshal(clean, &cfg) != nil || cfg == nil {
		return nil, "", errors.New("Railpack configuration must be an object")
	}
	provider := ""
	if value, present := cfg["provider"]; present {
		var ok bool
		provider, ok = value.(string)
		if !ok || !supportedRailpackProvider(provider) {
			return nil, "", errors.New("This Railpack path supports Node/npm, Go and shell providers; use a Dockerfile for another provider")
		}
	}
	if options != nil && options.BuildCommand != "" {
		steps, err := configObject(cfg, "steps")
		if err != nil {
			return nil, "", err
		}
		build, err := configObject(steps, "build")
		if err != nil {
			return nil, "", err
		}
		build["commands"] = []any{map[string]any{"src": ".", "dest": "."}, map[string]any{"cmd": "sh -c " + shellLiteral(options.BuildCommand)}}
	}
	if options != nil && options.StartCommand != "" {
		deploy, err := configObject(cfg, "deploy")
		if err != nil {
			return nil, "", err
		}
		deploy["startCommand"] = options.StartCommand
	}
	data, err := json.Marshal(cfg)
	return data, provider, err
}

type railpackInfo struct {
	Success   bool     `json:"success"`
	Version   string   `json:"railpackVersion"`
	Providers []string `json:"detectedProviders"`
	Packages  map[string]struct {
		Version *string `json:"resolvedVersion"`
	} `json:"resolvedPackages"`
}

func pinRailpackImage(layer map[string]any, inputs RailpackInputs) error {
	value, present := layer["image"]
	if !present {
		return nil
	}
	name, ok := value.(string)
	if !ok {
		return errors.New("Railpack image input must be a string")
	}
	for _, pinned := range []string{inputs.Builder, inputs.Runtime, inputs.HelperImage} {
		if name == strings.Split(pinned, "@")[0] || name == pinned {
			layer["image"] = pinned
			return nil
		}
	}
	if name == "scratch" {
		return nil
	}
	named, err := reference.ParseNormalizedNamed(name)
	if err == nil {
		if digest, ok := named.(reference.Digested); ok && digest.Digest().Algorithm().String() == "sha256" {
			return nil
		}
	}
	return errors.New("Railpack image inputs must use a supported base or an explicit SHA-256 digest; pin custom images or use a Dockerfile")
}

// Source policy also covers images created internally by the frontend rather
// than serialized in the plan. Last matching allow/deny wins; conversion retries.
func RailpackSourcePolicy(inputs RailpackInputs) []byte {
	data, _ := json.Marshal(map[string]any{"version": 1, "rules": []any{
		map[string]any{"action": "DENY", "selector": map[string]string{"identifier": "docker-image://*"}},
		map[string]any{"action": "ALLOW", "selector": map[string]string{"identifier": `^docker-image://.+@sha256:[a-f0-9]{64}$`, "match_type": "REGEX"}},
		map[string]any{"action": "CONVERT", "selector": map[string]string{"identifier": "docker-image://docker.io/library/alpine:latest", "match_type": "EXACT"}, "updates": map[string]string{"identifier": "docker-image://" + inputs.HelperImage}},
	}})
	return data
}

func FreezeRailpackPlan(raw, infoRaw []byte, c Command, configuredProvider string) (*PreparedRailpack, error) {
	if len(raw) > RailpackPlanLimit || len(infoRaw) > RailpackPlanLimit || c.Railpack == nil || c.Build == nil {
		return nil, errors.New("Railpack plan or metadata exceeds supported limits")
	}
	var info railpackInfo
	if json.Unmarshal(infoRaw, &info) != nil || !info.Success || info.Version != c.Railpack.Version {
		return nil, errors.New("Railpack did not produce successful metadata for the assigned planner version")
	}
	provider := configuredProvider
	if provider == "" && len(info.Providers) == 1 {
		provider = info.Providers[0]
	}
	if !supportedRailpackProvider(provider) {
		return nil, errors.New("This Railpack path supports Node/npm, Go and shell providers; use a Dockerfile for another provider")
	}
	var plan map[string]any
	if json.Unmarshal(raw, &plan) != nil || plan == nil {
		return nil, errors.New("Railpack produced an invalid plan")
	}
	steps, ok := plan["steps"].([]any)
	if !ok || len(steps) == 0 || len(steps) > 128 {
		return nil, errors.New("Railpack plan has an unsupported step count")
	}
	deploy, ok := plan["deploy"].(map[string]any)
	if !ok {
		return nil, errors.New("Railpack plan has no deployment configuration")
	}
	if start, ok := deploy["startCommand"].(string); !ok || strings.TrimSpace(start) == "" {
		return nil, errors.New("Railpack requires a start command")
	}
	names := map[string]bool{}
	for name := range c.Build.Arguments {
		names[name] = true
	}
	for name := range c.Build.Secrets {
		names[name] = true
	}
	if existing, present := plan["secrets"]; present {
		list, ok := existing.([]any)
		if !ok {
			return nil, errors.New("Railpack secret declarations must be a list")
		}
		for _, v := range list {
			name, ok := v.(string)
			if !ok || !names[name] {
				return nil, errors.New("Declare each Railpack secret or public argument before building")
			}
		}
	}
	allNames := make([]string, 0, len(names))
	for name := range names {
		allNames = append(allNames, name)
	}
	sort.Strings(allNames)
	plan["secrets"] = allNames
	packages := map[string]string{}
	for name, pkg := range info.Packages {
		if len(packages) >= 32 || !toolComponent.MatchString(name) || pkg.Version == nil || !toolComponent.MatchString(*pkg.Version) {
			return nil, errors.New("Railpack resolved an unsupported tool/version format; use a Dockerfile")
		}
		packages[name] = *pkg.Version
	}
	keys := make([]string, 0, len(packages))
	for k := range packages {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	tools, paths := []string{}, []string{}
	for _, name := range keys {
		tools = append(tools, shellLiteral(name+"@"+packages[name]))
		paths = append([]string{path.Join("/mise/installs", name, packages[name], "bin")}, paths...)
	}
	installed := len(packages) == 0
	for _, value := range steps {
		step, ok := value.(map[string]any)
		if !ok {
			return nil, errors.New("Railpack step must be an object")
		}
		if err := pinRailpackLayers(step["inputs"], *c.Railpack); err != nil {
			return nil, err
		}
		if secrets, present := step["secrets"]; present {
			list, ok := secrets.([]any)
			if !ok {
				return nil, errors.New("Railpack step secrets must be a list")
			}
			for _, v := range list {
				name, ok := v.(string)
				if !ok || name != "*" && !names[name] {
					return nil, errors.New("Declare each Railpack step secret before building")
				}
			}
		}
		commands, ok := step["commands"].([]any)
		if step["commands"] != nil && !ok {
			return nil, errors.New("Railpack commands must be a list")
		}
		for _, value := range commands {
			cmd, ok := value.(map[string]any)
			if !ok {
				continue
			}
			if err := pinRailpackImage(cmd, *c.Railpack); err != nil {
				return nil, err
			}
			if step["name"] == "packages:mise" && cmd["cmd"] == "mise install" {
				cmd["cmd"] = "mise install " + strings.Join(tools, " ")
				installed = true
			}
		}
		if step["name"] == "packages:mise" && len(paths) > 0 {
			step["commands"] = append(commands, map[string]any{"path": strings.Join(paths, ":")})
		}
	}
	if !installed {
		return nil, errors.New("Railpack tool installation could not be frozen; use a Dockerfile")
	}
	base, ok := deploy["base"].(map[string]any)
	if !ok {
		return nil, errors.New("Railpack deployment has no base layer")
	}
	if err := pinRailpackImage(base, *c.Railpack); err != nil {
		return nil, err
	}
	if err := pinRailpackLayers(deploy["inputs"], *c.Railpack); err != nil {
		return nil, err
	}
	if len(paths) > 0 {
		old, ok := deploy["paths"].([]any)
		if deploy["paths"] != nil && !ok {
			return nil, errors.New("Railpack runtime paths must be a list")
		}
		deploy["paths"] = append(old, strings.Join(paths, ":"))
	}
	frozen, err := json.Marshal(plan)
	if err != nil || len(frozen) > RailpackPlanLimit {
		return nil, errors.New("Frozen Railpack plan exceeds supported limits")
	}
	summary := RailpackSummary{c.Railpack.Version, c.Railpack.Frontend, provider, railpackPlanHash(frozen, *c.Railpack), packages}
	return &PreparedRailpack{frozen, summary}, nil
}

func pinRailpackLayers(value any, inputs RailpackInputs) error {
	if value == nil {
		return nil
	}
	list, ok := value.([]any)
	if !ok {
		return errors.New("Railpack inputs must be a list")
	}
	for _, v := range list {
		layer, ok := v.(map[string]any)
		if !ok {
			return errors.New("Railpack input must be an object")
		}
		if err := pinRailpackImage(layer, inputs); err != nil {
			return err
		}
	}
	return nil
}
