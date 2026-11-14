package dbmigrate

import (
	"io/fs"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

var (
	migrationName = regexp.MustCompile(`^(\d+)_[a-z0-9_]+\.sql$`)
	notAdditive   = regexp.MustCompile(`(?m)^-- astilba: not additive: \S`)
	space         = regexp.MustCompile(`\s+`)
	tagPattern    = regexp.MustCompile(`^\$(?:[A-Za-z_][A-Za-z0-9_]*)?\$`)

	// Statements are matched in upper case, as PostgreSQL reads keywords and names in any
	// case. Each form is anchored at both ends, so anything it does not name is not additive.
	marker       = regexp.MustCompile(`^INSERT INTO SCHEMA_ADDITIVE_MIGRATIONS ?\(VERSION\) VALUES ?\((\d+)\)$`)
	createTable  = regexp.MustCompile(`^CREATE TABLE ([A-Z_][A-Z0-9_]*) ?\(`)
	createIndex  = regexp.MustCompile(`^CREATE (UNIQUE )?INDEX [A-Z0-9_]+ ON ([A-Z_][A-Z0-9_]*) ?(.*)$`)
	plainColumns = regexp.MustCompile(`^\((?:[A-Z_][A-Z0-9_]*(?: ASC| DESC)?(?:, ?|\)$))+$`)
	// A column compared with string constants, which no row an older release writes can fail.
	constantPredicate = regexp.MustCompile(`^[A-Z_][A-Z0-9_]* (?:NOT )?IN ?\('[^']*'(?:, ?'[^']*')*\)$`)
	createFunc        = regexp.MustCompile(`^CREATE FUNCTION [A-Z_][A-Z0-9_]* ?\(`)
	createTrigger     = regexp.MustCompile(`^CREATE TRIGGER [A-Z0-9_]+ (?:BEFORE|AFTER) .*? ON ([A-Z_][A-Z0-9_]*) FOR EACH (?:ROW|STATEMENT) EXECUTE FUNCTION [A-Z_][A-Z0-9_]* ?\(\)$`)
	addColumn         = regexp.MustCompile(`^ALTER TABLE ([A-Z_][A-Z0-9_]*) ADD COLUMN [A-Z_][A-Z0-9_]* (.*)$`)
	insertValues      = regexp.MustCompile(`^INSERT INTO ([A-Z_][A-Z0-9_]*) ?(?:\([A-Z0-9_, ]*\) ?)?VALUES ?\(`)
)

// Each migration from the one that created the marker table on either records itself as
// additive, and then holds only statements an older release runs unchanged beside, or says why
// it does not. This catches honest mistakes in the repository's own style; it is not a parser.
func TestMigrationsClaimOnlyWhatTheyDo(t *testing.T) {
	entries, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	var newest int64
	firstMarked := int64(-1)
	for _, entry := range entries {
		match := migrationName.FindStringSubmatch(entry.Name())
		if match == nil {
			t.Fatalf("%s: not a numbered migration", entry.Name())
		}
		version, _ := strconv.ParseInt(match[1], 10, 64)
		newest = max(newest, version)
		raw, err := fs.ReadFile(migrations, "migrations/"+entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		_, rest, _ := strings.Cut(string(raw), "-- +goose Up")
		up, _, _ := strings.Cut(rest, "-- +goose Down")
		for _, s := range splitStatements(up) {
			if m := createTable.FindStringSubmatch(s); m != nil && m[1] == "SCHEMA_ADDITIVE_MIGRATIONS" {
				firstMarked = version
			}
		}
		// Entries are in version order, so every migration from the marker table's own is held to
		// its claim, whatever number that one ends up with.
		if firstMarked < 0 {
			continue
		}
		if why := checkClaim(version, string(raw)); why != "" {
			t.Errorf("%s: %s", entry.Name(), why)
		}
	}
	if firstMarked < 0 {
		t.Fatal("no migration creates schema_additive_migrations")
	}
	if newest != Version {
		t.Fatalf("Version is %d, but the newest migration is %d", Version, newest)
	}
}

// checkClaim returns why a migration does not match what it claims, or "".
func checkClaim(version int64, text string) string {
	_, rest, ok := strings.Cut(text, "-- +goose Up")
	if !ok {
		return "no goose Up section"
	}
	up, down, _ := strings.Cut(rest, "-- +goose Down")
	declared := notAdditive.MatchString(up)
	statements := splitStatements(up)
	marked := 0
	for _, s := range statements {
		if m := marker.FindStringSubmatch(s); m != nil {
			if m[1] != strconv.FormatInt(version, 10) {
				return "records version " + m[1] + " as additive"
			}
			marked++
		}
	}
	switch {
	case declared && marked > 0:
		return "both records itself as additive and says it is not"
	case !declared && marked != 1:
		return "must record itself once as additive, or say `-- astilba: not additive: <reason>`"
	case declared:
		return ""
	}
	created := map[string]bool{}
	for _, s := range statements {
		if m := createTable.FindStringSubmatch(s); m != nil {
			created[m[1]] = true
		}
	}
	for _, s := range statements {
		if why := additive(s, created); why != "" {
			return why + ": " + s
		}
	}
	// Goose's Down deletes only its own version row, so going down and up again would find the
	// marker still there. The migration that creates the table drops it instead.
	unmark := "DELETE FROM SCHEMA_ADDITIVE_MIGRATIONS WHERE VERSION = " + strconv.FormatInt(version, 10)
	if !created["SCHEMA_ADDITIVE_MIGRATIONS"] && !slices.Contains(splitStatements(down), unmark) {
		return "its Down section must delete its marker: `DELETE FROM schema_additive_migrations WHERE version = " + strconv.FormatInt(version, 10) + ";`"
	}
	return ""
}

// additive returns why one statement is not additive, or "".
func additive(s string, created map[string]bool) string {
	if marker.MatchString(s) || strings.HasPrefix(s, "COMMENT ON ") || strings.HasPrefix(s, "GRANT ") || createFunc.MatchString(s) {
		// A function is created, never replaced: one that exists already makes the migration fail.
		return ""
	}
	if m := createTable.FindStringSubmatch(s); m != nil {
		// The column list must end the statement: INHERITS or AS would tie the table to existing rows.
		body, ok := enclosed(s[len(m[0])-1:])
		if !ok {
			return "a new table is its column list and nothing more"
		}
		for _, element := range topLevel(body) {
			if why := reference(element, true); why != "" {
				return why
			}
		}
		return ""
	}
	if m := createIndex.FindStringSubmatch(s); m != nil {
		switch {
		case created[m[2]]:
			return ""
		case m[1] != "":
			return "a unique index on an existing table can refuse an older release's writes"
		}
		// An expression or predicate runs on every write an older release makes, so it may only be
		// one that cannot fail.
		columns, predicate, partial := strings.Cut(m[3], " WHERE ")
		if !plainColumns.MatchString(columns) || partial && !constantPredicate.MatchString(predicate) {
			return "an index on an existing table may list only its columns, limited only by comparing a column with constants"
		}
		return ""
	}
	if m := createTrigger.FindStringSubmatch(s); m != nil {
		if !created[m[1]] {
			return "a trigger on an existing table changes what an older release's writes do"
		}
		return ""
	}
	if m := addColumn.FindStringSubmatch(s); m != nil {
		definition := m[2]
		switch {
		case created[m[1]]:
			return "define a new table's columns in its CREATE TABLE"
		case len(topLevel(definition)) != 1:
			return "add one column per statement, and nothing else"
		case strings.Contains(definition, "NOT NULL") && !strings.Contains(definition, "DEFAULT "),
			strings.Contains(definition, "CHECK"), strings.Contains(definition, "UNIQUE"),
			strings.Contains(definition, "PRIMARY KEY"), strings.Contains(definition, "GENERATED"):
			return "a column added to an existing table must accept every row an older release inserts"
		}
		return reference(definition, false)
	}
	if m := insertValues.FindStringSubmatch(s); m != nil {
		switch {
		case !created[m[1]]:
			return "rows written into an existing table are data an older release did not expect"
		case strings.Contains(s, "SELECT"):
			// Rows an older release writes after a rollback would never be copied: goose does not
			// run the migration again. Copying belongs in a job that runs again.
			return "a new table may be seeded with constants, not filled from existing rows"
		}
		return ""
	}
	return "not recognised as additive"
}

// reference returns why a column or constraint's foreign key could refuse or widen an older
// release's delete of the row it refers to, or "". A new table's rows may go with that row; an
// existing table's rows are the older release's, so its new column may only be emptied.
func reference(element string, newTable bool) string {
	switch {
	case !strings.Contains(element, "REFERENCES "):
		return ""
	case newTable && strings.Contains(element, " ON DELETE CASCADE"):
		return ""
	case strings.Contains(element, " ON DELETE SET NULL") && !strings.Contains(element, "NOT NULL") && !strings.Contains(element, "PRIMARY KEY") && !strings.HasPrefix(element, "FOREIGN KEY") && !strings.HasPrefix(element, "CONSTRAINT "):
		// Only a column's own reference shows that the column may be null.
		return ""
	case newTable:
		return "a new table's reference must say ON DELETE CASCADE, or ON DELETE SET NULL on a column that may be null"
	}
	return "a column added to an existing table may refer to another row only with ON DELETE SET NULL"
}

// enclosed returns what the parentheses opening s enclose, if they close at its end.
func enclosed(s string) (string, bool) {
	depth := 0
	for i, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return s[1:i], i == len(s)-1
			}
		}
	}
	return "", false
}

