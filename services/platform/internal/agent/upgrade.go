package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/upgrade"
	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
	bolt "go.etcd.io/bbolt"
)

type upgradeState struct {
	OperationID uuid.UUID

	Command upgrade.Command
	Status  upgrade.Status
	Writers []string
}

func (j *Journal) upgrade(id uuid.UUID) (s upgradeState, err error) {
	err = j.db.View(func(tx *bolt.Tx) error { return j.get(tx, "upgrades", id.String(), &s) })
	return
}
func (j *Journal) saveUpgrade(s upgradeState) error {
	return j.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("upgrades"))
		if bucket.Get([]byte(s.Command.Plan.ID.String())) == nil && bucket.Stats().KeyN >= 100 {
			return errors.New("upgrade history limit reached")
		}
		return j.put(tx, "upgrades", s.Command.Plan.ID.String(), s)
	})
}

// UpgradeViews omits credentials retained for disconnected recovery.
func (j *Journal) UpgradeViews() ([]upgrade.View, error) {
	states, err := j.upgrades()
	if err != nil {
		return nil, err
	}
	out := []upgrade.View{}
	for _, s := range states {
		out = append(out, upgrade.View{Plan: s.Command.Plan, Status: s.Status})
	}
	return out, nil
}
func (j *Journal) upgrades() (out []upgradeState, err error) {
	err = j.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("upgrades")).ForEach(func(k, v []byte) error {
			var s upgradeState
			if err := j.get(tx, "upgrades", string(k), &s); err != nil {
				return err
			}
			out = append(out, s)
			return nil
		})
	})
	return
}
func (e *Engine) upgradeStep(c api.ExecutionCommand, s *upgradeState, step string) error {
	s.Status.Step = step
	if err := e.Journal.saveUpgrade(*s); err != nil {
		return err
	}
	return e.checkpoint(c.Id, "upgrade-"+step)
}
func (e *Engine) upgradeReport(c api.ExecutionCommand, s upgradeState, success bool) error {
	if err := e.Journal.saveUpgrade(s); err != nil {
		return err
	}
	raw, _ := json.Marshal(s.Status)
	if err := e.Journal.Mutate(c.Id, func(r *Record) { value := json.RawMessage(raw); r.Report.Upgrade = &value }); err != nil {
		return err
	}
	state := api.ExecutionState("failed")
	if success {
		state = "succeeded"
	}
	return e.finish(c.Id, state, s.Status.Note)
}
func (e *Engine) executeUpgrade(ctx context.Context, c api.ExecutionCommand, r Record) error {
	var cmd upgrade.Command
	if c.Upgrade == nil || json.Unmarshal(*c.Upgrade, &cmd) != nil || cmd.Digest() != c.PlanHash || cmd.Plan.Validate() != nil || cmd.Plan.ID != c.ResourceId || cmd.Plan.Source.ServerID != c.ServerId || cmd.Plan.Source.EnvironmentID != c.EnvironmentId || cmd.Installation.Policy.ID != cmd.Plan.PolicyID || cmd.Installation.Policy.Plan.Hash != cmd.Plan.Source.Hash || cmd.Installation.Policy.ResourceID != cmd.Plan.ResourceID || cmd.Installation.Policy.Method != "postgres-logical" || cmd.Installation.Policy.Validate() != nil {
		return e.finish(c.Id, "failed", "Upgrade command does not match its reviewed recipe.")
	}
	s, err := e.Journal.upgrade(cmd.Plan.ID)
	if errors.Is(err, os.ErrNotExist) && cmd.Action == "prepare" {
		s = upgradeState{Command: cmd, Status: upgrade.Status{Phase: "planned", Serving: "old", KeepUntil: time.Now().UTC().Add(time.Duration(cmd.Plan.RetainDays) * 24 * time.Hour)}}
	} else if err != nil {
		return e.finish(c.Id, "failed", "Upgrade journal is unavailable.")
	}
	if backup.Hash(s.Command.Plan) != backup.Hash(cmd.Plan) {
		return e.finish(c.Id, "failed", "Upgrade plan changed.")
	}
	// Completed host effects can be acknowledged after a crash without repeating them.
	if (cmd.Action == "prepare" && s.Status.Phase == "ready") || (cmd.Action == "return" && s.Status.Phase == "returned") || ((cmd.Action == "cutover" || cmd.Action == "reconcile") && s.Status.Phase == "complete") {
		return e.upgradeReport(c, s, true)
	}
	if r.Checkpoint != "accepted" || s.OperationID == c.Id {
		s.Status.Phase, s.Status.Note = "attention", "Execution was interrupted. Inspect the recorded step; use the explicit recovery action. No uncertain effect was repeated."
		return e.upgradeReport(c, s, false)
	}
	if !s.Status.Allows(cmd.Action) {
		return e.finish(c.Id, "failed", "This upgrade action is not valid in the current state.")
	}
	if e.cancelled(c.Id) || cmd.Action == "prepare" && time.Now().After(cmd.Plan.ExpiresAt) {
		s.Status.Note = "Upgrade cancelled before host effects."
		if s.Status.Phase == "planned" {
			s.Status.Phase = "rejected"
		}
		return e.upgradeReport(c, s, false)
	}
	ctx, stop := e.commandContext(ctx, c)
	defer stop()
	s.Status.Phase = "running"
	s.OperationID = c.Id
	if err = e.Journal.saveUpgrade(s); err != nil {
		return err
	}
	e.point("upgrade-admitted")
	switch cmd.Action {
	case "prepare":
		err = e.prepareUpgrade(ctx, c, &s)
	case "cutover", "reconcile":
		err = e.activateUpgrade(ctx, c, &s, false)
	case "return":
		err = e.activateUpgrade(ctx, c, &s, true)
	default:
		err = errors.New("unsupported action")
	}
	if err != nil {
		s.Status.Phase = "attention"
		if len(s.Writers) == 0 && cmd.Action == "prepare" {
			s.Status.Phase = "rejected"
		}
		s.Status.Note = "Upgrade paused at " + s.Status.Step + ": " + safeVisualError(err, s.Command.Secrets)
		if len(s.Status.Note) > 1000 {
			s.Status.Note = "Upgrade paused at " + s.Status.Step + ". Inspect the host before recovery."
		}
		return e.upgradeReport(c, s, false)
	}
	return e.upgradeReport(c, s, true)
}

