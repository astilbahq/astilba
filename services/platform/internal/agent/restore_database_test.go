package agent

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
)

// A rendering of an archive's database entries as pg_restore --create writes it, with a comment
// whose text holds what a looser reader would take for statements and commands.
const renderedDatabase = `--
-- PostgreSQL database dump
--

\restrict K

-- Dumped from database version 18.4
-- Dumped by pg_dump version 18.4

SET statement_timeout = 0;
SET client_encoding = 'UTF8';
SET standard_conforming_strings = on;
SELECT pg_catalog.set_config('search_path', '', false);
SET row_security = off;

--
-- Name: app; Type: DATABASE; Schema: -; Owner: app
--

CREATE DATABASE app WITH TEMPLATE = template0 ENCODING = 'UTF8' LOCALE_PROVIDER = libc LOCALE = 'en_US.utf8';


ALTER DATABASE app OWNER TO app;

\unrestrict K
\connect app
\restrict K

SET statement_timeout = 0;

COMMENT ON DATABASE app IS 'it''s; DROP DATABASE app;
\! id';

ALTER DATABASE app CONNECTION LIMIT = 50;
ALTER DATABASE app SET work_mem TO '8MB';
ALTER DATABASE app SET search_path TO 'public', 'x';
ALTER DATABASE app SET application_name TO E'a\\b''; c';
ALTER DATABASE app SET "PLPGSQL.VARIABLE_CONFLICT" TO 'use_column';
ALTER ROLE app IN DATABASE app SET work_mem TO '2MB';
ALTER ROLE "Reader" IN DATABASE app SET "DateStyle" TO 'ISO, DMY';

REVOKE CONNECT,TEMPORARY ON DATABASE app FROM PUBLIC;
GRANT TEMPORARY ON DATABASE app TO PUBLIC;
GRANT CONNECT,TEMPORARY ON DATABASE app TO "Reader" WITH GRANT OPTION;

\unrestrict K
`

func TestDatabaseStatementsReadOnlyWhatPgDumpWrites(t *testing.T) {
	m := backup.Manifest{Database: "app", Username: "app"}
	got, err := databaseStatements(renderedDatabase, m)
	if err != nil {
		t.Fatal(err)
	}
	kinds := []databaseStatementKind{}
	for _, s := range got {
		kinds = append(kinds, s.kind)
	}
	want := []databaseStatementKind{statementCreate, statementOwner, statementComment, statementProperty, statementSetting, statementSetting, statementSetting, statementSetting, statementSetting, statementSetting, statementPrivilege, statementPrivilege, statementPrivilege}
	if !slices.Equal(kinds, want) {
		t.Fatalf("kinds %v, want %v", kinds, want)
	}
	if got[1].owner != "app" || got[2].text != "COMMENT ON DATABASE app IS 'it''s; DROP DATABASE app;\n\\! id'" {
		t.Fatalf("read %+v", got[:3])
	}
	settings := []clusterSetting{}
	for _, s := range got {
		if s.kind == statementSetting {
			settings = append(settings, s.setting)
		}
	}
	if want := []clusterSetting{{name: "work_mem"}, {name: "search_path"}, {name: "application_name"}, {name: "PLPGSQL.VARIABLE_CONFLICT"}, {role: "app", name: "work_mem"}, {role: "Reader", name: "DateStyle"}}; !slices.Equal(settings, want) {
		t.Fatalf("settings %v, want %v", settings, want)
	}

	for _, c := range []struct{ name, script string }{
		{"a psql command", "\\! id\n"},
		{"a command after a statement's start", "ALTER DATABASE app SET work_mem TO '1MB'\n\\! id\n;"},
		{"a session switch", "SET SESSION AUTHORIZATION intruder;"},
		{"a function call", "SELECT pg_catalog.pg_read_file('postgresql.conf');"},
		{"another database", "ALTER DATABASE other SET work_mem TO '1MB';"},
		{"another database's grant", "GRANT CONNECT ON DATABASE other TO PUBLIC;"},
		{"a setting from the current session", "ALTER DATABASE app SET work_mem FROM CURRENT;"},
		{"a statement after a value", "ALTER DATABASE app SET work_mem TO '1MB'; ALTER ROLE app SUPERUSER;"},
		{"a dollar-quoted value", "COMMENT ON DATABASE app IS $$x$$;"},
		{"a backslash escape that isn't a backslash", `ALTER DATABASE app SET application_name TO E'\'; ALTER ROLE app SUPERUSER; --';`},
		{"an unterminated literal", "COMMENT ON DATABASE app IS 'x;"},
		{"a statement without its end", "COMMENT ON DATABASE app IS 'x'"},
		{"a second database", "CREATE DATABASE app WITH TEMPLATE = template0;"},
	} {
		t.Run(c.name, func(t *testing.T) {
			script := "CREATE DATABASE app WITH TEMPLATE = template0 ENCODING = 'UTF8';\n" + c.script
			if _, err := databaseStatements(script, m); err == nil {
				t.Fatalf("read %q", c.script)
			}
		})
	}
	if _, err := databaseStatements("ALTER DATABASE app OWNER TO app;", m); err == nil {
		t.Fatal("read entries without the database's creation")
	}
}

