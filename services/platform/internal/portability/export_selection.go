package portability

import (
	"bytes"
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// exportSelection chooses what a workspace export carries: the workspace's configuration and what
// the app lists, so an imported workspace shows the same lists as its source and an export doesn't
// grow with the workspace's age. That is:
//   - every environment except a retired preview, one no open preview request names, and each
//     carried environment's state rows;
//   - unfinished work and its children, each environment's runtime head and retirement, and the
//     newest 100 operations;
//   - per environment, the 20 releases, 50 revisions, 100 pipeline runs and 100 submissions the app lists;
//   - per preview template, the 100 preview requests the app lists;
//   - per backup policy, the 100 restores onto another server the app lists, and the restore of
//     each carried operation;
//   - the newest 100 audit events and build artifacts;
//   - the build artifacts those configurations use (archivePinnedArtifacts);
//   - everything those rows name, closed over every reference the import checks and every
//     database foreign key, which can bring in a retired preview environment that something
//     carried names;
//   - of each carried secret, its newest 10 versions and current version (keptSecretVersions), and
//     every version a carried row names (keepNamedSecretVersions).
//
// An export is imported as a new workspace and never merged with older copies, so this is safe
// in a way it isn't for a recovery base, which chooses its rows itself (baseSelection).
func exportSelection(ctx context.Context, tx pgx.Tx, ws uuid.UUID) (map[string]string, error) {
	return selectArchive(ctx, tx, ws, []archiveStep{{exportEnvironments, nil}, {exportSeeds, nil}}, archiveStep{keptSecretVersions, []any{SecretVersionsKept, false}})
}

// archiveStep seeds the working set: a statement over $1, the workspace, and its own arguments.
type archiveStep struct {
	sql  string
	args []any
}

// selectArchive runs the seeds, adds the build artifacts their configurations use, closes the
// set over every reference and then runs versions, which chooses secret versions by the carried
// environments. The rows chosen go in a transaction-local table, which the returned filters read.
// A capture that runs again in the same transaction starts from an empty set.
func selectArchive(ctx context.Context, tx pgx.Tx, ws uuid.UUID, seeds []archiveStep, versions archiveStep) (map[string]string, error) {
	// inline-sql: transaction-local working set for an archive's selection
	if _, err := tx.Exec(ctx, `CREATE TEMP TABLE IF NOT EXISTS archive_keep(kind text NOT NULL, id uuid NOT NULL, PRIMARY KEY(kind,id)) ON COMMIT DROP`); err != nil {
		return nil, err
	}
	// inline-sql: transaction-local working set for an archive's selection
	if _, err := tx.Exec(ctx, `TRUNCATE archive_keep`); err != nil {
		return nil, err
	}
	for _, step := range append(seeds, archiveStep{archivePinnedArtifacts, nil}) {
		if _, err := tx.Exec(ctx, step.sql, append([]any{ws}, step.args...)...); err != nil {
			return nil, err
		}
	}
	for {
		tag, err := tx.Exec(ctx, archiveClosure, ws)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			break
		}
	}
	if _, err := tx.Exec(ctx, versions.sql, append([]any{ws}, versions.args...)...); err != nil {
		return nil, err
	}
	return archiveFilters, nil
}

// SecretVersionsKept is how many of each secret's newest versions an archive carries, whatever
// names them (owner decision on PLAT-206). A secret that automation sets on every deploy would
// otherwise grow every export and recovery base by a version each time.
const SecretVersionsKept = 10

// Of each carried secret: its newest $2 versions, newest by creation as LiveSecretVersion orders
// them, erased ones included; its current version, the newest with a value, which a recovery that
// restored newer ones erased can leave older than those; and, when $3, every erased version.
// inline-sql: chooses the secret versions in the archive's transaction-local working set
const keptSecretVersions = `INSERT INTO archive_keep(kind,id) SELECT 'version',x.id FROM (
	SELECT v.id,v.erased_at,row_number() OVER (PARTITION BY v.secret_id ORDER BY v.created_at DESC,v.id DESC) n,
		row_number() OVER (PARTITION BY v.secret_id,v.erased_at IS NULL ORDER BY v.created_at DESC,v.id DESC) live FROM secret_versions v
	JOIN secrets s ON s.workspace_id=v.workspace_id AND s.id=v.secret_id JOIN archive_keep k ON k.kind='env' AND k.id=s.environment_id
	WHERE v.workspace_id=$1) x WHERE x.n<=$2 OR (x.erased_at IS NULL AND x.live=1) OR ($3 AND x.erased_at IS NOT NULL) ON CONFLICT DO NOTHING`

