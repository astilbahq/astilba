package portability

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"strings"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
)

func decodeArchive(raw []byte) (Archive, error) {
	return decodeArchiveMode(raw, false)
}

func decodeArchiveMode(raw []byte, recovery bool) (Archive, error) {
	var archive Archive
	if json.Unmarshal(raw, &archive, json.RejectUnknownMembers(true)) != nil || archive.Version < 1 || archive.Version > ArchiveVersion || archive.Workspace == uuid.Nil {
		return archive, ErrArchive
	}
	if archive.Version == 1 {
		if err := normalizeLegacyArchive(&archive); err != nil {
			return archive, err
		}
	}
	if archive.Version < 5 {
		if err := normalizePreNetworkArchive(&archive); err != nil {
			return archive, err
		}
	}
	if archive.Version < 6 {
		for _, row := range archive.Tables["environments"] {
			if _, exists := row["runtime_operation_id"]; exists {
				return archive, ErrArchive
			}
			setValue(row, "runtime_operation_id", nil)
		}

		for _, row := range archive.Tables["execution_operations"] {
			for _, field := range []string{"runtime_outcome", "runtime_plan_id"} {
				if _, exists := row[field]; exists {
					return archive, ErrArchive
				}
				setValue(row, field, nil)
			}
		}
	}
	// Version 7 carries the submissions management makes. An older archive has only runners'.
	if archive.Version < 7 {
		if _, exists := archive.Tables["source_submissions"]; exists {
			return archive, ErrArchive
		}
		archive.Tables["source_submissions"] = []Row{}
	}
	if archive.Version < 8 {
		// An environment from before retirement is active, and nothing recorded which PostgreSQL
		// major initialised a volume: its next deployment records it.
		fields := map[string]map[string]any{
			"environments":    {"state": "active", "retired_at": nil, "retired_by": nil, "retirement_operation_id": nil, "retired_without_host": false},
			"managed_volumes": {"postgres_major": nil},
		}
		for table, values := range fields {
			for _, row := range archive.Tables[table] {
				for field, value := range values {
					if _, exists := row[field]; exists {
						return archive, ErrArchive
					}
					setValue(row, field, value)
				}
			}
		}
	}
	if len(archive.Tables) != len(tables) {
		return archive, ErrArchive
	}
	if !recovery && len(archive.Tables["cross_host_reservations"]) > 0 {
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
			// An export cannot write NUL, which PostgreSQL does not store, so an archive holding
			// one was not written by an export, and its insert would fail.
			if _, found := domain.NULField(row, ""); found {
				return archive, ErrArchive
			}
			// A recovery checkpoint is management's own record and must stay restorable. Any
			// other archive gets what an export would have written, so retention, which
			// rebuilds rows with logs inside the shared database, only ever handles values
			// whose size agents had to respect.
			if !recovery {
				if e = checkImportedRow(table.Name, row); e != nil {
					return archive, e
				}
				if e = omitExportLogs(table.Name, row); e != nil {
					return archive, e
				}
			}
		}
	}
	if !recovery {
		if e = checkReleasedRoutes(archive); e != nil {
			return archive, e
		}
	}
	// Foreign keys on global IDs must refer to records within this archive; a
	// destination row in another workspace must never satisfy an imported link.
	refs := []struct{ table, column, target string }{
		{"environments", "runtime_operation_id", "execution_operations"},
		{"environments", "retirement_operation_id", "execution_operations"},
		{"backup_cutovers", "source_plan_id", "visual_requests"},
		{"route_claims", "server_id", "servers"},
		{"execution_operations", "parent_id", "execution_operations"},
		{"network_members", "network_id", "workspace_networks"},
		{"network_members", "server_id", "servers"},
		{"network_endpoints", "plan_id", "visual_requests"},
		{"network_plan_endpoints", "plan_id", "visual_requests"},
		{"network_plan_sources", "plan_id", "visual_requests"},
		{"cross_host_operations", "id", "execution_operations"},
		{"cross_host_operations", "plan_id", "visual_requests"},
		{"cross_host_approvals", "parent_id", "execution_operations"},
		{"cross_host_approvals", "upgrade_id", "supported_upgrades"},
		{"cross_host_runtime", "plan_id", "visual_requests"},
		{"cross_host_reservations", "parent_id", "execution_operations"},
		{"execution_operations", "plan_id", "execution_plans"},
		{"execution_operations", "visual_plan_id", "visual_plans"},
		{"execution_operations", "runtime_plan_id", "visual_plans"},
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

// AdmitImport refuses an account that Import would refuse whatever its archive holds, so that
// such a request is refused before its body is read.
func (s *Service) AdmitImport(p store.Principal) error {
	if !p.Recent() {
		return domain.RecentRequired
	}
	if s.Store.Hosted && (!p.MFA || p.Account.EmailVerifiedAt == nil) {
		return domain.Forbidden
	}
	return nil
}

// admitImportTransaction makes the checks Import's transaction makes before it writes, in a
// transaction that never commits: the session is still current and, when hosted, the invitation
// is valid and for this account. The transaction itself decides again.
func (s *Service) admitImportTransaction(ctx context.Context, p store.Principal, invitation string) error {
	tx, e := s.Store.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if s.Store.Hosted {
		email, e := s.Store.ConsumeWorkspaceInvitation(ctx, tx, invitation, time.Now())
		if e != nil {
			return e
		}
		if email != p.Account.Email {
			return domain.Forbidden
		}
	}
	return store.AdmitBrowserSession(ctx, db.New(tx), p)
}

func (s *Service) Import(ctx context.Context, p store.Principal, passphrase string, envelope []byte, invitation string) (_ store.Workspace, failure error) {
	var out store.Workspace
	if e := s.AdmitImport(p); e != nil {
		return out, e
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
	// The credentials are sealed again for the workspace this import creates, once the import
	// would be admitted: no record is made for a session or invitation it would refuse.
	if e := s.admitImportTransaction(ctx, p, invitation); e != nil {
		return out, e
	}
	keys, e := s.Store.Broker.Begin(ctx, secretbroker.Request{Workspace: archive.Workspace, Purpose: secretbroker.Seal, Principal: p.Secrets(), Kinds: secretbroker.CredentialKinds, NewWorkspace: true})
	if e != nil {
		return out, e
	}
	defer func() { keys.Close(ctx, secretbroker.Done(failure)) }()
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
	q := db.New(tx)
	if _, e = q.LockAccount(ctx, p.Account.ID); e != nil {
		return out, e
	}
	// Opening the archive is slow, and the lock may have waited behind a credential change,
	// so the session may have ended or stopped being recent by now. Refuse before holding
	// the account through the insert.
	if e = store.AdmitBrowserSession(ctx, q, p); e != nil {
		return out, e
	}
	if !p.Recent() {
		return out, domain.RecentRequired
	}
	if e = q.InsertWorkspace(ctx, db.InsertWorkspaceParams{ID: archive.Workspace, Name: archive.Name}); e != nil {
		return out, store.Normalize(e)
	}
	if e = q.InsertMembership(ctx, db.InsertMembershipParams{WorkspaceID: archive.Workspace, AccountID: p.Account.ID, Role: "owner"}); e != nil {
		return out, e
	}
	if e = q.SetMFAPolicy(ctx, db.SetMFAPolicyParams{ID: archive.Workspace, RequireMfa: archive.RequireMFA}); e != nil {
		return out, e
	}
	if s.Store.Hosted {
		if e = q.InsertHostedSubscription(ctx, db.InsertHostedSubscriptionParams{WorkspaceID: archive.Workspace, ServerLimit: s.Store.Hosting.ServerLimit}); e != nil {
			return out, e
		}
	}
	applied := map[string]jsontext.Value{}
	appliedOperations := map[string]jsontext.Value{}
	visualPlans := map[string]jsontext.Value{}
	for _, table := range tables {
		for _, row := range archive.Tables[table.Name] {
			for _, actor := range []string{"author_id", "actor_id", "policy_actor"} {
				if _, ok := row[actor]; ok {
					setValue(row, actor, p.Account.ID)
				}
			}
			switch table.Name {
			case "source_submissions":
				// Its job stays behind, so an attempt still queued would never be made.
				var state string
				if json.Unmarshal(row["state"], &state) != nil {
					return out, ErrArchive
				}
				if state == "queued" {
					setValue(row, "state", "failed")
					setValue(row, "note", "Imported before this attempt was made. Submit the draft again.")
					setValue(row, "finished_at", time.Now().UTC())
				}
			case "network_members":
				setValue(row, "acknowledged_generation", 0)
				setValue(row, "note", "Imported private links require renewed owner approval after host transfer.")
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
				// The archive does not get to choose its place in the shared sweep.
				setValue(row, "next_check_at", time.Now().UTC().Add(5*time.Minute))
			case "servers":
				var old int64
				if json.Unmarshal(row["controller_generation"], &old) != nil {
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
				// The account that retired it is not in this installation; the importer answers for it.
				if string(row["retired_by"]) != "null" {
					setValue(row, "retired_by", p.Account.ID)
				}
				applied[id] = row["applied_revision_id"]
				appliedOperations[id] = row["runtime_operation_id"]
				setValue(row, "applied_revision_id", nil)
				setValue(row, "runtime_operation_id", nil)
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
				var config map[string]jsontext.Value
				if json.Unmarshal(row["configuration"], &config) != nil || config == nil {
					return out, ErrArchive
				}
				setValue(config, "autoDeploy", false)
				if table.Name == "build_targets" {
					setValue(config, "autoBuild", false)
				}
				setValue(row, "configuration", config)
			}
			if e = transformCredential(keys, table.Name, row, false); e != nil {
				return out, e
			}
			raw, e := stablejson.Marshal(row)
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
		var operation *uuid.UUID
		if e = json.Unmarshal(appliedOperations[id], &operation); e != nil {
			return out, ErrArchive
		}
		if _, e = tx.Exec(ctx, "UPDATE environments SET applied_revision_id=$3,runtime_operation_id=$4 WHERE workspace_id=$1 AND id=$2", archive.Workspace, id, value, operation); e != nil {
			return out, store.Normalize(e)
		}
	}
	if e = backfillPostgresMajors(ctx, q, archive.Workspace); e != nil {
		return out, e
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
	// The account lock stops a credential change during the insert, but not a logout.
	if e = store.AdmitBrowserSession(ctx, q, p); e != nil {
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

func normalizePreNetworkArchive(a *Archive) error {
	for _, table := range tables {
		if strings.HasPrefix(table.Name, "network_") || table.Name == "workspace_networks" || strings.HasPrefix(table.Name, "cross_host_") || table.Name == "backup_cutovers" {
			if _, ok := a.Tables[table.Name]; ok {
				return ErrArchive
			}
			a.Tables[table.Name] = []Row{}
		}
	}
	for _, row := range a.Tables["deployment_units"] {
		if _, ok := row["primary_unit"]; ok {
			return ErrArchive
		}
		setValue(row, "primary_unit", true)
	}
	for _, row := range a.Tables["execution_operations"] {
		if _, ok := row["parent_id"]; ok {
			return ErrArchive
		}
		setValue(row, "parent_id", nil)
	}
	return nil
}

// checkReleasedRoutes refuses an archive in which an environment whose server confirmed its
// retirement still claims a hostname or port. That retirement released them for other
// environments, and nothing but a reviewed deployment after a restore claims them again. A
// retirement that could not reach a revoked server keeps what that server may still serve.
func checkReleasedRoutes(a Archive) error {
	released := map[string]bool{}
	for _, row := range a.Tables["environments"] {
		var state string
		var withoutHost bool
		if json.Unmarshal(row["state"], &state) != nil || json.Unmarshal(row["retired_without_host"], &withoutHost) != nil {
			return ErrArchive
		}
		if state == "retired" && !withoutHost {
			id, err := rowString(row, "id")
			if err != nil {
				return err
			}
			released[id] = true
		}
	}
	for _, row := range a.Tables["route_claims"] {
		env, err := rowString(row, "environment_id")
		if err != nil {
			return err
		}
		if released[env] {
			return ErrArchive
		}
	}
	return nil
}
