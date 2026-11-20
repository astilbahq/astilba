package agent

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
)

// crashRestoreBytes is how much of a snapshot's archive the interrupted restore stages: enough
// that what it leaves would make the next restore refuse for capacity.
const crashRestoreBytes = 600 << 20

// crashFixture is what the crash helper and the test share: a policy whose repository is a
// directory on this host, and the snapshot a restore asks for.
type crashFixture struct {
	plan     deployment.Plan
	policy   backup.Policy
	install  backup.Installation
	manifest backup.Manifest
}

func newCrashFixture(server, environment, unit, volume, workspace, policy, destination uuid.UUID, root string) crashFixture {
	plan := deployment.Plan{ID: uuid.NewSHA1(unit, []byte("plan")), ServerID: server, EnvironmentID: environment, UnitID: unit, RevisionID: uuid.NewSHA1(unit, []byte("revision")), Recipe: deployment.Recipe, Images: map[string]string{}, Specification: domain.Specification{Version: 1, Applications: []domain.Application{}, Variables: []domain.Variable{}, Volumes: []domain.Volume{{ID: volume, Name: "Data"}}}, ExpiresAt: time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)}.WithHash()
	p := backup.Policy{ID: policy, Name: "Files", WorkspaceID: workspace, EnvironmentID: environment, ServerID: server, DestinationID: destination, ResourceID: volume, Generation: 1, Method: backup.QuiescedFiles, Schedule: backup.Schedule{Timezone: "UTC", Hour: 2, Minute: 30}, Retain: 1, Enabled: true, Plan: plan}
	in := backup.Installation{Policy: p, Destination: backup.Destination{ID: destination, Name: "Local", Kind: "filesystem", Root: root}, Access: backup.Access{Password: "crash-fixture-repository-password"}}
	m := backup.Manifest{Recipe: backup.Recipe, PolicyID: policy, EnvironmentID: environment, ResourceID: volume, Method: backup.QuiescedFiles, Bytes: crashRestoreBytes + 100<<20, Roles: []backup.Role{}, Extensions: []string{}}
	return crashFixture{plan: plan, policy: p, install: in, manifest: m}
}

// crashRestic stands in for restic: it knows the repository, and a backup or a restore's download
// stages its data, says so through ready, and then hangs until it is killed.
func crashRestic(t *testing.T, ready string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "restic")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = version ]; then printf '%s\\n' 'restic " + backup.ResticVersion + " fixture'; exit 0; fi\n" +
		"case \"$*\" in\n" +
		"*'cat config'*) printf '{\"id\":\"" + strings.Repeat("e", 64) + "\",\"version\":2}\\n'; exit 0;;\n" +
		"*' backup '*) touch '" + ready + "'; exec sleep 3600;;\n" +
		"*' dump '*) head -c " + strconv.Itoa(crashRestoreBytes) + " /dev/zero; touch '" + ready + "'; exec sleep 3600;;\n" +
		"esac\nexit 1\n"
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	return path
}

func crashIDs(t *testing.T) []uuid.UUID {
	t.Helper()
	ids := []uuid.UUID{}
	for _, name := range []string{"SERVER", "ENVIRONMENT", "UNIT", "VOLUME", "WORKSPACE", "POLICY", "DESTINATION"} {
		id, err := uuid.Parse(os.Getenv("ASTILBA_SCRATCH_CRASH_" + name))
		if err != nil {
			t.Fatal(name, err)
		}
		ids = append(ids, id)
	}
	return ids
}