// keepNamedSecretVersions adds to the selection the versions of carried secrets in named, the IDs
// the archive's other rows name. No foreign key points at a secret version, so a capture finds them
// in the rows it read (captureTables): every revision, plan, operation command, preview, upgrade,
// runtime and backup configuration it carries, in any shape, including ones added later. So no
// carried row names a version of a carried secret that the archive leaves out.
func keepNamedSecretVersions(ctx context.Context, tx pgx.Tx, ws uuid.UUID, named map[uuid.UUID]struct{}) error {
	ids := make([]uuid.UUID, 0, len(named))
	for id := range named {
		ids = append(ids, id)
	}
	// inline-sql: chooses the secret versions in the archive's transaction-local working set
	_, err := tx.Exec(ctx, `INSERT INTO archive_keep(kind,id) SELECT 'version',v.id FROM secret_versions v
	JOIN secrets s ON s.workspace_id=v.workspace_id AND s.id=v.secret_id JOIN archive_keep k ON k.kind='env' AND k.id=s.environment_id
	WHERE v.workspace_id=$1 AND v.id=ANY($2::uuid[]) ON CONFLICT DO NOTHING`, ws, ids)
	return err
}

// collectNamedIDs adds the IDs a row of table names to named: every lower-case ID, as PostgreSQL
// and the platform write them, whether a JSON string of its own or inside one, such as an escaped
// document. An audit event names the version it records without needing it, and the newest 100
// would keep the versions set last whatever the bound, so its rows are not read. The scan looks only
// around dashes: a sealed value, written as hex, has none, so a full archive takes milliseconds.
func collectNamedIDs(table string, raw []byte, named map[uuid.UUID]struct{}) {
	if table == "audit_events" || table == "secret_versions" {
		return
	}
	for i := 0; ; {
		dash := bytes.IndexByte(raw[i:], '-')
		if dash < 0 {
			return
		}
		start := i + dash - 8
		if start < 0 || start+36 > len(raw) || !idShaped(raw[start:start+36]) {
			i += dash + 1
			continue
		}
		if id, err := uuid.ParseBytes(raw[start : start+36]); err == nil {
			named[id] = struct{}{}
		}
		i = start + 36
	}
}

// idShaped reports whether b is an ID in its canonical lower-case form.
func idShaped(b []byte) bool {
	for i, c := range b {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
				return false
			}
		}
	}
	return true
}

// inline-sql: seeds the export's transaction-local working set
const exportEnvironments = `INSERT INTO archive_keep(kind,id) SELECT 'env',e.id FROM environments e WHERE e.workspace_id=$1
	AND NOT (e.purpose='preview' AND NOT EXISTS (SELECT 1 FROM preview_requests r WHERE r.workspace_id=$1 AND r.environment_id=e.id AND r.state<>'deleted'))`

