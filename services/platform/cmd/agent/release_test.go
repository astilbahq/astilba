package main

import (
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/agentrelease"
)

// Every release build embeds this file as its minimum version, so it must be one.
func TestMinimumVersionFileIsAVersion(t *testing.T) {
	if v := strings.TrimSpace(sourceMinimumVersion); !agentrelease.ValidVersion(v) {
		t.Fatalf("cmd/agent/minimum-version holds %q, not a semantic version", v)
	}
}