func (e *Engine) upgradeContainers(ctx context.Context, s upgradeState) error {
	list, err := e.Docker.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return err
	}
	for _, v := range list.Items {
		for _, m := range v.Mounts {
			if m.Type != mount.TypeVolume || m.Name != deployment.VolumeName(s.Command.Plan.OldVolume()) {
				continue
			}
			owned := visualOwned(v.Labels, s.Command.Plan.Source) && v.Labels[labelResource] == s.Command.Plan.ResourceID.String() && v.Labels[labelKind] == "visual-v1"
			helper := v.Labels["com.astilba.upgrade"] == s.Command.Plan.ID.String() && upgradeOwned(v.Labels, s.Command.Plan.Source) && v.Labels[labelKind] == "upgrade-source-v1"
			if !owned && !helper {
				return errors.New("unknown container mounts the database volume")
			}
		}
	}
	return nil
}
func (e *Engine) prepareUpgrade(ctx context.Context, c api.ExecutionCommand, s *upgradeState) error {
	p := s.Command.Plan
	policy := s.Command.Installation.Policy
	if err := e.upgradeStep(c, s, "preflight"); err != nil {
		return err
	}
	schedules, err := e.Journal.scheduleStates()
	if err != nil {
		return err
	}
	for _, schedule := range schedules {
		if len(schedule.Writers) > 0 || hasUncertainRun(schedule.Runs) {
			return errors.New("resolve interrupted local schedules before upgrading")
		}
	}
	if err := e.checkBackupSource(ctx, policy); err != nil {
		return err
	}
	if _, err := e.composeVersion(ctx); err != nil {
		return err
	}
	if err := e.upgradeContainers(ctx, *s); err != nil {
		return err
	}
	if _, err := e.volumePath(ctx, policy, p.OldVolume()); err != nil {
		return err
	}
	if _, err := e.Docker.VolumeInspect(ctx, deployment.VolumeName(p.TargetVolumeID), client.VolumeInspectOptions{}); !errdefs.IsNotFound(err) {
		return errors.New("target volume must be absent")
	}
	a, _ := sourceApp(policy)
	source, err := e.sourceContainer(ctx, policy, a.ID)
	if err != nil {
		return err
	}
	s.Command.Installation.Access.DatabasePassword = s.Command.Secrets[a.Postgres.PasswordSecretVersionID.String()]
	if s.Command.Installation.Access.DatabasePassword == "" {
		return errors.New("database credential missing")
	}
	m := backup.Manifest{Roles: []backup.Role{}, Extensions: []string{}}
	if err = e.postgresManifest(ctx, source.ID, a, domain.Postgres17Image, &m); err != nil {
		return err
	}
	if err = e.upgradeDatabaseChecks(ctx, source.ID, a); err != nil {
		return err
	}
	if err = e.knownUpgradeWriters(ctx, source.ID, a, p.Source); err != nil {
		return err
	}
	size, err := e.pgQuery(ctx, source.ID, a, "SELECT pg_database_size(current_database())")
	if err != nil {
		return err
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(size)), 10, 64)
	if err != nil || n < 0 || n > 1<<40 || freeBytes(e.Journal.dir) < n*2+256<<20 || dockerFreeBytes(ctx, e.Docker) < n*3+256<<20 {
		return errors.New("insufficient or unknown capacity for dump and fresh database while retaining the old volume")
	}
	if err = e.pullImageAuth(ctx, domain.PostgresImage, ""); err != nil {
		return err
	}
	if err = e.prepareRepository(ctx, &s.Command.Installation, true); err != nil {
		return err
	}
	owned, err := e.activeContainers(ctx, p.Source)
	if err != nil {
		return err
	}
	expected := 0
	for _, app := range p.Source.Specification.Applications {
		expected += len(p.Source.InstanceIDs(app.ID))
	}
	if len(owned) != expected {
		return errors.New("all writers must be owned by this deployment unit")
	}
	for _, v := range owned {
		s.Writers = append(s.Writers, v.ID)
	}
	if err = e.upgradeStep(c, s, "quiesce"); err != nil {
		return err
	}
	for _, id := range s.Writers {
		if _, err = e.Docker.ContainerStop(ctx, id, client.ContainerStopOptions{}); err != nil {
			return err
		}
	}
	s.Status.Serving = "none"
	e.point("upgrade-quiesce-effect")
	if err = e.upgradeStep(c, s, "isolate"); err != nil {
		return err
	}
	if err = e.upgradeContainers(ctx, *s); err != nil {
		return err
	}
	name := "astilba-upgrade-source-" + p.ID.String()
	labels := visualLabels(p.Source, p.ResourceID)
	labels[labelKind] = "upgrade-source-v1"
	labels["com.astilba.upgrade"] = p.ID.String()
	created, err := e.Docker.ContainerCreate(ctx, client.ContainerCreateOptions{Name: name, Config: &container.Config{Image: domain.Postgres17Image, Labels: labels, Cmd: []string{"postgres", "-c", "listen_addresses=127.0.0.1"}}, HostConfig: &container.HostConfig{NetworkMode: "none", Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: deployment.VolumeName(p.OldVolume()), Target: domain.PostgresMount(domain.Postgres17Image)}}, Resources: container.Resources{Memory: 512 << 20, PidsLimit: ptr(int64(128))}}})
	if err != nil {
		return err
	}
	if _, err = e.Docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return err
	}
	if err = e.waitPostgresStartup(ctx, created.ID, a.Postgres.Username, a.Postgres.Database); err != nil {
		return err
	}
	e.point("upgrade-isolate-effect")
	if err = e.upgradeDatabaseChecks(ctx, created.ID, a); err != nil {
		return err
	}
	if err = e.upgradeStep(c, s, "backup"); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(e.Journal.dir, "upgrade-stage-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	m = backup.Manifest{Recipe: backup.Recipe, PolicyID: policy.ID, RunID: p.ID, EnvironmentID: p.Source.EnvironmentID, ResourceID: p.ResourceID, Method: "postgres-logical", CreatedAt: time.Now().UTC(), Roles: []backup.Role{}, Extensions: []string{}}
	if err = e.postgresManifest(ctx, created.ID, a, domain.Postgres17Image, &m); err != nil {
		return err
	}
	if err = e.upgradeDump(ctx, *s, created.ID, a, stage, &m); err != nil {
		return err
	}
	raw, _ := json.Marshal(m)
	if err = os.WriteFile(filepath.Join(stage, "manifest.json"), raw, 0600); err != nil {
		return err
	}
	out, err := e.restic(ctx, s.Command.Installation, stage, "backup", "--quiet", "--host", policy.ID.String(), "--tag", "upgrade="+p.ID.String(), ".")
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		var line struct {
			Type     string `json:"message_type"`
			Snapshot string `json:"snapshot_id"`
		}
		if err = dec.Decode(&line); err != nil {
			return err
		}
		if line.Type == "summary" {
			s.Status.Snapshot = line.Snapshot
		}
	}
	if !backup.SnapshotID(s.Status.Snapshot) {
		return errors.New("final backup was not confirmed")
	}
	e.point("upgrade-backup-effect")
	if err = e.upgradeStep(c, s, "restore"); err != nil {
		return err
	}
	// Restore the uploaded copy, so the recovery point itself is demonstrated.
	download, err := os.MkdirTemp(e.Journal.dir, "upgrade-download-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(download)
	if _, err = e.restic(ctx, s.Command.Installation, download, "restore", s.Status.Snapshot, "--target", download, "--verify"); err != nil {
		return err
	}
	dir := ""
	err = filepath.WalkDir(download, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Name() == "manifest.json" {
			if dir != "" {
				return errors.New("ambiguous snapshot")
			}
			dir = filepath.Dir(path)
		}
		return nil
	})
	if err != nil || dir == "" {
		return errors.New("backup manifest missing")
	}
	stored, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil || !bytes.Equal(raw, stored) {
		return errors.New("backup manifest differs")
	}
	checked := m
	if err = checksumFile(filepath.Join(dir, "data.dump"), &checked); err != nil || checked.SHA256 != m.SHA256 || checked.Bytes != m.Bytes {
		return errors.New("backup checksum differs")
	}
	if freeBytes(e.Journal.dir) < m.Bytes+256<<20 || dockerFreeBytes(ctx, e.Docker) < m.Bytes*4+256<<20 {
		return errors.New("insufficient restore capacity")
	}
	if _, err = e.Docker.VolumeCreate(ctx, client.VolumeCreateOptions{Name: deployment.VolumeName(p.TargetVolumeID), Driver: "local", Labels: visualLabels(p.Source, p.TargetVolumeID)}); err != nil {
		return err
	}
	if _, err = e.volumePath(ctx, policy, p.TargetVolumeID); err != nil {
		return err
	}
	m.Image = domain.PostgresImage
	run := backup.Run{ID: p.ID, TargetID: p.TargetVolumeID}
	if err = e.restorePostgres(ctx, policy, &run, dir, m, s.Command.Installation.Access.DatabasePassword); err != nil {
		return err
	}
	e.point("upgrade-restore-effect")
	s.Status.Phase, s.Status.Step, s.Status.Note = "ready", "verified", "Backup and PostgreSQL 18 restore verified. Applications remain stopped. Review cutover or return to the old database."
	return nil
}

