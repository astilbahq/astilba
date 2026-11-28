package agent

import (
	"context"
	"encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/moby/moby/client"
)

func (e *Engine) upgradeDatabaseChecks(ctx context.Context, db pgClient, a domain.Application) error {
	for _, check := range []struct{ sql, want, message string }{
		// template0 is oid 4, which the checks below compare the other databases with. A superuser
		// can drop it once it isn't a template, and nothing can make it again under that oid.
		// inline-sql: reads a managed database's catalog, not the platform database.
		{"SELECT count(*) FROM pg_database WHERE oid = 4", "1", "a cluster without template0 requires a separate recipe"},
		// By name: a database its owner marked as a template is still one the upgrade would lose.
		// template0 is always oid 4, so another database a superuser names template0 is one too.
		// inline-sql: reads a managed database's catalog, not the platform database.
		{"SELECT count(*) FROM pg_database WHERE oid <> 4 AND datname NOT IN (current_database(),'postgres','template1')", "0", "additional databases require a separate recipe"},
		// inline-sql: reads a managed database's catalog, not the platform database.
		{"SELECT count(*) FROM pg_tablespace WHERE spcname NOT IN ('pg_default','pg_global')", "0", "custom tablespaces are unsupported"},
		// inline-sql: reads a managed database's catalog, not the platform database.
		{"SELECT count(*) FROM pg_prepared_xacts", "0", "prepared transactions are unsupported"},
		// inline-sql: reads a managed database's catalog, not the platform database.
		{"SELECT count(*) FROM pg_replication_slots", "0", "replication slots are unsupported"},
		// inline-sql: reads a managed database's catalog, not the platform database.
		{"SELECT count(*) FROM pg_subscription", "0", "subscriptions are unsupported"},
		// The upgrade restores the database into a cluster initdb made, so the maintenance database,
		// template1 and template0, always oid 4, must hold only what initdb gives them. Before the
		// owner model the database user is a superuser and can change any of them, template0 too
		// once it opens it, and close it again.
		// inline-sql: reads a managed database's catalog, not the platform database.
		{"SELECT count(*) FROM pg_db_role_setting WHERE setdatabase=0 OR setdatabase IN (SELECT oid FROM pg_database WHERE oid = 4 OR datname IN (current_database(),'postgres','template1'))", "0", "database or role settings require a separate recipe"},
		// The database user owns the database; the bootstrap superuser, the database user too
		// before the owner model, owns the others. The maintenance database isn't a template and
		// accepts connections, template1 is a template that accepts them, and template0 is one
		// that doesn't, under its own name. Each keeps the grants it starts with: the default,
		// and for the templates initdb's, which takes TEMPORARY from PUBLIC. An ACL that only
		// spells those out, as a restore's grant and revoke leaves one, is no custom privilege.
		// inline-sql: reads a managed database's catalog, not the platform database.
		{"SELECT count(*) FROM (SELECT d.*, coalesce(datacl, acldefault('d',datdba)) AS acl, CASE WHEN oid = 4 OR datname='template1' THEN ARRAY[makeaclitem(0,datdba,'CONNECT',false), makeaclitem(datdba,datdba,'CREATE,TEMPORARY,CONNECT',false)] ELSE acldefault('d',datdba) END AS initial FROM pg_database d WHERE oid = 4 OR datname IN (current_database(),'postgres','template1')) d WHERE NOT (acl @> initial AND acl <@ initial) OR datconnlimit<>-1 OR datdba<>CASE WHEN datname=current_database() THEN (SELECT oid FROM pg_roles WHERE rolname=" + quoteLiteral(a.Postgres.Username) + ") ELSE 10 END OR oid = 4 AND (datname<>'template0' OR NOT (datistemplate AND NOT datallowconn)) OR datname='template1' AND NOT (datistemplate AND datallowconn) OR datname='postgres' AND datname<>current_database() AND (datistemplate OR NOT datallowconn) OR shobj_description(oid,'pg_database') IS NOT NULL AND shobj_description(oid,'pg_database') IS DISTINCT FROM CASE WHEN oid = 4 THEN 'unmodifiable empty database' WHEN datname='postgres' THEN 'default administrative connection database' WHEN datname='template1' THEN 'default template for new databases' END", "0", "custom database privileges, ownership, comments or limits require a separate recipe"},
	} {
		raw, err := e.pgQuery(ctx, db, a, check.sql)
		if err != nil || strings.TrimSpace(string(raw)) != check.want {
			return errors.New(check.message)
		}
	}
	if err := e.upgradeLocaleCheck(ctx, db, a); err != nil {
		return err
	}
	for _, other := range []struct{ database, called string }{{"postgres", "the postgres maintenance database"}, {"template1", "template1"}} {
		database, called := other.database, other.called
		if a.Postgres.Database == database {
			continue
		}
		admin := a
		pg := *a.Postgres
		pg.Database = database
		admin.Postgres = &pg
		raw, err := e.pgQuery(ctx, db, admin, "SELECT count(*) FROM pg_largeobject_metadata") // inline-sql: reads a managed database's catalog, not the platform database.
		if err != nil || strings.TrimSpace(string(raw)) != "0" {
			return errors.New(called + " contains large objects")
		}
		// The pinned dumper inventories every dumpable object class, including
		// collations and text-search objects; a catalog allowlist misses classes.
		inventory := &boundedOutput{limit: 65536, stopOnLimit: true}
		if err = e.containerExec(ctx, db, inventory, "sh", "-c", `set -o pipefail && pg_dump --no-password -U "$1" -d "$2" --schema-only -Fc | pg_restore --list`, "sh", pg.MaintenanceUser(), database); err != nil {
			return errors.New("cannot inventory " + called)
		}
		for _, line := range strings.Split(inventory.b.String(), "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, ";") {
				return errors.New(called + " contains user objects")
			}
		}
		// The dump above leaves out PostgreSQL's own schemas, which a superuser can add to too, so
		// every catalog row with an OID initdb didn't give is counted, whatever its class. The
		// catalogs are listed from the catalog itself, so a release's new ones are counted as well.
		// Sessions' temporary tables don't count, and nor do temporary schemas themselves: any role
		// may make them in postgres, and the schemas stay once their sessions end.
		// inline-sql: reads a managed database's catalog, not the platform database.
		raw, err = e.pgQuery(ctx, db, admin, "SELECT string_agg(c.relname, ',' ORDER BY c.relname) FROM pg_class c WHERE c.relnamespace = 'pg_catalog'::regnamespace AND c.relkind = 'r' AND NOT c.relisshared AND EXISTS (SELECT 1 FROM pg_attribute a WHERE a.attrelid = c.oid AND a.attname = 'oid' AND NOT a.attisdropped)")
		if err != nil {
			return errors.New("cannot inventory " + called)
		}
		var rows []string
		for name := range strings.SplitSeq(strings.TrimSpace(string(raw)), ",") {
			if !catalogName.MatchString(name) {
				return errors.New("cannot inventory " + called)
			}
			// inline-sql: reads a managed database's catalog, not the platform database.
			rows = append(rows, "SELECT 'pg_catalog."+name+"'::regclass AS classid, oid FROM pg_catalog."+name+" WHERE oid >= 16384")
		}
		// inline-sql: reads a managed database's catalog, not the platform database.
		raw, err = e.pgQuery(ctx, db, admin, uncountedSQL+"SELECT count(*) FROM ("+strings.Join(rows, " UNION ALL ")+") o WHERE (o.classid, o.oid) NOT IN (SELECT classid, objid FROM uncounted)")
		if err != nil || strings.TrimSpace(string(raw)) != "0" {
			// Each object named can be dropped: its own parts, which can't, go with it.
			// inline-sql: reads a managed database's catalog, not the platform database.
			raw, err = e.pgQuery(ctx, db, admin, uncountedSQL+"SELECT coalesce(json_agg(pg_catalog.pg_describe_object(d.classid, d.objid, 0)), '[]') FROM pg_catalog.pg_depend d JOIN pg_catalog.pg_namespace n ON d.refclassid = 'pg_catalog.pg_namespace'::regclass AND d.refobjid = n.oid WHERE n.nspname ~ '^pg_(toast_)?temp_[0-9]+$' AND (d.classid, d.objid) NOT IN (SELECT classid, objid FROM uncounted) AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_depend i WHERE i.classid = d.classid AND i.objid = d.objid AND i.deptype = 'i')")
			var leftovers []string
			if err != nil || json.Unmarshal(raw, &leftovers) != nil {
				leftovers = nil
			}
			return errors.New(userObjectsNote(called, leftovers))
		}
	}
	return nil
}

