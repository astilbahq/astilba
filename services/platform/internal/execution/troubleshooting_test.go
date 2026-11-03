package execution

import (
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
)

// A session must bind to the host that actually runs the requested instance.
func TestTroubleshootingServerResolvesTheInstanceHost(t *testing.T) {
	primary, remote, placed, single, replicated := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	spec := domain.Specification{
		Applications: []domain.Application{{ID: placed}, {ID: single}, {ID: replicated, Replicas: 3}},
		Placement:    &domain.Placement{Applications: []domain.ApplicationPlacement{{ApplicationID: placed, Targets: []domain.PlacementTarget{{ServerID: primary, Count: 1}, {ServerID: remote, Count: 2}}}}},
	}
	for name, test := range map[string]struct {
		app      uuid.UUID
		server   *uuid.UUID
		instance int
		want     uuid.UUID
	}{
		"placed instance on another host": {placed, &remote, 2, remote},
		"placed instance on the primary":  {placed, &primary, 1, primary},
		"placed instance beyond its host": {placed, &primary, 2, uuid.Nil},
		"placed needs a host":             {placed, nil, 1, uuid.Nil},
		"placed elsewhere":                {placed, &replicated, 1, uuid.Nil},
		"unplaced on the primary":         {single, nil, 1, primary},
		"unplaced names the primary":      {single, &primary, 1, primary},
		"unplaced names another host":     {single, &remote, 1, uuid.Nil},
		"replica in range":                {replicated, nil, 3, primary},
		"replica out of range":            {replicated, nil, 4, uuid.Nil},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := troubleshootingServer(spec, primary, api.OpenTroubleshootingSession{ApplicationId: test.app, ServerId: test.server, Instance: test.instance})
			if got != test.want || (err != nil) != (test.want == uuid.Nil) {
				t.Fatalf("server %s, error %v; want %s", got, err, test.want)
			}
		})
	}
}
