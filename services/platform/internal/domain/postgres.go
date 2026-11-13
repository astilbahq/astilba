package domain

import (
	"fmt"
	"slices"
)

// Postgres17Image and PostgresImage are the current pins of each supported major. A new
// managed database runs one of them.
const Postgres17Image = "postgres:17.10-alpine3.23@sha256:8189a1f6e40904781fc9e2612687877791d21679866db58b1de996b31fc312e4"

const PostgresImage = "postgres:18.4-alpine3.23@sha256:996d0920e4ff9df1fc19dacb904492f3c1ec0ec1cc338f0ad7123be7731c5f5e"

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
	// Reindex, when set, is a query run in each database after a data directory moves to
	// this release in place. It returns the indexes the release notes say to rebuild,
	// because the fix that comes with the release does not repair entries already written.
	Reindex string
}

// postgresReleases lists every pinned release, oldest first within each major.
var postgresReleases = []PostgresRelease{
	{Image: Postgres17Image, Version: 170010, Line: "alpine3.23", Mount: "/var/lib/postgresql/data", DataDirectory: "/var/lib/postgresql/data"},
	{Image: PostgresImage, Version: 180004, Line: "alpine3.23", Mount: "/var/lib/postgresql", DataDirectory: "/var/lib/postgresql/18/docker"},
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
	for _, between := range postgresReleases {
		if between.Major() == r.Major() && between.Version > source.Version && between.Version <= r.Version && between.Reindex != "" {
			out = append(out, between.Reindex)
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

// PostgresMount is where an image keeps its volume. An image Astilba never pinned gets the
// current layout; validation refuses it for a managed database.
func PostgresMount(image string) string {
	if r, ok := PostgresReleaseOf(image); ok {
		return r.Mount
	}
	return "/var/lib/postgresql"
}

func knownPostgres(image string) bool {
	_, ok := PostgresReleaseOf(image)
	return ok
}