func (e *Engine) upgradeDatabaseChecks(ctx context.Context, id string, a domain.Application) error {
	for _, check := range []struct{ sql, want, message string }{
		{"SELECT count(*) FROM pg_database WHERE NOT datistemplate AND datname NOT IN (current_database(),'postgres')", "0", "additional databases require a separate recipe"},
		{"SELECT count(*) FROM pg_tablespace WHERE spcname NOT IN ('pg_default','pg_global')", "0", "custom tablespaces are unsupported"},
		{"SELECT count(*) FROM pg_prepared_xacts", "0", "prepared transactions are unsupported"},
		{"SELECT count(*) FROM pg_replication_slots", "0", "replication slots are unsupported"},
		{"SELECT count(*) FROM pg_subscription", "0", "subscriptions are unsupported"},
		{"SELECT count(*) FROM pg_db_role_setting WHERE setdatabase=0 OR setdatabase IN (SELECT oid FROM pg_database WHERE datname IN (current_database(),'postgres'))", "0", "database or role settings require a separate recipe"},
		{"SELECT count(*) FROM pg_database WHERE datname IN (current_database(),'postgres') AND (datacl IS NOT NULL OR datconnlimit<>-1 OR datdba<>(SELECT oid FROM pg_roles WHERE rolname=current_user) OR (shobj_description(oid,'pg_database') IS NOT NULL AND (datname<>'postgres' OR shobj_description(oid,'pg_database')<>'default administrative connection database')))", "0", "custom database privileges, ownership, comments or limits require a separate recipe"},
	} {
		raw, err := e.pgQuery(ctx, id, a, check.sql)
		if err != nil || strings.TrimSpace(string(raw)) != check.want {
			return errors.New(check.message)
		}
	}
	if a.Postgres.Database != "postgres" {
		admin := a
		pg := *a.Postgres
		pg.Database = "postgres"
		admin.Postgres = &pg
		raw, err := e.pgQuery(ctx, id, admin, "SELECT count(*) FROM pg_largeobject_metadata")
		if err != nil || strings.TrimSpace(string(raw)) != "0" {
			return errors.New("the postgres maintenance database contains large objects")
		}
		// The pinned dumper inventories every dumpable object class, including
		// collations and text-search objects; a catalog allowlist misses classes.
		archive := &boundedOutput{limit: 65536, stopOnLimit: true}
		if err = e.dataTool(ctx, nil, archive, "exec", id, "pg_dump", "-U", pg.Username, "-d", "postgres", "--schema-only", "-Fc"); err != nil {
			return errors.New("cannot inventory the postgres maintenance database")
		}
		inventory := &boundedOutput{limit: 65536, stopOnLimit: true}
		if err = e.dataTool(ctx, bytes.NewReader(archive.b.Bytes()), inventory, "exec", "-i", id, "pg_restore", "--list"); err != nil {
			return errors.New("cannot read maintenance object inventory")
		}
		for _, line := range strings.Split(inventory.b.String(), "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, ";") {
				return errors.New("the postgres maintenance database contains user objects")
			}
		}
	}
	return nil
}

