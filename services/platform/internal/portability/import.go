package portability

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
)

func decodeArchive(raw []byte) (Archive, error) {
	var archive Archive
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&archive) != nil || d.Decode(new(any)) != io.EOF || (archive.Version != 1 && archive.Version != 2) || archive.Workspace == uuid.Nil {
		return archive, ErrArchive
	}
	if archive.Version == 1 {
		if err := normalizeLegacyArchive(&archive); err != nil {
			return archive, err
		}
	}
	if len(archive.Tables) != len(tables) {
		return archive, ErrArchive
	}
	name, e := domain.Name(archive.Name)
	if e != nil || name != archive.Name {
		return archive, ErrArchive
	}
	count := 0
	for _, table := range tables {
		rows, ok := archive.Tables[table.Name]
		if !ok || rows == nil {
			return archive, ErrArchive
		}
		columns := strings.Split(table.Columns, ",")
		for _, row := range rows {
			count++
			if count > 10000 || len(row) != len(columns) {
				return archive, ErrArchive
			}
			for _, column := range columns {
				if _, ok := row[column]; !ok {
					return archive, ErrArchive
				}
			}
			ws, e := rowString(row, "workspace_id")
			if e != nil || ws != archive.Workspace.String() {
				return archive, ErrArchive
			}
		}
	}
	// Foreign keys on global IDs must refer to records within this archive; a
	// destination row in another workspace must never satisfy an imported link.
	refs := []struct{ table, column, target string }{
		{"route_claims", "server_id", "servers"},
		{"execution_operations", "plan_id", "execution_plans"},
		{"execution_operations", "visual_plan_id", "visual_plans"},
		{"visual_requests", "operation_id", "execution_operations"},
		{"visual_plans", "id", "visual_requests"},
		{"visual_runtime", "plan_id", "visual_plans"},
		{"pipeline_runs", "id", "execution_operations"},
		{"build_artifacts", "id", "pipeline_runs"},
		{"git_submissions", "operation_id", "pipeline_runs"},
		{"pipeline_auto_deployments", "id", "pipeline_runs"},
		{"pipeline_auto_deployments", "operation_id", "execution_operations"},
		{"pipeline_auto_deployments", "plan_id", "visual_plans"},
		{"preview_requests", "build_id", "execution_operations"},
		{"preview_requests", "operation_id", "execution_operations"},
		{"preview_requests", "plan_id", "visual_requests"},
	}
	for _, ref := range refs {
		ids := map[string]bool{}
		for _, row := range archive.Tables[ref.target] {
			id, e := rowString(row, "id")
			if e != nil {
				return archive, e
			}
			ids[id] = true
		}
		for _, row := range archive.Tables[ref.table] {
			if string(row[ref.column]) == "null" {
				continue
			}
			id, e := rowString(row, ref.column)
			if e != nil || !ids[id] {
				return archive, ErrArchive
			}
		}
	}
	return archive, nil
}

