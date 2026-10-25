# Astilba and OpenShip: disposable acceptance comparison

Run date: 25 October 2026. This is one API-driven acceptance run on native arm64, not a performance benchmark or a general assessment of either product. It does not evaluate the dashboard's usability, public certificate issuance, amd64, off-server disaster recovery or Kubernetes.

The practical finding is to keep P11 next: protect healthy traffic from an unhealthy candidate and make recovery a clear product action. The PostgreSQL recovery checks also demonstrate why application-image updates and stateful upgrades need separate safety rules.

## Inputs and isolation

| Input | Tested value |
| --- | --- |
| Astilba management and agent | `fc0b89fb77c47d19d226d4de76e86f9c1897223a`, immediately after GitHub App onboarding |
| OpenShip | Published `v0.9.5`, official CLI in Compose mode; API, dashboard and edge images tagged `0.9.5` |
| Shared application source | `6082001e26eb7a97e17c8cfe0eda85e542e51cf6`, `experiments/platform-comparison/app` |
| Hosts | Two fresh Ubuntu 24.04 VMs; aarch64; four vCPUs, 8 GiB configured RAM, 40 GiB disk each; about 7,923 MiB reported usable RAM |
| Host tools | Docker 29.9.2 and Compose 5.6.1, checksum-verified; Astilba additionally uses restic 0.19.1 |
| Workload database | The same PostgreSQL 17.10 image digest; Astilba's supported migration targets the pinned PostgreSQL 18.4 image |
| Exposure | No public DNS or public deployment; explicit loopback SSH tunnels; local HTTP workload checks; a private test CA for Astilba's management HTTPS |

Each controller shares its own fresh host with its workload. OpenShip's installation also runs a control-plane PostgreSQL 16 database and Redis 7; those are separate from the workload database. Astilba uses a separate management database and signed deployment agent. These are product installation differences, not equal-overhead deployment arrangements.

The guests have no host-directory mounts, no SSH-agent forwarding and no automatic application-port forwarding. A private random ownership marker identifies each host. The live alpha, its accounts, DNS and backup storage are outside this run. Raw logs, credentials, resource IDs and database archives remain in ignored local artifacts.

OpenShip's [release notes](https://github.com/oblien/openship/releases/tag/v0.9.5) distinguish the corrected release metadata from the original built-artifact source. This comparison identifies the actual published images rather than assuming a checkout of its later default branch is equivalent.

## Observed outcomes

| Scenario | Astilba | OpenShip 0.9.5 |
| --- | --- | --- |
| Initial deployment | Git build of the pinned fixture commit succeeded; explicit artifact promotion, plan review and deployment succeeded | Public Git deployment required a GitHub credential. Its supported upload → scan → ensure → build workflow then built the same application files and deployed the PostgreSQL/web stack |
| Baseline data | 100 seeded notes and 100 linked records; HTTP reads succeeded | Same counts and canonical data hashes; HTTP reads succeeded |
| Explicit failing Dockerfile build | Authenticated rerun failed at the intended Dockerfile instruction with exit 1 and produced no artifact; the applied revision, healthy HTTP and all 101 current records remained intact | `FAIL_BUILD=true` failed at the intended Dockerfile instruction; deployment reported failed and no service was activated |
| Unready application, initial Compose traffic path | Reported `needs_attention`; retained the unhealthy replacement; no automatic old-image restart. Inspection, cancellation, local agent reconciliation and a reviewed healthy deployment recovered service | Enabled checks defaulted to warning policy and left the unready revision live. With per-service `onFailure: fail`, reported partial failure; explicit **reject** restored the previous healthy revision |
| Backup | Encrypted logical backup uploaded; explicitly unverified until a fresh restore passed | `auto` selected a custom-format `pg_dump` payload, not a live-data-directory archive; local-destination preflight and backup succeeded |
| Restore recovery point | Fresh target verified; original source stayed active until a separate cutover plan was applied | Database fork restore was rejected as mail-only. Explicitly prepared and confirmed in-place restore succeeded |
| Restored data | Original 100 rows; post-backup sentinel absent; identity sequence, schema, `hstore`, ownership, role and both reader grants matched baseline | Original 100 rows; post-backup sentinel absent; identity sequence, schema, `hstore` and ownership matched. The reader role remained, but its two explicit SELECT grants were absent |
| PostgreSQL 17→18 | Final backup, fresh restore, verification and explicit cutover succeeded. New write received ID 101; returning to the old volume was refused | The documented PG18 backup-import request returned `CLUSTER_TARGET_REQUIRED`: choose a ready server cluster. No Docker-only major migration was demonstrated; the separate cluster path was not installed or tested |
| Checkpoint restart | Management and agent restarted after migration verification and before cutover; same snapshot, target and ready state survived | API container restarted after a second restore reached prepared; the same prepared record survived. The app accepted a write while management was stopped; the unapplied restore was then cancelled |

