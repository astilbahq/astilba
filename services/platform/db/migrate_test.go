package dbmigrate

import (
	"io/fs"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// firstMarked is the first migration that could record itself as additive.
const firstMarked = 43

var (
	migrationName = regexp.MustCompile(`^(\d+)_[a-z0-9_]+\.sql$`)
	marker        = regexp.MustCompile(`^INSERT INTO schema_additive_migrations \(version\) VALUES \((\d+)\)$`)
	notAdditive   = regexp.MustCompile(`(?m)^-- astilba: not additive: \S`)
	space         = regexp.MustCompile(`\s+`)

	// Each form is anchored at both ends, so anything it does not name is not additive.
	createTable   = regexp.MustCompile(`^CREATE TABLE ([a-z_][a-z0-9_]*) \((.*)\)$`)
	createIndex   = regexp.MustCompile(`^CREATE (UNIQUE )?INDEX [a-z0-9_]+ ON ([a-z_][a-z0-9_]*) [^;]*$`)
	createFunc    = regexp.MustCompile(`^CREATE FUNCTION [a-z_][a-z0-9_]*\(`)
	createTrigger = regexp.MustCompile(`^CREATE TRIGGER [a-z0-9_]+ (?:BEFORE|AFTER) .*? ON ([a-z_][a-z0-9_]*) FOR EACH ROW EXECUTE FUNCTION [a-z_][a-z0-9_]*\(\)$`)
	addColumn     = regexp.MustCompile(`^ALTER TABLE ([a-z_][a-z0-9_]*) ADD COLUMN [a-z_][a-z0-9_]* (.*)$`)
	insertValues  = regexp.MustCompile(`^INSERT INTO ([a-z_][a-z0-9_]*) (?:\([a-z0-9_, ]*\) )?VALUES `)
)

// Each migration from firstMarked on either records itself as additive, and then holds only
// statements an older release runs unchanged beside, or says why it does not.
func TestMigrationsClaimOnlyWhatTheyDo(t *testing.T) {
	entries, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	var newest int64
	for _, entry := range entries {
		match := migrationName.FindStringSubmatch(entry.Name())
		if match == nil {
			t.Fatalf("%s: not a numbered migration", entry.Name())
		}
		version, _ := strconv.ParseInt(match[1], 10, 64)
		newest = max(newest, version)
		if version < firstMarked {
			continue
		}
		raw, err := fs.ReadFile(migrations, "migrations/"+entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		if why := checkClaim(version, string(raw)); why != "" {
			t.Errorf("%s: %s", entry.Name(), why)
		}
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
	unmark := "DELETE FROM schema_additive_migrations WHERE version = " + strconv.FormatInt(version, 10)
	if !created["schema_additive_migrations"] && !contains(splitStatements(down), unmark) {
		return "its Down section must say `" + unmark + ";`"
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
		for _, element := range topLevel(m[2]) {
			if why := reference(element); why != "" {
				return why
			}
		}
		return ""
	}
	if m := createIndex.FindStringSubmatch(s); m != nil {
		if m[1] != "" && !created[m[2]] {
			return "a unique index on an existing table can refuse an older release's writes"
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
			strings.Contains(definition, "CHECK"), strings.Contains(definition, "UNIQUE"), strings.Contains(definition, "PRIMARY KEY"):
			return "a column added to an existing table must accept every row an older release inserts"
		}
		return reference(definition)
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

// reference returns why a column or constraint's foreign key could refuse an older release's
// delete of the row it refers to, or "".
func reference(element string) string {
	switch {
	case !strings.Contains(element, "REFERENCES "):
		return ""
	case strings.Contains(element, " ON DELETE CASCADE"):
		return ""
	case strings.Contains(element, " ON DELETE SET NULL") && !strings.Contains(element, "NOT NULL") && !strings.Contains(element, "PRIMARY KEY") && !strings.HasPrefix(element, "FOREIGN KEY") && !strings.HasPrefix(element, "CONSTRAINT "):
		// Only a column's own reference shows that the column may be null.
		return ""
	}
	return "a reference must say ON DELETE CASCADE, or ON DELETE SET NULL on a column that may be null"
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
// constants with 'x' and dollar-quoted bodies with $$x$$, and normalising white space.
func splitStatements(sql string) []string {
	var out []string
	var b strings.Builder
	flush := func() {
		if s := strings.TrimSpace(space.ReplaceAllString(b.String(), " ")); s != "" {
			out = append(out, s)
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
			// A doubled quote inside a constant ends one scan and starts the next, harmlessly.
			i = skipTo(i+1, "'")
			b.WriteString("'x'")
		case sql[i] == '"':
			j := skipTo(i+1, `"`)
			b.WriteString(sql[i:j])
			i = j
		case sql[i] == '$':
			if tag := dollarTag(sql[i:]); tag != "" {
				i = skipTo(i+len(tag), tag)
				b.WriteString("$$x$$")
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

var tagPattern = regexp.MustCompile(`^\$(?:[A-Za-z_][A-Za-z0-9_]*)?\$`)

func dollarTag(s string) string { return tagPattern.FindString(s) }

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
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
		{"update", "UPDATE secret_versions SET n=1;", false},
		{"seed existing table", "INSERT INTO workspaces (id) VALUES (gen_random_uuid());", false},
		{"seed new table", "CREATE TABLE a (n integer);\nINSERT INTO a (n) VALUES (1);", true},
		{"backfill new table", "CREATE TABLE a (id uuid);\nINSERT INTO a SELECT id FROM servers;", false},
		{"backfill through values", "CREATE TABLE a (id uuid);\nINSERT INTO a (id) VALUES ((SELECT id FROM servers LIMIT 1));", false},
		{"quoted comment marker", "CREATE TABLE a (s text DEFAULT '--'); DROP TABLE servers;\nCREATE INDEX i ON a (s);", false},
		{"nested dollar tags", "CREATE FUNCTION f() RETURNS text LANGUAGE sql AS $a$ SELECT '$$' $a$; DROP TABLE servers;", false},
		{"semicolon in a constant", "COMMENT ON TABLE servers IS 'a; DROP TABLE servers';", true},
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
