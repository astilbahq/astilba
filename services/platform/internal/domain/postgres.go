package domain

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Postgres17Image and PostgresImage are the current pins of each supported major. A new
// managed database runs one of them.
const Postgres17Image = "postgres:17.12-alpine3.23@sha256:a7a82c6bf77e2a6414ea07c8709f8c51911bfb92cb6c49156d63ac2a8b8effbc"

const PostgresImage = "postgres:18.7-alpine3.23@sha256:e6014245dccbc0f28b07dac8e3f793c94a18420f78690e9db547957514a524f1"

// PostgresRelease is one PostgreSQL image Astilba has pinned for managed databases.
//
// A pin bump adds a release and never removes one. Databases, backups, restore targets and
// upgrade plans name the exact image they were made with, so every release they can name
// stays known: an earlier release of a major is retired, not forgotten. The rules below say
// what a retired release's data may still do.
type PostgresRelease struct {
	Image string
	// Version is the server's server_version_num, such as 180004 for 18.4.
	Version int
	// Line is the base image build the release comes from. Releases of one line share their
	// C library and ICU, so an index sorted by one sorts the same under the other.
	Line string
	// Mount is where the image expects its volume, and DataDirectory is the cluster in it.
	Mount         string
	DataDirectory string
}

// postgresReindex lists, by the PostgreSQL release that fixed them, the indexes release notes say
// to rebuild after updating, because the fix does not repair entries already written. Each query
// returns index OIDs; the agent rebuilds them concurrently in every database. It follows
// PostgreSQL's releases rather than Astilba's pins, so an update that skips a release still
// rebuilds what that release named. Advice for indexes only suspected of damage, such as GIN
// after a VACUUM race, is left to the owner.
var postgresReindex = func() []reindexAdvice {
	advice := []reindexAdvice{
		// 17.11 and 18.6: contrib/btree_gist on float4/float8 that may hold NaN (and, in 18, on
		// bit/varbit, which sorted wrongly), and btree on contrib/ltree values with many labels.
		{Version: 170011, condition: gistOpclasses("gist_float4_ops", "gist_float8_ops") + " OR " + btreeType("ltree")},
		{Version: 180006, condition: gistOpclasses("gist_float4_ops", "gist_float8_ops", "gist_bit_ops", "gist_vbit_ops") + " OR " + btreeType("ltree")},
		// 17.12 and 18.7: btree on unbounded bpchar, which deduplicated equal values that differ
		// in trailing spaces, and contrib/pg_trgm's gist_trgm_ops, which could lose entries'
		// format flag.
		{Version: 170012, condition: unboundedBpchar + " OR " + gistOpclasses("gist_trgm_ops")},
		{Version: 180007, condition: unboundedBpchar + " OR " + gistOpclasses("gist_trgm_ops")},
	}
	for i := range advice {
		advice[i].Query = reindexIndexes(advice[i].condition)
	}
	return advice
}()

type reindexAdvice struct {
	Version   int
	Query     string
	condition string
}

// reindexIndexes selects valid indexes in the database's own schemas that REINDEX INDEX
// CONCURRENTLY can rebuild. An invalid index is left alone: it is either a leftover of an
// interrupted concurrent rebuild, which ReindexLeftovers drops, or the owner's own, and rebuilding
// it would make it valid. Temporary tables belong to their sessions, and an exclusion
// constraint's index cannot be rebuilt concurrently; ExclusionIndexes lists those for the owner.
func reindexIndexes(condition string) string {
	return reindexSelect + " AND NOT EXISTS (SELECT 1 FROM pg_constraint x WHERE x.conindid = c.oid AND x.contype = 'x') AND (" + condition + ")"
}

// inline-sql: runs in a managed customer database through psql, not in the platform database
const reindexSelect = "SELECT DISTINCT c.oid FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid JOIN pg_am am ON am.oid = c.relam WHERE c.relkind = 'i' AND i.indisvalid AND c.relpersistence <> 't' AND c.relnamespace NOT IN ('pg_catalog'::regnamespace, 'pg_toast'::regnamespace, 'information_schema'::regnamespace)"

func gistOpclasses(names ...string) string {
	return "am.amname = 'gist' AND EXISTS (SELECT 1 FROM unnest(i.indclass) AS k(opclass) JOIN pg_opclass o ON o.oid = k.opclass WHERE o.opcname IN ('" + strings.Join(names, "', '") + "'))"
}