// TestNativeScratchCrashHelper is the agent the test kills: it runs a backup or a restore until its
// restic hangs.
func TestNativeScratchCrashHelper(t *testing.T) {
	mode := os.Getenv("ASTILBA_SCRATCH_CRASH")
	if mode == "" {
		t.Skip("helper process only")
	}
	ids := crashIDs(t)
	f := newCrashFixture(ids[0], ids[1], ids[2], ids[3], ids[4], ids[5], ids[6], os.Getenv("ASTILBA_SCRATCH_CRASH_ROOT"))
	j, err := Open(os.Getenv("ASTILBA_SCRATCH_CRASH_STATE"))
	if err != nil {
		t.Fatal(err)
	}
	docker, err := client.New(client.WithHost(os.Getenv("PLAT_APP_DOCKER_HOST")))
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{Journal: j, Docker: docker, ResticCLI: os.Getenv("ASTILBA_SCRATCH_CRASH_RESTIC"), BackupRoots: []string{f.install.Destination.Root}}
	ctx := context.Background()
	switch mode {
	case "backup":
		command := func(b backup.Command) api.ExecutionCommand {
			b.Installation = f.install
			return scheduleCommand(f.policy, b, time.Now().Add(time.Hour))
		}
		install := command(backup.Command{Action: "install"})
		if err = e.Execute(ctx, install); err != nil {
			t.Fatal(err)
		}
		r, err := j.Record(install.Id)
		if err != nil || r.Report.State != "succeeded" {
			t.Fatal("install ended", r.Report.State, r.Report.Note, err)
		}
		if err = j.Acknowledge(install.Id, r.Report.Sequence); err != nil {
			t.Fatal(err)
		}
		run := command(backup.Command{Action: "backup", RunID: uuid.New()})
		run.Generation = install.Generation + 1
		err = e.Execute(ctx, run)
		r, _ = j.Record(run.Id)
		t.Fatal("the backup was not killed while its restic ran:", err, r.Report.State, r.Report.Note)
	case "restore":
		_, _, err = e.restoreArchive(ctx, f.install, uuid.New(), uuid.New(), strings.Repeat("a", 64), "files.tar", f.manifest, func() error { return nil })
		t.Fatal("the restore was not killed while its restic ran:", err)
	}
}

