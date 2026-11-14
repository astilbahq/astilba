package agent

import (
	"errors"

	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/google/uuid"
)

// protectRestoreSources keeps a snapshot from retention exactly while a restore of it is running
// or unresolved. A restore run is the only run with a target, and protection follows from run
// states alone, so whatever ends a restore (success, failure, settling an unstarted run,
// resolve-run, rejection) releases its snapshot, and history written by agents that left a
// failed restore's snapshot protected is corrected when it is next read.
func protectRestoreSources(s *scheduleState) {
	active := map[string]bool{}
	for _, r := range s.Runs {
		if r.TargetID != uuid.Nil && r.Snapshot != "" && (r.State == "running" || r.State == "needs_attention") {
			active[r.Snapshot] = true
		}
	}
	for i := range s.Runs {
		s.Runs[i].Protected = s.Runs[i].Snapshot != "" && active[s.Runs[i].Snapshot]
	}
}

func compactManifest(r *backup.Run) {
	if r.Manifest == nil {
		return
	}
	m := *r.Manifest
	if r.ManifestHash == "" {
		r.ManifestHash = backup.Hash(m)
	}
	m.Roles = []backup.Role{}
	m.Extensions = []string{}
	r.Manifest = &m
}

// Keep a representative for every retained snapshot, while ordinary run history
// is bounded independently of how often a snapshot is restore-tested.
func trimRunHistory(s *scheduleState, limit int) error {
	protectRestoreSources(s)
	for i := range s.Runs {
		compactManifest(&s.Runs[i])
	}
	for len(s.Runs) > limit {
		// A snapshot's representative is its latest catalogued run, as before: the backup that
		// uploaded it or a restore that verified it, either of which a restore and retention
		// read, and the latest of which a cutover is likeliest to name. A failed run never
		// stands in for one that is catalogued.
		representative := map[string]int{}
		for i, r := range s.Runs {
			if r.Snapshot == "" || r.Pruned {
				continue
			}
			if kept, ok := representative[r.Snapshot]; !ok || catalogued(r) || !catalogued(s.Runs[kept]) {
				representative[r.Snapshot] = i
			}
		}
		drop := -1
		for i, r := range s.Runs {
			if r.Protected || r.State == "running" || r.State == "needs_attention" {
				continue
			}
			if r.Snapshot == "" || r.Pruned || representative[r.Snapshot] != i {
				drop = i
				break
			}
		}
		if drop < 0 {
			return errors.New("run history is full of protected snapshots")
		}
		s.Runs = append(s.Runs[:drop], s.Runs[drop+1:]...)
	}
	return nil
}

// catalogued reports a run that records its snapshot's manifest: a backup that uploaded it or a
// restore that verified it.
func catalogued(r backup.Run) bool { return r.State == "succeeded" && r.Manifest != nil }