// localeDatabases names, by the key upgradeLocaleSQL gives it, each database whose encoding and
// locale the upgrade's new cluster sets.
var localeDatabases = map[string]string{"application": "the application database", "postgres": "the postgres maintenance database", "template1": "template1"}

// upgradeLocaleSQL lists, one per line as key:properties, the databases of localeDatabases whose
// encoding or locale differs from template0's. template0 is always oid 4, which the checks
// before it require, and can't be made again, so it keeps what initdb gave the cluster, and the upgrade's new cluster gives every database what its
// own initdb chose, from the same image defaults. Before the owner model the database user is a
// superuser and can drop template1 or postgres and create it again from template0 in another
// encoding or locale, or do the same to the application's database from another one. The columns
// are PostgreSQL 17's and 18's, the majors an upgrade starts from or reaches. datcollversion is left
// out: it records the collation library's version, which a new cluster records afresh.
// inline-sql: reads a managed database's catalog, not the platform database.
const upgradeLocaleSQL = `SELECT CASE WHEN d.datname = pg_catalog.current_database() THEN 'application' ELSE d.datname END || ':' || pg_catalog.concat_ws(',',
	CASE WHEN d.encoding <> t.encoding THEN 'encoding' END,
	CASE WHEN d.datlocprovider <> t.datlocprovider THEN 'locale provider' END,
	CASE WHEN d.datcollate <> t.datcollate THEN 'LC_COLLATE' END,
	CASE WHEN d.datctype <> t.datctype THEN 'LC_CTYPE' END,
	CASE WHEN d.datlocale IS DISTINCT FROM t.datlocale THEN 'ICU or builtin locale' END,
	CASE WHEN d.daticurules IS DISTINCT FROM t.daticurules THEN 'ICU rules' END)
FROM pg_catalog.pg_database d, pg_catalog.pg_database t
WHERE t.oid = 4 AND d.oid <> 4 AND d.datname IN (pg_catalog.current_database(), 'postgres', 'template1')
	AND (d.encoding, d.datlocprovider, d.datcollate, d.datctype, d.datlocale, d.daticurules) IS DISTINCT FROM (t.encoding, t.datlocprovider, t.datcollate, t.datctype, t.datlocale, t.daticurules)
ORDER BY 1`

