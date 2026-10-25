# Disposable platform comparison

Compare Astilba and OpenShip through the same small HTTP application and PostgreSQL dataset before P11. The application is a test fixture, separate from production platform code. This directory does not install or modify either product.

The run uses Astilba `fc0b89fb77c47d19d226d4de76e86f9c1897223a` and published OpenShip `v0.9.5`, on separate fresh native arm64 Ubuntu 24.04 hosts with matched resources. The [dated acceptance results](RESULTS.md) record the tested source, image digests, installation modes, outcomes and limitations.

## Common application

Build with `app` as the Docker context and its pinned Dockerfile. Supply `DATABASE_URL`, a random `WRITE_TOKEN` of at least 32 bytes, and an `APP_REVISION` label. `/healthz` is liveness, `/ready` checks PostgreSQL connectivity, `/` identifies the serving revision, and `/notes` reads or writes the synthetic corpus. Writes require `Authorization: Bearer <WRITE_TOKEN>`. The fixture neither seeds nor repairs data on startup.

After creating the owned PostgreSQL database, apply `app/seed.sql` exactly once using a private connection, then collect `app/verify.sql` as the baseline. Keep database credentials out of shell arguments and logs. The fixed corpus has 100 notes, identity sequences, a unique key, a foreign key, a check constraint and `hstore`. Use PostgreSQL 17.10 and 18.4 image digests from [the supported upgrade matrix](../../docs/supported-upgrades.md) where the tested product supports that path.

Use build argument `FAIL_BUILD=true` to produce an explicit nonzero build. Set runtime `FAIL_READINESS=true` and a distinct revision to produce a running container whose HTTP readiness fails. Observe both outcomes through the platform and a separate HTTP sampler. These controls change only the comparison app.

## Required observations

| Scenario | Evidence |
| --- | --- |
| Successful deployment | Serving revision, persisted writes and SQL baseline; installation/manual steps and cold/warm timings |
| Build or readiness failure | Actual serving revision, errors/outage, retained data, reported state and recovery action |
| Backup and restore | Exact recovery point; a later sentinel is absent in the restored target; sequence, constraints, grants and data checks; source retention and cutover |
| PostgreSQL major upgrade | Supported version/topology prerequisites, preserved source, data/metadata verification, new writes and the point at which returning would lose them |
| Interruption | Owned operation boundary, restart behavior, ambiguity, duplicate effects and operator actions |

A documented manual recovery is recorded as manual. Direct Docker or SQL can seed and inspect this owned fixture, but cannot stand in for a missing product workflow. Label unsupported paths, infrastructure limitations and untested claims explicitly. A filesystem backup on the same host does not demonstrate off-server disaster recovery. Record any additional cluster or S3 fixture required by a product as a separate topology.

## Isolation and cleanup

Use newly created, uniquely named disposable VMs, no host mounts or SSH-agent forwarding, and explicit loopback tunnels. Keep a random run-ownership marker both outside and inside each guest. Verify the exact VM name and both markers before stopping or deleting it. Stop owned sampler/tunnel processes first. Never use Docker prune or wildcard cleanup, and never point this procedure at an existing deployment host.

Keep credentials, real resource identifiers, raw logs, backups and screenshots in ignored local artifacts. Publish only sanitized results and reproduction steps. Review installed external scripts and verify pinned artifacts before executing them inside a disposable guest.
