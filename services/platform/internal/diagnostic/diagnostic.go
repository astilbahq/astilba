// Package diagnostic turns a failure into log fields that are safe to keep.
package diagnostic

import (
	"context"
	"errors"
	"log/slog"
	"net"

	"github.com/jackc/pgx/v5/pgconn"
)

// Failure names the kind of failure without its text, which can carry stored values.
func Failure(err error) slog.Attr {
	category := "internal"
	var database *pgconn.PgError
	var network net.Error
	switch {
	case errors.Is(err, context.Canceled):
		category = "context_canceled"
	case errors.Is(err, context.DeadlineExceeded):
		category = "context_deadline_exceeded"
	case errors.As(err, &database):
		// PostgreSQL text fields can contain query values; only retain its code.
		if validSQLState(database.Code) {
			return slog.Group("error", "category", "database", "sqlstate", database.Code)
		}
		category = "database"
	case errors.As(err, &network):
		category = "network"
		if network.Timeout() {
			category = "network_timeout"
		}
	}
	return slog.Group("error", "category", category)
}

func validSQLState(code string) bool {
	if len(code) != 5 {
		return false
	}
	for _, c := range code {
		if !(c >= '0' && c <= '9' || c >= 'A' && c <= 'Z') {
			return false
		}
	}
	return true
}
