package agent

import (
	"cmp"
	"context"
	"errors"
	"os"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

// postgresData records, for a managed PostgreSQL volume, the release whose indexes its data is
// known to hold. A release whose notes say to rebuild some indexes after updating fixes only new
// entries, so the record stays at the earlier release until those indexes are rebuilt.
type postgresData struct {
	Image string `json:"image"`
	// Served is the later release that served the data while a rebuild of Image's indexes is
	// outstanding: set just before a database first starts on a later release, when a unit first
	// saves a plan with storage that has no record, or before a rebuild starts, and dropped once
	// it finishes. Image may then be one assumed of storage with
	// no record, the oldest release of its major, which says nothing of what wrote the data; Served
	// is what the data must still open with.
	Served string `json:"served,omitempty"`
	// Initialising is set before the agent creates a database's new volume, and cleared once its
	// cluster is ready: a volume found while it is set never served the database, so the agent
	// deletes and creates it again.
	Initialising bool `json:"initialising,omitempty"`
}

const postgresDataBucket = "postgres-data"

func (j *Journal) postgresData(volume uuid.UUID) (postgresData, bool, error) {
	var v postgresData
	err := j.db.View(func(tx *bolt.Tx) error { return j.get(tx, postgresDataBucket, volume.String(), &v) })
	if errors.Is(err, os.ErrNotExist) {
		return v, false, nil
	}
	return v, err == nil, err
}

func (j *Journal) savePostgresData(volume uuid.UUID, v postgresData) error {
	return j.db.Update(func(tx *bolt.Tx) error { return j.put(tx, postgresDataBucket, volume.String(), v) })
}

// forgetPostgresData drops a deleted volume's records, so storage later made at the same identity
// starts its own.
func (j *Journal) forgetPostgresData(volume uuid.UUID) error {
	return j.db.Update(func(tx *bolt.Tx) error {
		return errors.Join(tx.Bucket([]byte(postgresDataBucket)).Delete([]byte(volume.String())), tx.Bucket([]byte(postgresCredentialBucket)).Delete([]byte(volume.String())))
	})
}

// postgresReleaseOf resolves an image to its pinned release for postgresDataOpens. Tests replace it
// to add a release of another line, which no pin comes from yet.
var postgresReleaseOf = domain.PostgresReleaseOf

// postgresDataOpens refuses a plan whose managed database on the volume runs a release that cannot
// open the data the journal records there: another major's, another line's or a later release's.
// It compares with the serving release a record names while a rebuild is outstanding, and
// otherwise with the release it holds, the last one the agent recorded serving the data.
// The record follows the volume, not the unit's applied plan, so it also holds for a restore
// target, an upgrade's target and storage a retired environment kept, which the comparison with
// the applied plan never sees. A volume with no record, such as one an earlier agent made, is left
// to that comparison and the volume's major label.
func (j *Journal) postgresDataOpens(p deployment.Plan, volume uuid.UUID) error {
	for _, a := range p.LocalApplications() {
		if a.Postgres == nil || a.Source.Image == nil || len(a.Mounts) != 1 || a.Mounts[0].VolumeID != volume {
			continue
		}
		record, found, err := j.postgresData(volume)
		if err != nil || !found {
			return err
		}
		return postgresDataRefuses(a, record)
	}
	return nil
}

// postgresDataRefuses says why a's release cannot open the data record describes, if it cannot.
func postgresDataRefuses(a domain.Application, record postgresData) error {
	last := cmp.Or(record.Served, record.Image)
	if last == *a.Source.Image {
		return nil
	}
	written, known := postgresReleaseOf(last)
	serving, ok := postgresReleaseOf(*a.Source.Image)
	if !known || !ok {
		return errPostgresVersionChange
	}
	if err := serving.OpensDataFrom(written); err != nil {
		return postgresDataRefusal{application: a.Name, reason: err}
	}
	return nil
}

// postgresDataRefusal is a PostgreSQL version change refused for the data on a volume before any
// service starts, with the reason the owner reads.
type postgresDataRefusal struct {
	application string
	reason      error
}

func (r postgresDataRefusal) Error() string {
	return r.application + " cannot start on its volume: " + r.reason.Error() + "."
}

func (r postgresDataRefusal) Is(target error) bool { return target == errPostgresVersionChange }

// assumePostgresData records each managed database volume of the plan a unit saves, then of the
// plan it leaves, that has no record yet. Such storage is one an earlier agent made or filled,
// whose indexes no release this agent saw rebuilt, so it is assumed to hold the oldest release of
// its major's, served by the plan's own release: the rebuild stays due from the oldest whatever
// plans the unit saves first, and the data still refuses what cannot open what that release
// wrote. Runs inside saveUnit, after the plan it saves has served the data.
func (j *Journal) assumePostgresData(tx *bolt.Tx, plans ...deployment.Plan) error {
	for _, p := range plans {
		for _, a := range p.LocalApplications() {
			if a.Postgres == nil || a.Source.Image == nil || len(a.Mounts) != 1 {
				continue
			}
			serving, ok := domain.PostgresReleaseOf(*a.Source.Image)
			if !ok {
				continue
			}
			var existing postgresData
			err := j.get(tx, postgresDataBucket, a.Mounts[0].VolumeID.String(), &existing)
			if err == nil {
				continue
			}
			if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err = j.put(tx, postgresDataBucket, a.Mounts[0].VolumeID.String(), assumedPostgresData(serving)); err != nil {
				return err
			}
		}
	}
	return nil
}

// assumedPostgresData is the record of storage with no record that serving has served: the oldest
// release of its major holds its indexes.
func assumedPostgresData(serving domain.PostgresRelease) postgresData {
	assumed := postgresData{Image: oldestPostgres(serving.Major()).Image}
	if assumed.Image != serving.Image {
		assumed.Served = serving.Image
	}
	return assumed
}

// recordNewPostgresStorage records a data volume the agent is creating for a managed database,
// initialising until its cluster is ready: the release that initialises it holds every index it
// builds.
func (j *Journal) recordNewPostgresStorage(p deployment.Plan, volume uuid.UUID, initialising bool) error {
	for _, a := range p.LocalApplications() {
		if a.Postgres != nil && a.Source.Image != nil && len(a.Mounts) == 1 && a.Mounts[0].VolumeID == volume {
			if _, ok := domain.PostgresReleaseOf(*a.Source.Image); ok {
				return j.savePostgresData(volume, postgresData{Image: *a.Source.Image, Initialising: initialising})
			}
		}
	}
	return nil
}

// reindexPostgres rebuilds, in each running managed database of the plan, the indexes the
// releases between the one whose indexes its data holds and the one serving it say to rebuild.
// Each index is rebuilt concurrently, so the database keeps serving. The update itself has
// already happened and is never undone: a rebuild that does not finish leaves the record at the
// earlier release, so the unit's next deployment, or this one resumed after a restart, tries
// again, and the returned note says so. begin runs once before the first rebuild, to record the
// step. A journal failure is an error, and so is the agent stopping, which leaves the command to
// resume when it restarts.
func (e *Engine) reindexPostgres(ctx context.Context, p deployment.Plan, begin func() error) (string, error) {
	type rebuild struct {
		application domain.Application
		serving     domain.PostgresRelease
		queries     []string
	}
	var due []rebuild
	for _, a := range p.LocalApplications() {
		if a.Postgres == nil || a.Source.Image == nil || len(a.Mounts) != 1 {
			continue
		}
		serving, ok := domain.PostgresReleaseOf(*a.Source.Image)
		if !ok {
			continue
		}
		volume := a.Mounts[0].VolumeID
		record, _, err := e.Journal.postgresData(volume)
		if err != nil {
			return "", err
		}
		written, known := domain.PostgresReleaseOf(record.Image)
		if !known {
			// A record that names no pinned release, or none at all, says nothing of the indexes:
			// assume the oldest release of the major holds them, as assumePostgresData does.
			written = oldestPostgres(serving.Major())
		}
		var queries []string
		if written.Major() == serving.Major() && written.Version < serving.Version {
			queries = serving.ReindexSince(written)
		}
		next := postgresData{Image: serving.Image}
		if len(queries) > 0 {
			// Every outstanding rebuild is recorded before the first starts, so if the agent
			// stops during one, the data of each still refuses releases that cannot open what the
			// serving one writes.
			next = postgresData{Image: cmp.Or(record.Image, written.Image), Served: serving.Image}
			due = append(due, rebuild{application: a, serving: serving, queries: queries})
		}
		if record != next {
			if err = e.Journal.savePostgresData(volume, next); err != nil {
				return "", err
			}
		}
	}
	var unfinished, exclusions []string
	var closed, invalid []listed
	for i, r := range due {
		if i == 0 && begin != nil {
			if err := begin(); err != nil {
				return "", err
			}
		}
		a := r.application
		left, err := e.rebuildPostgresIndexes(ctx, p, a, r.serving, r.queries)
		if ctx.Err() != nil {
			// The agent is stopping: the command resumes when it restarts.
			return "", ctx.Err()
		}
		if err != nil {
			unfinished = append(unfinished, a.Name)
			continue
		}
		exclusions = append(exclusions, left.exclusions...)
		if len(left.closed) > 0 || len(left.invalid) > 0 {
			// The record stays at the earlier release, so the next deployment rebuilds them once
			// their owner allows connections again or drops the invalid ones.
			for _, database := range left.closed {
				closed = append(closed, listed{database, a.Name})
			}
			for _, database := range left.invalid {
				invalid = append(invalid, listed{database, a.Name})
			}
			continue
		}
		if err = e.Journal.savePostgresData(a.Mounts[0].VolumeID, postgresData{Image: r.serving.Image}); err != nil {
			return "", err
		}
	}
	return reindexNote(unfinished, closed, invalid, exclusions), nil
}

// The bytes each of reindexNote's lists takes at most. With the note's own words they fit the
// room an operation's note has after the longest note a report puts before it.
const (
	unfinishedRoom = 100
	closedRoom     = 70
	invalidRoom    = 70
	exclusionsRoom = 170
)

// reindexNote tells the owner what the rebuild left. A maintenance report puts its own note after it.
func reindexNote(unfinished []string, closed, invalid []listed, exclusions []string) string {
	var parts []string
	if len(unfinished) > 0 {
		parts = append(parts, "Rebuilding the indexes the PostgreSQL release notes name did not finish for "+listSome(unqualified(unfinished), unfinishedRoom)+"; the data is served on the new release and the next deployment retries it.")
	}
	if len(closed) > 0 {
		parts = append(parts, "The databases "+listSome(closed, closedRoom)+" do not accept connections, so their indexes were not rebuilt; the next deployment after they do rebuilds them.")
	}
	if len(invalid) > 0 {
		parts = append(parts, "The databases "+listSome(invalid, invalidRoom)+" are invalid after an interrupted DROP DATABASE; the deployment after they are dropped rebuilds them.")
	}
	if len(exclusions) > 0 {
		parts = append(parts, "Rebuild these exclusion-constraint indexes with REINDEX INDEX when writes can pause, as the release notes advise: "+listSome(unqualified(exclusions), exclusionsRoom)+".")
	}
	return strings.Join(parts, " ")
}

// listed is a name in an operation's note, with the application it belongs to when that says
// which deployment's it is.
type listed struct{ name, application string }

// listSome joins names within limit bytes, its count of the rest included. A name longer than
// 120 bytes, or than the room the count leaves, is cut at a whole character, its own part before
// its application's, so the note still says whose it is. An application keeps whatever room its
// database's whole name leaves, and at least half.
func listSome(names []listed, limit int) string {
	names = slices.Clone(names)
	slices.SortFunc(names, func(a, b listed) int {
		return cmp.Or(cmp.Compare(a.name, b.name), cmp.Compare(a.application, b.application))
	})
	names = slices.Compact(names)
	rest := func(n int) string {
		if n == 0 {
			return ""
		}
		return " and " + strconv.Itoa(n) + " more"
	}
	// The first name always fits beside the count of the names after it.
	width := min(limit-len(rest(max(len(names)-1, 0))), 120)
	out := ""
	for i, n := range names {
		name := cutName(n.name, width)
		if n.application != "" {
			qualifier := " (" + cutName(n.application, max(width-len(n.name), width/2)-len(" ()")) + ")"
			name = cutName(n.name, width-len(qualifier)) + qualifier
		}
		next := name
		if out != "" {
			next = out + ", " + name
		}
		if out != "" && len(next)+len(rest(len(names)-i-1)) > limit {
			return out + rest(len(names)-i)
		}
		out = next
	}
	return out
}

// cutName shortens name to whole characters within width bytes, the mark of the cut included.
func cutName(name string, width int) string {
	if len(name) <= width {
		return name
	}
	end := max(width-len("…"), 0)
	for end > 0 && !utf8.RuneStart(name[end]) {
		end--
	}
	return name[:end] + "…"
}

// unqualified lists names that belong to no application in particular.
func unqualified(v []string) []listed {
	out := make([]listed, len(v))
	for i, name := range v {
		out[i] = listed{name: name}
	}
	return out
}

func oldestPostgres(major int) domain.PostgresRelease {
	for _, r := range domain.PostgresReleases() {
		if r.Major() == major {
			return r
		}
	}
	return domain.PostgresRelease{}
}
