# PLAT-5: build artifacts and PostgreSQL restore

This disposable P0 experiment builds Dockerfile fixtures locally and through a separate BuildKit service, transfers an image through an authenticated OCI registry, and restores a PostgreSQL database into a new volume. It validates the proposed tools and failure boundaries before the production application is scaffolded.

## Boundaries and evidence

| Check | Required observation |
| --- | --- |
| Local and remote build | Two BuildKit services have independent state. The remote service requires a client certificate. The deployment Docker image store does not contain the remote image reference before its registry pull. |
| Artifact identity | Build metadata and registry manifest agree on the digest; the image declares the requested architecture. Deploy by that digest, move the tag, and keep serving the original contents. |
| Registry authority | A builder can read/create/update only the fixture repository namespace. A deployer can read it but cannot initiate an upload; unauthenticated and unrelated-repository reads fail. TLS trust and client identity are checked. |
| Build secrets | Only a declared BuildKit secret reaches the build. Synthetic runtime credentials stay outside its context and command environment. Build output, exported layers and image configuration do not contain the synthetic secrets. |
| Build failures | A failing Dockerfile, an upload cut after a byte barrier and real ENOSPC in capped BuildKit state cannot replace the running application or its database. An interrupted upload exposes no completed manifest and succeeds on retry. |
| Backup completeness | Native custom-format `pg_dump` plus selected role metadata form a checksummed bundle. A failed dump, truncated copy or full temporary backup filesystem cannot publish a complete bundle. |
| Restore | Restore into a distinct container and volume using matching PostgreSQL tools. Verify roles, ownership, grants, hstore, constraints, record hashes and the identity sequence. The source rejects in-place restore and retains its original contents. |
| Restore failure | A conflicting target schema makes `pg_restore --exit-on-error --single-transaction` fail; archive changes roll back. The source remains untouched. |
| Railpack | `prepare` detects a no-Dockerfile shell app. Freeze its generated base references, build through the matching versioned frontend, and run the image. |

The local and remote BuildKit services run as separate containers in one disposable Ubuntu VM. Their filesystems, build state and connection paths are distinct; they have no deployment Docker socket or runtime credential mounts. This establishes remote API execution and registry transfer across independent stores. It does not establish VM isolation, hostile multi-tenant build safety or physical multi-host provisioning. Builds remain privileged trusted inputs within the documented host boundary.

The zot registry backend listens only on VM loopback. An ephemeral TLS gateway on the owned Docker bridge permits the builders and deployment daemon to reach it and injects the upload interruption. The gateway does not implement registry authorization: zot enforces the repository ACLs. Temporary registry trust and credentials are removed at cleanup; an existing host trust entry is never replaced.

## Reproduce

Use an entirely disposable native Ubuntu 24.04 host with systemd and rootful Docker. For local Apple Silicon testing, the existing pinned Lima image can be used with 8 GiB RAM:

```sh
limactl start --name=astilba-plat5 --memory=8 experiments/durable-execution/host/ubuntu-arm64.yaml
limactl shell astilba-plat5 sudo apt-get update
limactl shell astilba-plat5 sudo apt-get install -y gcc libc6-dev
```

