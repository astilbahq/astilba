package agent

import (
	"encoding/json"
	"strings"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
)

// releaseCommandEnvironment returns the variables Compose would give a service, with
// Compose's $$ escapes undone because a release command starts without Compose.
func releaseCommandEnvironment(p deployment.Payload, service string) ([]string, error) {
	body, err := deployment.Compose(p.Plan, p.Secrets, false)
	if err != nil {
		return nil, err
	}
	var rendered struct {
		Services map[string]struct {
			Environment map[string]string `json:"environment"`
		} `json:"services"`
	}
	if err = json.Unmarshal(body, &rendered); err != nil {
		return nil, err
	}
	env := []string{}
	for key, value := range rendered.Services[service].Environment {
		env = append(env, key+"="+strings.ReplaceAll(value, "$$", "$"))
	}
	return env, nil
}
