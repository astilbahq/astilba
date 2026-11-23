package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
	bolt "go.etcd.io/bbolt"
)

// In the owner model a managed database's cluster has two kinds of role. Its bootstrap superuser,
// postgres, has no password, so only a client in the database's own network namespace can reach
// it, over the loopback address the cluster trusts: the agent's helpers, for backups, restores,
// upgrades and index rebuilds. The database user is an ordinary role that owns the database, so
// code in the database, a restored dump's included, never runs with the superuser's rights. The
// agent creates every such cluster itself, in a container with no network, before anything else
// can reach it. The database's own container carries no PostgreSQL variables, so an empty volume
// makes the image refuse to start rather than create a cluster whose superuser the deployment
// knows the password of.

// labelPostgresRoles records on a data volume that its cluster is in the owner model. A volume
// without it is from before, and its database user is the superuser.
const labelPostgresRoles = "com.astilba.postgres-roles"

const postgresOwnerRoles = "owner-v1"

const postgresInitKind = "postgres-init-v1"

var errLegacyNewDatabase = errors.New("A new database must use the owner role model, so its database user isn't PostgreSQL's superuser.")

// postgresOn is the plan's managed database that keeps its data on volume.
func postgresOn(p deployment.Plan, volume uuid.UUID) (domain.Application, bool) {
	for _, a := range p.Specification.Applications {
		if a.Postgres != nil && a.Source.Image != nil && len(a.Mounts) == 1 && a.Mounts[0].VolumeID == volume {
			return a, true
		}
	}
	return domain.Application{}, false
}

// ownerClusterSQL is what the bootstrap superuser of a cluster the pinned image has just created
// runs, from template1, to put it in the owner model: the database user, roles, the user a member
// of each so it can manage what they own, and an empty database owned by owner, the user or, for a
// restore whose archive says so, one of those roles. The user has no password yet. The superuser's
// own throwaway password goes last.
func ownerClusterSQL(username, database string, roles []string, owner string) string {
	var b strings.Builder
	// inline-sql: sets up a managed database's fresh cluster, not the platform database.
	b.WriteString("CREATE ROLE " + quoteIdentifier(username) + " LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS INHERIT;\n")
	for _, role := range roles {
		// inline-sql: sets up a managed database's fresh cluster, not the platform database.
		b.WriteString("CREATE ROLE " + quoteIdentifier(role) + " NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;\n")
		// inline-sql: sets up a managed database's fresh cluster, not the platform database.
		b.WriteString("GRANT " + quoteIdentifier(role) + " TO " + quoteIdentifier(username) + " WITH INHERIT TRUE, SET TRUE;\n")
	}
	if database == "postgres" {
		// The maintenance database the image made is empty, and the superuser owns it.
		// inline-sql: sets up a managed database's fresh cluster, not the platform database.
		b.WriteString("DROP DATABASE postgres;\n")
	}
	// inline-sql: sets up a managed database's fresh cluster, not the platform database.
	b.WriteString("CREATE DATABASE " + quoteIdentifier(database) + " OWNER " + quoteIdentifier(owner) + " TEMPLATE template0;\n")
	// inline-sql: sets up a managed database's fresh cluster, not the platform database.
	b.WriteString("ALTER ROLE " + quoteIdentifier(domain.PostgresSuperuser) + " PASSWORD NULL;\n")
	return b.String()
}

// passwordSQL gives username credential, a password or the verifier PostgreSQL stored for one,
// which it reads as such.
func passwordSQL(username, credential string) string {
	// inline-sql: sets a managed database's password, not the platform database's.
	return "ALTER ROLE " + quoteIdentifier(username) + " PASSWORD " + quoteLiteral(credential) + ";\n"
}

// superuserSQL runs sql as the bootstrap superuser of the isolated cluster in container id. The
// statements reach psql on its standard input, so a password in them is never in a process list.
func (e *Engine) superuserSQL(ctx context.Context, id, database, sql string) error {
	if strings.ContainsRune(sql, 0) {
		return errors.New("invalid statement")
	}
	return e.dataTool(ctx, strings.NewReader(sql), io.Discard, "exec", "-i", id, "psql", "-XAq", "-v", "ON_ERROR_STOP=1", "-U", domain.PostgresSuperuser, "-d", database, "-f", "-")
}

// isolatedCluster is a PostgreSQL server the agent runs on a volume with no network, no
// capabilities and nothing else mounted, as postgres, to create or restore a cluster nothing else
// can reach yet.
type isolatedCluster struct {
	name    string
	labels  map[string]string
	release domain.PostgresRelease
	volume  uuid.UUID
	env     []string
	// options are settings its clients have besides sessionOptions.
	options []string
	cmd     []string
}