// The app's lists, with the same order and limits as the queries that show them.
// inline-sql: seeds the export's transaction-local working set
const exportSeeds = `INSERT INTO archive_keep(kind,id) SELECT DISTINCT kind,id FROM (
	SELECT 'op' AS kind,o.id FROM execution_operations o WHERE o.workspace_id=$1 AND o.state IN ('queued','dispatched','running','needs_attention')
	UNION ALL SELECT 'op',c.id FROM execution_operations c JOIN execution_operations p ON p.id=c.parent_id WHERE c.workspace_id=$1 AND p.state IN ('queued','dispatched','running','needs_attention')
	UNION ALL SELECT 'op',n.id FROM (SELECT id FROM execution_operations WHERE workspace_id=$1 ORDER BY created_at DESC FETCH FIRST 100 ROWS WITH TIES) n
	UNION ALL SELECT 'op',x.id FROM archive_keep k CROSS JOIN LATERAL (SELECT o.id FROM execution_operations o
		WHERE o.workspace_id=$1 AND o.environment_id=k.id AND o.state='succeeded' AND o.visual_plan_id IS NOT NULL AND o.command->'visual'->>'action'='deploy'
		ORDER BY o.created_at DESC,o.id DESC LIMIT 20) x WHERE k.kind='env'
	UNION ALL SELECT 'run',x.id FROM (SELECT r.id,row_number() OVER (PARTITION BY r.environment_id ORDER BY r.created_at DESC) n FROM pipeline_runs r JOIN archive_keep k ON k.kind='env' AND k.id=r.environment_id WHERE r.workspace_id=$1) x WHERE x.n<=100
	UNION ALL SELECT 'submission',x.id FROM (SELECT s.id,row_number() OVER (PARTITION BY s.environment_id ORDER BY s.created_at DESC,s.id DESC) n FROM source_submissions s JOIN archive_keep k ON k.kind='env' AND k.id=s.environment_id WHERE s.workspace_id=$1) x WHERE x.n<=100
	UNION ALL SELECT 'revision',x.id FROM (SELECT s.id,row_number() OVER (PARTITION BY s.environment_id ORDER BY s.created_at DESC,s.id DESC) n FROM specification_revisions s JOIN archive_keep k ON k.kind='env' AND k.id=s.environment_id WHERE s.workspace_id=$1) x WHERE x.n<=50
	UNION ALL SELECT 'preview',r.id FROM preview_requests r WHERE r.workspace_id=$1 AND r.state<>'deleted'
	UNION ALL SELECT 'preview',x.id FROM (SELECT r.id,row_number() OVER (PARTITION BY p.environment_id ORDER BY (r.state='deleted'),r.updated_at DESC,r.id) n FROM preview_requests r JOIN preview_policies p ON p.workspace_id=r.workspace_id AND p.id=r.policy_id WHERE r.workspace_id=$1) x WHERE x.n<=100
	UNION ALL SELECT 'recovery',x.id FROM (SELECT r.id,row_number() OVER (PARTITION BY r.policy_id ORDER BY r.created_at DESC,r.id) n FROM backup_recoveries r JOIN archive_keep k ON k.kind='env' AND k.id=r.environment_id WHERE r.workspace_id=$1) x WHERE x.n<=100
	UNION ALL SELECT 'artifact',a.id FROM (SELECT id FROM build_artifacts WHERE workspace_id=$1 ORDER BY created_at DESC LIMIT 100) a
	UNION ALL SELECT 'audit',a.id FROM (SELECT id FROM audit_events WHERE workspace_id=$1 ORDER BY created_at DESC,id DESC LIMIT 100) a
) seed ON CONFLICT DO NOTHING`

// The build artifacts a configuration the app can still apply uses, as planning finds them: by the
// ID an application's source names, or, for Compose, the newest artifact with an application's
// image. The configurations are each listed revision, each environment's draft and applied
// revision, and the plans of carried operations and current runtimes. Only those, once: a build
// records the draft it built, which uses the previous build's artifact, so following every carried
// revision would walk back through every build. The plans are looked up one by one: archive_keep
// has no statistics, so as a subquery the planner would read every plan the workspace has.
// inline-sql: seeds the export's transaction-local working set
const archivePinnedArtifacts = `WITH configurations(doc) AS (
	SELECT s.specification FROM specification_revisions s WHERE s.workspace_id=$1 AND s.id IN (
		SELECT id FROM archive_keep WHERE kind='revision'
		UNION SELECT e.draft_revision_id FROM environments e JOIN archive_keep k ON k.kind='env' AND k.id=e.id
		UNION SELECT e.applied_revision_id FROM environments e JOIN archive_keep k ON k.kind='env' AND k.id=e.id)
	UNION ALL SELECT p.plan FROM visual_plans p WHERE p.workspace_id=$1 AND p.id=ANY(ARRAY(
		SELECT o.visual_plan_id FROM execution_operations o JOIN archive_keep k ON k.kind='op' AND k.id=o.id
		UNION SELECT o.runtime_plan_id FROM execution_operations o JOIN archive_keep k ON k.kind='op' AND k.id=o.id
		UNION SELECT t.plan_id FROM visual_runtime t JOIN archive_keep k ON k.kind='env' AND k.id=t.environment_id
		UNION SELECT t.plan_id FROM cross_host_runtime t JOIN archive_keep k ON k.kind='env' AND k.id=t.environment_id)))
INSERT INTO archive_keep(kind,id)
	SELECT 'artifact',a.id FROM build_artifacts a WHERE a.workspace_id=$1 AND a.id::text IN (SELECT jsonb_path_query(doc,'lax $.**.artifactId')#>>'{}' FROM configurations)
	UNION SELECT 'artifact',x.id FROM (SELECT DISTINCT ON (a.identity->>'image') a.id FROM build_artifacts a
		WHERE a.workspace_id=$1 AND a.identity->>'image' IN (SELECT jsonb_path_query(doc,'lax $.**.source.image')#>>'{}' FROM configurations)
		ORDER BY a.identity->>'image',a.created_at DESC) x
ON CONFLICT DO NOTHING`