// topLevel splits a list on the commas outside parentheses.
func topLevel(list string) []string {
	var out []string
	depth, start := 0, 0
	for i, r := range list {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, strings.TrimSpace(list[start:i]))
				start = i + 1
			}
		}
	}
	return append(out, strings.TrimSpace(list[start:]))
}

// splitStatements splits SQL on semicolons outside quotes, dropping comments, replacing string
// constants with 'X' and dollar-quoted bodies with $$X$$, and normalising white space and case.
func splitStatements(sql string) []string {
	var out []string
	var b strings.Builder
	flush := func() {
		if s := strings.TrimSpace(space.ReplaceAllString(b.String(), " ")); s != "" {
			out = append(out, strings.ToUpper(s))
		}
		b.Reset()
	}
	skipTo := func(i int, end string) int {
		j := strings.Index(sql[i:], end)
		if j < 0 {
			return len(sql)
		}
		return i + j + len(end)
	}
	for i := 0; i < len(sql); {
		switch {
		case strings.HasPrefix(sql[i:], "--"):
			i = skipTo(i, "\n")
			b.WriteByte(' ')
		case strings.HasPrefix(sql[i:], "/*"):
			i = skipTo(i+2, "*/")
			b.WriteByte(' ')
		case sql[i] == '\'':
			escapes := i > 0 && (sql[i-1] == 'E' || sql[i-1] == 'e') && (i < 2 || !identifier(sql[i-2]))
			// A doubled quote ends one constant and starts the next, which is harmless. In an
			// E'' constant a backslash also escapes the character after it.
			for i++; i < len(sql) && sql[i] != '\''; i++ {
				if escapes && sql[i] == '\\' {
					i++
				}
			}
			i++
			b.WriteString("'X'")
		case sql[i] == '"':
			j := skipTo(i+1, `"`)
			b.WriteString(sql[i:j])
			i = j
		case sql[i] == '$' && (i == 0 || !identifier(sql[i-1])):
			if tag := tagPattern.FindString(sql[i:]); tag != "" {
				i = skipTo(i+len(tag), tag)
				b.WriteString("$$X$$")
				continue
			}
			b.WriteByte(sql[i])
			i++
		case sql[i] == ';':
			flush()
			i++
		default:
			b.WriteByte(sql[i])
			i++
		}
	}
	flush()
	return out
}

