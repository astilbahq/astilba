package integration

import (
	"context"
	"database/sql"
	"os"
	"testing"

	dbmigrate "github.com/astilbahq/astilba/services/platform/db"
	"github.com/google/uuid"
	"github.com/pressly/goose/v3"
)

// destinationBindingMigration binds existing backup destinations to their servers.
const destinationBindingMigration = 75

// The migration binds an S3 destination whose policies are all on one server to that server, and
// marks one whose policies are on several shared. One no policy uses, or only a container job
// names, stays unbound for its first policy to bind, and a filesystem destination is never bound.
func TestDestinationBindingMigrationBindsEachExistingDestination(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	first, _, _ := enrollment(t, f, b, ws, "deployment")
	other, _, _ := enrollment(t, f, b, ws, "deployment")
	env := envForExecution(t, f, b, ws)
	conn, err := sql.Open("pgx", f.st.Pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, conn, os.DirFS("../../db/migrations"), goose.WithDisableGlobalRegistry(true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = provider.ApplyVersion(ctx, destinationBindingMigration, false); err != nil {
		t.Fatal(err)
	}
	destination := func(kind string) uuid.UUID {
		t.Helper()
		id := uuid.New()
		if _, err := f.st.Pool.Exec(ctx, "INSERT INTO backup_destinations(id,workspace_id,configuration,encrypted_access) VALUES($1,$2,jsonb_build_object('id',$4::text,'kind',$3::text),'\\x00')", id, ws, kind, id.String()); err != nil {
			t.Fatal(err)
		}
		return id
	}
	policy := func(server, destination uuid.UUID, method string) {
		t.Helper()
		if _, err := f.st.Pool.Exec(ctx, "INSERT INTO backup_policies(id,workspace_id,environment_id,server_id,configuration,encrypted_access) VALUES($1,$2,$3,$4,jsonb_build_object('method',$5::text,'destinationId',$6::text),'\\x00')", uuid.New(), ws, env, server, method, destination); err != nil {
			t.Fatal(err)
		}
	}
	oneServer, twoServers, unused, jobOnly, local := destination("s3"), destination("s3"), destination("s3"), destination("s3"), destination("filesystem")
	policy(first.Server.Id, oneServer, "postgres-logical")
	policy(first.Server.Id, oneServer, "quiesced-files")
	policy(first.Server.Id, twoServers, "postgres-logical")
	policy(other.Server.Id, twoServers, "quiesced-files")
	policy(other.Server.Id, jobOnly, "container-job")
	policy(first.Server.Id, jobOnly, "container-job")
	policy(first.Server.Id, local, "quiesced-files")
	policy(other.Server.Id, local, "quiesced-files")
	if _, err = provider.ApplyVersion(ctx, destinationBindingMigration, true); err != nil {
		t.Fatal(err)
	}
	if err = dbmigrate.Check(ctx, f.st.Pool.Config().ConnString()); err != nil {
		t.Fatal(err)
	}
	want := map[uuid.UUID]struct {
		server *uuid.UUID
		shared bool
	}{oneServer: {&first.Server.Id, false}, twoServers: {nil, true}, unused: {nil, false}, jobOnly: {nil, false}, local: {nil, false}}
	for id, w := range want {
		var server *uuid.UUID
		var shared bool
		if err = f.st.Pool.QueryRow(ctx, "SELECT server_id,shared FROM backup_destinations WHERE id=$1", id).Scan(&server, &shared); err != nil {
			t.Fatal(err)
		}
		if shared != w.shared || (server == nil) != (w.server == nil) || server != nil && *server != *w.server {
			t.Errorf("destination %s: server %v shared %t, want %v %t", id, server, shared, w.server, w.shared)
		}
	}
	// What the database holds now keeps to the binding.
	for _, bad := range []string{
		"UPDATE backup_destinations SET server_id=$2 WHERE id=$1",
		"UPDATE backup_destinations SET shared=true WHERE id=$1 AND $2::uuid IS NOT NULL",
	} {
		if _, err = f.st.Pool.Exec(ctx, bad, local, first.Server.Id); err == nil {
			t.Errorf("a filesystem destination took %q", bad)
		}
	}
	if _, err = f.st.Pool.Exec(ctx, "UPDATE backup_destinations SET server_id=$2 WHERE id=$1", twoServers, first.Server.Id); err == nil {
		t.Error("a shared destination was bound to a server")
	}
}