// Every row a carried row names, until nothing more is added.
// inline-sql: closes the export's transaction-local working set over its references
const archiveClosure = `INSERT INTO archive_keep(kind,id) SELECT DISTINCT kind,id FROM (
	SELECT 'op' AS kind,o.parent_id AS id FROM execution_operations o JOIN archive_keep k ON k.kind='op' AND k.id=o.id WHERE o.workspace_id=$1
	UNION ALL SELECT 'op',e.runtime_operation_id FROM environments e JOIN archive_keep k ON k.kind='env' AND k.id=e.id WHERE e.workspace_id=$1
	UNION ALL SELECT 'op',e.retirement_operation_id FROM environments e JOIN archive_keep k ON k.kind='env' AND k.id=e.id WHERE e.workspace_id=$1
	UNION ALL SELECT 'op',e.purge_operation_id FROM environments e JOIN archive_keep k ON k.kind='env' AND k.id=e.id WHERE e.workspace_id=$1
	UNION ALL SELECT 'op',r.id FROM pipeline_runs r JOIN archive_keep k ON k.kind='run' AND k.id=r.id WHERE r.workspace_id=$1
	UNION ALL SELECT 'op',v.operation_id FROM visual_requests v JOIN archive_keep k ON k.kind='request' AND k.id=v.id WHERE v.workspace_id=$1
	UNION ALL SELECT 'op',r.build_id FROM preview_requests r JOIN archive_keep k ON k.kind='preview' AND k.id=r.id WHERE r.workspace_id=$1
	UNION ALL SELECT 'op',r.operation_id FROM preview_requests r JOIN archive_keep k ON k.kind='preview' AND k.id=r.id WHERE r.workspace_id=$1
	UNION ALL SELECT 'op',d.operation_id FROM pipeline_auto_deployments d JOIN archive_keep k ON k.kind='run' AND k.id=d.id WHERE d.workspace_id=$1
	UNION ALL SELECT 'run',r.id FROM pipeline_runs r JOIN archive_keep k ON k.kind='op' AND k.id=r.id WHERE r.workspace_id=$1
	UNION ALL SELECT 'run',a.id FROM build_artifacts a JOIN archive_keep k ON k.kind='artifact' AND k.id=a.id WHERE a.workspace_id=$1
	UNION ALL SELECT 'artifact',a.id FROM build_artifacts a JOIN archive_keep k ON k.kind='run' AND k.id=a.id WHERE a.workspace_id=$1
	UNION ALL SELECT 'env',o.environment_id FROM execution_operations o JOIN archive_keep k ON k.kind='op' AND k.id=o.id WHERE o.workspace_id=$1
	UNION ALL SELECT 'env',v.environment_id FROM visual_requests v JOIN archive_keep k ON k.kind='request' AND k.id=v.id WHERE v.workspace_id=$1
	UNION ALL SELECT 'env',r.environment_id FROM pipeline_runs r JOIN archive_keep k ON k.kind='run' AND k.id=r.id WHERE r.workspace_id=$1
	UNION ALL SELECT 'env',a.environment_id FROM build_artifacts a JOIN archive_keep k ON k.kind='artifact' AND k.id=a.id WHERE a.workspace_id=$1
	UNION ALL SELECT 'env',r.environment_id FROM preview_requests r JOIN archive_keep k ON k.kind='preview' AND k.id=r.id WHERE r.workspace_id=$1
	UNION ALL SELECT 'env',s.environment_id FROM specification_revisions s JOIN archive_keep k ON k.kind='revision' AND k.id=s.id WHERE s.workspace_id=$1
	UNION ALL SELECT 'env',n.environment_id FROM network_endpoints n JOIN archive_keep k ON k.kind='endpoint' AND k.id=n.id WHERE n.workspace_id=$1
	UNION ALL SELECT 'env',u.environment_id FROM supported_upgrades u WHERE u.workspace_id=$1
	UNION ALL SELECT 'request',o.visual_plan_id FROM execution_operations o JOIN archive_keep k ON k.kind='op' AND k.id=o.id WHERE o.workspace_id=$1
	UNION ALL SELECT 'request',o.runtime_plan_id FROM execution_operations o JOIN archive_keep k ON k.kind='op' AND k.id=o.id WHERE o.workspace_id=$1
	UNION ALL SELECT 'request',v.id FROM visual_requests v JOIN archive_keep k ON k.kind='op' AND k.id=v.operation_id WHERE v.workspace_id=$1
	UNION ALL SELECT 'request',c.plan_id FROM cross_host_operations c JOIN archive_keep k ON k.kind='op' AND k.id=c.id WHERE c.workspace_id=$1
	UNION ALL SELECT 'request',t.plan_id FROM visual_runtime t JOIN archive_keep k ON k.kind='env' AND k.id=t.environment_id WHERE t.workspace_id=$1
	UNION ALL SELECT 'request',t.plan_id FROM cross_host_runtime t JOIN archive_keep k ON k.kind='env' AND k.id=t.environment_id WHERE t.workspace_id=$1
	UNION ALL SELECT 'request',c.source_plan_id FROM backup_cutovers c JOIN archive_keep k ON k.kind='env' AND k.id=c.environment_id WHERE c.workspace_id=$1
	UNION ALL SELECT 'request',r.plan_id FROM backup_recoveries r JOIN archive_keep k ON k.kind='recovery' AND k.id=r.id WHERE r.workspace_id=$1
	UNION ALL SELECT 'op',r.operation_id FROM backup_recoveries r JOIN archive_keep k ON k.kind='recovery' AND k.id=r.id WHERE r.workspace_id=$1
	UNION ALL SELECT 'op',r.discard_operation_id FROM backup_recoveries r JOIN archive_keep k ON k.kind='recovery' AND k.id=r.id WHERE r.workspace_id=$1
	UNION ALL SELECT 'recovery',r.id FROM backup_recoveries r JOIN archive_keep k ON k.kind='op' AND k.id=r.operation_id WHERE r.workspace_id=$1
	UNION ALL SELECT 'request',n.plan_id FROM network_endpoints n JOIN archive_keep k ON k.kind='endpoint' AND k.id=n.id WHERE n.workspace_id=$1
	UNION ALL SELECT 'request',r.plan_id FROM preview_requests r JOIN archive_keep k ON k.kind='preview' AND k.id=r.id WHERE r.workspace_id=$1
	UNION ALL SELECT 'request',d.plan_id FROM pipeline_auto_deployments d JOIN archive_keep k ON k.kind='run' AND k.id=d.id WHERE d.workspace_id=$1
	UNION ALL SELECT 'endpoint',n.id FROM network_endpoints n JOIN archive_keep k ON k.kind='env' AND k.id=n.environment_id WHERE n.workspace_id=$1 AND NOT n.retired
	UNION ALL SELECT 'endpoint',n.id FROM network_endpoints n JOIN archive_keep k ON k.kind='request' AND k.id=n.plan_id WHERE n.workspace_id=$1
	UNION ALL SELECT 'endpoint',p.endpoint_id FROM network_plan_endpoints p JOIN archive_keep k ON k.kind='request' AND k.id=p.plan_id WHERE p.workspace_id=$1
	UNION ALL SELECT 'endpoint',p.endpoint_id FROM network_plan_sources p JOIN archive_keep k ON k.kind='request' AND k.id=p.plan_id WHERE p.workspace_id=$1
	UNION ALL SELECT 'revision',e.draft_revision_id FROM environments e JOIN archive_keep k ON k.kind='env' AND k.id=e.id WHERE e.workspace_id=$1
	UNION ALL SELECT 'revision',e.applied_revision_id FROM environments e JOIN archive_keep k ON k.kind='env' AND k.id=e.id WHERE e.workspace_id=$1
	UNION ALL SELECT 'revision',p.revision_id FROM execution_plans p JOIN execution_operations o ON o.plan_id=p.id JOIN archive_keep k ON k.kind='op' AND k.id=o.id WHERE p.workspace_id=$1
	UNION ALL SELECT 'revision',v.revision_id FROM visual_requests v JOIN archive_keep k ON k.kind='request' AND k.id=v.id WHERE v.workspace_id=$1
	UNION ALL SELECT 'revision',g.imported_revision_id FROM git_sources g JOIN archive_keep k ON k.kind='env' AND k.id=g.environment_id WHERE g.workspace_id=$1
	UNION ALL SELECT 'revision',r.revision_id FROM pipeline_runs r JOIN archive_keep k ON k.kind='run' AND k.id=r.id WHERE r.workspace_id=$1
	UNION ALL SELECT 'revision',s.revision_id FROM git_submissions s JOIN archive_keep k ON k.kind='run' AND k.id=s.operation_id WHERE s.workspace_id=$1
	UNION ALL SELECT 'revision',s.revision_id FROM source_submissions s JOIN archive_keep k ON k.kind='submission' AND k.id=s.id WHERE s.workspace_id=$1
	UNION ALL SELECT 'revision',d.revision_id FROM pipeline_auto_deployments d JOIN archive_keep k ON k.kind='run' AND k.id=d.id WHERE d.workspace_id=$1
	UNION ALL SELECT 'revision',p.revision_id FROM preview_policies p WHERE p.workspace_id=$1
	UNION ALL SELECT 'revision',c.revision_id FROM backup_cutovers c JOIN archive_keep k ON k.kind='env' AND k.id=c.environment_id WHERE c.workspace_id=$1
	UNION ALL SELECT 'recovery',k.recovery_id FROM backup_storage_keys k WHERE k.workspace_id=$1 AND k.state IN ('active','revoking')
) edge WHERE id IS NOT NULL ON CONFLICT DO NOTHING`

