# Management backups

A management installation's backup has two parts: its PostgreSQL database and its wrapping
keyring. The database holds accounts, workspaces, configuration and every encrypted secret;
the keyring decrypts those secrets. You need both to recover, and you keep them apart, so that
someone who obtains one does not hold the other.

[`services/platform/management`](../services/platform/management) has a reference backup for
the database: a script with a systemd service and timer. Each run:

1. dumps the database with `pg_dump --format=custom`;
2. encrypts the dump with [age](https://age-encryption.org) as it is written, so no plaintext
   copy reaches the disk;
3. uploads the encrypted dump to S3-compatible storage, then a small manifest naming it, its
   SHA-256, the schema version and the image revision that was running;
4. reports success to a check-in monitor, or reports failure with the step that failed.

The keyring is not part of it: you back that up by hand when you create it, as
[described below](#the-keyring).

Set up as below, the server can add backups but cannot delete or replace the ones it has
uploaded, so a compromised server cannot destroy that history. On AWS, B2 and MinIO it cannot
read or list them either; on Cloudflare R2 it can, so there age encryption is what keeps them
private. Either way only a [restore drill](#restore-drills) shows that a backup works.

## What you need

- An age identity for restores, kept off the management server. Make two, one for daily use
  and one kept offline, and encrypt to both:

  ```sh
  age-keygen -o backup-primary.agekey
  age-keygen -o backup-offline.agekey
  ```

  Each command prints a public key (`age1…`). Only those public keys go on the server.
- `age`, `curl`, `openssl` and coreutils on the server. On Ubuntu 24.04, `sudo apt install age`
  adds the one that is missing.
- A bucket on S3-compatible storage, ideally with a different provider from the one hosting
  management, and two access keys: one for the server that can only write, and one for
  restores that can read. Set the bucket up as [described next](#storage-the-server-cannot-erase).
- A check-in monitor, such as a dead man's switch from an uptime service, that alerts when it
  has not heard from the backup for a day plus a grace period. This is the alert that catches
  a timer that never ran or a server that is down, which the script cannot report itself.

## Storage the server cannot erase

The server's key must not be able to delete objects, and a write to an existing name must not
destroy what was there. Write-only permissions cover the first; versioning or object lock covers
the second. Expire old backups with the provider's lifecycle rules, not from the server.

| Provider | Server key | Protecting existing backups | Script settings |
| --- | --- | --- | --- |
| Cloudflare R2 | An R2 API token with Object Read & Write, limited to the bucket. R2 has no write-only permission, so this key could delete | A [bucket lock rule](https://developers.cloudflare.com/r2/buckets/bucket-locks/) on the backup prefix, with a retention period such as 35 days, refuses deletion and overwriting until it ends. Add a lifecycle rule that deletes objects after it | URL `https://ACCOUNT_ID.r2.cloudflarestorage.com/BUCKET/PREFIX`, region `auto` |
| Backblaze B2 | An application key restricted to the bucket, with `listBuckets`, `listAllBucketNames` and `writeFiles` only. Without `deleteFiles` it cannot delete a version. Check that the key can upload before relying on it | B2 keeps every version of a name, but `writeFiles` can still hide a file, and a lifecycle rule may then delete hidden versions. Turn on [Object Lock](https://www.backblaze.com/docs/cloud-storage-object-lock) with a default retention period | URL `https://s3.REGION.backblazeb2.com/BUCKET/PREFIX`, region such as `eu-central-003` |
| AWS S3, MinIO and others | A user whose policy allows only `s3:PutObject` on `arn:aws:s3:::BUCKET/PREFIX/*` | Create the bucket with Object Lock, which also turns on versioning, and a default retention period | URL `https://ENDPOINT/BUCKET/PREFIX` (path-style), the bucket's region |

Use the lock, not versioning alone: without it, the server's key can overwrite a backup, and a
lifecycle rule that expires old versions then deletes the original. Even with the lock, a
retried upload can leave a second version, so restores [use the oldest version](#restore) of
each object. Keep management backups in a bucket, and under a key, of their own, separate from application
backups.

Make the restore key separately, with read and list permissions only, and keep it with the age
identity, not on the server.

## Install

As root on the management server, from a checkout of the revision you run:

```sh
install -m 0755 services/platform/management/astilba-management-backup.sh /usr/local/sbin/astilba-management-backup
install -m 0644 services/platform/management/astilba-management-backup.{service,timer} /etc/systemd/system/
install -d -m 0755 /etc/astilba
install -m 0644 recipients.txt /etc/astilba/management-backup.recipients   # the age1… public keys, one per line
install -m 0600 services/platform/management/management-backup.env.example /etc/astilba/management-backup.env
```

Edit `/etc/astilba/management-backup.env`; the example explains each setting. Then write the
server key as a curl configuration file, which the service loads as a systemd credential, so
the secret never appears in the environment or a process's arguments. Create it empty and
private, and type the key into it with an editor, not on a command line your shell records:

```sh
install -m 0600 /dev/null /etc/astilba/management-backup.s3
editor /etc/astilba/management-backup.s3   # one line: user = "ACCESS_KEY_ID:SECRET_ACCESS_KEY"
```

Then run a backup at once, and turn on the timer, which runs nightly at a random time between 02:30 and
03:00 UTC and catches up after a missed run:

```sh
systemctl daemon-reload
systemctl start astilba-management-backup.service
journalctl -u astilba-management-backup.service -n 5
systemctl enable --now astilba-management-backup.timer
```

A successful run logs `Uploaded STAMP.pgdump.age (… bytes, schema version N)`. Check that the
two objects appear in the bucket, using the restore key, and that the monitor saw the
check-in.

The script reaches PostgreSQL with `docker exec` into the container named in
`ASTILBA_BACKUP_DATABASE_CONTAINER`, so its `pg_dump` always matches the server. For a database
elsewhere, leave that empty and install the PostgreSQL client for the server's major version,
or newer, on the host, and set libpq's variables, such as `PGHOST`, `PGSSLMODE=verify-full`,
`PGSSLROOTCERT` and `PGPASSFILE`, in the environment file. The service hides `/root`, so libpq
cannot use `~/.postgresql/root.crt` or `~/.pgpass` there: set `PGSSLROOTCERT=system` (libpq 16
or newer) or a CA file under `/etc`, and give the password through a `PGPASSFILE`, never
`PGPASSWORD`.

## The keyring

The keyring changes only when you create it or add a key to it, so back it up then, not
nightly. Encrypt it to the same age recipients, record its checksum, and keep it in a password
manager and an offline copy, not in the backup bucket:

```sh
age -R /etc/astilba/management-backup.recipients -o keyring.json.age /etc/astilba/keyring.json
sha256sum /etc/astilba/keyring.json
```

Keep every keyring that a retained backup was taken with. A database restored with the wrong
keyring starts, but cannot decrypt secrets, two-factor credentials or recovery envelopes.

## Restore

Restore into an empty database, with an Astilba image whose schema is the backup's or newer.
These steps assume the PostgreSQL container is `astilba-database-1`, the management container
is a Compose service called `platform`, and the same major version of PostgreSQL as the backup,
which the manifest records.

1. **Choose and fetch.** With the restore key, list the backup prefix and choose a `.json`
   manifest by the time the store says it was written, not by its name, then fetch it and the
   `.pgdump.age` with the same stamp. If either object has more than one version, take the
   oldest, and find out what wrote the others. Check the dump against the manifest:

   ```sh
   echo "$(jq -r .sha256 STAMP.json)  STAMP.pgdump.age" | sha256sum -c -
   ```

   Anyone with the server's key can upload a backup, and the recipients are public, so a
   backup is only as trustworthy as the server was when it was written. After a suspected
   compromise, choose one written before it. Restoring runs the archive's SQL as a database
   superuser, so an untrusted archive can run commands in the database container.

2. **Decrypt** the dump, and the keyring from your password manager or offline copy, on the
   machine you are restoring to or a private one you copy them from:

   ```sh
   umask 077
   age -d -i backup-primary.agekey -o management.pgdump STAMP.pgdump.age
   age -d -i backup-primary.agekey -o keyring.json keyring.json.age
   ```

3. **Prepare an empty database and the keyring.** Start only the database. If it already holds
   data, stop management and recreate it:

   ```sh
   docker exec astilba-database-1 psql -U astilba -d postgres -v ON_ERROR_STOP=1 \
     -c 'DROP DATABASE astilba WITH (FORCE)' -c 'CREATE DATABASE astilba OWNER astilba'
   ```

   Install the keyring readable only by UID 65532, where the management container mounts it.

4. **Restore** from a file, so `pg_restore` can seek in it, in one transaction:

   ```sh
   docker cp management.pgdump astilba-database-1:/tmp/management.pgdump
   docker exec astilba-database-1 pg_restore --username astilba --dbname astilba \
     --no-owner --no-privileges --single-transaction /tmp/management.pgdump
   docker exec astilba-database-1 rm /tmp/management.pgdump
   ```

5. **Choose the image.** The manifest's `sourceRevision` is the image that took the backup. A
   newer image also works once migrated; an older one refuses to start. Run
   `astilba migrate` with the image you choose, which does nothing when the schema already
   matches.

6. **Fence the hosts before starting.** If any servers were enrolled, keep management stopped
   and follow [installation backup and recovery](hosting.md#installation-backup-and-recovery)
   to fence each one first. If you'll recover cross-host work, use an image at least as new as
   the one that last ran management: hosts' recovery checkpoints can only be read by the release
   that sealed them or a newer one. For cross-host recovery, use a backup taken after the newest
   checkpoint's window opened, which the Workspace page shows, normally 90 days before the
   checkpoint: an older one is refused if it holds a runtime the window doesn't reach ([the
   recovery window](cross-host-placement.md#the-recovery-window)). Keeping backups for 35 days
   stays well inside it.

7. **Start and check.** Start `astilba serve`, sign in with your second factor, and check that
   the workspaces, servers and recent history are what you expect. A second factor that works
   shows the keyring matches. Then delete the decrypted files.

Anything written after the backup is lost, because there is no point-in-time recovery: the
backup interval is how much you can lose.

## Restore drills

Restore a backup on a disposable machine before you rely on it, and again every quarter. Never
run a drill on the management host: the restore steps name the real database container. A
drill copy holds the same Git, provider and webhook credentials as the real installation, so it
must not be able to use them. Give it an environment file of its own that holds only the
database password, keyring path, origin and image, with no SMTP, DNS provider or GitHub App
settings. Put it and its database only on a Docker network created with `internal: true`,
which has no route off the host, and publish no ports. With PostgreSQL as a Compose service
called `database`, an override does this:

```yaml
services:
  database:
    networks: !override [drill]
  platform:
    networks: !override [drill]
    ports: !reset []
networks:
  drill:
    internal: true
```

If your drill's containers have other names, use them in the restore steps.

To sign in to it, run the client in the management container's network namespace, where it
can reach `http://127.0.0.1:8080`, for example with
`nsenter -t "$(docker inspect -f '{{.State.Pid}}' CONTAINER)" -n curl …`. Send the real origin
in the `Origin` header. Afterwards remove the drill's containers, volume and decrypted files.

## How it was tested

The script, service and timer were run on Ubuntu 24.04 with PostgreSQL 18.6, against MinIO
with an object-locked bucket. The server's key could upload but was refused reading, listing
and deletion; an overwrite added a version and left the original locked. A failed connection,
a refused upload and a storage outage were each reported, or retried, as above. A drill
restored a backup with the offline identity into an isolated copy that had no route out, and
signed in with a second factor, as did an in-place restore with the previous image. A backup
killed by its timeout reported failure too.