// upgradeLocaleCheck refuses an upgrade whose new cluster would silently give the application's
// database, postgres or template1 another encoding or locale, so that databases made from
// template1 afterwards would differ too. It names each database and what differs, never a value.
func (e *Engine) upgradeLocaleCheck(ctx context.Context, db pgClient, a domain.Application) error {
	raw, err := e.pgQuery(ctx, db, a, upgradeLocaleSQL)
	if err != nil {
		return errors.New("cannot read the databases' encodings and locales")
	}
	var differ []string
	for line := range strings.SplitSeq(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		key, what, _ := strings.Cut(line, ":")
		called, known := localeDatabases[key]
		if !known || what == "" {
			return errors.New("cannot read the databases' encodings and locales")
		}
		differ = append(differ, called+" ("+strings.ReplaceAll(what, ",", ", ")+")")
	}
	if len(differ) > 0 {
		return errors.New("an encoding or locale other than template0's, which the new cluster gives every database, requires a separate recipe: " + strings.Join(differ, "; "))
	}
	return nil
}

// The bytes userObjectsNote's list of leftovers takes at most. With the note's own words it fits
// the 900 bytes an upgrade's note keeps of its error.
const leftoversRoom = 400

// userObjectsNote refuses a maintenance database holding user objects. It names, by kind and
// qualified name but never content, those in temporary schemas: a connected session's temporary
// functions and types are there, and so are those of a session that ended without cleaning up,
// as when the server crashes, which stay until a later session given the same schema uses
// temporary objects.
func userObjectsNote(called string, leftovers []string) string {
	if len(leftovers) == 0 {
		return called + " contains user objects"
	}
	return called + " contains user objects, among them " + listSome(unqualified(leftovers), leftoversRoom) + " in temporary schemas, held by a connected session or left by one that ended uncleanly; end the session or drop them before upgrading"
}

// catalogName is a PostgreSQL system catalog's name, put into a query only once it matches.
var catalogName = regexp.MustCompile(`^pg_[a-z_]+$`)

// uncountedSQL names, as uncounted(classid, objid), the temporary relations, the parts dropped
// with them (row types, indexes, sequences, defaults, constraints, rules and the like) and the
// temporary schemas. Only automatic and internal dependencies whose dependent is a whole object are
// followed, so a permanent object that merely uses a temporary one still counts, as does whatever
// else a temporary schema holds. PostgreSQL reserves the pg_ prefix, so only a superuser can name a schema like those.
// inline-sql: reads a managed database's catalog, not the platform database.
const uncountedSQL = `WITH RECURSIVE temporary(classid, objid) AS (
	SELECT 'pg_catalog.pg_class'::regclass, oid FROM pg_catalog.pg_class WHERE relpersistence = 't'
	UNION SELECT d.classid, d.objid FROM pg_catalog.pg_depend d JOIN temporary ON d.refclassid = temporary.classid AND d.refobjid = temporary.objid WHERE d.objsubid = 0 AND d.deptype IN ('a', 'i')
), uncounted(classid, objid) AS (
	SELECT classid, objid FROM temporary
	UNION ALL SELECT 'pg_catalog.pg_namespace'::regclass, oid FROM pg_catalog.pg_namespace WHERE nspname ~ '^pg_(toast_)?temp_[0-9]+$'
) `

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
	raw, err := e.pgQuery(ctx, db, a, "SELECT coalesce(json_agg(host(client_addr)),'[]') FROM pg_stat_activity WHERE backend_type='client backend' AND pid<>pg_backend_pid()") // inline-sql: reads a managed database's catalog, not the platform database.
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
	// Unknown clients are refused first: what a connected session holds isn't what it leaves.
	if err = e.knownUpgradeWriters(ctx, helper, a, s.Command.Plan.Source); err != nil {
		return nil, err
	}
	e.point("upgrade-preflight-clients-checked")
	if err = e.upgradeDatabaseChecks(ctx, helper, a); err != nil {
		return nil, err
	}
	return e.pgQuery(ctx, helper, a, "SELECT pg_database_size(current_database())") // inline-sql: reads a managed database's catalog, not the platform database.
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
