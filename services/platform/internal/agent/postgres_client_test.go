package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/domain"
)

// PLAT-388. A database helper runs only one of Astilba's pinned PostgreSQL images, so a database
// on a retired pin, or any other image, is refused before Docker is asked to run anything.
func TestDatabaseHelperRunsOnlyPinnedImages(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	e := &Engine{}
	for _, image := range []string{"postgres:17.9-alpine3.22", "postgres:18.4-alpine3.23", domain.PostgresImage + "-other", "registry.example/postgres@sha256:00"} {
		if _, err := e.postgresHelper(ctx, "helper", nil, "source", image, "password"); !errors.Is(err, errUnsupportedPostgresImage) {
			t.Fatalf("a helper from %s was not refused: %v", image, err)
		}
	}
	// A pinned image gets past the refusal to the unbounded-command check, before Docker.
	if _, err := e.postgresHelper(context.Background(), "helper", nil, "source", domain.Postgres17Image, "password"); err == nil || errors.Is(err, errUnsupportedPostgresImage) {
		t.Fatalf("a helper from a pinned image was refused as unsupported: %v", err)
	}
}
