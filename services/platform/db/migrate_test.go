package dbmigrate

import (
	"io/fs"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// firstMarked is the first migration that could record itself as additive.
const firstMarked = 39

var (
	migrationName = regexp.MustCompile(`^(\d+)_[a-z0-9_]+\.sql$`)
	marker        = regexp.MustCompile(`^INSERT INTO schema_additive_migrations \(version\) VALUES \((\d+)\)$`)
	notAdditive   = regexp.MustCompile(`(?m)^-- astilba: not additive: \S`)
	dollarQuoted  = regexp.MustCompile(`(?s)(\$[A-Za-z_]*\$).*?(\$[A-Za-z_]*\$)`)
	lineComment   = regexp.MustCompile(`--[^\n]*`)
	space         = regexp.MustCompile(`\s+`)

	createTable   = regexp.MustCompile(`^CREATE TABLE (?:IF NOT EXISTS )?([a-z_][a-z0-9_]*)\b`)
	createIndex   = regexp.MustCompile(`^CREATE (UNIQUE )?INDEX (?:IF NOT EXISTS )?[a-z0-9_]+ ON ([a-z_][a-z0-9_]*)\b`)
	createFunc    = regexp.MustCompile(`^CREATE (?:OR REPLACE )?FUNCTION `)
	createTrigger = regexp.MustCompile(`^CREATE (?:OR REPLACE )?TRIGGER [a-z0-9_]+ .* ON ([a-z_][a-z0-9_]*)\b`)
	alterTable    = regexp.MustCompile(`^ALTER TABLE (?:IF EXISTS )?(?:ONLY )?([a-z_][a-z0-9_]*) (.*)$`)
	addColumn     = regexp.MustCompile(`^ADD COLUMN (?:IF NOT EXISTS )?[a-z_][a-z0-9_]* `)
	insertInto    = regexp.MustCompile(`^INSERT INTO ([a-z_][a-z0-9_]*)\b`)
	references    = regexp.MustCompile(`\bREFERENCES\b[^,)]*(?:\)[^,]*)?`)
	safeDelete    = regexp.MustCompile(`\bON DELETE (?:CASCADE|SET NULL)\b`)
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
		up, ok := upSection(string(raw))
		if !ok {
			t.Fatalf("%s: no goose Up section", entry.Name())
		}
		if err := checkClaim(version, up); err != "" {
			t.Errorf("%s: %s", entry.Name(), err)
		}
	}
	if newest != Version {
		t.Fatalf("Version is %d, but the newest migration is %d", Version, newest)
	}
}

func upSection(text string) (string, bool) {
	_, rest, ok := strings.Cut(text, "-- +goose Up")
	if !ok {
		return "", false
	}
	up, _, _ := strings.Cut(rest, "-- +goose Down")
	return up, true
}

// checkClaim returns why a migration's Up section does not match what it claims, or "".
func checkClaim(version int64, up string) string {
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
	return ""
}

// additive returns why one statement is not additive, or "".
func additive(s string, created map[string]bool) string {
	unsafeReference := func(clause string) bool {
		for _, ref := range references.FindAllString(clause, -1) {
			if !safeDelete.MatchString(ref) {
				return true
			}
		}
		return false
	}
	switch {
	case marker.MatchString(s):
		return ""
	case createTable.MatchString(s):
		// An older release that deletes a referenced row must not be refused for rows it
		// never wrote.
		if unsafeReference(s) {
			return "a new table's reference must say ON DELETE CASCADE or SET NULL"
		}
		return ""
	case createIndex.MatchString(s):
		m := createIndex.FindStringSubmatch(s)
		if m[1] != "" && !created[m[2]] {
			return "a unique index on an existing table can refuse an older release's writes"
		}
		return ""
	case createFunc.MatchString(s):
		return ""
	case createTrigger.MatchString(s):
		if !created[createTrigger.FindStringSubmatch(s)[1]] {
			return "a trigger on an existing table changes what an older release's writes do"
		}
		return ""
	case alterTable.MatchString(s):
		m := alterTable.FindStringSubmatch(s)
		if created[m[1]] {
			return ""
		}
		for _, clause := range strings.Split(m[2], ", ADD ") {
			if !strings.HasPrefix(clause, "ADD ") {
				clause = "ADD " + clause
			}
			if !addColumn.MatchString(clause) {
				return "only ADD COLUMN may change an existing table"
			}
			if strings.Contains(clause, " NOT NULL") && !strings.Contains(clause, " DEFAULT ") || strings.Contains(clause, " UNIQUE") || strings.Contains(clause, " PRIMARY KEY") || unsafeReference(clause) {
				return "a column added to an existing table must accept an older release's inserts and deletes"
			}
		}
		return ""
	case insertInto.MatchString(s):
		if !created[insertInto.FindStringSubmatch(s)[1]] {
			return "rows written into an existing table are data an older release did not expect"
		}
		return ""
	case strings.HasPrefix(s, "COMMENT ON "), strings.HasPrefix(s, "GRANT "):
		return ""
	}
	return "not recognised as additive"
}