func TestRestorableSettings(t *testing.T) {
	m := backup.Manifest{Database: "app", Username: "app"}
	contexts := map[string]string{"work_mem": "user", "log_statement": "superuser", "plpgsql.variable_conflict": "superuser", "plpgsql.extra_warnings": "user", "datestyle": "user"}
	for _, c := range []struct {
		setting clusterSetting
		ok      bool
	}{
		{clusterSetting{name: "work_mem"}, true},
		{clusterSetting{role: "app", name: "DateStyle"}, true},
		{clusterSetting{name: "log_statement"}, false},
		{clusterSetting{name: "PLPGSQL.VARIABLE_CONFLICT"}, false},
		{clusterSetting{name: "custom.flag"}, false},
		{clusterSetting{role: "reader", name: "work_mem"}, false},
		{clusterSetting{name: "Role"}, false},
		{clusterSetting{role: "app", name: "session_authorization"}, false},
		{clusterSetting{name: "seed"}, false},
		{clusterSetting{name: "plpgsql.extra_warnings"}, false},
	} {
		if ok, err := restorable(c.setting, m, contexts); err != nil || ok != c.ok {
			t.Errorf("%+v: %v, %v", c.setting, ok, err)
		}
	}
	if _, err := restorable(clusterSetting{name: "intruder"}, m, contexts); clusterReason(err) != "a setting the server doesn't know" {
		t.Fatal("a name no module could define was not refused:", err)
	}
}

func TestReadTOCSortsEntriesIntoPasses(t *testing.T) {
	listing := `;
; Archive created at 2026-11-21 14:27:32 UTC
;
3433; 1262 16387 DATABASE - app app
3434; 0 0 COMMENT - DATABASE app app
3435; 0 0 ACL - DATABASE app app
3436; 0 0 DATABASE PROPERTIES - app app
6; 2615 16390 SCHEMA - reporting reader
2; 3079 16391 EXTENSION - hstore
218; 1259 16387 TABLE reporting records reader
3450; 0 16387 TABLE DATA reporting records reader
219; 1259 16400 MATERIALIZED VIEW public totals app
3437; 0 0 ACL - SCHEMA reporting reader
3438; 826 16401 DEFAULT ACL reporting DEFAULT PRIVILEGES FOR TABLES reader
3451; 0 16400 MATERIALIZED VIEW DATA public totals app
`
	entries, err := readTOC([]byte(listing))
	if err != nil {
		t.Fatal(err)
	}
	kinds := []tocKind{}
	for _, e := range entries {
		kinds = append(kinds, e.kind)
	}
	if want := []tocKind{tocDatabase, tocDatabase, tocDatabase, tocDatabase, tocSchema, tocOther, tocOther, tocOther, tocOther, tocACL, tocACL, tocMaterializedData}; !slices.Equal(kinds, want) {
		t.Fatalf("kinds %v, want %v", kinds, want)
	}
	if got := tocList(entries, func(k tocKind) bool { return k == tocSchema }); got != "6; 2615 16390 SCHEMA - reporting reader\n" {
		t.Fatalf("schema pass %q", got)
	}
	if _, err := readTOC([]byte("not an entry\n")); err == nil {
		t.Fatal("read a line that is no entry")
	}
}

