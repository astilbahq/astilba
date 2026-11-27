package agent

import (
	"context"
	"encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/moby/moby/client"
)

func (e *Engine) upgradeDatabaseChecks(ctx context.Context, db pgClient, a domain.Application) error {
	for _, check := range []struct{ sql, want, message string }{
		// By name: a database its owner marked as a template is still one the upgrade would lose.
		{"SELECT count(*) FROM pg_database WHERE datname NOT IN (current_database(),'postgres','template0','template1')", "0", "additional databases require a separate recipe"},
		{"SELECT count(*) FROM pg_tablespace WHERE spcname NOT IN ('pg_default','pg_global')", "0", "custom tablespaces are unsupported"},
		{"SELECT count(*) FROM pg_prepared_xacts", "0", "prepared transactions are unsupported"},
		{"SELECT count(*) FROM pg_replication_slots", "0", "replication slots are unsupported"},
		{"SELECT count(*) FROM pg_subscription", "0", "subscriptions are unsupported"},
		{"SELECT count(*) FROM pg_db_role_setting WHERE setdatabase=0 OR setdatabase IN (SELECT oid FROM pg_database WHERE datname IN (current_database(),'postgres'))", "0", "database or role settings require a separate recipe"},
		// The database user owns the database; the bootstrap superuser, the database user too
		// before the owner model, owns the maintenance database. An ACL that only spells out the
		// default, as a restore's grant and revoke leaves one, is no custom privilege.
		{"SELECT count(*) FROM pg_database WHERE datname IN (current_database(),'postgres') AND (NOT (coalesce(datacl, acldefault('d',datdba)) @> acldefault('d',datdba) AND coalesce(datacl, acldefault('d',datdba)) <@ acldefault('d',datdba)) OR datconnlimit<>-1 OR datdba<>CASE WHEN datname=current_database() THEN (SELECT oid FROM pg_roles WHERE rolname=" + quoteLiteral(a.Postgres.Username) + ") ELSE 10 END OR (shobj_description(oid,'pg_database') IS NOT NULL AND (datname<>'postgres' OR shobj_description(oid,'pg_database')<>'default administrative connection database')))", "0", "custom database privileges, ownership, comments or limits require a separate recipe"},
	} {
		raw, err := e.pgQuery(ctx, db, a, check.sql)
		if err != nil || strings.TrimSpace(string(raw)) != check.want {
			return errors.New(check.message)
		}
	}
	if a.Postgres.Database != "postgres" {
		admin := a
		pg := *a.Postgres
		pg.Database = "postgres"
		admin.Postgres = &pg
		raw, err := e.pgQuery(ctx, db, admin, "SELECT count(*) FROM pg_largeobject_metadata")
		if err != nil || strings.TrimSpace(string(raw)) != "0" {
			return errors.New("the postgres maintenance database contains large objects")
		}
		// The pinned dumper inventories every dumpable object class, including
		// collations and text-search objects; a catalog allowlist misses classes.
		inventory := &boundedOutput{limit: 65536, stopOnLimit: true}
		if err = e.containerExec(ctx, db, inventory, "sh", "-c", `set -o pipefail && pg_dump --no-password -U "$1" -d postgres --schema-only -Fc | pg_restore --list`, "sh", pg.MaintenanceUser()); err != nil {
			return errors.New("cannot inventory the postgres maintenance database")
		}
		for _, line := range strings.Split(inventory.b.String(), "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, ";") {
				return errors.New("the postgres maintenance database contains user objects")
			}
		}
	}
	return nil
}

