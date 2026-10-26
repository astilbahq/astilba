# Backups, scheduled jobs and fresh restores

The deployment agent sends encrypted backup data directly to a workspace destination. The management database stores policies and bounded run history, not customer database dumps. A completed upload is **unverified** until a fresh restore passes its recipe checks. Preparing cutover creates a draft revision; the existing deployment-plan review is still required before traffic or storage changes.

## Supported recipes

| Source | Consistency and verification | Limits |
| --- | --- | --- |
| Managed PostgreSQL 17.10 or 18.4 | Native custom-format logical dump; restore roles and recreate the database in a new volume; compare canonical SQL/data and database metadata from a second dump | The pinned PostgreSQL images and their version-specific layouts; one managed database; `plpgsql` and `hstore`; additional roles must be unprivileged NOLOGIN roles without memberships or cluster-wide role settings |
| Named file volume | Stop every declared container using the volume, reject unknown users, archive stable files, resume writers; extract into a new volume and compare its archive | Local Docker volumes with regular files, directories and symbolic links (up to 100,000 entries); all writers must be visible to this Docker daemon. Hard links and special files require another recipe. Directory timestamps are excluded from restore comparison. Do not use this recipe to claim a live database copy is consistent |
| Scheduled container job | Run an explicit argument array using an applied image worker's environment and mounts; record its exit code | Visual image workers; fixed resource bounds; missed runs are skipped; arbitrary job replay is never automatic |

