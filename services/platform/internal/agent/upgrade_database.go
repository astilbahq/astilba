package agent

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

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
