package httpapi

import (
	"encoding/json/v2"
	"net/http"
	"strings"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// secretAccessPage is how many records one page shows.
const secretAccessPage = 100

// installationMaintenance is what owners see as the reason for an operator's record. The
// operator's reference, and what they wrote, stay with the installation's own key events.
const installationMaintenance = "installation-key-maintenance"

// ListSecretAccess shows owners every time management was given the keys to the workspace's
// stored values, with names for what still exists.
func (s *Server) ListSecretAccess(w http.ResponseWriter, r *http.Request, ws uuid.UUID, params api.ListSecretAccessParams) {
	s.scope(w, r, ws, store.WorkspaceAccess{Owner: true}, func(q *db.Queries, _ store.Workspace) (any, error) {
		ctx := r.Context()
		rows, e := q.ListSecretAccess(ctx, db.ListSecretAccessParams{WorkspaceID: &ws, Before: params.Before, PageSize: secretAccessPage + 1})
		if e != nil {
			return nil, e
		}
		out := api.SecretAccessPage{Records: []api.SecretAccessRecord{}}
		if len(rows) > secretAccessPage {
			rows = rows[:secretAccessPage]
			out.Next = &rows[secretAccessPage-1].ID
		}
		items := make([][]api.SecretAccessItem, len(rows))
		ids := []uuid.UUID{}
		for i, row := range rows {
			if e = json.Unmarshal(row.Items, &items[i]); e != nil {
				return nil, e
			}
			for _, item := range items[i] {
				if item.Id != nil {
					ids = append(ids, *item.Id)
				}
			}
		}
		names := map[string]string{}
		if len(ids) > 0 {
			found, e := q.SecretAccessItemNames(ctx, db.SecretAccessItemNamesParams{WorkspaceID: ws, Ids: ids})
			if e != nil {
				return nil, e
			}
			for _, n := range found {
				if n.Name != "" {
					names[n.Kind+":"+n.ID.String()] = n.Name
				}
			}
		}
		for i, row := range rows {
			for j, item := range items[i] {
				if item.Id == nil {
					continue
				}
				if name, ok := names[item.Kind+":"+item.Id.String()]; ok {
					items[i][j].Name = &name
				}
			}
			record := api.SecretAccessRecord{Id: row.ID, At: row.At, Purpose: api.SecretAccessRecordPurpose(row.Purpose), Principal: secretAccessPrincipal(row), OperationId: row.OperationID, Items: items[i], ItemCount: int(row.ItemCount), Reason: row.Reason, Outcome: api.SecretAccessRecordOutcome(row.Outcome)}
			if row.PrincipalKind == "operator" {
				record.Reason = installationMaintenance
			}
			out.Records = append(out.Records, record)
		}
		return out, nil
	})
}

func secretAccessPrincipal(row db.ListSecretAccessRow) api.SecretAccessPrincipal {
	p := api.SecretAccessPrincipal{Kind: api.SecretAccessPrincipalKind(row.PrincipalKind), AccountId: row.AccountID, AutomationId: row.AutomationID, ServerId: row.ServerID}
	if row.PrincipalKind == "operator" {
		p.Kind = api.SecretAccessPrincipalKindInstallation
	}
	p.AccountEmail = optional(row.AccountEmail)
	p.AutomationName = optional(row.AutomationName)
	p.ServerName = optional(row.ServerName)
	p.Job = optional(row.Job)
	if row.AgentInstance.Valid {
		// The instance names a key or credential by its hash: a prefix is enough to tell two apart.
		kind, hash, _ := strings.Cut(row.AgentInstance.String, ":")
		instance := kind + ":" + hash[:min(len(hash), 16)]
		p.Instance = &instance
	}
	return p
}

func optional(v pgtype.Text) *string {
	if !v.Valid {
		return nil
	}
	return &v.String
}
