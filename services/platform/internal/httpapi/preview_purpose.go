package httpapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/google/uuid"
)

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
	if err = s.Store.Pool.QueryRow(ctx, "SELECT e.purpose FROM environments e JOIN memberships m ON m.workspace_id=e.workspace_id AND m.account_id=$4 WHERE e.workspace_id=$1 AND e.project_id=$2 AND e.id=$3", ws, pid, env, principal(ctx).Account.ID).Scan(&purpose); err != nil {
		return domain.NotFound
	}
	if purpose == "standard" {
		return nil
	}
	op := strings.ToLower(in.Route.Operation.OperationID)
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
