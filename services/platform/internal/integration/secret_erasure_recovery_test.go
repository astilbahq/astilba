package integration

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// replicaExec runs statements with triggers and foreign keys off, as restoring a backup writes
// rows, and as a fixture sets what the app doesn't change, such as when a row was created.
func replicaExec(t *testing.T, f *fixture, statements ...restoreStatement) {
	t.Helper()
	ctx := context.Background()
	conn, err := f.st.Pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	defer conn.Exec(ctx, `SET session_replication_role=DEFAULT`)
	if _, err = conn.Exec(ctx, `SET session_replication_role=replica`); err != nil {
		t.Fatal(err)
	}
	for _, s := range statements {
		if _, err = conn.Exec(ctx, s.sql, s.args...); err != nil {
			t.Fatal(s.sql, err)
		}
	}
}

type restoreStatement struct {
	sql  string
	args []any
}

// backupRows is the rows of table matching where, as a backup holds them, to be written back with
// restoreRows.
type backupRows struct {
	table string
	ids   []uuid.UUID
	rows  [][]byte
}

func takeBackup(t *testing.T, f *fixture, table, where string, args ...any) backupRows {
	t.Helper()
	// inline-sql: the table and condition are the test's own constants
	rows, err := f.st.Pool.Query(context.Background(), "SELECT id,to_jsonb(t) FROM "+table+" t WHERE "+where, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := backupRows{table: table}
	for rows.Next() {
		var id uuid.UUID
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			t.Fatal(err)
		}
		out.ids, out.rows = append(out.ids, id), append(out.rows, raw)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(out.ids) == 0 {
		t.Fatal("the backup of", table, "holds no rows")
	}
	return out
}

// restoreRows puts each backed-up row back as it was, as restoring the backup does.
func restoreRows(t *testing.T, f *fixture, backups ...backupRows) {
	t.Helper()
	statements := []restoreStatement{}
	for _, b := range backups {
		statements = append(statements, restoreStatement{"DELETE FROM " + b.table + " WHERE id=ANY($1)", []any{b.ids}})
		for _, raw := range b.rows {
			statements = append(statements, restoreStatement{"INSERT INTO " + b.table + " SELECT * FROM jsonb_populate_record(NULL::" + b.table + ",$1)", []any{raw}})
		}
	}
	replicaExec(t, f, statements...)
}

// inRecovery merges base over the database, and deletes the copies of each preview no open request
// names as RecoverCrossHost does next, in a transaction it rolls back after check reads the result,
// so one restored database can be recovered with several bases.
func inRecovery(t *testing.T, f *fixture, ws uuid.UUID, base portability.Archive, check func(tx pgx.Tx)) {
	t.Helper()
	ctx := context.Background()
	tx, err := f.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err = portability.MergeRecovery(ctx, tx, recoverKeys(t, f.st, ws), base, f.owner); err != nil {
		t.Fatal(err)
	}
	if err = db.New(tx).DeleteEndedPreviewSecretVersions(ctx, ws); err != nil {
		t.Fatal(err)
	}
	check(tx)
}

func txCount(t *testing.T, tx pgx.Tx, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := tx.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// olderFormat is base as a release before recovery base version 23 sealed it: without erasure
// ranges, and with every erased version of its secrets as a row, as the database holds them now.
// It is decoded as recovery decodes it.
func olderFormat(t *testing.T, f *fixture, ws uuid.UUID, base portability.Archive) portability.Archive {
	t.Helper()
	older := base
	section := *base.Recovery
	section.Erasures = nil
	older.Version, older.Recovery, older.Tables = 22, &section, maps.Clone(base.Tables)
	columns := []string{}
	for column := range base.Tables["secret_versions"][0] {
		columns = append(columns, column)
	}
	carried := map[string]bool{}
	for _, row := range base.Tables["secret_versions"] {
		carried[string(row["id"])] = true
	}
	older.Tables["secret_versions"] = slices.Clone(base.Tables["secret_versions"])
	// inline-sql: the columns are those of the base's own rows
	rows, err := f.st.Pool.Query(context.Background(), "SELECT to_jsonb(q) FROM (SELECT v."+strings.Join(columns, ",v.")+` FROM secret_versions v
		JOIN secrets s ON s.workspace_id=v.workspace_id AND s.id=v.secret_id WHERE v.workspace_id=$1 AND v.erased_at IS NOT NULL AND s.id=ANY($2)) q`, ws, carriedSecrets(t, base))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var row portability.Row
		if err := json.Unmarshal(raw, &row); err != nil {
			t.Fatal(err)
		}
		if !carried[string(row["id"])] {
			older.Tables["secret_versions"] = append(older.Tables["secret_versions"], row)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	raw, err := stablejson.Marshal(older)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := portability.DecodeRecovery(raw)
	if err != nil {
		t.Fatal("a version 22 base was refused:", err)
	}
	return decoded
}

func carriedSecrets(t *testing.T, base portability.Archive) []uuid.UUID {
	t.Helper()
	out := []uuid.UUID{}
	for _, row := range base.Tables["secrets"] {
		var id uuid.UUID
		if err := json.Unmarshal(row["id"], &id); err != nil {
			t.Fatal(err)
		}
		out = append(out, id)
	}
	return out
}

func carriesEnvironment(t *testing.T, base portability.Archive, env uuid.UUID) bool {
	t.Helper()
	for _, row := range base.Tables["environments"] {
		var id uuid.UUID
		if err := json.Unmarshal(row["id"], &id); err != nil {
			t.Fatal(err)
		}
		if id == env {
			return true
		}
	}
	return false
}

// A recovery base leaves out a preview that ended before its window, and a preview's end deletes
// its copies of its template's secret values, whether by its cleanup or by revoking its preview
// host (DeleteEnvironmentSecretVersions), or when its request is given a new environment. So no
// base has a row of those values. Recovery from a backup taken before any of that, by a base of
// either format, still leaves none of them with a value. It keeps the values of every preview still
// open: one opened after the base's selection cut, one opened before it, and one the restored
// database opened after the base was captured, which the base doesn't carry.
func TestRecoveryFromAnOlderBackupKeepsEndedPreviewSecretsErased(t *testing.T) {
	f := setup(t)
	b, ws, _ := secretEnvironment(t, f)
	server, _, _ := enrollment(t, f, b, ws, "deployment")
	previews := seedPreviews(t, f, b, ws, server.Server.Id)
	moved, movedEnv, _ := openPreview(t, f, b, ws, previews, 9)
	_, openEnv, open := openPreview(t, f, b, ws, previews, 10)
	_, acrossEnv, across := openPreview(t, f, b, ws, previews, 11)
	// The preview that ended long before the window, the one whose secret rows its cleanup or its
	// host's revocation deleted inside the window, and the one whose request moved on.
	old, deleted := previews.envs[0], previews.envs[1]
	ended := []uuid.UUID{old, deleted, movedEnv}

	backup := []backupRows{
		takeBackup(t, f, "secrets", "environment_id=ANY($1)", ended),
		takeBackup(t, f, "secret_versions", "secret_id IN (SELECT id FROM secrets WHERE environment_id=ANY($1))", ended),
		takeBackup(t, f, "preview_requests", "environment_id=ANY($1)", ended),
	}
	replicaExec(t, f,
		restoreStatement{`DELETE FROM secret_versions v USING secrets s WHERE s.id=v.secret_id AND s.environment_id=ANY($1)`, []any{ended}},
		restoreStatement{`DELETE FROM secrets WHERE environment_id=ANY($1)`, []any{ended}},
		restoreStatement{`UPDATE preview_requests SET state='deleted',updated_at=now()-interval '200 days' WHERE id=$1`, []any{previews.requests[0]}},
		restoreStatement{`UPDATE environments SET created_at=now()-interval '200 days' WHERE id=ANY($1)`, []any{[]uuid.UUID{old, acrossEnv}}},
		restoreStatement{`UPDATE preview_requests SET state='deleted',released_by_revocation=true,updated_at=now() WHERE id=$1`, []any{previews.requests[1]}},
	)
	// The moved request's next generation gets an environment of its own.
	var next api.Environment
	b.call("POST", previews.environments, map[string]string{"name": "Preview 9 again"}, 201, &next)
	replicaExec(t, f,
		restoreStatement{`UPDATE environments SET purpose='preview' WHERE id=$1`, []any{next.Id}},
		restoreStatement{`UPDATE preview_requests SET generation=2,environment_id=$2,updated_at=now() WHERE id=$1`, []any{moved, next.Id}},
	)

	base := capturedBase(t, f, ws)
	if carriesEnvironment(t, base, old) || !carriesEnvironment(t, base, deleted) || !carriesEnvironment(t, base, movedEnv) || !carriesEnvironment(t, base, acrossEnv) {
		t.Fatal("the base doesn't carry the previews the fixture expects")
	}
	if n := claimCounter(t, f, `SELECT count(*) FROM environments WHERE id=ANY($1) AND created_at<$2`, []uuid.UUID{old, acrossEnv}, base.Recovery.WindowStart.Add(-8*24*time.Hour)); n != 2 {
		t.Fatal("a preview the fixture backdated was created after the base's selection cut")
	}
	restoreRows(t, f, backup...)
	// The restored database opened a preview after the base was captured.
	_, newerEnv, newer := openPreview(t, f, b, ws, previews, 12)
	live := map[uuid.UUID]uuid.UUID{openEnv: open.Id, acrossEnv: across.Id, newerEnv: newer.Id}
	if n := claimCounter(t, f, `SELECT count(*) FROM secret_versions v JOIN secrets s ON s.id=v.secret_id WHERE s.environment_id=ANY($1) AND v.encrypted_value IS NOT NULL`, ended); n != 3 {
		t.Fatal("the restored backup holds", n, "of the ended previews' values")
	}

	for format, recovered := range map[string]portability.Archive{"new": base, "version 22": olderFormat(t, f, ws, base)} {
		inRecovery(t, f, ws, recovered, func(tx pgx.Tx) {
			for _, env := range ended {
				if n := txCount(t, tx, `SELECT count(*) FROM secret_versions v JOIN secrets s ON s.id=v.secret_id WHERE s.environment_id=$1 AND (v.encrypted_value IS NOT NULL OR v.erased_at IS NULL)`, env); n != 0 {
					t.Errorf("recovery with a %s base brought back %d of an ended preview's values", format, n)
				}
			}
			for env, version := range live {
				if n := txCount(t, tx, `SELECT count(*) FROM secret_versions v JOIN secrets s ON s.id=v.secret_id WHERE s.environment_id=$1 AND v.id=$2 AND v.encrypted_value IS NOT NULL`, env, version); n != 1 {
					t.Errorf("recovery with a %s base erased an open preview's value", format)
				}
			}
		})
	}
}

// Retiring a secret, purging an environment or retiring versions one by one used to add every
// erased version to every later recovery base as a row. A base now carries of each secret only
// the versions an export carries, and lists the other erased versions of live secrets as ranges
// in their secret's order, so more erasures add nothing to it. Recovery from a backup taken before
// them all still erases every value, with the new format's ranges or a version 22 base's rows, and
// keeps every live value, including those beside an erased one and one made after the capture.
func TestRecoveryBasesDeriveErasuresRatherThanCarryThem(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	b, ws, path := secretEnvironment(t, f)
	makeVersions := func(path, name string, n int) (uuid.UUID, []uuid.UUID) {
		t.Helper()
		ids := make([]uuid.UUID, n)
		var first api.SecretVersion
		b.call("POST", path+"/secrets", map[string]string{"name": name, "value": name + "-canary-0"}, 201, &first)
		ids[0] = first.Id
		for i := 1; i < n; i++ {
			var v api.SecretVersion
			b.call("POST", path+"/secrets/"+first.SecretId.String()+"/versions", map[string]string{"value": fmt.Sprintf("%s-canary-%d", name, i)}, 201, &v)
			ids[i] = v.Id
		}
		return first.SecretId, ids
	}
	// Versions made 100 days ago, a minute apart, in the order they were made.
	backdate := func(ids []uuid.UUID) {
		t.Helper()
		statements := []restoreStatement{}
		for i, id := range ids {
			statements = append(statements, restoreStatement{`UPDATE secret_versions SET created_at=now()-interval '100 days'+$2*interval '1 minute' WHERE id=$1`, []any{id, i}})
		}
		replicaExec(t, f, statements...)
	}
	erase := func(ids ...uuid.UUID) {
		t.Helper()
		if _, err := f.st.Pool.Exec(ctx, `UPDATE secret_versions SET encrypted_value=NULL,plaintext_bytes=NULL,json_bytes=NULL,erased_at=now() WHERE id=ANY($1)`, ids); err != nil {
			t.Fatal(err)
		}
	}
	// TOKEN rotates often and its old versions are retired; RECENT has versions made this week;
	// RETIRED is retired whole, and PURGED is in an environment that is purged.
	token, tokens := makeVersions(path, "TOKEN", 30)
	backdate(tokens)
	recent, recents := makeVersions(path, "RECENT", 12)
	retired, retireds := makeVersions(path, "RETIRED", 15)
	backdate(retireds)
	var project api.Project
	b.call("POST", "/api/v1/workspaces/"+ws.String()+"/projects", map[string]string{"name": "Purged"}, 201, &project)
	var env api.Environment
	purgedPath := "/api/v1/workspaces/" + ws.String() + "/projects/" + project.Id.String() + "/environments"
	b.call("POST", purgedPath, map[string]string{"name": "Purged"}, 201, &env)
	purgedPath += "/" + env.Id.String()
	_, purgeds := makeVersions(purgedPath, "PURGED", 15)
	backdate(purgeds)
	all := slices.Concat(tokens, recents, retireds, purgeds)

	backup := takeBackup(t, f, "secret_versions", "id=ANY($1)", all)
	restoredSecrets := takeBackup(t, f, "secrets", "id=$1", retired)
	restoredEnvironment := takeBackup(t, f, "environments", "id=$1", env.Id)

	// Versions 0 to 14 and 17 of TOKEN are retired, one of them through the app; RECENT's first.
	b.call("DELETE", path+"/secrets/"+token.String()+"/versions/"+tokens[17].String(), nil, 204, nil)
	erase(append(slices.Clone(tokens[:15]), recents[0])...)
	b.call("DELETE", path+"/secrets/"+retired.String(), nil, 204, nil)
	b.call("POST", purgedPath+"/retire", api.EnvironmentLifecycleRequest{ConfirmName: "Purged"}, 201, nil)
	b.call("POST", purgedPath+"/purge", api.EnvironmentPurgeRequest{ConfirmName: "Purged", VolumeIds: []uuid.UUID{}}, 201, nil)

	position := func(id uuid.UUID) portability.VersionPosition {
		t.Helper()
		p := portability.VersionPosition{ID: id}
		if err := f.st.Pool.QueryRow(ctx, `SELECT created_at FROM secret_versions WHERE id=$1`, id).Scan(&p.CreatedAt); err != nil {
			t.Fatal(err)
		}
		p.CreatedAt = p.CreatedAt.UTC()
		return p
	}
	ranges := func(want ...portability.SecretErasure) []portability.SecretErasure {
		slices.SortFunc(want, func(a, b portability.SecretErasure) int {
			if c := bytes.Compare(a.Secret[:], b.Secret[:]); c != 0 {
				return c
			}
			if c := a.First.CreatedAt.Compare(b.First.CreatedAt); c != 0 {
				return c
			}
			return bytes.Compare(a.First.ID[:], b.First.ID[:])
		})
		return want
	}
	// A base carries each secret's newest 10 versions and its current one, erased or not, and no
	// older erased version.
	newest := map[uuid.UUID]bool{}
	for _, ids := range [][]uuid.UUID{tokens, recents, retireds, purgeds} {
		for _, id := range ids[len(ids)-portability.SecretVersionsKept:] {
			newest[id] = true
		}
	}
	checkBase := func(base portability.Archive, want []portability.SecretErasure) {
		t.Helper()
		if base.Version != portability.RecoveryArchiveVersion {
			t.Fatal("the base is version", base.Version)
		}
		for _, id := range archivedVersions(t, base) {
			if !newest[id] {
				t.Error("the base carries an older version as a row:", id)
			}
		}
		if got := base.Recovery.Erasures; !slices.Equal(got, want) {
			t.Errorf("the base's erasures are %+v, want %+v", got, want)
		}
	}
	// TOKEN's old erased versions are two ranges. RECENT's first version was made within a week of
	// the capture, so a version committed after it could still come before it: it stands alone.
	first := capturedBase(t, f, ws)
	checkBase(first, ranges(
		portability.SecretErasure{Secret: token, First: position(tokens[0]), Last: position(tokens[14])},
		portability.SecretErasure{Secret: token, First: position(tokens[17]), Last: position(tokens[17])},
		portability.SecretErasure{Secret: recent, First: position(recents[0]), Last: position(recents[0])},
	))
	firstSize, err := stablejson.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	olderFirst := olderFormat(t, f, ws, first)

	// Retiring four more old versions joins TOKEN's ranges into one, and adds no row.
	erase(tokens[15], tokens[16], tokens[18], tokens[19])
	base := capturedBase(t, f, ws)
	checkBase(base, ranges(
		portability.SecretErasure{Secret: token, First: position(tokens[0]), Last: position(tokens[19])},
		portability.SecretErasure{Secret: recent, First: position(recents[0]), Last: position(recents[0])},
	))
	size, err := stablejson.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(archivedVersions(t, base)) != len(archivedVersions(t, first)) || len(size) > len(firstSize) {
		t.Fatalf("more erasures grew the base from %d to %d bytes", len(firstSize), len(size))
	}
	older := olderFormat(t, f, ws, base)
	// A version 22 base carried all 51 erased versions as rows.
	if got, want := len(older.Tables["secret_versions"])-len(base.Tables["secret_versions"]), 20+1+15+15-erasedCarried(t, base); got != want {
		t.Fatalf("the version 22 base carries %d more rows, want %d", got, want)
	}

	// The restored backup is from before every erasure.
	restoreRows(t, f, backup, restoredSecrets, restoredEnvironment)
	if n := claimCounter(t, f, `SELECT count(*) FROM secret_versions WHERE id=ANY($1) AND encrypted_value IS NULL`, all); n != 0 {
		t.Fatal("the restored backup holds erased versions:", n)
	}
	// It also holds a version of TOKEN made after both bases were captured.
	var later api.SecretVersion
	b.call("POST", path+"/secrets/"+token.String()+"/versions", map[string]string{"value": "TOKEN-canary-later"}, 201, &later)
	// Each base erases what was erased when it was captured, and keeps every live value: those
	// between and beside its ranges, and the one made after it.
	firstErased := slices.Concat(tokens[:15], tokens[17:18], recents[:1], retireds, purgeds)
	firstLive := slices.Concat(tokens[15:17], tokens[18:], recents[1:], []uuid.UUID{later.Id})
	erased := slices.Concat(tokens[:20], recents[:1], retireds, purgeds)
	live := slices.Concat(tokens[20:], recents[1:], []uuid.UUID{later.Id})
	for _, c := range []struct {
		format       string
		base         portability.Archive
		erased, live []uuid.UUID
	}{{"first new", first, firstErased, firstLive}, {"first version 22", olderFirst, firstErased, firstLive}, {"new", base, erased, live}, {"version 22", older, erased, live}} {
		inRecovery(t, f, ws, c.base, func(tx pgx.Tx) {
			if n := txCount(t, tx, `SELECT count(*) FROM secret_versions WHERE id=ANY($1) AND (encrypted_value IS NOT NULL OR erased_at IS NULL)`, c.erased); n != 0 {
				t.Errorf("recovery with a %s base brought back %d erased values", c.format, n)
			}
			if n := txCount(t, tx, `SELECT count(*) FROM secret_versions WHERE id=ANY($1) AND encrypted_value IS NOT NULL`, c.live); n != int64(len(c.live)) {
				t.Errorf("recovery with a %s base kept %d of %d live values", c.format, n, len(c.live))
			}
		})
	}
}

// erasedCarried is how many erased versions base carries as rows.
func erasedCarried(t *testing.T, base portability.Archive) int {
	t.Helper()
	n := 0
	for _, row := range base.Tables["secret_versions"] {
		if string(row["erased_at"]) != "null" {
			n++
		}
	}
	return n
}
