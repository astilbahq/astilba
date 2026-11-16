package agent

import (
	"slices"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/domain"
)

// A stopped cluster the pinned images created holds only what PostgreSQL keeps in it; anything
// else in its volume is refused, wherever it is.
func TestRestoredClusterHoldsOnlyAClustersFiles(t *testing.T) {
	for _, image := range []string{domain.PostgresImage, domain.Postgres17Image} {
		_, data, err := postgresDataDirectory(image)
		if err != nil {
			t.Fatal(err)
		}
		in := func(path string) string {
			if data == "" {
				return path
			}
			return data + "/" + path
		}
		directories := []string{"base", "base/1", "base/16384", "base/pgsql_tmp", "global", "pg_commit_ts", "pg_dynshmem", "pg_logical", "pg_logical/mappings", "pg_logical/snapshots", "pg_multixact", "pg_multixact/members", "pg_multixact/offsets", "pg_notify", "pg_replslot", "pg_serial", "pg_snapshots", "pg_stat", "pg_stat_tmp", "pg_subtrans", "pg_tblspc", "pg_twophase", "pg_wal", "pg_wal/archive_status", "pg_wal/summaries", "pg_xact"}
		files := []string{"PG_VERSION", "postmaster.opts", "postgresql.conf", "postgresql.auto.conf", "pg_hba.conf", "pg_ident.conf", "base/1/PG_VERSION", "base/1/pg_filenode.map", "base/16384/pg_internal.init", "base/16384/16385", "base/16384/16385_fsm", "base/16384/16385_vm", "base/16384/16385_init", "base/16384/16385.1", "global/1260", "global/pg_control", "global/pg_filenode.map", "global/pg_internal.init", "pg_xact/0000", "pg_subtrans/0000", "pg_multixact/members/0000", "pg_multixact/offsets/0000", "pg_logical/replorigin_checkpoint", "pg_stat/pgstat.stat", "pg_wal/000000010000000000000001"}
		for _, path := range directories {
			if !clusterEntry(data, in(path), true) {
				t.Errorf("%s: directory %s refused", image, in(path))
			}
		}
		for _, path := range files {
			if !clusterEntry(data, in(path), false) {
				t.Errorf("%s: file %s refused", image, in(path))
			}
		}
		if data != "" && (!clusterEntry(data, "18", true) || clusterEntry(data, "18/other", true) || clusterEntry(data, "17", true)) {
			t.Errorf("%s: the path to the cluster is not all that sits beside it", image)
		}
		for _, path := range []string{"postmaster.pid", "standby.signal", "recovery.signal", "backup_label", "tablespace_map", "evil.so", "base/16384/evil.so", "base/16384/16385.so", "base/x/16385", "base/pgsql_tmp/pgsql_tmp1.0", "global/evil", "pg_tblspc/16400", "pg_replslot/slot", "pg_twophase/00000300", "pg_wal/000000010000000000000001.partial", "pg_wal/00000002.history", "pg_wal/archive_status/000000010000000000000001.ready", "pg_stat_tmp/global.stat", "pg_logical/snapshots/0-1.snap", "pg_dynshmem/mmap.1", "conf.d/evil.conf"} {
			if clusterEntry(data, in(path), false) {
				t.Errorf("%s: file %s accepted", image, in(path))
			}
		}
		for _, path := range []string{"conf.d", "base/16384/sub", "base/pgsql_tmp/sub", "pg_tblspc/16400", "pg_wal/other", "base/x"} {
			if clusterEntry(data, in(path), true) {
				t.Errorf("%s: directory %s accepted", image, in(path))
			}
		}
		if data != "" && (clusterEntry(data, "evil.so", false) || clusterEntry(data, "18/evil.so", false) || clusterEntry(data, "other", true)) {
			t.Errorf("%s: a file outside the cluster was accepted", image)
		}
	}
}

// A listing is refused at its first path a cluster doesn't hold, however its output is split.
func TestRestoredClusterListingStopsAtAFileAClusterDoesNotHold(t *testing.T) {
	l := &clusterListing{accept: func(path string) bool { return clusterEntry("", path, false) }, left: 1 << 10}
	if _, err := l.Write([]byte("./PG_VERSION\x00./base/1/16")); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Write([]byte("385\x00")); err != nil || len(l.partial) != 0 {
		t.Fatal("a path split across writes was refused: ", err)
	}
	if _, err := l.Write([]byte("./evil.so\x00")); err == nil || clusterReason(l.refusal) != "a file a cluster doesn't hold" {
		t.Fatal("a file a cluster doesn't hold was accepted")
	}
	small := &clusterListing{accept: func(string) bool { return true }, left: 4}
	if _, err := small.Write([]byte("./PG_VERSION\x00")); err == nil || clusterReason(small.refusal) != "more files than a cluster holds" {
		t.Fatal("a listing longer than its limit was accepted")
	}
}

// Setting names are read from an array as PostgreSQL writes it, and anything else is refused.
func TestSettingNamesReadsAnArrayAsPostgreSQLWritesIt(t *testing.T) {
	for literal, want := range map[string][]string{
		`{work_mem=16MB}`:                        {"work_mem"},
		`{"search_path=\"a, b\"",work_mem=16MB}`: {"search_path", "work_mem"},
		`{"app.note=x\\y"}`:                      {"app.note"},
	} {
		got, ok := settingNames(literal)
		if !ok || !slices.Equal(got, want) {
			t.Errorf("%s: %q %v", literal, got, ok)
		}
	}
	for _, literal := range []string{"", "{}", "work_mem=1", "{work_mem=1", "{work_mem}", "{work_mem=1,}", `{"work_mem=1}`, `{"work_mem=1"x}`, "{a=1,,b=2}"} {
		if got, ok := settingNames(literal); ok {
			t.Errorf("%s was read as %q", literal, got)
		}
	}
}

// A setting is matched as the server matches it, in any case of ASCII letters only.
func TestSettingNameMatchesAsTheServerDoes(t *testing.T) {
	for name, want := range map[string]string{"PLPGSQL.Variable_Conflict": "plpgsql.variable_conflict", "work_mem": "work_mem", "\u212Aey": "\u212Aey"} {
		if got := settingName(name); got != want {
			t.Errorf("%q: %q", name, got)
		}
	}
}

func TestClearedNoteNamesWhatFitsInAReport(t *testing.T) {
	if clearedNote(nil) != "" {
		t.Fatal("a restore that cleared nothing has a note")
	}
	few := clearedNote([]clusterSetting{{name: "log_statement", role: "reader"}, {name: "session_preload_libraries"}})
	if !strings.Contains(few, `"log_statement" for role "reader", "session_preload_libraries".`) {
		t.Fatalf("the note does not name each setting: %q", few)
	}
	many := make([]clusterSetting, 2000)
	for i := range many {
		many[i] = clusterSetting{name: "log_statement", role: strings.Repeat("\xff", 63)}
	}
	if note := clearedNote(many); len(note) > clearedNoteLimit || !strings.Contains(note, " more.") {
		t.Fatalf("the note is %d bytes: %q", len(note), note)
	}
	long := []clusterSetting{{name: strings.Repeat("\xff", 200), role: strings.Repeat("\xff", 63)}}
	if note := clearedNote(long); len(note) > clearedNoteLimit || !strings.Contains(note, ": 1 setting with a long name.") {
		t.Fatalf("the note is %d bytes: %q", len(note), note)
	}
}
