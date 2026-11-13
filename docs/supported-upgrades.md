# Supported PostgreSQL upgrades

Astilba moves a managed PostgreSQL database to a newer release in two ways:

- **Minor updates in place:** a later release of the same major replaces the running one on the same volume. See [PostgreSQL releases](#postgresql-releases).
- **The major upgrade:** a logical migration from any PostgreSQL 17 release Astilba has pinned to its current PostgreSQL 18 release.

## The major upgrade

Creating a plan changes no running service. Starting it stops every service in the environment, uploads a final encrypted backup, restores that uploaded copy into fresh storage and verifies the result. Applications remain stopped until a separate cutover approval or an explicit return to the old database.

| Component | Supported input |
| --- | --- |
| Source | Any PostgreSQL 17 release in [the release table](#postgresql-releases): today 17.10 (retired) and 17.12 |
| Target and migration client | The current PostgreSQL 18 release when the plan is made, today `postgres:18.7-alpine3.23@sha256:e6014245dccbc0f28b07dac8e3f793c94a18420f78690e9db547957514a524f1`. The plan records it, so a later pin does not change a plan already reviewed or under way. Plans made before plans recorded their target restore into 18.4 |
| Source storage | Owned local Docker volume at `/var/lib/postgresql/data` |
| Target storage | Fresh owned local Docker volume at `/var/lib/postgresql`, with data at `/var/lib/postgresql/18/docker` |
| Configuration | One managed database in a visual deployment; Compose remains manual |
| Database | One application database, standard tablespaces; no custom database settings, ACL, owner, comments or connection limits. The `postgres` maintenance database must contain no user objects |
| Extensions | `plpgsql` 1.0 and `hstore` 1.8; no other extensions |
| Roles | The managed owner plus the backup recipe's unprivileged NOLOGIN roles; no memberships, per-role settings or additional login roles |
| Replication | No replication slots, subscriptions or prepared transactions |
| Backup | Existing PostgreSQL logical-backup policy with an accessible filesystem or S3 destination and its repository password |
| Tools | Docker Compose 5.6.1, restic 0.19.1 and the pinned PostgreSQL client images |

The agent rejects unsupported server versions, storage layouts, roles, extensions and unknown clients before downtime. Disconnect external clients before starting. During migration, the old database runs only in an isolated network namespace; managed applications stay stopped. Astilba cannot coordinate writers in other environments or outside its ownership boundary. The host operator remains responsible for privileged access to Docker and PostgreSQL.

The backup policy can use daily, cron or interval scheduling and a custom timeout. General schedules and custom timeouts require the negotiated `schedules` agent capability at command delivery. Upgrade actions remain `upgrade` commands; preparing one does not install or run the policy's recurring schedule.

Reserve staging space for twice the database size plus 256 MiB, and Docker space for three times its size plus 256 MiB. Before restoring, the agent checks the actual archive size again, requiring four times that size plus 256 MiB on Docker storage. The duration depends on data size and destination throughput; no online or fixed-duration migration is promised.

### Verification and cutover

The migration uses PostgreSQL 18 `pg_dump` with quoted identifiers, reads the uploaded restic snapshot back with verification, and recreates the database with `pg_restore --create --exit-on-error` inside an isolated fresh cluster. It creates the supported roles and compares complete restored SQL streams from the same pinned client, including database creation, schema, records, constraints, ownership, grants and sequence values. Only the two comment lines in the SQL preamble that name the dumped server's and `pg_dump`'s versions are normalized; database records are unchanged. PostgreSQL recommends using the newer dump tools for a major-version migration. [PostgreSQL upgrade documentation](https://www.postgresql.org/docs/18/upgrading.html), [pg_dump](https://www.postgresql.org/docs/18/app-pgdump.html).

Cutover records that new writes are possible **before** connecting the new database to the deployment network. This is conservative: external clients may connect before managed applications restart. The agent verifies ingress activation before resuming applications. A failed or interrupted cutover reports the last step and an unknown serving state until an explicit reconciliation confirms the new database and its routes. It never offers old-volume recovery as lossless after the write boundary.

Upgrade state is encrypted in the agent journal and summarized in operation history. An interrupted backup or restore pauses for inspection instead of rerunning an uncertain effect. Before new writes, choose **Return to old database**. After that boundary, choose **Reconcile new database and resume services**. All host operations and local schedules pause while an upgrade is active. After a successful cutover, refresh and reinstall backup policies for the new deployment; the old frozen schedules are disabled.

Choose a minimum retention period of 1–365 days. The platform protects the old and target volumes during migration and through that period after completion or return. Nothing is automatically deleted afterward. Upgrade snapshots are outside scheduled retention's catalog and remain until explicit operator cleanup. Keep the repository password and destination access independently of the management database.

### Manual completion

Use ordinary Docker, PostgreSQL and restic tools if management is unavailable. Stop the agent service before reading its exclusive journal, run `astilba-agent inspect-upgrades --state /var/lib/astilba-agent`, and restart the service after inspection. The command prints plans and status without execution credentials. Inspect this output and operation history first; identify the recorded source volume, target volume, snapshot and last step. Do not infer success from a stopped container or missing acknowledgement.

Before the new-write boundary, stop the labeled upgrade helpers and any target database, then start the original PostgreSQL 17 service with its original volume and configuration. Confirm database readiness and ingress before starting applications. Preserve both volumes and the recovery snapshot.

After the boundary, retain PostgreSQL 18 as authoritative. Inspect its volume, database readiness and the owned Traefik configuration, then reconcile routing and restart applications against that database. Returning to the old volume would discard new writes; recovering from a snapshot is a separate recovery decision. Do not erase the encrypted journal or switch controllers while recovery is outstanding. Once normal service is established, restore management and reconcile its recorded state before accepting new operations.

The native fixture exercises data, ownership, constraints and hstore, plus restart/lost-acknowledgement cases at migration and activation steps on disposable Linux hosts. Its complete journey starts from the retired 17.10, and a plan without a recorded target finishes on 18.4. Local checks are not evidence of an arbitrary imported database's compatibility. Additional application or database recipes need their own supported input matrix and fixtures.

## PostgreSQL releases

[`domain/postgres.go`](../services/platform/internal/domain/postgres.go) lists every PostgreSQL image Astilba has pinned for managed databases: its exact digest, server version, Alpine line, mount and data directory. A new database runs its major's current release, the newest row. An earlier row is **retired**: databases, backups, restore targets and upgrade plans made under it still name its exact image, so it stays known. Nothing new starts on a retired release, and a draft can keep one only for a database that already runs it. A preview's database is new storage, so it takes the current release.

| Major | Current | Retired |
| --- | --- | --- |
| 17 | 17.12 | 17.10 |
| 18 | 18.7 | 18.4 |

The dashboard reads this table from `GET /api/v1/workspaces/{workspaceId}/postgres-releases`. A database on a retired release shows **Stage update to PostgreSQL 18.7** in its properties; deploy the staged revision after reviewing its plan. A database is never moved without that review.

Three rules decide what data a release may use:

- **Restore:** a logical backup restores into its own major, in a release at least as new as the one that dumped it. The restore runs in the database's own release, which then serves the data, not in the archive's. So a backup taken on 18.4 restores into a database now on 18.7. A backup from another major is refused before anything runs, with the reason: restore it into a database of its own major, or use the major upgrade. Each run records the release that filled its target. Runs from before that record were filled by the archive's own image.
- **In place:** a later release replaces an earlier one on the same volume only within one major and one Alpine line. A line keeps its C library and ICU build, so indexes sort the same; every release above uses ICU 76.1. A release never moves back to an earlier one. The plan review names the update. Moving to another line needs a restore into new storage, which rebuilds every index.
- **Rebuilt indexes:** some releases say to rebuild indexes after updating, because their fix does not repair entries already written. The agent records, per volume, the release whose indexes the data holds: the release that created new storage or filled a restore target, or the one an applied plan ran on it. After a deployment, or a restore cutover onto a later release, leaves the database healthy on a later release, a `postgres-reindex` step rebuilds those indexes with `REINDEX INDEX CONCURRENTLY` in every database, so the database keeps serving. The advice follows PostgreSQL's releases, not the pins, so skipping a release still rebuilds what it named. Storage with no record, such as a restore target an earlier agent filled, is assumed to hold the oldest release's indexes. The step:
  - resolves names only in `pg_catalog`, so a function a database owner adds can never run with the agent's privileges;
  - gives up a lock wait after 10 seconds and a single rebuild after 30 minutes, because a concurrent rebuild waits for every older transaction, and cancels its own sessions if a database takes more than an hour in all;
  - first drops the invalid `_ccnew` and `_ccold` indexes an interrupted rebuild leaves, and leaves other invalid indexes and other sessions' temporary tables alone;
  - names, rather than rebuilds, an index that backs an exclusion constraint, which cannot be rebuilt while writes continue: run `REINDEX INDEX` on it when writes can pause;
  - never fails or undoes the deployment. If it does not finish, the operation's note says so, the record keeps the earlier release, and the next deployment, or this one resumed after an agent restart, tries again.

  Advice for indexes only suspected of damage, such as GIN after a VACUUM race, is left to the owner.

| Release | Indexes rebuilt after updating to it |
| --- | --- |
| 17.11, 18.6 | `btree_gist` GiST indexes on `float4` and `float8` (and in 18, `bit` and `varbit`); btree indexes on `ltree`, including domains and arrays of it |
| 17.12, 18.7 | btree indexes on unbounded `bpchar` (not `CHAR(n)`), including domains and arrays of it; `pg_trgm` GiST indexes using `gist_trgm_ops` |

### Adding a release

1. Pick the new tag in the same Alpine line, digest-pinned and older than the 7-day release-age cooldown. The `postgres` track in [`.github/pinned-releases.json`](../.github/pinned-releases.json) keeps the pin report to that line. Moving to another line, such as Alpine 3.24 with ICU 78.1, changes how text sorts, so it needs its own recipe rather than a new row.
2. Add a row to the table and move `PostgresImage` or `Postgres17Image` to it. Never remove a row: give the old image its literal digest instead.
3. Read the release notes of every release since the last pin. Add any post-update reindex advice to `postgresReindex`, and check for changes to dump output, the archive format or the data layout.
4. List the old digest under `retired` in [`.github/pinned-releases.json`](../.github/pinned-releases.json), so [the pin report](continuous-integration.md#pinned-images-and-tools) stops offering it newer releases.
5. Keep both digests in the native CI pull list, and run `TestNativePostgresReleases`, which restores a backup from each retired release and updates one in place.