Copy the repository into the VM without mounting the host checkout. Inside that copy, run the [host bootstrap and three image pulls](HOST.md#reproduce-on-a-disposable-host), then:

```sh
sudo env PLAT4_DISPOSABLE_HOST=1 bash experiments/durable-execution/host/build-tools.sh
sudo /opt/astilba-plat4/bin/docker --host unix:///run/astilba-plat4/docker.sock pull moby/buildkit:v0.34.1@sha256:99368f3e46177c6405b9740e4461d91cee004128daeecca78d4ceac50ec1da5c
make verify-build PLAT4_ARCH=arm64
```

Use `PLAT4_ARCH=amd64` for a native amd64 machine. CI runs this command after the existing host checks on both native architectures. `make verify` continues to run the durability and HTTPS regression suite, formatting, module verification and vet; it explicitly skips the opt-in native fixtures outside their disposable setup.

All containers, networks and volumes use the existing run/project ownership checks. Build workers use bounded tmpfs state and memory limits; the backup ENOSPC test uses a private 64 KiB tmpfs. Cleanup never uses Docker prune or targets existing user data. The experiment uses generated credentials, not production services or credentials.

## Toolchain and limits

The host baseline is Go 1.27.3, Docker 29.9.2, Compose 5.6.1 and PostgreSQL 18.4 on Alpine 3.23. `host/build-tools.sh` verifies official release SHA-256 digests for BuildKit/buildctl 0.34.1, zot 2.1.22 and Railpack 0.43.0. The BuildKit image, Railpack 0.43.0 frontend and its `mise-2026.10.14` builder/runtime images are pinned to multi-platform manifest digests. The bcrypt fixture helper uses `golang.org/x/crypto` 0.58.0; its required `x/sync`, `x/sys` and `x/text` updates are recorded in the module lockfiles.

The Dockerfile workers use 192 MiB of state each, with a separate 32 MiB failure worker. Railpack's default shell-provider images exceeded a 1 GiB state budget; the same build succeeded with a 3 GiB state cap. This is a measured fixture requirement, not a production sizing guarantee. The shell smoke test does not establish Node/Go/framework detection or language dependency reproducibility. Generated Railpack image references are frozen before execution; runtime timestamps mean this experiment does not promise byte-identical rebuilds.

The database recipe covers the pinned PostgreSQL 18 layout: mount the volume at `/var/lib/postgresql`, with `PGDATA` at `/var/lib/postgresql/18/docker`. It restores selected unprivileged NOLOGIN roles and direct grants, one database and hstore. Login passwords, general role hierarchies, other extensions, tablespaces, encrypted remote storage, retention, major upgrades and binding cutover need their later recipes and tests. Custom-format dumps use compression level zero so the bounded disk failure has a predictable size. Logical consistency is provided by `pg_dump`; this is not an application-wide snapshot.

BuildKit and zot use Apache-2.0, Railpack uses MIT, and Go's `x/crypto` uses BSD-3-Clause. This fixture downloads tools and images without redistributing them. Any future bundled distribution must retain the relevant source and image notices. [BuildKit licence](https://github.com/moby/buildkit/blob/v0.34.1/LICENSE), [zot licence](https://github.com/project-zot/zot/blob/v2.1.22/LICENSE), [Railpack licence](https://github.com/railwayapp/railpack/blob/v0.43.0/LICENSE).

## Results

On 22 October 2026, the reviewed native arm64 fixture passed on Ubuntu 24.04.5 with kernel 6.8.0-142-generic: Dockerfile/registry/Railpack checks (32.51 seconds), PostgreSQL restore checks (4.23 seconds), and the existing host-routing/export fixture. The full local durability/HTTPS regression suite passed in 230 seconds. Image inventory after the fixed cleanup contained no artifacts from the new run. Native CI repeats the fixture on amd64 and arm64 before merge.

## References

- [Docker BuildKit secrets](https://docs.docker.com/build/building/secrets/): ordinary build arguments and environment variables persist in images; secret mounts have an explicit lifetime.
- [zot authentication and authorization](https://zotregistry.dev/v2.1.10/articles/authn-authz/): repository policies distinguish read from create/update authority.
- [PostgreSQL logical dumps](https://www.postgresql.org/docs/18/backup-dump.html): database dumps need associated roles, and restore errors must stop or roll back the operation.
- [Railpack platform integration](https://railpack.com/platforms/running-railpack-in-production): prepare structured plans and use a matching BuildKit frontend for platform builds.

## Review

The High-risk implementation review used three native same-model finders (general, runtime/data/concurrency and security/dependencies/permissions). All three independent verifiers confirmed two findings: image cleanup ran before container cleanup, and the first HTTP probe could race listener startup. Both were fixed; all finders reran with no findings. The same pass made the negative TLS probe independent of ambient proxy/trust settings. Read-only review relied on instructions; runtime checks were run separately.