// indexColumnOf matches an index column whose type is, through any chain of domains and arrays,
// the named type. An index column keeps its table column's type, and a domain or an array uses
// the base type's comparison, so it shares the base type's defect. Each step keeps the type
// modifier it adds, so CHAR(n) behind a domain is still told apart.
func indexColumnOf(condition string) string {
	return "EXISTS (SELECT 1 FROM pg_attribute a WHERE a.attrelid = c.oid AND EXISTS (WITH RECURSIVE r(oid, typmod) AS (SELECT a.atttypid, a.atttypmod UNION ALL SELECT CASE WHEN t.typbasetype <> 0 THEN t.typbasetype ELSE t.typelem END, CASE WHEN t.typbasetype <> 0 THEN t.typtypmod ELSE -1 END FROM r JOIN pg_type t ON t.oid = r.oid WHERE t.typbasetype <> 0 OR t.typcategory = 'A') SELECT 1 FROM r JOIN pg_type e ON e.oid = r.oid WHERE " + condition + "))"
}

// CHAR(n), which is not affected, has a type modifier on its column or on a domain over it.
var unboundedBpchar = "am.amname = 'btree' AND " + indexColumnOf("e.oid = 'bpchar'::regtype AND NOT EXISTS (SELECT 1 FROM r AS m WHERE m.typmod >= 0)")

// Extension types are matched by name, so the query runs where the extension is not installed.
func btreeType(name string) string {
	return "am.amname = 'btree' AND " + indexColumnOf("e.typname = '"+name+"'")
}

// ReindexLeftovers selects invalid indexes an interrupted REINDEX INDEX CONCURRENTLY left behind,
// which PostgreSQL names <index>_ccnew or <index>_ccold. They are still updated on every write.
// inline-sql: runs in a managed customer database through psql, not in the platform database
const ReindexLeftovers = "SELECT c.oid FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid WHERE NOT i.indisvalid AND c.relpersistence <> 't' AND c.relname ~ '_cc(new|old)[0-9]*$' AND c.relnamespace NOT IN ('pg_catalog'::regnamespace, 'pg_toast'::regnamespace, 'information_schema'::regnamespace)"

// ExclusionIndexes returns, for the advice in queries, the indexes it skips because they back an
// exclusion constraint: rebuilding one blocks writes, so the owner chooses when.
func ExclusionIndexes(query string) string {
	for _, advice := range postgresReindex {
		if advice.Query == query {
			return reindexSelect + " AND EXISTS (SELECT 1 FROM pg_constraint x WHERE x.conindid = c.oid AND x.contype = 'x') AND (" + advice.condition + ")"
		}
	}
	return ""
}

// postgresReleases lists every pinned release, oldest first within each major.
var postgresReleases = []PostgresRelease{
	{Image: "postgres:17.10-alpine3.23@sha256:8189a1f6e40904781fc9e2612687877791d21679866db58b1de996b31fc312e4", Version: 170010, Line: "alpine3.23", Mount: "/var/lib/postgresql/data", DataDirectory: "/var/lib/postgresql/data"},
	{Image: Postgres17Image, Version: 170012, Line: "alpine3.23", Mount: "/var/lib/postgresql/data", DataDirectory: "/var/lib/postgresql/data"},
	{Image: "postgres:18.4-alpine3.23@sha256:996d0920e4ff9df1fc19dacb904492f3c1ec0ec1cc338f0ad7123be7731c5f5e", Version: 180004, Line: "alpine3.23", Mount: "/var/lib/postgresql", DataDirectory: "/var/lib/postgresql/18/docker"},
	{Image: PostgresImage, Version: 180007, Line: "alpine3.23", Mount: "/var/lib/postgresql", DataDirectory: "/var/lib/postgresql/18/docker"},
}

// PostgresReleases returns every known release, oldest first within each major.
func PostgresReleases() []PostgresRelease { return slices.Clone(postgresReleases) }

// PostgresReleaseOf returns the release an exact pinned image reference names.
func PostgresReleaseOf(image string) (PostgresRelease, bool) {
	for _, r := range postgresReleases {
		if r.Image == image {
			return r, true
		}
	}
	return PostgresRelease{}, false
}

// CurrentPostgres returns the release a new database of the major runs.
func CurrentPostgres(major int) (PostgresRelease, bool) {
	var current PostgresRelease
	for _, r := range postgresReleases {
		if r.Major() == major && r.Version > current.Version {
			current = r
		}
	}
	return current, current.Image != ""
}

func (r PostgresRelease) Major() int { return r.Version / 10000 }

// Name is the version people read, such as "18.4".
func (r PostgresRelease) Name() string { return fmt.Sprintf("%d.%d", r.Major(), r.Version%10000) }

