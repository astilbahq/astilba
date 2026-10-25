package domain

import (
	"slices"
	"time"

	"github.com/google/uuid"
)

const (
	EnvironmentRead    = "environment:read"
	ConfigurationWrite = "configuration:write"
	DeploymentPlan     = "deployment:plan"
	DeploymentApply    = "deployment:apply"
	BuildRun           = "build:run"
)

type AutomationGrant struct {
	ID, WorkspaceID, ProjectID, EnvironmentID uuid.UUID
	Capabilities                              []string
	ExpiresAt                                 time.Time
}

func (g AutomationGrant) Allows(capabilities ...string) bool {
	for _, capability := range capabilities {
		if slices.Contains(g.Capabilities, capability) {
			return true
		}
	}
	return false
}

func ValidateAutomation(capabilities []string, expires, now time.Time) error {
	if len(capabilities) == 0 || len(capabilities) > 5 || expires.Before(now.Add(time.Minute)) || expires.After(now.Add(90*24*time.Hour)) {
		return Invalid("capabilities or expiresAt")
	}
	seen := map[string]bool{}
	for _, c := range capabilities {
		switch c {
		case EnvironmentRead, ConfigurationWrite, DeploymentPlan, DeploymentApply, BuildRun:
		default:
			return Invalid("capabilities")
		}
		if seen[c] {
			return Invalid("capabilities")
		}
		seen[c] = true
	}
	return nil
}