PostgreSQL uses [the pinned deployment image](../services/platform/internal/domain/visual.go). Restic is pinned to **0.19.1**. The agent advertises backup support only when that version and the deployment prerequisites are present. Install the binary from the [official release](https://github.com/restic/restic/releases/tag/v0.19.1), verifying its checksum; the disposable test installer is [backup-tools.sh](../services/platform/host/backup-tools.sh).

The encrypted agent journal retains the database initialization credential, so fresh recovery can proceed when the source container is stopped or missing. Existing source resources are still checked for ownership.

PostgreSQL recovery includes database ownership, permissions, comments, connection limits and database-specific settings, including settings for roles within that database. Existing custom-format snapshots already contain this metadata. The agent uses `pg_restore --create` and includes database creation in its comparison with the restored dump. Database creation cannot run inside a transaction, so restoration stays in an isolated fresh cluster; a failed restore never becomes a verified cutover target. See [PostgreSQL's restore options](https://www.postgresql.org/docs/18/app-pgrestore.html).

A policy freezes its applied deployment. After a deployment changes, refresh the policy and install its new generation before expecting further backups. A failed source check does not silently back up a different resource. A new source, destination or consistency method requires a new policy.

## Destination setup

Owners with recent account verification can create reusable destinations. Credentials and repository passwords are write-only and encrypted in the management database and the agent journal. Keep the repository password and storage credentials in independent recovery records. Losing both those records and the agent's protected state makes encrypted snapshots unrecoverable.

- **S3 compatible:** provide an HTTPS origin, existing bucket, region and access credentials. Allow the agent to reach that origin. Each repository uses `astilba/<workspace UUID>/<policy UUID>` within the bucket. Customer private endpoints are contacted by the deployment agent, never fetched through the management service.
- **Filesystem:** create a real, private directory on the deployment server and enable its exact path with `--backup-roots`. The agent creates `<workspace UUID>/<policy UUID>/repository` and a separate ownership marker. Symlinked roots are rejected. This destination shares the server's failure boundary unless the operator provides independently durable mounted storage.

For the systemd installation, use a drop-in appropriate to the selected paths, for example:

```ini
[Service]
ExecStart=
ExecStart=/usr/local/bin/astilba-agent run --restic-cli /opt/astilba-platform-tools/restic --backup-roots /srv/backups
ReadWritePaths=/srv/backups
```

The root must already exist with suitable permissions. The agent's existing state directory remains private. A destination check opens the repository and validates its identity; installing a new policy initializes its separate encrypted repository. No shared repository pruning is used.

## Scheduling, retention and interruptions

Choose daily, cron or fixed-interval schedules. An existing daily policy keeps its original schedule representation and digest, including an empty timezone interpreted as UTC.

| Schedule | Timing |
| --- | --- |
| Daily | A local hour/minute in an IANA timezone, such as `Europe/London`. |
| Cron | Five fields: minute, hour, day of month, month, weekday. Supports lists, ranges, steps and month/weekday names; weekdays are 0–6 (Sunday–Saturday). `0 2 * * MON-FRI` runs at 02:00 on weekdays. Restricted day-of-month and weekday fields use OR semantics. No seconds, `@` shortcuts or embedded timezone prefixes. |
| Fixed interval | 1–525600 elapsed minutes, anchored to the first installed UTC instant. The first run is one interval after installation; later runs do not drift with job duration or daylight-saving changes. A new installed generation resets the anchor. |

Daily/cron schedules skip nonexistent local times and choose the first occurrence of a repeated wall-clock minute, including half-hour transitions. Cron expressions require an explicit IANA timezone; interval schedules use UTC. Calendar lookup uses the pinned parser's five-year look-ahead; an unavailable next occurrence blocks installation or execution instead of spinning or guessing.

The dashboard formats next runs in the configured timezone. If that zone is unavailable in the browser, it labels a UTC display without changing the schedule.

Each run's identity includes policy ID, generation and scheduled UTC instant. The agent persists the next instant together with its run record before host effects. Restart, reconnect and backward clock changes cannot replay an admitted run. Forward clock changes follow the missed-run rules: missed backups coalesce into one catch-up for the latest due instant, while container jobs more than one minute late are skipped with a history record. There is no arbitrary job replay or configurable parallelism.

Jobs and backups share the host execution lock with deployments and upgrades. If the host is busy, the same catch-up/skip rules apply when it becomes available. Installed schedules continue while management is unavailable; bounded reports upload when it returns. Disabling a local schedule requires delivery to that agent.

Upload/job timeouts can be set from 1 to 1800 seconds; omission preserves the thirty-minute limit. Restores retain their separate thirty-minute limit. A timed-out job stops and removes only its owned run container, with up to thirty additional seconds to confirm cleanup and record the exit result. Unconfirmed cleanup becomes `needs_attention` and blocks further host effects until inspected; a timeout is never evidence of successful application work.

General schedules and custom timeouts require an updated agent advertising the negotiated `schedules` capability. Extended commands use the `schedule` kind, which older executors reject before effects. Current agents omit this capability when talking to older management schemas. The management database advances to schema 17 and older management binaries refuse startup against it.

Advanced schedules remain encrypted in a versioned agent journal bucket, alongside a disabled legacy mirror of their run/writer fences. A rolled-back agent cannot run their cadence, but can still recover recorded writers and block operations over unresolved runs. After an older agent changes or resolves that state, the newer agent honors its newer generation/sequence rather than resurrecting hidden advanced work. The dashboard shows the effective disabled state; save and install a new policy generation to resume general scheduling. Controller transfer preserves policy IDs, history and interval cursors and still refuses unresolved effects.

Retention considers only this policy's catalogued snapshots after a successful upload. It keeps the configured recent uploads, the newest demonstrated recovery point and snapshots protected by active or interrupted restores. Unknown snapshots are left alone. Maintenance has a bounded repack size and operation deadline. Restores have a thirty-minute execution limit; upload/job limits are described above. Full manifests stay in the encrypted repository; operational records carry their hash and compact metadata. History is bounded to 128 records per policy while retaining a representative of each available snapshot; redundant completed restore records can age out. Unresolved protected history can require operator attention instead of being silently discarded.

An interrupted run does not become a success. Inspect any containers with the run's `com.astilba.run` label and the retained target volume. Once uncertain containers are stopped and the outcome is understood, a local operator can close the failed run:

```sh
astilba-agent resolve-run --policy POLICY_UUID --run RUN_UUID
```

This resumes recorded file writers where their identity still matches, refuses active run containers, and records failure rather than claiming a valid backup. The operator can then retry a new run. Snapshots from interrupted restores remain protected. Existing application data is never deleted by these operations.

## Restore and cutover

Choose an uploaded snapshot and request a fresh restore. The agent checks source ownership, repository identity, manifest checksums and capacity before creating a new volume. Capacity must cover the existing workload, staging and the new target; an unknown or insufficient capacity blocks restoration. PostgreSQL restore is isolated from application networks. A successful recipe check records a separate verified result and target volume.

Choose **Prepare cutover draft**, inspect the storage change, prepare a deployment plan and explicitly apply it. Writes made to the old source after the backup are not included in the restored data. The old volume stays retained. For Compose, the draft renames an explicit volume and its mounts while preserving other source bytes; ambiguous source constructs require an explicit file edit instead of an automatic rewrite. Review the resulting source before deployment.

Multiple independent resource backups do not represent an application-wide transaction. No generic major-version database migration or in-place restore is included.

## Recovery without the management database

Restic's repository and encrypted snapshot contain the manifest, roles and dump/archive. Recovery requires the storage credentials, repository password, snapshot ID and ordinary Docker/PostgreSQL tools; it does not require an Astilba API or management database.

1. Install restic 0.19.1 and set `RESTIC_REPOSITORY` to the policy repository. Set `RESTIC_PASSWORD_FILE` to a private password file; supply S3 credentials through a protected environment where applicable. Keep credentials out of command arguments and shell history.
2. List snapshots with `restic snapshots --json`, select the required snapshot and run `restic restore SNAPSHOT_ID --target /srv/recovery --verify` into an empty recovery directory.
3. Locate `manifest.json` beneath the restored directory. It identifies the recipe, source image, database/user, roles, extensions and the archive's SHA-256 and byte count. Verify `data.dump` or `files.tar` against that manifest before using it.
4. For PostgreSQL, create an empty named volume and a new container using the manifest's pinned image and database/user. Supply its new password through a private Docker environment file and keep it off application networks. Wait for `docker exec recovery-db pg_isready -h 127.0.0.1 -U USER -d DATABASE`; TCP readiness excludes the temporary initialization server. Create the listed NOLOGIN roles using quoted PostgreSQL identifiers. Choose `postgres` as the maintenance database, or `template1` when restoring a database named `postgres`, then pipe `data.dump` into `docker exec -i recovery-db pg_restore --exit-on-error --clean --if-exists --create -U USER -d MAINTENANCE_DATABASE`. This recreates the archived database inside the fresh cluster. Check the restored records, schema, database settings, ownership, grants, extensions and sequences through your application's queries before reconnecting any writers.
5. For files, create an empty named volume and extract `files.tar` into it with a temporary container. Verify its content and metadata against the archive, then deliberately attach the recovered volume to a stopped application. Preserve the old volume until its retention decision is explicit.

The native fixture exercises PostgreSQL records, schema, roles, grants, identity sequences and `hstore`; it also exercises filesystem and S3-compatible repositories, quiesced file recovery and offline container jobs. These are the supported recipe checks, not evidence that arbitrary database applications or extensions are supported.

## Verification

`make check-app` covers static checks and focused scheduling/retention tests. `make verify-app` runs the real API/PostgreSQL and Chromium/WebKit journeys. `make verify-agent-host` adds the native backup/restore and offline execution fixtures on both supported architectures in CI. Prepare only an owned disposable host; install the pinned restic tool and pull this additional fixture image:

```text
dxflrs/garage:v2.4.1@sha256:9c96caa2612d3411acc5b0e6701fb238dbfba33e533a6d7d3d811a4b12d0d020
```

Garage is a test-only S3 service. The fixture uses generated credentials, loopback exposure and a private test CA; it does not weaken production HTTPS validation. See [Garage's single-node setup](https://garagehq.deuxfleurs.fr/documentation/quick-start/).