// runIsolated runs c's server, calls use once it accepts connections, and then stops it, which
// must leave the cluster shut down cleanly. The container is gone when it returns.
func (e *Engine) runIsolated(ctx context.Context, c isolatedCluster, use func(id string) error) error {
	created, err := e.Docker.ContainerCreate(ctx, client.ContainerCreateOptions{Name: c.name, Config: &container.Config{Image: c.release.Image, User: "postgres", Env: append(slices.Clone(c.env), pgOptions(c.options...)), Cmd: c.cmd, Labels: c.labels}, HostConfig: &container.HostConfig{NetworkMode: "none", CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"}, Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: deployment.VolumeName(c.volume), Target: c.release.Mount}}, Resources: container.Resources{Memory: 512 << 20, PidsLimit: ptr(int64(128))}}})
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = e.Docker.ContainerRemove(cleanup, created.ID, client.ContainerRemoveOptions{Force: true})
	}()
	if _, err = e.Docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return err
	}
	e.point("restore-container")
	if err = e.waitPostgresStartup(ctx, created.ID, domain.PostgresSuperuser, "postgres"); err != nil {
		return err
	}
	if err = use(created.ID); err != nil {
		return err
	}
	if _, err = e.Docker.ContainerStop(ctx, created.ID, client.ContainerStopOptions{Timeout: ptr(60)}); err != nil {
		return err
	}
	stopped, err := e.Docker.ContainerInspect(ctx, created.ID, client.ContainerInspectOptions{})
	if err != nil || stopped.Container.State.Running || stopped.Container.State.ExitCode != 0 {
		return errors.Join(errors.New("the isolated database did not stop cleanly"), err)
	}
	return nil
}

// bootstrapEnv makes the pinned image create a cluster whose bootstrap superuser is postgres, with
// a password nothing keeps: the image refuses to create one without a password, and the cluster
// is put in the owner model, which removes it, before anything can connect over a network.
func bootstrapEnv() ([]string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return []string{"POSTGRES_USER=" + domain.PostgresSuperuser, "POSTGRES_PASSWORD=" + hex.EncodeToString(b)}, nil
}

// errNoDatabasePassword refuses to create a database's cluster without its user's password, which
// the plan's secrets hold.
var errNoDatabasePassword = errors.New("The database's password is unavailable.")

// initName is the container that creates volume's cluster.
func initName(volume uuid.UUID) string { return "astilba-postgres-init-" + volume.String() }

// removeLeftoverInit removes the container creating volume's cluster that an agent which stopped
// part way left behind. Docker removes no volume a container mounts, even a stopped one.
func (e *Engine) removeLeftoverInit(ctx context.Context, p deployment.Plan, volume uuid.UUID) error {
	v, err := e.Docker.ContainerInspect(ctx, initName(volume), client.ContainerInspectOptions{})
	if errdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !upgradeOwned(v.Container.Config.Labels, p) || v.Container.Config.Labels[labelKind] != postgresInitKind {
		return errors.New("A container that isn't this database's initialisation holds its name.")
	}
	e.removeHelper(pgClient{container: v.Container.ID})
	return nil
}

// initOwnerCluster creates a's cluster in the owner model on its new, empty data volume, with the
// database user's password, before any deployment starts a server on it. It returns the verifier
// PostgreSQL stored for the password.
func (e *Engine) initOwnerCluster(ctx context.Context, p deployment.Plan, a domain.Application, password string) (string, error) {
	release, ok := domain.PostgresReleaseOf(*a.Source.Image)
	if !ok {
		return "", errUnsupportedPostgresImage
	}
	if password == "" {
		return "", errNoDatabasePassword
	}
	volume := a.Mounts[0].VolumeID
	labels := visualLabels(p, volume)
	labels[labelKind] = postgresInitKind
	env, err := bootstrapEnv()
	if err != nil {
		return "", err
	}
	if err = e.ensureImage(ctx, release.Image); err != nil {
		return "", err
	}
	verifier := ""
	err = e.runIsolated(ctx, isolatedCluster{name: initName(volume), labels: labels, release: release, volume: volume, env: env}, func(id string) error {
		if err := e.superuserSQL(ctx, id, "template1", ownerClusterSQL(a.Postgres.Username, a.Postgres.Database, nil, a.Postgres.Username)+passwordSQL(a.Postgres.Username, password)); err != nil {
			return err
		}
		v, err := e.clusterVerifier(ctx, id, a.Postgres.Username)
		verifier = v
		return err
	})
	return verifier, err
}

// postgresVerifier is how PostgreSQL stores a password: SCRAM, or MD5 from a cluster that chose it.
var postgresVerifier = regexp.MustCompile(`^(SCRAM-SHA-256\$[0-9]{1,10}:[A-Za-z0-9+/=]{1,512}\$[A-Za-z0-9+/=]{1,512}:[A-Za-z0-9+/=]{1,512}|md5[0-9a-f]{32})$`)

// clusterVerifier reads the verifier PostgreSQL stored for username's password in the isolated
// cluster running in container id.
func (e *Engine) clusterVerifier(ctx context.Context, id, username string) (string, error) {
	return e.readVerifier(ctx, pgClient{container: id}, domain.PostgresSuperuser, "template1", username)
}

// readVerifier reads, as user in database through db, the verifier username's password has.
func (e *Engine) readVerifier(ctx context.Context, db pgClient, user, database, username string) (string, error) {
	// inline-sql: reads a managed database's catalog, not the platform database.
	raw, err := e.dataOutput(ctx, db, "psql", "-XAqt", "-v", "ON_ERROR_STOP=1", "-U", user, "-d", database, "-c", "SELECT rolpassword FROM pg_catalog.pg_authid WHERE rolname = "+quoteLiteral(username)+" AND rolcanlogin")
	verifier := strings.TrimSpace(string(raw))
	if err != nil || !postgresVerifier.MatchString(verifier) {
		return "", errors.New("the database user's password is unreadable")
	}
	return verifier, nil
}

// postgresCredentialBucket holds, for each managed database volume whose cluster the agent created
// or restored, or which a backup policy covers, the verifier of its database user's password. A restore gives the user that
// verifier in the cluster that replaces it, so only the verifier travels, never the password, and
// the database need not be running.
const postgresCredentialBucket = "postgres-credentials"

type postgresCredential struct {
	Verifier string `json:"verifier"`
}

func (j *Journal) postgresCredential(volume uuid.UUID) (string, bool, error) {
	var v postgresCredential
	err := j.db.View(func(tx *bolt.Tx) error { return j.get(tx, postgresCredentialBucket, volume.String(), &v) })
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	return v.Verifier, err == nil, err
}

func (j *Journal) savePostgresCredential(volume uuid.UUID, verifier string) error {
	return j.db.Update(func(tx *bolt.Tx) error {
		return j.put(tx, postgresCredentialBucket, volume.String(), postgresCredential{Verifier: verifier})
	})
}

// saveRestoredPostgres records a restore target that release filled, and the verifier its
// database user has.
func (j *Journal) saveRestoredPostgres(volume uuid.UUID, release domain.PostgresRelease, verifier string) error {
	return j.db.Update(func(tx *bolt.Tx) error {
		if err := j.put(tx, postgresDataBucket, volume.String(), postgresData{Image: release.Image}); err != nil {
			return err
		}
		return j.put(tx, postgresCredentialBucket, volume.String(), postgresCredential{Verifier: verifier})
	})
}

// databaseCredential is what a restore gives the policy's database user in the cluster it
// creates: the verifier the agent recorded for the database's volume, or, without one, the
// verifier the running database holds.
func (e *Engine) databaseCredential(ctx context.Context, p backup.Policy) (string, error) {
	a, err := sourceApp(p)
	if err != nil || a.Postgres == nil || a.Source.Image == nil || len(a.Mounts) != 1 {
		return "", errors.New("the policy has no managed database")
	}
	verifier, found, err := e.Journal.postgresCredential(a.Mounts[0].VolumeID)
	if err != nil || found {
		return verifier, err
	}
	return e.runningVerifier(ctx, p, a)
}

// recordDatabaseCredential keeps the verifier of the policy's database user, so a later restore
// needs neither that database running nor any password. A cluster the agent created or restored
// has one already; a database from before the owner model gets it from the running database.
func (e *Engine) recordDatabaseCredential(ctx context.Context, p backup.Policy) error {
	a, err := sourceApp(p)
	if err != nil || a.Postgres == nil || a.Source.Image == nil || len(a.Mounts) != 1 {
		return errors.New("the policy has no managed database")
	}
	if _, found, err := e.Journal.postgresCredential(a.Mounts[0].VolumeID); err != nil || found {
		return err
	}
	verifier, err := e.runningVerifier(ctx, p, a)
	if err != nil {
		return err
	}
	return e.Journal.savePostgresCredential(a.Mounts[0].VolumeID, verifier)
}

// runningVerifier reads the verifier a's database user has in the policy's running database.
func (e *Engine) runningVerifier(ctx context.Context, p backup.Policy, a domain.Application) (string, error) {
	source, err := e.sourceContainer(ctx, p, a.ID)
	if err != nil {
		return "", err
	}
	if !source.State.Running {
		return "", errors.New("the database is stopped")
	}
	bounded, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	labels := visualLabels(p.Plan, a.ID)
	labels[labelKind] = "backup-credential-v1"
	helper, err := e.postgresHelper(bounded, "astilba-credential-"+uuid.NewString(), labels, source.ID, *a.Source.Image)
	if err != nil {
		return "", err
	}
	defer e.removeHelper(helper)
	return e.readVerifier(bounded, helper, a.Postgres.MaintenanceUser(), a.Postgres.Database, a.Postgres.Username)
}
