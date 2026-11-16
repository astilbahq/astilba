package agent

import (
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"strings"
)

// scratchKind names scratch the agent makes in its state directory for one call: a repository's
// password, or data staged on its way to or from a repository. The call that makes it removes it
// before it returns, and nothing resumes from it after a restart: an interrupted backup or restore
// is settled by its run, and an upgrade action by its own state. Only a process holding the journal
// makes scratch, and bbolt's exclusive lock on journal.db lets only one hold it. So once a process
// has opened the journal, any scratch it finds was left by one that died, and none of it is needed.
//
// Scratch that must outlive its process belongs somewhere else, under a name not listed here.
type scratchKind struct {
	prefix string
	file   bool
}

var (
	// scratchResticAccess holds a repository's password file, and restic's own temporary files.
	scratchResticAccess = scratchKind{prefix: "restic-access"}
	scratchBackupStage  = scratchKind{prefix: "backup-stage"}
	scratchRestoreStage = scratchKind{prefix: "restore-stage"}
	// scratchRestoreVerification is a dump of a restored database.
	scratchRestoreVerification = scratchKind{prefix: "restore-verification", file: true}
	scratchUpgradeStage        = scratchKind{prefix: "upgrade-stage"}
	scratchUpgradeDownload     = scratchKind{prefix: "upgrade-download"}
)

// scratchKinds is every kind, in the order the startup sweep removes them: passwords first.
var scratchKinds = []scratchKind{scratchResticAccess, scratchBackupStage, scratchRestoreStage, scratchRestoreVerification, scratchUpgradeStage, scratchUpgradeDownload}

// scratchPassword is the name of the password file in restic-access scratch.
const scratchPassword = "password"

// scratchDir makes a directory of kind k in the state directory. The caller removes it.
func (j *Journal) scratchDir(k scratchKind) (string, error) {
	if k.file {
		return "", errors.New("scratch kind is a file")
	}
	return os.MkdirTemp(j.dir, k.prefix+"-")
}

// scratchFile makes a file of kind k in the state directory. The caller closes and removes it.
func (j *Journal) scratchFile(k scratchKind) (*os.File, error) {
	if !k.file {
		return nil, errors.New("scratch kind is a directory")
	}
	return os.CreateTemp(j.dir, k.prefix+"-")
}

// scratchName reports the kind of a state directory entry's name. Only the names scratchDir and
// scratchFile make match: a listed prefix, a hyphen, and the decimal suffix os.MkdirTemp and
// os.CreateTemp append.
func scratchName(name string) (scratchKind, bool) {
	for _, k := range scratchKinds {
		suffix, ok := strings.CutPrefix(name, k.prefix+"-")
		if ok && suffix != "" && strings.Trim(suffix, "0123456789") == "" {
			return k, true
		}
	}
	return scratchKind{}, false
}

// sweepScratch removes the scratch a process that died left in the state directory. The caller
// holds the journal's lock. It looks only at the directory's own entries and removes only those
// whose name and type are what scratchDir and scratchFile make, through the directory's root, so no
// link leads it elsewhere. Passwords go first. A removal that fails is logged and left for the next
// open: the journal still opens, so the agent can settle its commands.
func sweepScratch(dir string) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		slog.Warn("the agent could not open its state directory to remove leftover scratch", "error", err)
		return
	}
	defer root.Close()
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		slog.Warn("the agent could not list its state directory to remove leftover scratch", "error", err)
		return
	}
	var removed, unexpected []string
	for _, k := range scratchKinds {
		for _, entry := range entries {
			name := entry.Name()
			if kind, ok := scratchName(name); !ok || kind != k {
				continue
			}
			info, err := root.Lstat(name)
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				slog.Warn("the agent could not inspect leftover scratch", "name", name, "error", err)
				continue
			}
			if k.file && !info.Mode().IsRegular() || !k.file && !info.IsDir() {
				unexpected = append(unexpected, name)
				continue
			}
			if err = root.RemoveAll(name); err != nil {
				slog.Warn("the agent could not remove leftover scratch", "name", name, "error", err)
				continue
			}
			removed = append(removed, name)
		}
	}
	if len(removed) > 0 {
		slog.Info("the agent removed scratch an interrupted process left in its state directory", "names", removed)
	}
	if len(unexpected) > 0 {
		slog.Warn("the agent left entries with scratch names that it did not make in its state directory", "names", unexpected)
	}
}