func archiveKept(kind, column string) string {
	return ` AND ` + column + ` IN (SELECT id FROM archive_keep WHERE kind='` + kind + `')`
}

// archiveFilters narrow each archived table to the selection. Tables not named here hold
// configuration and are carried whole: projects, servers, connections, registries, destinations other than placeholders,
// networks and members, preview policies, build secret versions and supported upgrades (both
// capped at 100), and reservations, which an export refuses to carry.
var archiveFilters = func() map[string]string {
	out := map[string]string{
		"environments":              archiveKept("env", "id"),
		"specification_revisions":   archiveKept("revision", "id"),
		"execution_operations":      archiveKept("op", "id"),
		"visual_requests":           archiveKept("request", "id"),
		"visual_plans":              archiveKept("request", "id"),
		"network_endpoints":         archiveKept("endpoint", "id"),
		"network_endpoint_sources":  archiveKept("endpoint", "endpoint_id"),
		"network_plan_endpoints":    archiveKept("request", "plan_id"),
		"network_plan_sources":      archiveKept("request", "plan_id"),
		"cross_host_operations":     archiveKept("op", "id"),
		"cross_host_approvals":      archiveKept("op", "parent_id"),
		"pipeline_runs":             archiveKept("run", "id"),
		"build_artifacts":           archiveKept("artifact", "id"),
		"git_submissions":           archiveKept("run", "operation_id"),
		"source_submissions":        archiveKept("submission", "id"),
		"pipeline_auto_deployments": archiveKept("run", "id"),
		"preview_requests":          archiveKept("preview", "id"),
		"audit_events":              archiveKept("audit", "id"),
		"backup_recoveries":         archiveKept("recovery", "id"),
		// A placeholder destination, whose key was never active, goes with its keys; so do keys
		// still minting, which only their own request may finish. A recovery key goes with its
		// restore, which a live key's always is (archiveClosure).
		"backup_destinations": ` AND (account_id IS NULL OR EXISTS (SELECT 1 FROM backup_storage_keys k WHERE k.workspace_id=$1 AND k.destination_id=backup_destinations.id AND k.activated_at IS NOT NULL))`,
		"backup_storage_keys": ` AND state<>'minting' AND (destination_id IS NULL OR EXISTS (SELECT 1 FROM backup_storage_keys k WHERE k.workspace_id=$1 AND k.destination_id=backup_storage_keys.destination_id AND k.activated_at IS NOT NULL)) AND (recovery_id IS NULL OR recovery_id IN (SELECT id FROM archive_keep WHERE kind='recovery'))`,
		"execution_plans":     ` AND id IN (SELECT o.plan_id FROM execution_operations o JOIN archive_keep k ON k.kind='op' AND k.id=o.id)`,
		// Each statement that chooses a version keeps only those of carried secrets.
		"secret_versions": archiveKept("version", "id"),
	}
	for _, table := range []string{"deployment_units", "environment_layouts", "secrets", "managed_volumes", "managed_resources", "route_claims", "visual_runtime", "cross_host_runtime", "git_sources", "build_targets", "backup_policies", "backup_cutovers"} {
		out[table] = archiveKept("env", "environment_id")
	}
	return out
}()
