// SPDX-License-Identifier: AGPL-3.0-only

package durableexecution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const railpackFrontend = "ghcr.io/railwayapp/railpack-frontend:v0.43.0@sha256:355ef61fae6f2551103cd8b10c710cb07167dd353d561f15c69b8d7ac1c676cc"

func exerciseRailpack(t *testing.T, h *runtimeHarness, r *buildRegistry, pki buildCertificates, network string) {
	t.Helper()
	source := filepath.Join(h.dir, "railpack-source")
	planDir := filepath.Join(h.dir, "railpack-plan")
	for _, dir := range []string{source, planDir} {
		if e := os.Mkdir(dir, 0700); e != nil {
			t.Fatal(e)
		}
	}
	buildWrite(t, filepath.Join(source, "start.sh"), []byte("#!/bin/sh\nprintf 'railpack fixture\\n'\n"))
	buildWrite(t, filepath.Join(source, ".dockerignore"), []byte(".git\n.env*\n"))
	plan := filepath.Join(planDir, "railpack-plan.json")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	out, e := buildExec(ctx, buildEnv(r.builderConfig), "railpack", "prepare", source, "--plan-out", plan, "--info-out", filepath.Join(planDir, "info.json"))
	if e != nil {
		t.Fatalf("Railpack prepare: %v: %s", e, out)
	}
	data, e := os.ReadFile(plan)
	if e != nil {
		t.Fatal(e)
	}
	var tree any
	if e = json.Unmarshal(data, &tree); e != nil {
		t.Fatal(e)
	}
	images := map[string]string{
		"ghcr.io/railwayapp/railpack-builder:mise-2026.10.14": "ghcr.io/railwayapp/railpack-builder:mise-2026.10.14@sha256:03df9e9aa1e786c2e0dd12c5409acad3bbd5a0fa8a75c1117c7ddab5c0c3fde4",
		"ghcr.io/railwayapp/railpack-runtime:mise-2026.10.14": "ghcr.io/railwayapp/railpack-runtime:mise-2026.10.14@sha256:02820533713338569ae4d4f0df986c3e7cc6e93457fd06fbb0d2cfa86fba9cdb",
	}
	count := 0
	var pin func(any) error
	pin = func(node any) error {
		switch n := node.(type) {
		case map[string]any:
			for key, value := range n {
				if key == "image" {
					name, ok := value.(string)
					pinned, found := images[name]
					if !ok || !found {
						return errors.New("unrecognized Railpack base image")
					}
					n[key] = pinned
					count++
				} else if e := pin(value); e != nil {
					return e
				}
			}
		case []any:
			for _, value := range n {
				if e := pin(value); e != nil {
					return e
				}
			}
		}
		return nil
	}
	if e := pin(tree); e != nil || count != 2 {
		t.Fatal("Railpack image inputs changed:", e)
	}
	hostWriteJSON(t, plan, tree)
	worker := startBuildWorker(t, h, r, pki, network, "railpack-"+h.run, false, 3072)
	image := "plat5-" + h.run + ":railpack"
	archive := filepath.Join(h.dir, "railpack.tar")
	out, e = worker.command(ctx, r.builderConfig, "build", "--frontend", "gateway.v0", "--local", "context="+source, "--local", "dockerfile="+planDir, "--opt", "source="+railpackFrontend, "--opt", "platform=linux/"+runtime.GOARCH, "--output", "type=docker,name="+image+",dest="+archive, "--progress", "plain")
	if e != nil {
		t.Fatalf("Railpack image build: %v: %s", e, out)
	}
	h.command(t, nil, "load", "--input", archive)
	h.trackImage(t, image)
	output := h.command(t, nil, "run", "--rm", "--label", hostOwnerLabel+"="+h.run, "--label", hostProjectLabel+"=builds", image)
	if !bytes.Equal(output, []byte("railpack fixture\n")) {
		t.Fatalf("Railpack runtime returned %q", output)
	}
	actual := strings.TrimSpace(string(h.command(t, nil, "image", "inspect", "--format", "{{.Architecture}}", image)))
	if actual != runtime.GOARCH {
		t.Fatal("Railpack target architecture mismatch")
	}
	t.Log("Railpack 0.43.0 shell prepare, pinned frontend/base images, build and runtime passed")
}
