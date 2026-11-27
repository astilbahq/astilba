package store

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

var (
	createTable  = regexp.MustCompile(`(?is)^CREATE TABLE (?:IF NOT EXISTS )?(\w+)`)
	alterTable   = regexp.MustCompile(`(?is)^ALTER TABLE (?:ONLY )?(\w+)`)
	dropTable    = regexp.MustCompile(`(?is)^DROP TABLE (?:IF EXISTS )?(\w+)`)
	sealedColumn = regexp.MustCompile(`(?i)\b(encrypted_\w+)\s+bytea\b`)
	droppedCol   = regexp.MustCompile(`(?i)\bDROP COLUMN (?:IF EXISTS )?(encrypted_\w+)`)
	lineComment  = regexp.MustCompile(`--[^\n]*`)
	queryName    = regexp.MustCompile(`(?m)^-- name: (\w+)`)
	fromClause   = regexp.MustCompile(`(?i)\bFROM\s+`)
	selectClause = regexp.MustCompile(`(?i)\bSELECT\b`)
)

// A sealed column a rewrap or `custody convert` misses keeps its values under a retired key, or
// under the keyring, where nothing reports them. So every encrypted_* column the migrations leave
// is rewrapped, converted, counted, sampled at start-up and held while conversion completes. This
// reads the repository's own SQL in its own style; it is not a parser.
func TestEverySealedColumnIsRewrappedAndConverted(t *testing.T) {
	root := filepath.Join("..", "..")
	sealed := map[string][]string{} // table → its sealed columns
	migrations, err := filepath.Glob(filepath.Join(root, "db", "migrations", "*.sql"))
	if err != nil || len(migrations) == 0 {
		t.Fatal("no migrations", err)
	}
	slices.Sort(migrations)
	for _, path := range migrations {
		up, _, _ := strings.Cut(read(t, path), "-- +goose Down")
		for _, statement := range strings.Split(lineComment.ReplaceAllString(up, ""), ";") {
			statement = strings.TrimSpace(statement)
			if m := createTable.FindStringSubmatch(statement); m != nil {
				for _, c := range sealedColumn.FindAllStringSubmatch(statement, -1) {
					sealed[m[1]] = append(sealed[m[1]], c[1])
				}
			} else if m := alterTable.FindStringSubmatch(statement); m != nil {
				for _, c := range sealedColumn.FindAllStringSubmatch(statement, -1) {
					sealed[m[1]] = append(sealed[m[1]], c[1])
				}
				for _, c := range droppedCol.FindAllStringSubmatch(statement, -1) {
					sealed[m[1]] = slices.DeleteFunc(sealed[m[1]], func(s string) bool { return s == c[1] })
				}
			} else if m := dropTable.FindStringSubmatch(statement); m != nil {
				delete(sealed, m[1])
			}
		}
	}
	queryFiles, err := filepath.Glob(filepath.Join(root, "db", "queries", "*.sql"))
	if err != nil {
		t.Fatal(err)
	}
	queries := map[string]string{}
	for _, path := range queryFiles {
		source := read(t, path)
		names := queryName.FindAllStringSubmatchIndex(source, -1)
		for i, m := range names {
			end := len(source)
			if i+1 < len(names) {
				end = names[i+1][0]
			}
			queries[source[m[2]:m[3]]] = source[m[1]:end]
		}
	}
	rewrap, convert := read(t, "rewrap.go"), read(t, filepath.Join("..", "secretbroker", "convert.go"))
	// named reports whether a query with the prefix, called from code, matches.
	named := func(prefix, code string, matches func(body string) bool) bool {
		for name, body := range queries {
			if strings.HasPrefix(name, prefix) && strings.Contains(code, "q."+name+"(") && matches(body) {
				return true
			}
		}
		return false
	}
	if len(sealed) == 0 {
		t.Fatal("the migrations have no sealed columns")
	}
	for table, columns := range sealed {
		for _, column := range columns {
			selects := func(body string) bool { return selectsFrom(body, column, table) }
			updates := regexp.MustCompile(`(?is)\bUPDATE ` + table + `\b[^;]*?\bSET\b[^;]*?\b` + column + `\s*=`).MatchString
			if !named("All", rewrap, selects) || !named("Rewrap", rewrap, updates) {
				t.Errorf("%s.%s: no All… and Rewrap… query that rewrap.go uses", table, column)
			}
			if !named("Convert", convert, updates) {
				t.Errorf("%s.%s: no Convert… query that convert.go uses", table, column)
			}
			for _, inventory := range []string{"LegacyWorkspaceValues", "LegacyValueCounts", "SampleSealedValues"} {
				if !selectsFrom(queries[inventory], column, table) {
					t.Errorf("%s.%s: missing from %s", table, column, inventory)
				}
			}
			if !regexp.MustCompile(`\b` + table + `\b`).MatchString(queries["HoldSealedValueTables"]) {
				t.Errorf("%s: missing from HoldSealedValueTables", table)
			}
		}
	}
}

// selectsFrom reports whether some SELECT in the query names the column and reads the table.
func selectsFrom(body, column, table string) bool {
	parts := fromClause.Split(body, -1)
	names := regexp.MustCompile(`\b` + column + `\b`)
	for i := 1; i < len(parts); i++ {
		if !regexp.MustCompile(`^` + table + `\b`).MatchString(parts[i]) {
			continue
		}
		selects := selectClause.FindAllStringIndex(parts[i-1], -1)
		if len(selects) > 0 && names.MatchString(parts[i-1][selects[len(selects)-1][0]:]) {
			return true
		}
	}
	return false
}

func read(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