// PLAT-447. An agent killed while a backup or a restore runs restic leaves the repository's password
// and the staged data in its state directory. Opening the journal again removes them, before the
// agent takes any command, and a restore the leftover stage would have refused for capacity then
// passes the check.
func TestNativeCrashLeavesNoRepositoryAccessOrStaging(t *testing.T) {
	if testing.Short() || os.Getenv("ASTILBA_NATIVE_AGENT") != "1" {
		t.Skip("native agent test")
	}
	if os.Geteuid() != 0 {
		t.Skip("mounts a bounded state directory, which needs root")
	}
	docker, err := client.New(client.WithHost(os.Getenv("PLAT_APP_DOCKER_HOST")))
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close()
	for _, mode := range []string{"backup", "restore"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			base := t.TempDir()
			// A bounded state directory, so the leftover stage shows in its free space as it would
			// on a host's disk.
			state := filepath.Join(base, "state")
			if err := os.Mkdir(state, 0700); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command("mount", "-t", "tmpfs", "-o", "size=2g,mode=0700", "tmpfs", state).CombinedOutput(); err != nil {
				t.Fatal(string(out), err)
			}
			t.Cleanup(func() { _ = exec.Command("umount", "-l", state).Run() })
			root := filepath.Join(base, "backups")
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()}
			f := newCrashFixture(ids[0], ids[1], ids[2], ids[3], ids[4], ids[5], ids[6], root)
			j, err := Open(state)
			if err != nil {
				t.Fatal(err)
			}
			if err = j.SetConfig(Config{ServerID: f.plan.ServerID, Role: "deployment"}); err != nil {
				t.Fatal(err)
			}
			if err = j.RecordUnitPlan(f.plan); err != nil {
				t.Fatal(err)
			}
			if err = j.Close(); err != nil {
				t.Fatal(err)
			}
			volume := f.plan.Specification.Volumes[0].ID
			if _, err = docker.VolumeCreate(ctx, client.VolumeCreateOptions{Name: deployment.VolumeName(volume), Labels: visualLabels(f.plan, volume)}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = docker.VolumeRemove(context.Background(), deployment.VolumeName(volume), client.VolumeRemoveOptions{Force: true})
			})

			ready := filepath.Join(base, "ready")
			helper := exec.Command(os.Args[0], "-test.run=^TestNativeScratchCrashHelper$", "-test.v")
			helper.Env = append(os.Environ(), "ASTILBA_SCRATCH_CRASH="+mode, "ASTILBA_SCRATCH_CRASH_STATE="+state, "ASTILBA_SCRATCH_CRASH_ROOT="+root, "ASTILBA_SCRATCH_CRASH_RESTIC="+crashRestic(t, ready))
			for i, name := range []string{"SERVER", "ENVIRONMENT", "UNIT", "VOLUME", "WORKSPACE", "POLICY", "DESTINATION"} {
				helper.Env = append(helper.Env, "ASTILBA_SCRATCH_CRASH_"+name+"="+ids[i].String())
			}
			// Its own process group, killed whole, as systemd kills the agent's control group.
			helper.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			var out strings.Builder
			helper.Stdout, helper.Stderr = &out, &out
			if err = helper.Start(); err != nil {
				t.Fatal(err)
			}
			exited := make(chan error, 1)
			go func() { exited <- helper.Wait() }()
			select {
			case err = <-exited:
				t.Fatalf("the helper ended before its restic ran: %v\n%s", err, out.String())
			case <-waitForFile(ready, 3*time.Minute):
			case <-time.After(3 * time.Minute):
				_ = syscall.Kill(-helper.Process.Pid, syscall.SIGKILL)
				<-exited
				t.Fatalf("the helper's restic never ran\n%s", out.String())
			}
			if err = syscall.Kill(-helper.Process.Pid, syscall.SIGKILL); err != nil {
				t.Fatal(err)
			}
			<-exited

			left := scratchEntries(t, state)
			kinds := []scratchKind{scratchResticAccess, scratchBackupStage}
			if mode == "restore" {
				kinds = []scratchKind{scratchResticAccess, scratchRestoreStage}
			}
			for _, k := range kinds {
				if !slices.ContainsFunc(left, func(name string) bool { kind, _ := scratchName(name); return kind == k }) {
					t.Fatalf("the killed %s left no %s scratch to sweep: %v\n%s", mode, k.prefix, left, out.String())
				}
			}
			password := false
			for _, name := range left {
				if kind, _ := scratchName(name); kind == scratchResticAccess {
					_, err := os.Stat(filepath.Join(state, name, scratchPassword))
					password = password || err == nil
				}
			}
			if !password {
				t.Fatal("the killed agent left no repository password, so the test proves nothing about it")
			}
			needed := f.manifest.Bytes*2 + 256<<20
			if mode == "restore" && freeBytes(state) >= needed {
				t.Fatal("the leftover stage would not make the next restore refuse, so the test proves nothing about capacity")
			}

			j, err = Open(state)
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			if remaining := scratchEntries(t, state); len(remaining) != 0 {
				t.Fatal("opening the journal left scratch behind:", remaining)
			}
			if freeBytes(state) < needed {
				t.Fatal("the state directory has not got its space back")
			}
			// The next restore gets past the capacity check and stages its download, and stops
			// there: no restic answers it.
			e := &Engine{Journal: j, Docker: docker, ResticCLI: filepath.Join(base, "no-restic")}
			_, _, err = e.restoreArchive(ctx, f.install, uuid.New(), uuid.New(), strings.Repeat("a", 64), "files.tar", f.manifest, func() error { return nil })
			var refused restoreRefusal
			if err == nil || errors.As(err, &refused) && refused == "insufficient restore capacity" {
				t.Fatal("the next restore was refused for capacity:", err)
			}
			if remaining := scratchEntries(t, state); len(remaining) != 0 {
				t.Fatal("the checked restore left scratch behind:", remaining)
			}
		})
	}
}

// scratchEntries names the entries in dir with a scratch name.
func scratchEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, entry := range entries {
		if _, ok := scratchName(entry.Name()); ok {
			out = append(out, entry.Name())
		}
	}
	return out
}

// waitForFile closes its channel once path exists, or never.
func waitForFile(path string, limit time.Duration) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		for deadline := time.Now().Add(limit); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
			if _, err := os.Stat(path); err == nil {
				close(done)
				return
			}
		}
	}()
	return done
}
