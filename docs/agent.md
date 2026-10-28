# Host agent

The agent runs on a deployment server or build runner. It uses outbound HTTPS; it exposes no listener. Ubuntu 24.04 LTS on amd64 and arm64 is the initial supported host. The probe recipe starts, inspects or removes a small BusyBox container. The visual recipe deploys reviewed image services, named volumes and ingress; see [visual deployments](visual-deployments.md). The pipeline recipe runs Git imports/submissions and bounded Dockerfile/Railpack builds; see [Git and builds](git-builds.md).

The dashboard’s [guided server onboarding](server-onboarding.md) adds a reviewable, signed bootstrap for fresh supported hosts. The manual steps below remain available for existing installations and separately managed runtimes.

## Prepare verified artifacts

The management operator supplies a signed release catalog and a public key through a trusted channel. Pin that key independently of the download endpoint. A compromised download endpoint must not be able to substitute the key.

Build from the reviewed source using `AGENT_VERSION=0.2.0 pnpm agent:build`. Generate a protected signing key with `go run ./cmd/agent release-keygen --key /secure/release.pem` from `services/platform`. Keep the private key outside the repository and management container. Sign each binary with:

```sh
astilba-agent release-sign --key /secure/release.pem --version 0.2.0 \
  --arch amd64 --artifact astilba-agent-linux-amd64 --out ./releases
```

Repeat for arm64. Configure management with `ASTILBA_AGENT_RELEASES` pointing to this read-only catalog and `ASTILBA_AGENT_RELEASE_KEY` pointing to the public PEM. Startup verifies every manifest and artifact. The catalog uses the SHA-256 as the filename; the dashboard only offers catalog entries. Both management deployments use this same catalog configuration. Mount it into the container if distributing your own builds.

The enrollment panel links each configured agent binary and its signed JSON manifest. Download the matching pair (`/agent-artifacts/SHA256` and `/agent-artifacts/SHA256/manifest.json`). Before running any downloaded binary, verify the manifest with the independently pinned public key. These commands need `jq`, OpenSSL and standard Ubuntu tools:

```sh
jq -r '"astilba-agent-v1\n\(.version)\n\(.protocol)\n\(.arch)\n\(.size)\n\(.sha256)"' release.json > release.message
jq -r .signature release.json | base64 -d > release.signature
openssl pkeyutl -verify -pubin -inkey release-public.pem \
  -rawin -in release.message -sigfile release.signature
# Check these values before selecting the artifact:
jq '{version, protocol, arch, size, sha256}' release.json
printf '%s  %s\n' "$(jq -r .sha256 release.json)" ./astilba-agent | sha256sum -c -
test "$(wc -c < ./astilba-agent | tr -d ' ')" = "$(jq -r .size release.json)"
```

The signed architecture must match `uname -m` (`x86_64` → `amd64`, `aarch64` → `arm64`) and protocol must be 1. Review the version and digest. The signature covers all five fields, including size and digest. Only then make the artifact executable. Keep the matching corresponding source archive and licence notices alongside distributed artifacts.

## Install and enroll

Inspect `services/platform/host/install.sh` and `astilba-agent.service`, then run:

```sh
sudo bash install.sh ./astilba-agent ./release-public.pem ./astilba-agent.service
```

The installer prepares files and prints the read-only preflight. It does not install Docker, change ingress, adopt existing workloads or start the service. A deployment target requires a working local Docker socket and at least 256 MiB available disk. Visual deployment additionally requires Docker Compose 5.6.1. Review existing containers and occupied ingress ports; only owner-approved visual plans may publish the owned proxy. Rootful Docker access gives the agent authority over the host, so use only trusted workloads.

Create a server in the dashboard with its deployment, build or combined role. Paste the one-use, ten-minute code into stdin; never put it in process arguments or shell history:

```sh
sudo astilba-agent enroll --management https://platform.example.com \
  --server SERVER_UUID --release-key /etc/astilba/release-public.pem
# Paste the enrollment code, then press Ctrl-D.
sudo systemctl enable --now astilba-agent
```

Enrollment and run use the same protected `/var/lib/astilba-agent` directory. It holds a bbolt journal and a separate encryption key, both required for recovery. Only one process may open the directory. Preserve it during upgrades; do not clone it to a different machine. A lost enrollment response can be retried with the same directory and code. A new code preserves the original machine binding and allows that host to reconnect after revocation.

Deployment/build roles are fixed at server creation. Build-only agents cannot receive test deployment commands or runtime secrets. The current platform does not issue build commands. Create a new server record for a different role; disconnect the old identity explicitly.

## Observe and recover

The dashboard shows connectivity, the last reported preflight and operation state separately. Heartbeats run every 15 seconds, polls last at most 25 seconds and 45 seconds without contact is offline. Configure reverse proxies for at least a 30-second upstream-header timeout and a 35-second response timeout. Do not cache agent responses. Browser closure or a lost poll never cancels accepted work.

Start a reviewed probe from the Servers page, inspect it, then review a separate remove operation. The probe has the `com.astilba.server`, `resource`, `environment` and `kind` labels. Removal checks all four, then targets the inspected container ID; it never deletes a volume or network. A probe keeps running while management or the agent is stopped. Cancellation retains any created probe at a safe checkpoint; remove it through a new reviewed plan.

A `needs_attention` result blocks conflicting work. Inspect the named resource and Docker state before retrying the same command:

```sh
sudo systemctl stop astilba-agent
sudo astilba-agent retry --operation OPERATION_UUID
sudo systemctl start astilba-agent
```