The initial OpenShip Compose variant published a fixed loopback app port; Astilba used its managed Traefik route. That difference matters for overlapping releases, so a second OpenShip variant used its managed OpenResty proxy, a hostname route and an automatically allocated app port. Healthy traffic was established first. The explicitly vetoed unhealthy revision was observable through that proxy too; the deployment became a partial failure and explicit reject eventually restored healthy HTTP responses. An immediate probe after the reject response briefly returned 502, so API acknowledgement alone was not treated as completed traffic recovery.

The reserved `.test` hostname cannot receive a public certificate. OpenShip attempted ACME and reported the certificate problem alongside the service failure; this is an expected hosting-fixture limitation, not evidence of a certificate defect. The result covers HTTP Compose deployments, not OpenShip's separate single-application, cluster or alternative routing modes.

## What the data checks establish

Both baselines produced notes hash `035b55ca2061e87c3f104b7e6f69d436` and links hash `d2a2506c18d34153e1b3c66622fd64fb`. These hashes compare the canonical synthetic corpus; they are not security hashes for backup artifacts.

The corpus includes identity generation, a unique key, a foreign key, a numeric check, NOT NULL columns, `hstore` and an unprivileged NOLOGIN reader with SELECT on both tables. The fixture distinguishes duplicate writes (HTTP 409) from unavailable PostgreSQL (HTTP 503); an error response alone is not proof that an uncertain write could not commit. After each backup, an authenticated application request inserted a sentinel. Both restores excluded that later write and restored sequence value 100. Astilba's same-version verification output matched its baseline exactly. OpenShip's output differed in the two missing reader grants; an ordinary application read succeeding would not detect that loss. Its released [database round-trip fixture](https://github.com/oblien/openship/blob/v0.9.5/apps/api/test/e2e/backup-db-roundtrip.e2e.test.ts) describes intentionally restoring without ACLs, consistent with the observation.

Astilba's retained PostgreSQL 17 source was subsequently inspected in an isolated, networkless temporary container and still contained the original corpus. PostgreSQL 18 preserved the data hashes, extensions, owners, grants, identity and column types/nullability. Its `pg_constraint` output includes additional catalog entries for existing NOT NULL specifications, an [intentional PostgreSQL 18 change](https://www.postgresql.org/docs/18/release-18.html); separate `pg_attribute` checks matched the old source's column semantics. This catalog difference was not treated as lost constraints or hidden by ignoring arbitrary SQL differences.

The new-write boundary was tested through the application after PG18 cutover, then through Astilba's refused return request. It does not demonstrate online migration, coordination with unknown external writers, or recovery from every mid-effect interruption. The restart was at a completed verification checkpoint.

Both destinations were on the same host as their workloads. Neither result demonstrates survival of host loss. No generic extension, role topology, arbitrary imported database or large dataset is covered.

## Reproduction procedure

1. Create two fresh owned native arm64 Ubuntu 24.04 hosts with the resources above. Disable host mounts, agent forwarding and automatic forwarding. Keep the ownership marker and all credentials private; verify the marker before destructive actions.
2. Install the exact host tools. Build Astilba's stated revision and a matching signed agent catalog; follow [portable installation](../../docs/hosting.md#portable-installation), [agent installation](../../docs/agent.md) and [backup prerequisites](../../docs/backups.md). Install Compose's plugin in an accessible system directory when using the hardened service; do not rely on a plugin hidden under a protected home directory.
3. For OpenShip, inspect the official CLI and generated Compose configuration, verify its release archive digest, and run `openship up --compose --image-version 0.9.5`. Supply a disposable owner through the documented headless options and `OPENSHIP_ADMIN_PASSWORD`, not a password argument. Set the exact loopback dashboard origin. Record the resolved image digests below. The installer's host-channel check reported that `bun` was unavailable in the API image; actual deployment commands still worked, so that diagnostic was not scored as a failed deployment.
4. Freeze the fixture source at the stated commit. Use `app` as the Docker context, `Dockerfile`, and the same pinned PostgreSQL 17 image. Configure `DATABASE_URL`, a private random WRITE_TOKEN and APP_REVISION. Astilba uses a managed PostgreSQL node and binding; OpenShip uses its supported multi-service Compose configuration. Uploading the identical files is an explicit source-transport alternative, not proof of anonymous Git onboarding.
5. Apply `seed.sql` once through an owned administrative database connection. Run `verify.sql`, check all 100 notes through HTTP, and start a separate bounded sampler of `/` and `/ready` at 250 ms intervals with one-second request timeouts. Do not infer serving health from a deployment label alone.
6. Configure and preflight private filesystem destinations. In OpenShip, enable `BACKUP_ALLOW_LOCAL_DESTINATION=true`, set an owned `BACKUP_LOCAL_ROOT`, and persist that directory in its API-container configuration. Capture PostgreSQL through `auto`, inspect that the payload is `pg_dump`, and await completion. In Astilba, install the logical-backup policy, then request a backup and await completion.
7. Insert `after-backup` through the application. Restore through each product's workflow, observing preparation, confirmation and any separate cutover. Rerun SQL and HTTP checks; verify sentinel absence, sequence and grants. Record in-place overwrite separately from a source-preserving fresh restore.
8. Submit `FAIL_BUILD=true`. For readiness, retain a known healthy baseline and submit a distinct APP_REVISION with `FAIL_READINESS=true`. In OpenShip explicitly set the service readiness path `/ready`, port 8080, timeout 30 seconds and `onFailure: fail`; merely enabling a probe requests warning behavior by default. Observe actual HTTP responses before and after the operator's recovery decision. Keep fixed-port and managed-ingress results separate.
9. Refresh Astilba's frozen backup policy after restore/deployment changes. Prepare its supported 17→18 upgrade, inspect the ready checkpoint, restart management/agent, verify unchanged recovery identities, then explicitly cut over. Rerun SQL checks, insert a new application record and verify old-volume return is refused. Inspect retained source storage only after checking ownership and that no live container uses it.
10. On the Docker-only OpenShip host, inspect cluster readiness before requesting its documented PG18 backup import. Record the missing-cluster response as an unmet topology prerequisite. Do not substitute an image-tag swap for a database migration or classify the untested cluster workflow as broken.
11. For OpenShip's restart check, prepare a second restore, await prepared, stop the API container, write through the still-running app, restart the API and inspect the same restore record. Cancel without applying; do not overwrite the new write as part of an availability check.
12. Stop owned samplers and tunnels, revoke temporary credentials, and delete only the exact owned guests after verifying host and guest markers. Preserve sanitized results separately; never run Docker prune against an existing daemon.

These procedures exercise real product APIs, Docker workloads and PostgreSQL. SQL/Docker commands seed or independently inspect fixtures; they do not stand in for a missing deployment, restore or migration workflow. The API-driven setup time and heterogeneous image/source caches are not comparable onboarding or cold/warm performance measurements.

For traceability, OpenShip recorded 18.345 seconds for its first successful build and 0.779 seconds for a later warm managed-ingress build. Astilba's first successful build was observed within 52.948 seconds of admission, an upper bound from polling rather than a build-stage timer. Its later authenticated successful build took 18.290 seconds from admission to observed completion with no compatible project cache imported; repeating the identical inputs took 7.014 seconds and explicitly reported a compatible project layer-cache import. These different measurements and cache states do not support a speed ranking. Deliberate readiness failures and migration maintenance produced non-healthy HTTP samples; their durations include operator review/wait time and are not automatic recovery-time benchmarks.

An anonymous GitHub API quota exhausted during the run. Astilba rejected the affected admission with a `branch` validation error before compilation; a one-day token limited to the public source repository with contents/metadata read access allowed the intended test to proceed. The failing build's captured output confirms the deliberate `FAIL_BUILD=true` instruction, not a memory-limit inference from its generic failure summary. No failed or repeat-build artifact was promoted.

## Published image provenance

The observed arm64 manifests below make the default installation's mutable supporting tags reproducible. They do not imply that OpenShip's installer pins those supporting tags itself.

| Component | Manifest digest |
| --- | --- |
| OpenShip API 0.9.5 | `sha256:d5e12d2952146959fb83aa4b26e11f417b0592017237e12528857398cc34d30a` |
| OpenShip dashboard 0.9.5 | `sha256:718e17b003d760d12b504aa295b023b3ebe8b12c5e2ad5326dc40ebfc4a0ebbf` |
| OpenShip edge 0.9.5 | `sha256:1417b0a22d23f00b463b6dbf8bb703680433b778630178e66172f669c678d3af` |
| OpenShip management PostgreSQL 16 | `sha256:f4610795587492cd4fb887c2dc3de301b813278bb6f616992061141b5f15ac36` |
| OpenShip Redis 7 | `sha256:57e4255cc971b1c8d6f8a6ca283ac01bc3ecdd914273ef6bfaede87aaecd6541` |
| Workload PostgreSQL 17.10 | `sha256:8189a1f6e40904781fc9e2612687877791d21679866db58b1de996b31fc312e4` |
| PostgreSQL 18.4 | `sha256:996d0920e4ff9df1fc19dacb904492f3c1ec0ec1cc338f0ad7123be7731c5f5e` |

The OpenShip CLI release archive SHA-256 was `980d64ff90ef02ffd4c37f115c210a8474f1a81460cde1194e8824a92702fd2e`. Application Dockerfile bases are already pinned in the shared fixture.

## Cleanup and validation

Both applications answered readiness successfully and retained 101 current records immediately before disposal. The exact two comparison VMs were stopped and deleted after checking their external and guest ownership markers. Owned sampler and tunnel processes were stopped, and the unrelated VM inventory was retained. The temporary GitHub token was revoked, an authenticated request confirmed HTTP 401, and its local token file was removed. Private audit evidence remains outside Git.

The shared Go module passed vet, race-enabled compilation and module verification; it contains no unit tests. Both running applications returned 409 for a duplicate slug and 503 for a write while their owned database was stopped; after restart, all 101 current records remained and the failed write was absent. The required repository-wide `make verify` was attempted but stopped at the existing HTTPS timing check: a 24.989064-second poll gap against a 25-second threshold. No threshold or verification gate was changed, and that run is not claimed as a full pass.

## Effect on the implementation plan

P11 remains the next implementation milestone, with independently observed healthy traffic during failed candidates and recovery as acceptance criteria. Keep warning-only, skipped and verified checks distinguishable. Preserve P7's separate stateful migration boundary.

Running-workload advisory detection is now part of P18 and has a dedicated backlog item. Match observed runtime/component versions and immutable images against trusted, fresh advisories; show affected workloads, evidence and supported fixes in the dashboard and existing notification channels. Unknown inventory or stale feeds must remain unknown. Supported one-click fixes use reviewed release or database recipes. Automatic patching is optional and **off by default**, with owner-selected eligibility/windows and an audit trail; generic database major upgrades remain manual. The same capabilities belong in self-hosted and hosted installations.
