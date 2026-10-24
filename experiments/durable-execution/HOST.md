# PLAT-4: container, ingress and standalone export

This disposable experiment exercises the Go Docker SDK, the Compose plugin and Traefik on an actual Ubuntu 24.04 host with systemd and rootful Docker. It is a feasibility fixture, not the production agent or an installer for deployment servers.

## Reproduce on a disposable host

Use a fresh Ubuntu 24.04 VM with native amd64 or arm64 execution, systemd as PID 1, `curl`, CA certificates, `gcc` and libc development headers. The latter two enable Go's race detector. The bootstrap refuses an existing fixture installation and an active default Docker daemon with containers, volumes or custom networks. It can stop an empty preinstalled Docker daemon. Run it only on a disposable machine.

For a local Apple Silicon run, `host/ubuntu-arm64.yaml` pins a Lima 2.2.1 Ubuntu image, disables host mounts and disables port forwarding. Create it from the repository root:

```sh
limactl start --name=astilba-plat4-arm64 experiments/durable-execution/host/ubuntu-arm64.yaml
limactl shell astilba-plat4-arm64 sudo apt-get update
limactl shell astilba-plat4-arm64 sudo apt-get install -y gcc libc6-dev
```

Copy this repository into the VM rather than mounting the host checkout. Inside the VM, from that copy:

```sh
sudo env PLAT4_DISPOSABLE_HOST=1 bash experiments/durable-execution/host/bootstrap.sh

sudo /opt/astilba-plat4/bin/docker --host unix:///run/astilba-plat4/docker.sock pull postgres:18.4-alpine3.23@sha256:996d0920e4ff9df1fc19dacb904492f3c1ec0ec1cc338f0ad7123be7731c5f5e
sudo /opt/astilba-plat4/bin/docker --host unix:///run/astilba-plat4/docker.sock pull busybox:1.37.0@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e
sudo /opt/astilba-plat4/bin/docker --host unix:///run/astilba-plat4/docker.sock pull traefik:v3.7.16@sha256:22a759fb803abb5c3a7ba5d5a55a201c5fb0fc8993d5c72c47a89075e64f1953

make verify-host PLAT4_ARCH=arm64
```

Use `PLAT4_ARCH=amd64` on a native amd64 VM. The test rejects a mismatched binary/daemon architecture, non-Ubuntu hosts, rootless Docker and containers without systemd as PID 1. Cross-compilation and emulated containers do not establish a host result. CI runs the same test separately on GitHub's `ubuntu-24.04` and `ubuntu-24.04-arm` VMs. The required `Verify` job also requires the existing durability and transport checks to pass.

The dedicated `astilba-plat4-docker.service` owns its binaries, Unix socket and data directory. All test resources carry run/project labels; cleanup verifies ownership before deleting them. There is no Docker prune. Application and database ports are unpublished; fixture ingress binds only to loopback. Dispose of the VM after testing.

## Contracts exercised

| Boundary | Required observation |
| --- | --- |
| SDK and Compose | SDK project A and Compose project B run the same app/PostgreSQL shape. Each project resolves its own `app` alias; A cannot reach B's private IP. |
| Route ownership | Foreign backend references, duplicate HTTP hosts and duplicate TCP listener claims fail before changing the route file. Generated backend names are project-specific. |
| Publication | One host flock spans registry read, claim validation, publication, all runtime probes, approval and rollback. A revision-specific probe distinguishes new activation from a previously working graph. |
| Nontransactional reload | Malformed YAML and a missing backend are rejected. The semantic fault actually leaves A unavailable while B answers; restoring the previous graph returns A. No zero-outage claim is made. |
| Failed writes and approval | Pre-publication failure, failed approval and a failure after writing approval preserve the previous approved registry and recover its routes. |
| Failed recovery | A failed restoration requires attention. A new publisher reads the disk snapshot and recovers; it also recovers after a proxy restart with an unapproved file on disk. |
| Concurrent publishers | A second publisher waits while the first verifies and rolls back a failed update. Its later successful update preserves the other project's route. |
| Standalone export | A bundle contains pinned images, relative routing configuration, a secret placeholder and an external-volume prerequisite. After closing the SDK and deleting the original bundle directory, ordinary Compose starts/stops/restarts the relocated bundle and reads the original PostgreSQL row. |
| Unrelated resources | Project B's container identity, volume and HTTP route, plus an unmanaged sentinel's container and volume data, survive the route failures and export. |