// Retired reports whether a newer release of the same major replaces this one. Data made by
// a retired release stays usable, but nothing new starts on it.
func (r PostgresRelease) Retired() bool {
	current, _ := CurrentPostgres(r.Major())
	return current.Version != r.Version
}

// RestoresArchiveFrom says whether a logical archive dumped from the source release can be
// restored by this release's pg_restore into a fresh data directory. pg_restore reads archives
// from its own major's earlier minors, and a fresh restore rebuilds every index, so the line
// does not matter. Another major needs the supported upgrade, not a restore.
func (r PostgresRelease) RestoresArchiveFrom(source PostgresRelease) error {
	if source.Major() != r.Major() {
		return fmt.Errorf("this backup was taken on PostgreSQL %s, and the database now runs PostgreSQL %s. A backup restores only into the major version that made it; restore it into a PostgreSQL %d database instead", source.Name(), r.Name(), source.Major())
	}
	if source.Version > r.Version {
		return fmt.Errorf("this backup was taken on PostgreSQL %s, which is newer than the PostgreSQL %s the database runs. Update the database to PostgreSQL %s or later before restoring it", source.Name(), r.Name(), source.Name())
	}
	return nil
}

// OpensDataFrom says whether this release may serve a data directory the source release wrote,
// either by replacing it in place or by opening a restore target it filled. PostgreSQL keeps one
// on-disk format within a major, and the same line keeps index order. A release never moves
// backwards, so a minor's fixes and checks are not undone.
func (r PostgresRelease) OpensDataFrom(source PostgresRelease) error {
	switch {
	case source.Version == r.Version:
		return nil
	case source.Major() != r.Major():
		return fmt.Errorf("PostgreSQL %s cannot open data PostgreSQL %s wrote. Changing the major version needs the supported upgrade or new storage", r.Name(), source.Name())
	case source.Line != r.Line:
		return fmt.Errorf("PostgreSQL %s (%s) cannot open data PostgreSQL %s (%s) wrote in place, because their sorting libraries differ. Restore a backup into new storage instead", r.Name(), r.Line, source.Name(), source.Line)
	case source.Version > r.Version:
		return fmt.Errorf("PostgreSQL %s cannot replace PostgreSQL %s: a database never moves to an earlier release", r.Name(), source.Name())
	}
	return nil
}

// ReindexSince returns the reindex queries a data directory written by the source release needs
// before this release serves it, oldest first.
func (r PostgresRelease) ReindexSince(source PostgresRelease) []string {
	var out []string
	for _, advice := range postgresReindex {
		if advice.Version/10000 == r.Major() && source.Major() == r.Major() && advice.Version > source.Version && advice.Version <= r.Version {
			out = append(out, advice.Query)
		}
	}
	return out
}

// UpgradesTo says whether the supported logical upgrade moves a database from this release to the
// target. It is the one cross-major path, from any PostgreSQL 17 release to a current PostgreSQL 18.
func (r PostgresRelease) UpgradesTo(target PostgresRelease) error {
	if r.Major() != 17 || target.Major() != 18 {
		return fmt.Errorf("only PostgreSQL 17 upgrades to PostgreSQL 18, not %s to %s", r.Name(), target.Name())
	}
	return nil
}

// FreshPostgresImage is the image a new copy of a database starts on, such as a preview's or a
// cloned environment's: its major's current release. New storage holds no data an earlier release
// wrote, so nothing ties it to a retired one. An image Astilba never pinned is returned as given,
// for validation to refuse.
func FreshPostgresImage(image string) string {
	if r, ok := PostgresReleaseOf(image); ok {
		if current, ok := CurrentPostgres(r.Major()); ok {
			return current.Image
		}
	}
	return image
}

// PostgresMount is where an image keeps its volume. An image Astilba never pinned gets the
// current layout; validation refuses it for a managed database.
func PostgresMount(image string) string {
	if r, ok := PostgresReleaseOf(image); ok {
		return r.Mount
	}
	return "/var/lib/postgresql"
}

// PostgresMajor is the PostgreSQL major version a pinned release runs, such as "18", or "" for an
// image Astilba never pinned. A data directory one major initialised cannot be opened by another,
// so a managed volume keeps the major that first ran on it.
func PostgresMajor(image string) string {
	if r, ok := PostgresReleaseOf(image); ok {
		return strconv.Itoa(r.Major())
	}
	return ""
}

func knownPostgres(image string) bool {
	_, ok := PostgresReleaseOf(image)
	return ok
}
