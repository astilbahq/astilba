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

// authorizationFailure marks a failed read, or a refusal that is not about access, during the
// validator's security check, whose handler otherwise answers every error as a refusal or an
// invalid request.
type authorizationFailure struct{ error }

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
	var purpose, state string
	// This read grants no tenant access; handlers still establish membership and
	// lock workspace state. Purpose is immutable, so it cannot race that admission.
	// State can change, so writes that matter check it again under the workspace lock.
	err = s.Store.Pool.QueryRow(ctx, "SELECT e.purpose,e.state FROM environments e JOIN memberships m ON m.workspace_id=e.workspace_id AND m.account_id=$4 WHERE e.workspace_id=$1 AND e.project_id=$2 AND e.id=$3", ws, pid, env, principal(ctx).Account.ID).Scan(&purpose, &state)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.NotFound
	}
	if err != nil {
		return authorizationFailure{err}
	}
	op := strings.ToLower(in.Route.Operation.OperationID)
	// A retiring or retired environment is read-only. A retired preview template still cleans up
	// its previews; the service admits only that action, under the workspace lock.
	if state != "active" && op != "retireenvironment" && op != "restoreenvironment" && (op != "actonpreview" || purpose != "preview_template") {
		return authorizationFailure{domain.EnvironmentInactive}
	}
	if purpose == "standard" {
		return nil
	}
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
	case "updatedraft", "updatelayout", "createsecret", "createsecretversion", "createbuild", "getserviceoutput", "resolveserviceoutput", "exportenvironment", "exportenvironmentarchive", "savepreviewpolicy", "actonpreview", "createautomationcredential", "revokeautomationcredential", "retireenvironment", "restoreenvironment":
		return nil
	default:
		return domain.Forbidden
	}
}
