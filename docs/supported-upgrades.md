# Supported PostgreSQL major upgrades

The dashboard supports one logical migration: an Astilba-managed PostgreSQL 17.10 database to PostgreSQL 18.4. Creating a plan changes no running service. Starting it stops every service in the environment, uploads a final encrypted backup, restores that uploaded copy into fresh storage and verifies the result. Applications remain stopped until a separate cutover approval or an explicit return to the old database.

| Component | Supported input |
| --- | --- |
| Source | `postgres:17.10-alpine3.23@sha256:8189a1f6e40904781fc9e2612687877791d21679866db58b1de996b31fc312e4` |
| Target and migration client | `postgres:18.4-alpine3.23@sha256:996d0920e4ff9df1fc19dacb904492f3c1ec0ec1cc338f0ad7123be7731c5f5e` |
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

## Verification and cutover

The migration uses PostgreSQL 18 `pg_dump` with quoted identifiers, reads the uploaded restic snapshot back with verification, and recreates the database with `pg_restore --create --exit-on-error` inside an isolated fresh cluster. It creates the supported roles and compares complete restored SQL streams from the same pinned client, including database creation, schema, records, constraints, ownership, grants and sequence values. Only the known source-version line in the SQL preamble is normalized; database records are unchanged. PostgreSQL recommends using the newer dump tools for a major-version migration. [PostgreSQL upgrade documentation](https://www.postgresql.org/docs/18/upgrading.html), [pg_dump](https://www.postgresql.org/docs/18/app-pgdump.html).

Cutover records that new writes are possible **before** connecting the new database to the deployment network. This is conservative: external clients may connect before managed applications restart. The agent verifies ingress activation before resuming applications. A failed or interrupted cutover reports the last step and an unknown serving state until an explicit reconciliation confirms the new database and its routes. It never offers old-volume recovery as lossless after the write boundary.

Upgrade state is encrypted in the agent journal and summarized in operation history. An interrupted backup or restore pauses for inspection instead of rerunning an uncertain effect. Before new writes, choose **Return to old database**. After that boundary, choose **Reconcile new database and resume services**. All host operations and local schedules pause while an upgrade is active. After a successful cutover, refresh and reinstall backup policies for the new deployment; the old frozen schedules are disabled.

Choose a minimum retention period of 1–365 days. The platform protects the old and target volumes during migration and through that period after completion or return. Nothing is automatically deleted afterward. Upgrade snapshots are outside scheduled retention's catalog and remain until explicit operator cleanup. Keep the repository password and destination access independently of the management database.

## Manual completion

Use ordinary Docker, PostgreSQL and restic tools if management is unavailable. Stop the agent service before reading its exclusive journal, run `astilba-agent inspect-upgrades --state /var/lib/astilba-agent`, and restart the service after inspection. The command prints plans and status without execution credentials. Inspect this output and operation history first; identify the recorded source volume, target volume, snapshot and last step. Do not infer success from a stopped container or missing acknowledgement.

Before the new-write boundary, stop the labeled upgrade helpers and any target database, then start the original PostgreSQL 17 service with its original volume and configuration. Confirm database readiness and ingress before starting applications. Preserve both volumes and the recovery snapshot.

After the boundary, retain PostgreSQL 18 as authoritative. Inspect its volume, database readiness and the owned Traefik configuration, then reconcile routing and restart applications against that database. Returning to the old volume would discard new writes; recovering from a snapshot is a separate recovery decision. Do not erase the encrypted journal or switch controllers while recovery is outstanding. Once normal service is established, restore management and reconcile its recorded state before accepting new operations.

The native fixture exercises data, ownership, constraints and hstore, plus restart/lost-acknowledgement cases at migration and activation steps on disposable Linux hosts. Local checks are not evidence of an arbitrary imported database's compatibility. Additional application or database recipes need their own supported input matrix and fixtures.