The journal retains one unresolved command and up to 128 acknowledged outcomes, with generation fencing after eviction. Probe records are limited to 32 KiB; visual/pipeline records and the host registry to 512 KiB. Reports retain at most 16 steps, 1 KiB of diagnostic text and a bounded 24 KiB observation. Agent report bodies are limited to 192 KiB for bounded source results; other agent requests remain limited to 16 KiB; visual command responses have a 256 KiB bound. Visual logs are bounded and known environment values are redacted. Slow or disconnected management does not block local execution. Revocation rejects subsequent agent requests but does not stop containers or undo accepted effects. Re-enroll the same state directory to report and reconcile outstanding work.

## Explicit updates

An owner with recent authentication selects a catalog release on the Servers page. Updates are serialized with all other host mutations. The agent verifies protocol, architecture, Ed25519 signature, size, hash and the binary's compiled identity, then fsyncs and atomically replaces only its own executable. Systemd restarts it; the durable operation reports on reconnect. `/usr/local/bin/astilba-agent.previous` retains the previous binary. Workloads stay running. No automatic update policy is enabled.

For a disconnected host, stop the service, inspect its operation state, download and verify the same signed artifacts, then use:

```sh
sudo astilba-agent update-local --manifest release.json --artifact ./astilba-agent
sudo systemctl start astilba-agent
```

Local updates refuse unresolved workload operations. If an update was interrupted, restart first to reconcile the same update, or inspect and retry it locally. If necessary, verify and restore the previous binary with the service stopped; retain the journal and its key. An unavailable artifact, invalid signature or incompatible binary yields `needs_attention`, never a workload restart. Remove retained temporary files only after confirming their ownership and that no updater is active.

Executable rollback does not imply persisted-format compatibility. After a host has recipe-3 replica routes, preceding agents refuse host route publication, including changes for unrelated units. Keep workloads, journal and key intact and restore a replica-capable agent to resume those operations. Scaling back to one instance does not remove this fence. See [application scaling](application-scaling.md#compatibility-export-and-recovery).

## Certificate contact

For managed HTTP TLS, the host operator sets `--acme-email operator@example.com` on the agent's `run` command (for systemd, use an inspected `ExecStart` override retaining the existing arguments). An owner must then prepare a plan with host changes approved. The agent persists that reviewed contact in the host route registry. Changing the flag alone does not change running ingress. Public DNS must point at this host and TCP ports 80/443 must be reachable for HTTP-01 validation. The agent reports an unready or untrusted certificate as a deployment failure and restores the previous route registry.

## Backup support

Backup recipes, operator filesystem roots, the pinned restic prerequisite, daily/cron/interval schedules, bounded job timeouts and local interruption recovery are documented in [Backups and restoration](backups.md). Use those instructions before enabling backup policies on an enrolled server.

## Optional cross-host networking

Install a signed cross-host-capable agent before preparing private links. In
Servers, expand **Prepare a deployment server** and download the rendered
`astilba-network-prepare.sh` script from `/onboarding/network-prepare.sh`. Inspect
that downloaded script, run `sudo bash astilba-network-prepare.sh --check`, then
apply it with `sudo bash astilba-network-prepare.sh --apply` on a supported Ubuntu
24.04 host. The repository's `network-tools.sh` is a source template, so use the
rendered download. The preparation installs pinned WireGuard/nft tools,
an `astilba-network` boot-restore service and an agent systemd drop-in allowing
AF_NETLINK. It preserves the other service restrictions and refuses to overwrite
an existing network installation. Restart the agent at an idle point after
preparation; the dashboard still requires separate owner approval of membership,
UDP endpoints, capacity budgets and application grants.

Allow the selected UDP listener through the server's upstream firewall. Servers
need direct UDP reachability; this release has no rendezvous or traffic relay.
Private keys stay in the protected host state directory. Approved private links
survive management outages and are restored on host boot; revocation remains
pending until the affected peers acknowledge it.

Recipe-4 plans require the negotiated `crossHost` capability. A coordinated local
reservation blocks ordinary mutations, updates, local schedules that conflict
with maintenance, and ordinary controller transfer until its exact operation is
completed or reconciled. Inspect it with `astilba-agent inspect-cross-host` while
the service is stopped. Keep the journal and keys intact; no elapsed timeout
releases this reservation. See [cross-host placement and management
recovery](cross-host-placement.md) before transferring an interrupted group.

## Protected application releases

Agents advertise `safeReleases` only after a controller response includes `Astilba-Agent-Safe-Releases: v1`. Instance-bearing visual plans require that capability at preparation, apply and dispatch; older payloads retain their original names and hashes. [Application releases](application-releases.md) documents eligibility, command uncertainty, cancellation, retention and recovery. The encrypted `releases` journal bucket records switch and drain state alongside the active `units` mapping; existing observations and metrics keep one logical resource per application.

Named output references use the separately negotiated `serviceOutputs` capability (`Astilba-Agent-Service-Outputs: v1`). Update the agent before deploying a specification with custom outputs or named bindings. Existing PostgreSQL bindings remain compatible; see [service connections and catalog](service-catalog.md).

Preview-capable agents advertise `previews` after feature negotiation, persist the immutable server reservation and reject commands crossing that boundary. Preview builds use `build-preview` and visual plans use recipe 2, which older agents reject. Cleanup receipts survive controller transfer until acknowledged or consumed by authenticated recovery enrollment. See [Pull-request previews](pull-request-previews.md).