func TestOwnerClusterSQL(t *testing.T) {
	got := ownerClusterSQL("app", "postgres", []string{"reader"}, "reader")
	for _, want := range []string{
		`CREATE ROLE "app" LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS INHERIT;`,
		`CREATE ROLE "reader" NOLOGIN NOSUPERUSER`,
		`GRANT "reader" TO "app" WITH INHERIT TRUE, SET TRUE;`,
		"DROP DATABASE postgres;\nCREATE DATABASE \"postgres\" OWNER \"reader\" TEMPLATE template0;",
		`ALTER ROLE "postgres" PASSWORD NULL;`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("%q lacks %q", got, want)
		}
	}
	if strings.Contains(ownerClusterSQL("app", "app", nil, "app"), "DROP DATABASE") {
		t.Fatal("dropped the maintenance database for another database's name")
	}
	if got := passwordSQL("app", "it's"); got != `ALTER ROLE "app" PASSWORD 'it''s';`+"\n" {
		t.Fatalf("password statement %q", got)
	}
}

func TestRestoreRolesRefusesPostgreSQLsOwnNames(t *testing.T) {
	for _, m := range []backup.Manifest{
		{Username: "postgres", Database: "app"},
		{Username: "pg_app", Database: "app"},
		{Username: "app", Database: "template1"},
	} {
		if _, err := restoreRoles(m); err == nil {
			t.Errorf("restored %+v", m)
		}
	}
	for _, role := range []string{"app", "postgres", "pg_monitor", ""} {
		if _, err := restoreRoles(backup.Manifest{Username: "app", Database: "app", Roles: []backup.Role{{Name: role}}}); err == nil {
			t.Errorf("created role %q", role)
		}
	}
	if roles, err := restoreRoles(backup.Manifest{Username: "app", Database: "postgres", Roles: []backup.Role{{Name: "reader"}}}); err != nil || !slices.Equal(roles, []string{"reader"}) {
		t.Fatal(roles, err)
	}
}

func TestPostgresVerifier(t *testing.T) {
	for _, v := range []string{"SCRAM-SHA-256$4096:c2FsdA==$c3RvcmVk:c2VydmVy", "md5" + strings.Repeat("a", 32)} {
		if !postgresVerifier.MatchString(v) {
			t.Errorf("refused %q", v)
		}
	}
	for _, v := range []string{"", "password", "SCRAM-SHA-256$4096:a$b:c'; DROP ROLE app; --", "md5" + strings.Repeat("a", 31), "SCRAM-SHA-256$4096:a$b:c\n"} {
		if postgresVerifier.MatchString(v) {
			t.Errorf("accepted %q", v)
		}
	}
}

// A volume records whether its cluster is in the owner model, and a plan whose database is in the
// other model is refused on it: the model changes only with new storage.
func TestPostgresVolumeMatchesItsRoleModel(t *testing.T) {
	_, p := testVisualJournal(t)
	image := domain.PostgresImage
	volume := uuid.New()
	plan := func(roles string) deployment.Plan {
		q := p
		q.Specification.Volumes = []domain.Volume{{ID: volume, Name: "Data"}}
		q.Specification.Applications = []domain.Application{{ID: uuid.New(), Name: "Database", Kind: "postgres", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Postgres: &domain.Postgres{Database: "app", Username: "app", PasswordSecretVersionID: uuid.New(), Roles: roles}, Mounts: []domain.VolumeMount{{VolumeID: volume, Target: domain.PostgresMount(image)}}}}
		return q
	}
	owner, legacy := plan(domain.PostgresOwner), plan("")
	if volumeLabels(owner, volume)[labelPostgresRoles] != postgresOwnerRoles || volumeLabels(legacy, volume)[labelPostgresRoles] != "" {
		t.Fatal("a volume doesn't record its cluster's role model")
	}
	release, _ := domain.PostgresReleaseOf(image)
	if restoreTargetLabels(legacy, volume, release)[labelPostgresRoles] != postgresOwnerRoles {
		t.Fatal("a restore target doesn't record the owner model every restore creates")
	}
	for _, c := range []struct {
		labels map[string]string
		p      deployment.Plan
		err    error
	}{
		{volumeLabels(owner, volume), owner, nil},
		{volumeLabels(legacy, volume), legacy, nil},
		{volumeLabels(legacy, volume), owner, errPostgresRolesChange},
		{volumeLabels(owner, volume), legacy, errPostgresRolesChange},
	} {
		if err := postgresVolumeMatches(c.labels, c.p, volume); !errors.Is(err, c.err) {
			t.Errorf("%v on %v: %v, want %v", c.p.Specification.Applications[0].Postgres.Roles, c.labels[labelPostgresRoles], err, c.err)
		}
	}
	if storageState(errPostgresRolesChange) != "failed" || storageState(errLegacyNewDatabase) != "failed" {
		t.Fatal("a refused role model isn't a failed deployment")
	}
}