func identifier(c byte) bool {
	return c == '_' || c == '$' || '0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
}

func TestAdditiveClaims(t *testing.T) {
	const mark = "\nINSERT INTO schema_additive_migrations (version) VALUES (40);\n-- +goose Down\nDELETE FROM schema_additive_migrations WHERE version = 40;\n"
	for _, test := range []struct {
		name, up string
		ok       bool
	}{
		{"new table", "CREATE TABLE a (id uuid PRIMARY KEY, w uuid REFERENCES workspaces(id) ON DELETE CASCADE);", true},
		{"composite reference", "CREATE TABLE a (workspace_id uuid NOT NULL, server_id uuid NOT NULL, FOREIGN KEY (workspace_id, server_id) REFERENCES servers(workspace_id, id) ON DELETE CASCADE);", true},
		{"restricting reference", "CREATE TABLE a (w uuid REFERENCES workspaces(id));", false},
		{"set null on required column", "CREATE TABLE a (w uuid NOT NULL REFERENCES workspaces(id) ON DELETE SET NULL);", false},
		{"set null on optional column", "CREATE TABLE a (w uuid REFERENCES workspaces(id) ON DELETE SET NULL);", true},
		{"set null by constraint", "CREATE TABLE a (w uuid NOT NULL, FOREIGN KEY (w) REFERENCES workspaces(id) ON DELETE SET NULL);", false},
		{"existing name", "CREATE TABLE IF NOT EXISTS servers (id uuid);\nALTER TABLE servers DROP COLUMN name;", false},
		{"partition", "CREATE TABLE a PARTITION OF servers FOR VALUES IN (1);", false},
		{"nullable column", "ALTER TABLE secret_versions ADD COLUMN plaintext_bytes integer;", true},
		{"two columns", "ALTER TABLE secret_versions ADD COLUMN a integer, ADD COLUMN b integer;", false},
		{"add then drop", "ALTER TABLE servers ADD COLUMN a int, DROP COLUMN name;", false},
		{"add then drop default", "ALTER TABLE servers ADD COLUMN a int, ALTER COLUMN name DROP DEFAULT;", false},
		{"required column", "ALTER TABLE secret_versions ADD COLUMN n integer NOT NULL;", false},
		{"defaulted column", "ALTER TABLE secret_versions ADD COLUMN n integer NOT NULL DEFAULT 0;", true},
		{"checked column", "ALTER TABLE servers ADD COLUMN kind text CHECK (coalesce(kind, '') <> '');", false},
		{"restricting column", "ALTER TABLE servers ADD COLUMN w uuid REFERENCES workspaces(id);", false},
		{"column on new table", "CREATE TABLE a (id uuid);\nALTER TABLE a ADD COLUMN w uuid REFERENCES workspaces(id);", false},
		{"dropped column", "ALTER TABLE secret_versions DROP COLUMN n;", false},
		{"unique index on existing table", "CREATE UNIQUE INDEX i ON secret_versions (id);", false},
		{"index on existing table", "CREATE INDEX i ON secret_versions (workspace_id, created_at);", true},
		{"replaced function", "CREATE OR REPLACE FUNCTION immutable_specification() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$;", false},
		{"trigger on new table", "CREATE TABLE a (at timestamptz);\nCREATE FUNCTION f() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN DELETE FROM a; UPDATE b SET c=1; RETURN NEW; END; $$;\nCREATE TRIGGER t BEFORE INSERT ON a FOR EACH ROW EXECUTE FUNCTION f();", true},
		{"trigger on existing table", "CREATE TRIGGER t BEFORE INSERT ON secret_versions FOR EACH ROW EXECUTE FUNCTION f();", false},
		{"statement trigger on new table", "CREATE TABLE a (at timestamptz);\nCREATE TRIGGER t BEFORE TRUNCATE ON a FOR EACH STATEMENT EXECUTE FUNCTION f();", true},
		{"statement trigger on existing table", "CREATE TRIGGER t BEFORE TRUNCATE ON workspaces FOR EACH STATEMENT EXECUTE FUNCTION f();", false},
		{"update", "UPDATE secret_versions SET n=1;", false},
		{"seed existing table", "INSERT INTO workspaces (id) VALUES (gen_random_uuid());", false},
		{"seed new table", "CREATE TABLE a (n integer);\nINSERT INTO a (n) VALUES (1);", true},
		{"backfill new table", "CREATE TABLE a (id uuid);\nINSERT INTO a SELECT id FROM servers;", false},
		{"backfill through values", "CREATE TABLE a (id uuid);\nINSERT INTO a (id) VALUES ((SELECT id FROM servers LIMIT 1));", false},
		{"quoted comment marker", "CREATE TABLE a (s text DEFAULT '--'); DROP TABLE servers;\nCREATE INDEX i ON a (s);", false},
		{"nested dollar tags", "CREATE FUNCTION f() RETURNS text LANGUAGE sql AS $a$ SELECT '$$' $a$; DROP TABLE servers;", false},
		{"semicolon in a constant", "COMMENT ON TABLE servers IS 'a; DROP TABLE servers';", true},
		{"escaped quotes hide a statement", "CREATE TABLE a (s text);\nCOMMENT ON TABLE a IS E'it\\'s';\nALTER TABLE servers DROP COLUMN name;\nCOMMENT ON COLUMN a.s IS E'don\\'t';", false},
		{"index in house style", "CREATE INDEX i ON audit_events(workspace_id, created_at DESC);", true},
		{"expression index on existing table", "CREATE INDEX i ON servers (((labels->>'port')::int));", false},
		{"partial index on existing table", "CREATE INDEX i ON servers (name) WHERE f(name);", false},
		{"index limited by constants", "CREATE INDEX i ON execution_operations(server_id, created_at) WHERE state NOT IN ('succeeded','failed','cancelled');", true},
		{"index limited by one constant", "CREATE INDEX i ON execution_operations (server_id) WHERE kind IN ('network');", true},
		{"constants and a function", "CREATE INDEX i ON execution_operations (server_id) WHERE state IN ('failed') AND f(kind);", false},
		{"constant compared through an expression", "CREATE INDEX i ON servers (id) WHERE (labels->>'port') IN ('1');", false},
		{"quote inside a constant", "CREATE INDEX i ON servers (id) WHERE name IN ('a'' OR f(name) OR ''');", false},
		{"expression limited by constants", "CREATE INDEX i ON servers ((lower(name))) WHERE kind IN ('a');", false},
		{"predicate without columns", "CREATE INDEX i ON servers WHERE kind IN ('a');", false},
		{"expression index on new table", "CREATE TABLE a (s text);\nCREATE INDEX i ON a ((lower(s))) WHERE s <> '';", true},
		{"inherited table", "CREATE TABLE a (id uuid) INHERITS (servers);", false},
		{"table from a query", "CREATE TABLE a (id, w) AS SELECT id, workspace_id FROM servers WHERE (NOT revoked);", false},
		{"lower-case reference", "CREATE TABLE a (w uuid NOT NULL references workspaces(id));", false},
		{"lower-case constraint on added column", "ALTER TABLE servers ADD COLUMN n integer not null;", false},
		{"lower-case backfill", "CREATE TABLE a (id uuid);\nINSERT INTO a (id) VALUES ((select id from servers limit 1));", false},
		{"lower-case statement", "create table a (id uuid primary key);", true},
		{"seed in house style", "CREATE TABLE a (n integer);\nINSERT INTO a(n) VALUES(1);", true},
		{"cascading column on existing table", "ALTER TABLE deployment_units ADD COLUMN preview_id uuid REFERENCES previews(id) ON DELETE CASCADE;", false},
		{"emptied column on existing table", "ALTER TABLE deployment_units ADD COLUMN preview_id uuid REFERENCES previews(id) ON DELETE SET NULL;", true},
		{"generated column on existing table", "ALTER TABLE servers ADD COLUMN p text GENERATED ALWAYS AS ((labels->>'p')::int::text) STORED;", false},
	} {
		if why := checkClaim(40, "-- +goose Up\n"+test.up+mark); (why == "") != test.ok {
			t.Errorf("%s: %q", test.name, why)
		}
	}
	for _, test := range []struct {
		name, text string
		ok         bool
	}{
		{"declared", "-- +goose Up\n-- astilba: not additive: drops a column\nALTER TABLE a DROP COLUMN b;\n-- +goose Down\n", true},
		{"declared and marked", "-- +goose Up\n-- astilba: not additive: x\nINSERT INTO schema_additive_migrations (version) VALUES (40);\n", false},
		{"unmarked", "-- +goose Up\nCREATE TABLE a (id uuid);\n-- +goose Down\nDROP TABLE a;\n", false},
		{"other version", "-- +goose Up\nINSERT INTO schema_additive_migrations (version) VALUES (41);\n-- +goose Down\nDELETE FROM schema_additive_migrations WHERE version = 41;\n", false},
		{"marked twice", "-- +goose Up\nINSERT INTO schema_additive_migrations (version) VALUES (40);\nINSERT INTO schema_additive_migrations (version) VALUES (40);\n-- +goose Down\nDELETE FROM schema_additive_migrations WHERE version = 40;\n", false},
		{"marker kept on the way down", "-- +goose Up\nCREATE TABLE a (id uuid);\nINSERT INTO schema_additive_migrations (version) VALUES (40);\n-- +goose Down\nDROP TABLE a;\n", false},
	} {
		if why := checkClaim(40, test.text); (why == "") != test.ok {
			t.Errorf("%s: %q", test.name, why)
		}
	}
}

// A release may complete the next custody stage, or repeat one, but never skip one.
func TestCustodyStagesAreTakenInOrder(t *testing.T) {
	for _, c := range []struct{ required, completed int }{{1, 0}, {1, 1}, {2, 1}, {2, 5}} {
		if err := stageRefusal(c.required, c.completed); err != nil {
			t.Errorf("%d after %d refused: %v", c.required, c.completed, err)
		}
	}
	for _, c := range []struct{ required, completed int }{{2, 0}, {3, 1}} {
		if stageRefusal(c.required, c.completed) == nil {
			t.Errorf("%d after %d allowed", c.required, c.completed)
		}
	}
}
