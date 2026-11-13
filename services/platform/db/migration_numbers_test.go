package dbmigrate

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// numberedMigration is the only name a file in migrations/ may have: five digits, an
// underscore, then lower-case words joined by underscores.
var numberedMigration = regexp.MustCompile(`^(\d{5})_[a-z0-9]+(?:_[a-z0-9]+)*\.sql$`)

// Goose runs migrations in order and refuses a database that has a later one than a migration
// it has not applied, so a pull request that merges a number at or below main's highest breaks
// `migrate up` on every existing database. Fresh test databases do not show it. CI runs this on
// the merged tree of each pull request and merge group, where such a number is a duplicate or a
// gap. See docs/continuous-integration.md.
func TestMigrationNumbersAreConsecutive(t *testing.T) {
	entries, err := os.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			name += "/"
		}
		names = append(names, name)
	}
	problems := migrationNumberProblems(names, Version)
	for _, problem := range problems {
		t.Error(problem)
	}
	if len(problems) > 0 {
		t.Log("Number a new migration main's highest + 1 when its pull request is queued, and set Version in db/migrate.go to it.")
	}
}

// migrationNumberProblems returns what is wrong with the numbering of the migration files
// named, given the Version the binary claims, naming the files involved. It ignores hidden
// files the binary does not embed, such as .DS_Store and editor swap files.
func migrationNumberProblems(names []string, version int64) []string {
	var problems []string
	byNumber := map[int64][]string{}
	for _, name := range names {
		if strings.HasPrefix(name, ".") && !strings.HasSuffix(name, ".sql") {
			continue
		}
		match := numberedMigration.FindStringSubmatch(name)
		if match == nil {
			problems = append(problems, fmt.Sprintf("%s: a migration is named NNNNN_lower_case_words.sql", name))
			continue
		}
		number, _ := strconv.ParseInt(match[1], 10, 64)
		byNumber[number] = append(byNumber[number], name)
	}
	numbers := make([]int64, 0, len(byNumber))
	for number := range byNumber {
		numbers = append(numbers, number)
	}
	slices.Sort(numbers)
	previous := int64(0)
	for _, number := range numbers {
		files := byNumber[number]
		if len(files) > 1 {
			slices.Sort(files)
			problems = append(problems, fmt.Sprintf("%05d is used by %s", number, strings.Join(files, ", ")))
		}
		if number != previous+1 {
			after := "the start"
			if previous > 0 {
				after = byNumber[previous][0]
			}
			problems = append(problems, fmt.Sprintf("%s follows %s, so %s is missing", files[0], after, missing(previous+1, number-1)))
		}
		previous = number
	}
	if previous != version {
		highest := "there is no migration"
		if previous > 0 {
			highest = "the highest migration is " + byNumber[previous][0]
		}
		problems = append(problems, fmt.Sprintf("Version in db/migrate.go is %d, but %s", version, highest))
	}
	return problems
}

// missing describes the numbers from first to last.
func missing(first, last int64) string {
	if first == last {
		return fmt.Sprintf("%05d", first)
	}
	return fmt.Sprintf("%05d to %05d", first, last)
}

func TestMigrationNumberProblems(t *testing.T) {
	for _, test := range []struct {
		name    string
		files   []string
		version int64
		want    []string
	}{
		{"consecutive", []string{"00001_init.sql", "00002_a.sql", "00003_b_c.sql"}, 3, nil},
		{"listed out of order", []string{"00002_a.sql", "00001_init.sql"}, 2, nil},
		{"hidden local files", []string{".DS_Store", "00001_init.sql", ".00001_init.sql.swp"}, 1, nil},
		{
			"gap", []string{"00001_init.sql", "00002_a.sql", "00004_c.sql"}, 4,
			[]string{"00004_c.sql follows 00002_a.sql, so 00003 is missing"},
		},
		{
			"wide gap", []string{"00001_init.sql", "00005_e.sql"}, 5,
			[]string{"00005_e.sql follows 00001_init.sql, so 00002 to 00004 is missing"},
		},
		{
			"not starting at one", []string{"00002_a.sql"}, 2,
			[]string{"00002_a.sql follows the start, so 00001 is missing"},
		},
		{
			"duplicate", []string{"00001_init.sql", "00002_b.sql", "00002_a.sql"}, 2,
			[]string{"00002 is used by 00002_a.sql, 00002_b.sql"},
		},
		{
			"version behind", []string{"00001_init.sql", "00002_a.sql"}, 1,
			[]string{"Version in db/migrate.go is 1, but the highest migration is 00002_a.sql"},
		},
		{
			"version ahead", []string{"00001_init.sql", "00002_a.sql"}, 3,
			[]string{"Version in db/migrate.go is 3, but the highest migration is 00002_a.sql"},
		},
		{
			"empty", nil, 1,
			[]string{"Version in db/migrate.go is 1, but there is no migration"},
		},
		{
			"bad names", []string{"00001_init.sql", "2_a.sql", "00002_A.sql", "00002_a.sql.orig", "00002-a.sql", "000002_a.sql", "00002_.sql", "README.md", "old/", ".hidden.sql"}, 1,
			[]string{
				"2_a.sql: a migration is named NNNNN_lower_case_words.sql",
				"00002_A.sql: a migration is named NNNNN_lower_case_words.sql",
				"00002_a.sql.orig: a migration is named NNNNN_lower_case_words.sql",
				"00002-a.sql: a migration is named NNNNN_lower_case_words.sql",
				"000002_a.sql: a migration is named NNNNN_lower_case_words.sql",
				"00002_.sql: a migration is named NNNNN_lower_case_words.sql",
				"README.md: a migration is named NNNNN_lower_case_words.sql",
				"old/: a migration is named NNNNN_lower_case_words.sql",
				".hidden.sql: a migration is named NNNNN_lower_case_words.sql",
			},
		},
		{
			// Two pull requests took 00031 from the same main, as #212 and #216 did. Both set
			// Version to 31, so Git merges that line without a conflict.
			"same number from two branches", append(numbered(30), "00031_b.sql", "00031_c.sql"), 31,
			[]string{"00031 is used by 00031_b.sql, 00031_c.sql"},
		},
		{
			// A pull request that reserved 00040 lands before the one holding 00039.
			"reserved number lands first", append(numbered(38), "00040_reserved.sql"), 40,
			[]string{"00040_reserved.sql follows 00038_m38.sql, so 00039 is missing"},
		},
	} {
		got := migrationNumberProblems(test.files, test.version)
		if !slices.Equal(got, test.want) {
			t.Errorf("%s:\n got %q\nwant %q", test.name, got, test.want)
		}
	}
}

// numbered returns the names of migrations 00001 to last.
func numbered(last int) []string {
	var names []string
	for n := 1; n <= last; n++ {
		names = append(names, fmt.Sprintf("%05d_m%d.sql", n, n))
	}
	return names
}
