package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// authorizationFailure marks a failed read during the validator's security check, whose
// handler otherwise answers every error as a refusal or an invalid request.
type authorizationFailure struct{ error }

func (f authorizationFailure) Unwrap() error { return f.error }

func (s *Server) authorizeEnvironmentPurpose(ctx context.Context, in *openapi3filter.RequestValidationInput) error {
	if in.Request.Method == http.MethodGet || in.Request.Method == http.MethodHead {
		return nil
	}
	path := in.PathParams
	env, err := uuid.Parse(path["environmentId"])
	if err != nil {
		return nil
	}
	ws, err := uuid.Parse(path["workspaceId"])
	if err != nil {
		return domain.Forbidden
	}
	pid, err := uuid.Parse(path["projectId"])
	if err != nil {
		return domain.Forbidden
	}
	var purpose string
	// This read grants no tenant access; handlers still establish membership and
	// lock workspace state. Purpose is immutable, so it cannot race that admission.
	err = s.Store.Pool.QueryRow(ctx, "SELECT e.purpose FROM environments e JOIN memberships m ON m.workspace_id=e.workspace_id AND m.account_id=$4 WHERE e.workspace_id=$1 AND e.project_id=$2 AND e.id=$3", ws, pid, env, principal(ctx).Account.ID).Scan(&purpose)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.NotFound
	}
	if err != nil {
		return authorizationFailure{err}
	}
	if purpose == "standard" {
		return nil
	}
	op := strings.ToLower(in.Route.Operation.OperationID)
	if op == "opentroubleshootingsession" {
		return nil // these environments can never enable sessions, and admission audits the refusal
	}
	if purpose == "preview" {
		switch op {
		case "createautomationcredential", "revokeautomationcredential", "refreshruntime":
			return nil
		default:
			return domain.Forbidden
		}
	}
	switch op {
	case "updatedraft", "updatelayout", "createsecret", "createsecretversion", "createbuild", "getserviceoutput", "resolveserviceoutput", "exportenvironment", "exportenvironmentarchive", "savepreviewpolicy", "actonpreview", "createautomationcredential", "revokeautomationcredential":
		return nil
	default:
		return domain.Forbidden
	}
}