Compose command strings escape container-side variables with `$$`; otherwise Compose expands them before container startup. The renderer omits empty protocol sections because this Traefik version rejects empty router/service maps. The fixture also rediscovers automatically allocated loopback ports after proxy restart; production listener ports remain explicitly reserved.

`host_routes.go` is a small experiment-specific publisher. It does not implement the production advanced configuration schema, HTTPS/ACME, a general ingress controller or a production journal. `host_runtime_test.go` creates and exercises the disposable topology. Export proves standalone operation on the same host with an existing external volume; moving database data to another host requires a separate backup/restore experiment. A process can still die at a filesystem boundary; startup recovery reapplies the approved registry rather than claiming a cross-file transaction.

## Versions and results

Pinned runtime: Go 1.27.3, Docker Engine/CLI 29.9.2 (API 1.56), Compose 5.6.1, Traefik 3.7.16, PostgreSQL 18.4 on Alpine 3.23 and BusyBox 1.37.0. Bootstrap checks SHA-256 for the tool archives and binaries. Image references pin the multi-platform manifest digest. Docker archive hashes were calculated from official HTTPS downloads; Compose and Go hashes were checked against their official release metadata.

Local native arm64 validation passed on 22 October 2026: Ubuntu 24.04.5, kernel 6.8.0-142-generic, systemd 255.4-1ubuntu8.17, and the pinned runtime above. All six host scenarios passed under the race detector in 43 seconds. This run used the dedicated Lima VM, not a Docker container on macOS.

Both native CI host jobs passed on 22 October 2026 at revision `cc6e6d9`:

| Target | Ubuntu / kernel | Runner image | Host scenarios |
| --- | --- | --- | --- |
| amd64 | 24.04.5 / 7.0.0-1017-azure | `ubuntu-24.04`, 20261018.339.1 | 6 passed, 51.68 seconds |
| arm64 | 24.04.5 / 7.0.0-1017-azure | `ubuntu-24.04-arm`, 20261021.157.7 | 6 passed, 50.32 seconds |

The amd64 and arm64 CI jobs each install the pinned runtime from scratch and run every host scenario with the race detector. These passing combinations establish the P0 baseline; they do not establish a supported range of Docker, kernel or distribution versions. The existing durability/HTTPS suite also passes locally in 233 seconds, and the workflow reruns it alongside the host matrix on every PR.

Three independent native same-model reviewers covered general correctness/instructions, security/privacy/supply chain, and runtime/data integrity/concurrency/compatibility against the merge base `344b3a9`. All completed with no findings; no candidate required verifier adjudication. Read-only behavior relied on reviewer instructions.

Compose 5 is the maintained Go plugin successor to Compose v2 and preserves the `docker compose` command. This updates the plan's old version label without changing the Compose deployment model. [Docker Compose history](https://docs.docker.com/compose/intro/history/), [Compose 5.6.1 release](https://github.com/docker/compose/releases/tag/v5.6.1).

The fixture downloads tools and pulls images; it does not redistribute them. Docker Engine and Compose use Apache-2.0, Traefik uses MIT, PostgreSQL uses the PostgreSQL licence, and BusyBox uses GPL-2.0. Image distributions include additional packages and notices; any future bundled distribution needs its own notice/source obligations review. Existing Go module licences are recorded in the [durability fixture](README.md#versions-and-scope).