// splitStatements drops comments and function bodies, and normalises white space.
func splitStatements(up string) []string {
	up = lineComment.ReplaceAllString(up, "")
	up = dollarQuoted.ReplaceAllString(up, "$$body$$")
	var out []string
	for _, s := range strings.Split(up, ";") {
		s = strings.TrimSpace(space.ReplaceAllString(s, " "))
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func TestAdditiveClaims(t *testing.T) {
	for _, test := range []struct {
		name, up string
		ok       bool
	}{
		{"new table", "CREATE TABLE a (id uuid PRIMARY KEY, w uuid REFERENCES workspaces(id) ON DELETE CASCADE);\nINSERT INTO schema_additive_migrations (version) VALUES (40);", true},
		{"unmarked", "CREATE TABLE a (id uuid);", false},
		{"declared", "-- astilba: not additive: drops a column\nALTER TABLE a DROP COLUMN b;", true},
		{"declared and marked", "-- astilba: not additive: x\nINSERT INTO schema_additive_migrations (version) VALUES (40);", false},
		{"other version", "INSERT INTO schema_additive_migrations (version) VALUES (41);", false},
		{"marked twice", "INSERT INTO schema_additive_migrations (version) VALUES (40);\nINSERT INTO schema_additive_migrations (version) VALUES (40);", false},
		{"restricting reference", "CREATE TABLE a (w uuid REFERENCES workspaces(id));\nINSERT INTO schema_additive_migrations (version) VALUES (40);", false},
		{"nullable column", "ALTER TABLE secret_versions ADD COLUMN plaintext_bytes integer, ADD COLUMN json_bytes integer CHECK (json_bytes >= 0);\nINSERT INTO schema_additive_migrations (version) VALUES (40);", true},
		{"required column", "ALTER TABLE secret_versions ADD COLUMN n integer NOT NULL;\nINSERT INTO schema_additive_migrations (version) VALUES (40);", false},
		{"defaulted column", "ALTER TABLE secret_versions ADD COLUMN n integer NOT NULL DEFAULT 0;\nINSERT INTO schema_additive_migrations (version) VALUES (40);", true},
		{"dropped column", "ALTER TABLE secret_versions DROP COLUMN n;\nINSERT INTO schema_additive_migrations (version) VALUES (40);", false},
		{"unique index on existing table", "CREATE UNIQUE INDEX i ON secret_versions (id);\nINSERT INTO schema_additive_migrations (version) VALUES (40);", false},
		{"index on existing table", "CREATE INDEX i ON secret_versions (id);\nINSERT INTO schema_additive_migrations (version) VALUES (40);", true},
		{"trigger on new table", "CREATE TABLE a (at timestamptz);\nCREATE FUNCTION f() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN DELETE FROM a; UPDATE b SET c=1; RETURN NEW; END; $$;\nCREATE TRIGGER t BEFORE INSERT ON a FOR EACH ROW EXECUTE FUNCTION f();\nINSERT INTO schema_additive_migrations (version) VALUES (40);", true},
		{"trigger on existing table", "CREATE TRIGGER t BEFORE INSERT ON secret_versions FOR EACH ROW EXECUTE FUNCTION f();\nINSERT INTO schema_additive_migrations (version) VALUES (40);", false},
		{"update", "UPDATE secret_versions SET n=1;\nINSERT INTO schema_additive_migrations (version) VALUES (40);", false},
		{"seed existing table", "INSERT INTO workspaces (id) VALUES (gen_random_uuid());\nINSERT INTO schema_additive_migrations (version) VALUES (40);", false},
		{"seed new table", "CREATE TABLE a (n integer);\nINSERT INTO a (n) VALUES (1);\nINSERT INTO schema_additive_migrations (version) VALUES (40);", true},
	} {
		if why := checkClaim(40, test.up); (why == "") != test.ok {
			t.Errorf("%s: %q", test.name, why)
		}
	}
}