func (s *Service) Import(ctx context.Context, p store.Principal, passphrase string, envelope []byte, invitation string) (store.Workspace, error) {
	var out store.Workspace
	if !p.Recent {
		return out, domain.RecentRequired
	}
	if s.Store.Hosted && (!p.MFA || p.Account.EmailVerifiedAt == nil) {
		return out, domain.Forbidden
	}
	plaintext, e := Open(ctx, passphrase, envelope)
	if e != nil {
		return out, e
	}
	defer clear(plaintext)
	archive, e := decodeArchive(plaintext)
	if e != nil {
		return out, e
	}
	if archive.RequireMFA && !p.MFA {
		return out, domain.MFARequired
	}
	tx, e := s.Store.Pool.Begin(ctx)
	if e != nil {
		return out, e
	}
	defer tx.Rollback(ctx)
	if s.Store.Hosted {
		email, e := s.Store.ConsumeWorkspaceInvitation(ctx, tx, invitation, time.Now())
		if e != nil {
			return out, e
		}
		if email != p.Account.Email {
			return out, domain.Forbidden
		}
	}
	var version int64
	if e = tx.QueryRow(ctx, "SELECT auth_version FROM accounts WHERE id=$1 FOR UPDATE", p.Account.ID).Scan(&version); e != nil {
		return out, e
	}
	if version != p.Account.AuthVersion {
		return out, domain.Unauthenticated
	}
	q := db.New(tx)
	if e = q.InsertWorkspace(ctx, db.InsertWorkspaceParams{ID: archive.Workspace, Name: archive.Name}); e != nil {
		return out, store.Normalize(e)
	}
	if e = q.InsertMembership(ctx, db.InsertMembershipParams{WorkspaceID: archive.Workspace, AccountID: p.Account.ID, Role: "owner"}); e != nil {
		return out, e
	}
	if _, e = tx.Exec(ctx, "UPDATE workspaces SET require_mfa=$2 WHERE id=$1", archive.Workspace, archive.RequireMFA); e != nil {
		return out, e
	}
	if s.Store.Hosted {
		if e = q.InsertHostedSubscription(ctx, db.InsertHostedSubscriptionParams{WorkspaceID: archive.Workspace, ServerLimit: int32(s.Store.Hosting.ServerLimit)}); e != nil {
			return out, e
		}
	}
	applied := map[string]json.RawMessage{}
	visualPlans := map[string]json.RawMessage{}
	for _, table := range tables {
		for _, row := range archive.Tables[table.Name] {
			for _, actor := range []string{"author_id", "actor_id", "policy_actor"} {
				if _, ok := row[actor]; ok {
					setValue(row, actor, p.Account.ID)
				}
			}
			switch table.Name {
			case "preview_policies":
				setValue(row, "enabled", false)
				setValue(row, "auth_version", p.Account.AuthVersion)
			case "preview_requests":
				var state string
				if json.Unmarshal(row["state"], &state) != nil {
					return out, ErrArchive
				}
				if state != "deleted" {
					setValue(row, "state", "blocked")
					setValue(row, "note", "Imported preview requires renewed owner approval.")
				}
				setValue(row, "approved_head", "")
				setValue(row, "approved_repository", 0)
				setValue(row, "approved_generation", 0)
				setValue(row, "cleanup_requested", false)
				setValue(row, "resume_after_cancel", false)
				setValue(row, "cleanup_actor", nil)
				setValue(row, "cleanup_auth_version", nil)
				setValue(row, "cleanup_controller", nil)
			case "servers":
				var old int64
				if json.Unmarshal(row["controller_generation"], &old) != nil || old < 0 || old == math.MaxInt64 {
					return out, ErrArchive
				}
				setValue(row, "controller_generation", old+1)
				setValue(row, "awaiting_transfer", true)
				setValue(row, "rotation_requested", false)
			case "environments":
				id, e := rowString(row, "id")
				if e != nil {
					return out, e
				}
				applied[id] = row["applied_revision_id"]
				setValue(row, "applied_revision_id", nil)
			case "execution_operations":
				id, e := rowString(row, "id")
				if e != nil {
					return out, e
				}
				visualPlans[id] = row["visual_plan_id"]
				setValue(row, "visual_plan_id", nil)
				var state string
				if json.Unmarshal(row["state"], &state) != nil {
					return out, ErrArchive
				}
				if state != "succeeded" && state != "failed" && state != "cancelled" {
					setValue(row, "state", "cancelled")
					setValue(row, "cancel_requested", true)
					setValue(row, "note", "Imported history; pending execution was fenced.")
				}
			case "pipeline_auto_deployments":
				setValue(row, "state", "blocked")
				setValue(row, "note", "Imported policy requires owner approval.")
			case "git_sources", "build_targets":
				if table.Name == "git_sources" {
					setValue(row, "status", "approval_required")
				}
				var config map[string]json.RawMessage
				if json.Unmarshal(row["configuration"], &config) != nil || config == nil {
					return out, ErrArchive
				}
				setValue(config, "autoDeploy", false)
				if table.Name == "build_targets" {
					setValue(config, "autoBuild", false)
				}
				setValue(row, "configuration", config)
			}
			if e = s.transformCredential(table.Name, row, archive.Workspace, false); e != nil {
				return out, e
			}
			raw, e := json.Marshal(row)
			if e != nil {
				return out, e
			}
			// Table and column names come exclusively from the compiled format catalog.
			_, e = tx.Exec(ctx, fmt.Sprintf("INSERT INTO %s (%s) SELECT %s FROM jsonb_populate_record(NULL::%s,$1)", table.Name, table.Columns, table.Columns, table.Name), raw)
			if e != nil {
				return out, store.Normalize(e)
			}
		}
	}
	if _, e = tx.Exec(ctx, "UPDATE execution_operations SET alert_observed=true WHERE workspace_id=$1", archive.Workspace); e != nil {
		return out, e
	}
	for id, raw := range applied {
		var value *uuid.UUID
		if e = json.Unmarshal(raw, &value); e != nil {
			return out, ErrArchive
		}
		if _, e = tx.Exec(ctx, "UPDATE environments SET applied_revision_id=$3 WHERE workspace_id=$1 AND id=$2", archive.Workspace, id, value); e != nil {
			return out, store.Normalize(e)
		}
	}
	for id, raw := range visualPlans {
		var value *uuid.UUID
		if e = json.Unmarshal(raw, &value); e != nil {
			return out, ErrArchive
		}
		if _, e = tx.Exec(ctx, "UPDATE execution_operations SET visual_plan_id=$3 WHERE workspace_id=$1 AND id=$2", archive.Workspace, id, value); e != nil {
			return out, store.Normalize(e)
		}
	}
	if e = store.Audit(ctx, q, p, archive.Workspace, "workspace.import_owner_attribution", archive.Workspace); e != nil {
		return out, e
	}
	out = store.Workspace{ID: archive.Workspace, Name: archive.Name, Role: "owner", RequireMFA: archive.RequireMFA}
	if e = tx.Commit(ctx); e != nil {
		return out, store.Normalize(e)
	}
	return out, nil
}

func normalizeLegacyArchive(a *Archive) error {
	if len(a.Tables) != len(legacyTables) {
		return ErrArchive
	}
	for _, t := range legacyTables {
		rows, ok := a.Tables[t.Name]
		if !ok || rows == nil {
			return ErrArchive
		}
		columns := strings.Split(t.Columns, ",")
		for _, row := range rows {
			if len(row) != len(columns) {
				return ErrArchive
			}
			for _, column := range columns {
				if _, ok := row[column]; !ok {
					return ErrArchive
				}
			}
			if t.Name == "environments" {
				setValue(row, "purpose", "standard")
			}
			if t.Name == "servers" {
				setValue(row, "preview_only", false)
			}
		}
	}
	a.Tables["preview_policies"] = []Row{}
	a.Tables["preview_requests"] = []Row{}
	a.Version = 2
	return nil
}
