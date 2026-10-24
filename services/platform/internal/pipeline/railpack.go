package pipeline

import (
	"encoding/json"
	"strings"
)

const RailpackVersion = "0.43.0"
const RailpackFrontend = "ghcr.io/railwayapp/railpack-frontend:v0.43.0@sha256:355ef61fae6f2551103cd8b10c710cb07167dd353d561f15c69b8d7ac1c676cc"
const RailpackBuilder = "ghcr.io/railwayapp/railpack-builder:mise-2026.10.14@sha256:03df9e9aa1e786c2e0dd12c5409acad3bbd5a0fa8a75c1117c7ddab5c0c3fde4"
const RailpackRuntime = "ghcr.io/railwayapp/railpack-runtime:mise-2026.10.14@sha256:02820533713338569ae4d4f0df986c3e7cc6e93457fd06fbb0d2cfa86fba9cdb"
const RailpackHelper = "docker.io/library/alpine:3.23@sha256:ff7f0c727dd796aa36787bfcd9a64bf35a6ce72a73451b3359fde849b6703d63"
const RailpackMiseVersion = "2026.10.14"

type RailpackOptions struct {
	BuildCommand string `json:"buildCommand,omitempty"`
	StartCommand string `json:"startCommand,omitempty"`
}

type RailpackInputs struct {
	Version     string `json:"version"`
	Frontend    string `json:"frontend"`
	Builder     string `json:"builder"`
	Runtime     string `json:"runtime"`
	HelperImage string `json:"helperImage"`
	MiseVersion string `json:"miseVersion"`
	MiseArchive string `json:"miseArchive"`
	MiseSHA256  string `json:"miseSHA256"`
}

func RailpackFor(arch string) RailpackInputs {
	asset, sha := "x64", "0f95d528ad0b8c54f7022be2ff22a36be149123ff9b27bdaa216d349a9f59c17"
	if arch == "arm64" {
		asset, sha = "arm64", "8f7fa64c3d08ba515aac501f18f6f43c00c381793185380fd1a92c411c00cdde"
	}
	return RailpackInputs{RailpackVersion, RailpackFrontend, RailpackBuilder, RailpackRuntime, RailpackHelper, RailpackMiseVersion, "https://github.com/jdx/mise/releases/download/v" + RailpackMiseVersion + "/mise-v" + RailpackMiseVersion + "-linux-" + asset + "-musl.tar.gz", sha}
}

type RailpackSummary struct {
	Version  string            `json:"version"`
	Frontend string            `json:"frontend"`
	Provider string            `json:"provider"`
	PlanHash string            `json:"planHash"`
	Packages map[string]string `json:"packages"`
}

type PreparedRailpack struct {
	Plan    json.RawMessage `json:"plan"`
	Summary RailpackSummary `json:"summary"`
}

func (p PreparedRailpack) Valid(inputs RailpackInputs) bool {
	return len(p.Plan) > 0 && len(p.Plan) <= RailpackPlanLimit && p.Summary.Valid(inputs) && p.Summary.PlanHash == railpackPlanHash(p.Plan, inputs)
}

func (s RailpackSummary) Valid(inputs RailpackInputs) bool {
	if s.Version != inputs.Version || s.Frontend != inputs.Frontend || !supportedRailpackProvider(s.Provider) || len(s.PlanHash) != 64 || len(s.Packages) > 32 {
		return false
	}
	for _, c := range s.PlanHash {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	for name, version := range s.Packages {
		if !toolComponent.MatchString(name) || !toolComponent.MatchString(version) {
			return false
		}
	}
	return true
}

func (b BuildConfig) UsesRailpack() bool { return b.Builder == "railpack" }

func (b BuildConfig) Canonical() BuildConfig {
	if b.Builder == "dockerfile" {
		b.Builder = ""
	}
	return b
}

func (b BuildConfig) validateBuilder() error {
	if b.Builder != "" && b.Builder != "dockerfile" && b.Builder != "railpack" {
		return &BuildValidationError{"builder", "Choose Dockerfile or Railpack."}
	}
	if !b.UsesRailpack() {
		if b.Railpack != nil {
			return &BuildValidationError{"railpack", "Railpack command overrides require the Railpack builder."}
		}
		return nil
	}
	if b.Railpack != nil {
		for _, s := range []string{b.Railpack.BuildCommand, b.Railpack.StartCommand} {
			if len(s) > 4096 || strings.ContainsRune(s, 0) {
				return &BuildValidationError{"railpack", "Use build and start commands of at most 4096 bytes, without NUL characters."}
			}
		}
	}
	if b.Limits().StateMiB < 3072 || b.Limits().SourceMiB < 256 {
		return &BuildValidationError{"budget", "Railpack requires at least 3072 MiB builder storage and 256 MiB source storage; the recommended budget is 6144 MiB memory, 1024 MiB source and 4096 MiB builder storage."}
	}
	for name := range b.Secrets {
		if strings.HasPrefix(name, "RAILPACK_") {
			return &BuildValidationError{"secrets", "Railpack configuration belongs in public arguments; declared secret names must not begin with RAILPACK_."}
		}
		if _, exists := b.Arguments[name]; exists {
			return &BuildValidationError{"arguments", "Railpack public arguments and declared secrets must use different names."}
		}
	}
	if file := b.Arguments["RAILPACK_CONFIG_FILE"]; file != "" && (file == "." || !ValidContext(file)) {
		return &BuildValidationError{"arguments", "RAILPACK_CONFIG_FILE must be a context-relative file path."}
	}
	return nil
}
