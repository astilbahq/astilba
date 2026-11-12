# Upgrading management

Database migrations only go forward. A newer image refuses the database until you migrate it.
An older image refuses it afterwards, unless every migration it lacks only added to the schema:
new tables, columns that may be empty, and indexes. Such a migration records that it is
additive, and an older image then starts on the newer schema unchanged. A release whose notes
say its migrations are all additive can be rolled back by starting the old image again, as
long as that image is recent enough to check for additive migrations itself. For
any other release, rolling back means restoring the database you backed up just before
migrating, with the image you ran before. Migrations for River, the job queue, are never
additive, so a release that changes River's schema is one of these.

Upgrade management before the agents. These steps assume
[the Compose example](../services/platform/compose.example.yaml), with the image chosen by
`ASTILBA_IMAGE` in its environment file, PostgreSQL in a container called
`astilba-database-1`, and the backup from [management backups](management-backups.md).

## Before the upgrade

1. **Build the new image** from the reviewed commit, and tag both images by commit, so the one
   you run now stays available for a rollback:

   ```sh
   docker build --build-arg SOURCE_REVISION="$(git rev-parse HEAD)" -t "astilba:$(git rev-parse HEAD)" .
   ```

   Keep the old image until the new one has run for a while; do not prune it. If you build on
   another machine, move it with `docker save` and `docker load`, and check that its ID
   matches.
2. **Read what changed** between the two commits, especially
   `services/platform/db/migrations`, these docs and the agent: notes on agent compatibility
   tell you whether hosts need anything.
3. **Pick a quiet moment**, with no deployment, backup or other operation running, and none
   waiting for attention: agents still receive those. Stopping management does not wait for
   requests in progress. This shows how many operations are unfinished:

   ```sh
   docker exec astilba-database-1 psql -U astilba -d astilba -Atc \
     "SELECT count(*) FROM execution_operations WHERE state IN ('queued','dispatched','running','needs_attention')"
   ```

## Upgrade

1. **Stop management**, so nothing changes after the backup: `docker compose stop platform`.
   Check the count above is still 0. If it is not, start management, wait for those
   operations to finish, and stop it again.
2. **Back up.** Run the off-site backup and check that it succeeded, then also keep a local
   dump, which is quicker to restore and needs no keys from elsewhere:

   ```sh
   systemctl start astilba-management-backup.service
   journalctl -u astilba-management-backup.service -n 3
   install -d -m 0700 /var/backups/astilba
   umask 077
   docker exec astilba-database-1 pg_dump --username astilba --format=custom astilba \
     > /var/backups/astilba/pre-upgrade-OLD_COMMIT.pgdump
   ```

   Check that the keyring's checksum still matches the one you recorded with its backup.
3. Set `ASTILBA_IMAGE` to the new image, and migrate with it:

   ```sh
   docker compose run --rm --no-deps platform migrate
   ```

4. Start it with `docker compose up -d --wait platform`.
5. **Check** that:
   - `/healthz` answers 204 through your proxy;
   - you can sign in with your second factor;
   - every server shows as connected within a minute;
   - the log shows no errors: `docker compose logs --since 10m platform`.

If any check fails, roll back now: the longer the new version runs, the more you lose.

## Roll back

If the old image starts on the migrated database, which it does only when every migration it
lacks is additive, set `ASTILBA_IMAGE` back to it and run `docker compose up -d --wait platform`.
Nothing is lost, and you can upgrade again later. If it refuses with "database schema is newer
than this release", restore instead:

1. Stop management.
2. Before you replace the database, see whether the new version sent work to any server. This
   lists servers with operations created after the backup:

   ```sh
   docker exec astilba-database-1 psql -U astilba -d astilba -Atc \
     "SELECT DISTINCT server_id FROM execution_operations WHERE created_at > 'BACKUP_TIME'"
   ```

   Use the time you stopped management in step 1 of the upgrade, with its time zone, for
   example `2026-01-31 14:05:00+00`.
3. Recreate the database and restore the local dump into it, as in
   [restore](management-backups.md#restore), steps 3 and 4.
4. Set `ASTILBA_IMAGE` back to the old image. It needs no migration.
5. If step 2 listed any server, keep management stopped and fence each one, as
   [installation backup and recovery](hosting.md#installation-backup-and-recovery) describes,
   before you start it. Those servers may hold commands that the restored database does not
   know about. If you could not run step 2, fence every server.
6. Start management and run the same checks.

Anything written after the backup is lost.

## After the upgrade

Upgrade the agents from the Servers page once management is stable, a few hosts at a time.
Remove the local dump when you no longer need to roll back, and keep the old image until the
next upgrade.
