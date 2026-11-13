package execution

import (
	"context"
	"errors"
	"slices"
	"strconv"

	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/jackc/pgx/v5"
)

// refusedChange runs a background writer's change to an environment's configuration in a savepoint
// of tx. A change the configuration refuses, such as a revision over the size limit, would be
// refused again on every retry. It is rolled back, leaving tx usable, and refusedChange returns why
// in a sentence for the caller's note, so the job can stop and say so instead of failing until its
// retries run out. Other errors are returned as they are.
//
// The reason never quotes the configuration: a refusal's fields and Compose diagnostics can name
// services, files and values.
func refusedChange(ctx context.Context, tx pgx.Tx, change func(pgx.Tx) error) (string, error) {
	savepoint, err := tx.Begin(ctx)
	if err != nil {
		return "", err
	}
	err = change(savepoint)
	if err == nil {
		return "", savepoint.Commit(ctx)
	}
	reason := refusal(err)
	if reason == "" {
		return "", err
	}
	if err = savepoint.Rollback(ctx); err != nil {
		return "", err
	}
	return reason, nil
}

// refusal is why a change was refused, or "" for an error that may pass on a retry. PostgreSQL's
// refusal of text it cannot store counts, through store.Normalize, since a retry writes it again.
func refusal(err error) string {
	err = store.Normalize(err)
	var invalid *domain.Error
	var diagnostic *composeproject.Diagnostic
	switch {
	case errors.Is(err, domain.EnvironmentInactive):
		return "the environment is retired or being retired. Restore it before changing it."
	case errors.Is(err, domain.SpecificationTooLarge):
		return "the configuration would be over the " + strconv.Itoa(domain.SpecificationLimit/1024) + " KiB limit."
	case errors.As(err, &diagnostic):
		return "the Compose files could not be changed automatically. Change them in the file editor."
	case errors.As(err, &invalid) && slices.Contains(invalid.Fields, postgresReleasesField):
		// The configuration is fine; the server's agent needs updating, and the message says how.
		return invalid.Message
	case errors.As(err, &invalid) && invalid.Code == "invalid_input":
		return "the configuration would be invalid."
	}
	return ""
}