func (e *Engine) knownUpgradeWriters(ctx context.Context, id string, a domain.Application, p deployment.Plan) error {
	allowed := map[string]bool{}
	for _, app := range p.Specification.Applications {
		for _, instance := range p.InstanceIDs(app.ID) {
			v, err := e.Docker.ContainerInspect(ctx, deployment.UnitName(instance), client.ContainerInspectOptions{})
			if err != nil {
				return err
			}
			if !visualOwned(v.Container.Config.Labels, p) || v.Container.Config.Labels[labelResource] != app.ID.String() {
				return errors.New("writer ownership changed")
			}
			if v.Container.NetworkSettings != nil {
				for _, n := range v.Container.NetworkSettings.Networks {
					allowed[n.IPAddress.String()] = true
					allowed[n.GlobalIPv6Address.String()] = true
				}
			}
		}
	}
	delete(allowed, "")
	raw, err := e.pgQuery(ctx, id, a, "SELECT coalesce(json_agg(host(client_addr)),'[]') FROM pg_stat_activity WHERE backend_type='client backend' AND pid<>pg_backend_pid()")
	var clients []*string
	if err != nil || json.Unmarshal(raw, &clients) != nil {
		return errors.New("cannot identify database writers")
	}
	for _, ip := range clients {
		if ip == nil || !allowed[*ip] {
			return errors.New("disconnect external or unknown database clients before upgrading")
		}
	}
	return nil
}
func (e *Engine) upgradeDump(ctx context.Context, s upgradeState, source string, a domain.Application, stage string, m *backup.Manifest) error {
	labels := visualLabels(s.Command.Plan.Source, a.ID)
	labels[labelKind] = "upgrade-tools-v1"
	labels["com.astilba.upgrade"] = s.Command.Plan.ID.String()
	tool, err := e.Docker.ContainerCreate(ctx, client.ContainerCreateOptions{Name: "astilba-upgrade-tools-" + s.Command.Plan.ID.String(), Config: &container.Config{Image: domain.PostgresImage, Cmd: []string{"sleep", "3600"}, Env: []string{"PGPASSWORD=" + s.Command.Installation.Access.DatabasePassword}, Labels: labels}, HostConfig: &container.HostConfig{NetworkMode: container.NetworkMode("container:" + source), ReadonlyRootfs: true, CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"}, Resources: container.Resources{Memory: 256 << 20, PidsLimit: ptr(int64(32))}}})
	if err != nil {
		return err
	}
	defer func() {
		clean, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = e.Docker.ContainerRemove(clean, tool.ID, client.ContainerRemoveOptions{Force: true})
	}()
	if _, err = e.Docker.ContainerStart(ctx, tool.ID, client.ContainerStartOptions{}); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(stage, "data.dump"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	err = e.dataTool(ctx, nil, f, "exec", tool.ID, "pg_dump", "-h", "127.0.0.1", "-U", a.Postgres.Username, "-d", a.Postgres.Database, "-Fc", "-Z0", "--no-password", "--quote-all-identifiers")
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	return checksumFile(filepath.Join(stage, "data.dump"), m)
}

func (e *Engine) removeUpgradeHelpers(ctx context.Context, s upgradeState) error {
	p := s.Command.Plan
	for _, entry := range []struct{ name, kind, image string }{
		{"astilba-upgrade-tools-" + p.ID.String(), "upgrade-tools-v1", domain.PostgresImage},
		{"astilba-restore-" + p.ID.String(), "backup-restore-v1", domain.PostgresImage},
		{"astilba-upgrade-source-" + p.ID.String(), "upgrade-source-v1", domain.Postgres17Image},
	} {
		v, err := e.Docker.ContainerInspect(ctx, entry.name, client.ContainerInspectOptions{})
		if errdefs.IsNotFound(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !upgradeOwned(v.Container.Config.Labels, p.Source) || v.Container.Config.Labels[labelKind] != entry.kind || v.Container.Config.Image != entry.image {
			return errors.New("helper ownership changed")
		}
		if entry.kind == "backup-restore-v1" {
			if v.Container.Config.Labels["com.astilba.run"] != p.ID.String() {
				return errors.New("restore helper identity changed")
			}
		} else if v.Container.Config.Labels["com.astilba.upgrade"] != p.ID.String() {
			return errors.New("upgrade helper identity changed")
		}
		if _, err = e.Docker.ContainerRemove(ctx, v.Container.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: false}); err != nil {
			return err
		}
	}
	return nil
}
func (e *Engine) activateUpgrade(ctx context.Context, c api.ExecutionCommand, s *upgradeState, old bool) error {
	p := s.Command.Plan
	if old && s.Status.WritesPossible {
		return errors.New("new writes may exist; returning to the old database would lose data")
	}
	if err := e.upgradeStep(c, s, "reconcile"); err != nil {
		return err
	}
	if err := e.removeUpgradeHelpers(ctx, *s); err != nil {
		return err
	}
	target := p.Target()
	if old {
		target = p.Source
	}
	if _, err := e.volumePath(ctx, s.Command.Installation.Policy, func() uuid.UUID {
		if old {
			return p.OldVolume()
		}
		return p.TargetVolumeID
	}()); err != nil {
		return err
	}
	// The unit network can admit external clients before published routing or
	// managed writers resume. Record this conservative boundary first.
	if !old {
		s.Status.WritesPossible = true
	}
	s.Status.Serving = "unknown"
	if err := e.upgradeStep(c, s, "database"); err != nil {
		return err
	}
	body, err := deployment.Compose(target, s.Command.Secrets, false)
	if err != nil {
		return err
	}
	for _, a := range target.Specification.Applications {
		for _, instance := range target.InstanceIDs(a.ID) {
			v, err := e.Docker.ContainerInspect(ctx, deployment.UnitName(instance), client.ContainerInspectOptions{})
			if err != nil && !errdefs.IsNotFound(err) {
				return err
			}
			if err == nil && (!visualOwned(v.Container.Config.Labels, p.Source) || v.Container.Config.Labels[labelResource] != a.ID.String()) {
				return errors.New("service ownership changed")
			}
		}
	}
	if err = e.compose(ctx, target, body, s.Command.Secrets, "up", "--detach", "--no-deps", "--wait", "--wait-timeout", "60", deployment.ServiceName(p.ResourceID)); err != nil {
		return err
	}
	e.point("upgrade-database-effect")
	if err = e.upgradeStep(c, s, "routes"); err != nil {
		return err
	}
	if err = e.publishVisualRoutes(ctx, target); err != nil {
		return err
	}
	routes, err := e.Journal.routing()
	if err != nil {
		return err
	}
	if drift, err := e.ingressDrift(ctx, routes); err != nil || drift {
		return errors.New("route activation is unconfirmed; writers remain stopped")
	}
	e.point("upgrade-routes-effect")
	if err = e.upgradeStep(c, s, "writers"); err != nil {
		return err
	}
	for _, a := range serviceOrder(target.Specification) {
		if a.ID == p.ResourceID {
			continue
		}
		if err = e.compose(ctx, target, body, s.Command.Secrets, "up", "--detach", "--no-deps", "--wait", "--wait-timeout", "60", deployment.ServiceName(a.ID)); err != nil {
			return err
		}
		if a.Health != nil {
			if err = e.waitHTTP(ctx, c, target, a); err != nil {
				return err
			}
		}
	}
	e.point("upgrade-writers-effect")
	observed, expected, err := e.observeVisualPlan(ctx, target, s.Command.Secrets, true)
	if err != nil {
		return err
	}
	if observed.InfrastructureDrift || len(observed.Resources) != len(target.Specification.Applications) {
		return errors.New("activation is unconfirmed")
	}
	for _, r := range observed.Resources {
		if r.State != "running" && r.State != "healthy" {
			return errors.New("service is not running")
		}
	}
	if err = e.Journal.saveUnit(unitState{Plan: target, Expected: expected}); err != nil {
		return err
	}
	observed, _, err = e.observeUnit(ctx, target, s.Command.Secrets)
	if err != nil {
		return err
	}
	observed.Applied = true
	if err = e.visualObservation(c, observed); err != nil {
		return err
	}
	if !old {
		schedules, err := e.Journal.scheduleStates()
		if err != nil {
			return err
		}
		for _, schedule := range schedules {
			if schedule.Installation.Policy.EnvironmentID == p.Source.EnvironmentID {
				schedule.Installation.Policy.Enabled = false
				if err = e.Journal.saveSchedule(schedule); err != nil {
					return err
				}
			}
		}
	}
	s.Status.Phase, s.Status.Serving, s.Status.Step, s.Status.Note = "complete", "new", "complete", "PostgreSQL 18 is serving. New writes may exist; the old volume is not a lossless rollback."
	if old {
		s.Status.Phase, s.Status.Serving, s.Status.Note = "returned", "old", "The old PostgreSQL database is serving. Retained backup and target storage require explicit cleanup."
	}
	s.Status.KeepUntil = time.Now().UTC().Add(time.Duration(p.RetainDays) * 24 * time.Hour)
	return nil
}

// PostgreSQL embeds the source server version in the archive header. Normalize
// only the bounded SQL preamble; identical-looking COPY data is never touched.
type upgradeSQLHeader struct {
	out     io.Writer
	pending []byte
	done    bool
}

func (w *upgradeSQLHeader) Write(b []byte) (int, error) {
	n := len(b)
	if w.done {
		return w.out.Write(b)
	}
	w.pending = append(w.pending, b...)
	if i := bytes.Index(w.pending, []byte("\nSET ")); i >= 0 {
		header := bytes.ReplaceAll(w.pending[:i], []byte("-- Dumped from database version 17.10"), []byte("-- Dumped from database version 18.4"))
		if _, err := w.out.Write(header); err != nil {
			return 0, err
		}
		if _, err := w.out.Write(w.pending[i:]); err != nil {
			return 0, err
		}
		w.pending = nil
		w.done = true
	} else if len(w.pending) > 8192 {
		return 0, fmt.Errorf("unexpected archive preamble")
	}
	return n, nil
}

func upgradeOwned(labels map[string]string, p deployment.Plan) bool {
	return labels[labelServer] == p.ServerID.String() && labels[labelEnvironment] == p.EnvironmentID.String() && labels["com.astilba.unit"] == p.UnitID.String()
}