func (e *Engine) knownUpgradeWriters(ctx context.Context, db pgClient, a domain.Application, p deployment.Plan) error {
	allowed := map[string]bool{}
	for _, app := range p.Specification.Applications {
		for _, instance := range p.InstanceIDs(app.ID) {
			v, err := e.Docker.ContainerInspect(ctx, deployment.UnitName(instance), client.ContainerInspectOptions{})
			if err != nil {
				return err
			}
			if !visualOwned(v.Container.Config.Labels, p) || v.Container.Config.Labels[labelResource] != app.ID.String() {
				return errors.New("writer ownership changed")
			}
			if v.Container.NetworkSettings != nil {
				for _, n := range v.Container.NetworkSettings.Networks {
					allowed[n.IPAddress.String()] = true
					allowed[n.GlobalIPv6Address.String()] = true
				}
			}
		}
	}
	delete(allowed, "")
	raw, err := e.pgQuery(ctx, db, a, "SELECT coalesce(json_agg(host(client_addr)),'[]') FROM pg_stat_activity WHERE backend_type='client backend' AND pid<>pg_backend_pid()")
	var clients []*string
	if err != nil || json.Unmarshal(raw, &clients) != nil {
		return errors.New("cannot identify database writers")
	}
	for _, ip := range clients {
		if ip == nil || !allowed[*ip] {
			return errors.New("disconnect external or unknown database clients before upgrading")
		}
	}
	return nil
}

// upgradePreflight checks the live source database from a helper in its network namespace, so
// nothing runs in the source and removing the helper ends whatever a stopped check left running.
// The helper runs the source's own PostgreSQL 17 release, which the host already has: PostgreSQL 18
// is pulled only once the checks pass. It returns the database's size in bytes, as PostgreSQL
// prints it.
func (e *Engine) upgradePreflight(ctx context.Context, s upgradeState, source string, a domain.Application) ([]byte, error) {
	from, err := s.Command.Plan.SourceRelease()
	if err != nil {
		return nil, err
	}
	helper, err := e.postgresHelper(ctx, "astilba-upgrade-preflight-"+s.Command.Plan.ID.String(), upgradeHelperLabels(s, a, "upgrade-preflight-v1"), source, from.Image)
	if err != nil {
		return nil, err
	}
	defer e.removeHelper(helper)
	m := backup.Manifest{Roles: []backup.Role{}, Extensions: []string{}}
	if err = e.postgresManifest(ctx, helper, a, from.Image, &m); err != nil {
		return nil, err
	}
	if err = e.upgradeDatabaseChecks(ctx, helper, a); err != nil {
		return nil, err
	}
	if err = e.knownUpgradeWriters(ctx, helper, a, s.Command.Plan.Source); err != nil {
		return nil, err
	}
	return e.pgQuery(ctx, helper, a, "SELECT pg_database_size(current_database())")
}

func upgradeHelperLabels(s upgradeState, a domain.Application, kind string) map[string]string {
	labels := visualLabels(s.Command.Plan.Source, a.ID)
	labels[labelKind] = kind
	labels["com.astilba.upgrade"] = s.Command.Plan.ID.String()
	return labels
}

func (e *Engine) upgradeDump(ctx context.Context, s upgradeState, source string, a domain.Application, stage string, m *backup.Manifest) error {
	target, err := s.Command.Plan.TargetRelease()
	if err != nil {
		return err
	}
	helper, err := e.postgresHelper(ctx, "astilba-upgrade-tools-"+s.Command.Plan.ID.String(), upgradeHelperLabels(s, a, "upgrade-tools-v1"), source, target.Image)
	if err != nil {
		return err
	}
	defer e.removeHelper(helper)
	dump, err := e.dumpSession(ctx, helper, a, m)
	if err != nil {
		return err
	}
	// pg_dump sets it for its flag below, whatever the session has.
	m.DumpSettings["quote_all_identifiers"] = "on"
	f, err := os.OpenFile(filepath.Join(stage, "data.dump"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	err = e.containerExec(ctx, dump, f, "pg_dump", "-U", a.Postgres.MaintenanceUser(), "-d", a.Postgres.Database, "-Fc", "-Z0", "--no-password", "--quote-all-identifiers")
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	return checksumFile(filepath.Join(stage, "data.dump"), m)
}
