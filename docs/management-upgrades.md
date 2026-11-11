# Upgrading management

Database migrations only go forward, and each version of Astilba runs only on its own schema
version: a newer image refuses the database until you migrate it, and an older one refuses it
afterwards. So you cannot undo an upgrade by starting the old image again. Rolling back means
restoring the database you backed up just before migrating, with the image you ran before.

Upgrade management before the agents. These steps assume the setup in
[management backups](management-backups.md): PostgreSQL in a container called
`astilba-database-1`, management as a Compose service called `platform`, and the image chosen by
a variable in its environment file.

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
3. **Wait until nothing is running.** Stopping management does not wait for requests in
   progress, so pick a quiet moment, with no deployment, backup or other operation running.
   With the database, this shows the count:

   ```sh
   docker exec astilba-database-1 psql -U astilba -d astilba -Atc \
     "SELECT count(*) FROM execution_operations WHERE state IN ('queued','dispatched','running')"
   ```

4. **Back up.** Run the off-site backup and check that it succeeded, then also keep a local
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

## Upgrade

1. Stop management: `docker compose stop platform`.
2. Point the environment file at the new image, and migrate with it:

   ```sh
   docker compose run --rm --no-deps platform migrate
   ```

3. Start it with `docker compose up -d --wait platform`.
4. **Check** that:
   - `/healthz` answers 204 through your proxy;
   - you can sign in with your second factor;
   - every server shows as connected within a minute;
   - the log shows no errors: `docker compose logs --since 10m platform`.

If any check fails, roll back now: the longer the new version runs, the more you lose.

## Roll back

1. Stop management.
2. Recreate the database and restore the local dump into it, as in
   [restore](management-backups.md#restore), steps 3 and 4.
3. Point the environment file at the old image and start it. It needs no migration.
4. Run the same checks.

Anything written between the backup and the rollback is lost. If an agent ran an operation for
the new version in that time, reconcile that server as the
[recovery steps](hosting.md#installation-backup-and-recovery) describe.

## After the upgrade

Upgrade the agents from the Servers page once management is stable, a few hosts at a time.
Remove the local dump when you no longer need to roll back, and keep the old image until the
next upgrade.
