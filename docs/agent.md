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

The installer prepares files and prints the read-only preflight. It does not install Docker, change ingress, adopt existing workloads or start the service. A deployment target requires a working local Docker socket and at least 256 MiB available disk. Visual deployment additionally requires Docker Compose 5.6.1. Review existing containers and occupied ingress ports; only owner-approved visual plans may publish the owned proxy. Rootful Docker access gives the agent authority over the host, so use only trusted workloads. Builds also need nftables 1.0.9, which Ubuntu installs by default, because the agent fences each build off the host and private networks with an nftables policy; its systemd unit allows netlink sockets for that. The preflight says when a server can't build for this reason. If the host's metadata service answers, which it does on most cloud instances, the preflight also warns that deployed services and host processes can reach it.

Create a server in the dashboard with its deployment, build or combined role. Paste the one-use, ten-minute code into stdin; never put it in process arguments or shell history:

```sh
sudo astilba-agent enroll --management https://platform.example.com \
  --server SERVER_UUID --release-key /etc/astilba/release-public.pem
# Paste the enrollment code, then press Ctrl-D.
sudo systemctl enable --now astilba-agent
```

Enrollment and run use the same protected `/var/lib/astilba-agent` directory. It holds a bbolt journal and a separate encryption key, both required for recovery. Only one process may open the directory. Preserve it during upgrades; do not clone it to a different machine. A lost enrollment response can be retried with the same directory and code: the agent sends whatever it first sent with that code. A new code preserves the original machine binding and allows that host to reconnect after revocation.

### The agent's key

The agent authenticates with an ECDSA P-256 key it generates at enrollment. Management keeps only the public key. For each request the agent uses a 15-minute access token, which it gets by signing a one-time challenge and keeps only in memory. The key never leaves the host.

The agent renews its token with a signed refresh secret. Through an outage it keeps trying the same secret rather than starting a new session, which would end its terminal sessions. Management accepts a secret until an hour after its token expired: long enough for ordinary deploys and restarts, and short enough that a secret copied from the agent's memory and left unused stops working. After that, the agent starts a new session with a signed challenge.

How well the key is protected depends on the host, and the Servers page says which applies:

- **TPM:** the key is sealed with `systemd-creds` to the host's TPM, without binding it to firmware measurements, so firmware and Secure Boot updates don't lock the agent out. A copy of the disk can't use the key.
- **Host key:** without a TPM, the key is sealed with systemd's host key, `/var/lib/systemd/credential.secret`, which lives outside the state directory. A copy of the state directory alone can't use the key. **A copy of the whole disk can**, because it includes the host key.
- **Setup required:** the host has no systemd host key yet, and the agent service's sandbox can't create one. Run `sudo systemd-creds setup` once, then restart the agent service; it registers a key when it starts. `astilba-agent enroll` and `transfer` do this themselves.
- **Sealing failed:** `systemd-creds` is missing or couldn't read back what it sealed. The Servers page shows the reason the agent reports.

The agent registers a key only after it has sealed a value and read it back. Until then it keeps authenticating with its bearer credential. While its key store is unavailable, the agent opens it again every minute and keeps running local work, such as scheduled backups. An agent that already holds a key can't authenticate until its store opens, so the server shows as offline; `journalctl -u astilba-agent` shows why.

A copy of the state directory still holds the journal, which its keyring decrypts, and the host's private network keys. Treat a copied state directory as a leak of the host's secrets and network keys even when it can't authenticate.

The agent replaces its key every 30 days, or when an owner asks with **Rotate key now**, proving that it holds both the old and the new key. Rotation waits while a terminal session is open, because a new key ends it.

### Identity conflicts

Management looks for proof that a second copy of an agent is using the server's key:

- session starts whose counters don't rise in the order management issued their challenges;
- a key the agent replaced, used after its replacement;
- the credential a key replaced, used again from two minutes after the key's first use until a day after the agent replaced it. Outside that window, it is only refused;
- a consumed enrollment code presented with another key.

Management then raises an **identity conflict** alert and shows it on the Servers page. A running agent never produces these, whatever its retries, restarts or lost answers. **Restoring the state directory from a backup or snapshot does**, because the restored agent repeats what the live one already sent: re-key the host after a restore. Anything ambiguous, such as a late or duplicated request, is refused without raising one. A key that a new enrollment code replaced is refused and recorded once, without an alert. Nothing that someone who knows only the server's ID can send raises a conflict or stops the server.

Only a new enrollment code clears a conflict; an agent replacing its bearer with a key doesn't. Management keeps the keys a code replaced for 30 days after it replaced them, so a copy that presents one is recorded.

**Remove from private networks** cuts the server's network key at once, without re-keying, for example while you investigate. Workloads on it lose their private connections to other servers until you add it back.

By default a conflict only alerts, and both copies can keep authenticating until you act. Set `ASTILBA_AGENT_FORK_FENCING=true` on management to also clear the server's credential when a conflict is proven, which stops both copies until the host is re-keyed.

If you see a conflict, or suspect the state directory leaked:

1. Stop the agent service (`sudo systemctl stop astilba-agent`), since `enroll` can't open the state directory while the service has it.
2. Create a **New enrollment code**, enroll this host again with its existing state directory, and start the service. The agent registers a new key, and every copy of the old one stops working. A new code also takes the server out of its private networks, since a copy of the state directory holds its network key; add it back to give it a new one.
3. If the host itself may be compromised, revoke the connection instead and enroll a rebuilt host as a new server; then [move the work off the revoked server](cross-host-placement.md#moving-off-a-revoked-server). Renewal accepts only the machine that enrolled. After a whole-disk copy of a host without a TPM, the systemd host key leaked too: rebuild the host, or regenerate that key before re-keying.

Revoking still ends unfinished work, and the host keeps the secrets, registry access and TLS keys it was given, so rotate those at their source.

### Agents that still use a bearer credential

Agents released before keys authenticate with a 256-bit bearer credential, which a copy of the state directory can use from anywhere. After an update, the agent registers a key at its next start and deletes the bearer; that bearer then stops working. Management can't safely rotate a bearer, so it refuses **Rotate key now** until the agent is updated and has registered a key. An older agent that was part way through rotating its bearer when management was upgraded can't finish, and shows as offline although it still receives commands; update it.

Until a host is updated, the response to a suspected leak is to revoke it and enroll a rebuilt host or a fresh state directory as a new server: renewing it with the same state directory sends the leaked bearer again.

### Rollback and downgrade

Once an agent holds a key, it refuses to install a release that can't authenticate with one, and its journal is marked so that an earlier agent, restored by hand, refuses to start rather than presenting a retired credential. Don't replace the agent with an earlier release while an enrollment is waiting for its answer: it can't send what the newer agent sent with that code, so you'd need a new code. Running `enroll` again with a code already used fails and leaves the agent's key as it was.

Upgrade every management replica before updating agents: a replica from before keys refuses the access tokens an updated agent presents.

Downgrading management below this release clears the credential of every key-bound server, because earlier releases can only check bearers. Those servers then need a new enrollment code each.

## Observe and recover

The dashboard shows connectivity, the last reported preflight and operation state separately. Heartbeats run every 15 seconds, polls last at most 25 seconds and 45 seconds without contact is offline. Configure reverse proxies for at least a 30-second upstream-header timeout and a 35-second response timeout. Do not cache agent responses. Browser closure or a lost poll never cancels accepted work.

Start a reviewed probe from the Servers page, inspect it, then review a separate remove operation. The probe has the `com.astilba.server`, `resource`, `environment` and `kind` labels. Removal checks all four, then targets the inspected container ID; it never deletes a volume or network. A probe keeps running while management or the agent is stopped. Cancellation retains any created probe at a safe checkpoint; remove it through a new reviewed plan.

A `needs_attention` result blocks conflicting work. Inspect the named resource and Docker state before retrying the same command:

```sh
sudo systemctl stop astilba-agent
sudo astilba-agent retry --operation OPERATION_UUID
sudo systemctl start astilba-agent
```

A writer that does not come back after coordinated maintenance can instead be accepted as it is, so that the maintenance can finish and release its reservation. See [maintenance and compatibility](cross-host-placement.md#maintenance-and-compatibility).

The journal retains one unresolved command and up to 128 acknowledged outcomes, with generation fencing after eviction. A command at or below the generation the journal already holds for its scope never runs. When the journal can tell it never ran here, it ends it as failed, or cancelled if cancellation was requested, so management stops delivering it and anything waiting on it can finish. One at or below a command the journal has since forgotten may have run, so it is refused and its outcome stays unknown; cancelling it ends it unrun, with a note that an earlier delivery may have run it. Only this agent records what it forgets, and only a clean stop vouches that nothing else wrote to the journal. After an earlier agent has opened the journal, or after an unclean stop, every command up to the journal's counters is treated as possibly run. So restoring the previous agent, and upgrading again, keeps working. A network removal is always taken, whatever its generation. Probe records are limited to 32 KiB; visual/pipeline records to 512 KiB. The host route registry keeps each environment's routes in a record of their own, at most 256 KiB, which no reviewed specification reaches. A host routes at most 50 environments; there is no host-wide route count. Reports retain at most 16 steps, 1 KiB of diagnostic text and a bounded 24 KiB observation. Agent report bodies are limited to 192 KiB for bounded source results; other agent requests remain limited to 16 KiB; visual command responses have a 256 KiB bound. Visual logs are bounded and known environment values are redacted. Slow or disconnected management does not block local execution. Revocation rejects subsequent agent requests but does not stop containers or undo accepted effects. Re-enroll the same state directory to report and reconcile outstanding work.

## Terminal session consent

[Interactive troubleshooting](interactive-troubleshooting.md) is off on every host. The host owner opts in with the agent stopped, which also ends any open session when opting out:

```sh
sudo systemctl stop astilba-agent
sudo astilba-agent troubleshooting-enable
sudo systemctl start astilba-agent
```

`troubleshooting-disable` withdraws consent the same way. Consent names the current controller, so transfer, recovery handover and re-enrolling with a new release key withdraw it. Sessions need runc 1.3.3 or later on the 1.3 branch, or a final 1.x release from 1.4 onwards; other hosts report that the container runtime needs updating. The agent writes a journald record of each session's start and end, with the session, the actor as supplied by management, the container and the reason, and no terminal content. A crash leaves no end record: the restarted agent ends what is left of the session and records the cleanup, with the session and the container. If Docker has already forgotten a shell that exited during the outage, the agent warns that any background jobs cannot be identified, and they run until the container restarts. If you run a reverse proxy, it must also forward these stream upgrades and allow them to last 60 minutes; see [Hosting](hosting.md#terminal-sessions-behind-a-reverse-proxy). Dashboard sessions are not available yet.

## Explicit updates

An owner with recent authentication selects a catalog release on the Servers page. Updates are serialized with all other host mutations. The agent verifies protocol, architecture, Ed25519 signature, size, hash and the binary's compiled identity, then fsyncs and atomically replaces only its own executable. Systemd restarts it; the durable operation reports on reconnect. `/usr/local/bin/astilba-agent.previous` retains the previous binary. Workloads stay running. No automatic update policy is enabled.

For a disconnected host, stop the service, inspect its operation state, download and verify the same signed artifacts, then use:

```sh
sudo astilba-agent update-local --manifest release.json --artifact ./astilba-agent
sudo systemctl start astilba-agent
```

Local updates refuse unresolved workload operations. If an update was interrupted, restart first to reconcile the same update, or inspect and retry it locally. The successor verifies the exact signed installed artifact when reconciling an unfinished update; migration alone never completes an update. A durably recorded terminal report remains reportable if a later permitted local update installs another signed release before acknowledgment. An expired update can only reconcile a matching artifact already installed for a command retained at startup. New update records retain their first local acceptance time, so an expired first delivery cannot gain this permission across a crash. Cancellation and current controller authority still apply. An unavailable artifact, invalid signature or incompatible binary yields `needs_attention`, never a workload restart. Remove retained temporary files only after confirming their ownership and that no updater is active.

The agent-local keyring now has an explicit versioned envelope. First startup migrates an idle legacy journal atomically, preserving every key and encrypted record. Finish active cross-host maintenance or controller recovery, database upgrades, paused or uncertain scheduled runs, and unacknowledged workload commands with the previous agent before upgrading. Pending signed agent updates remain eligible for migration, including replacement before success recording and success awaiting acknowledgment. Idle distributed-runtime compatibility fences are retained and do not count as pending workload commands.

A refused migration leaves the legacy journal and keyring unchanged. Once migrated, use a format-compatible agent for executable recovery: previous agents refuse the key envelope before journal initialization, pruning, command replay or maintenance effects. There is no automatic format downgrade. Keep the journal, keys and retained recovery capsules together; offline recovery exports keep their existing format. Previously stranded legacy terminal resumptions require separate inspected recovery and are not made successful by migration.

Executable rollback does not imply persisted-format compatibility. An upgraded agent rewrites a host's route file so that the proxy resolves each backend only on its own unit's network. Routes keep serving if a preceding agent is restored afterwards, but it reports ingress drift and refuses deployments on that host until a reconciliation plan is applied. After a host has recipe-3 replica routes, preceding agents refuse host route publication, including changes for unrelated units. Keep workloads, journal and key intact and restore a replica-capable agent to resume those operations. Scaling back to one instance does not remove this fence. See [application scaling](application-scaling.md#compatibility-export-and-recovery). Once an upgraded agent records routes, it keeps each environment's routes in a journal record of its own. From then on, preceding agents refuse route publication and observation on that host, for every environment. A command that acts before publishing routes, such as a release or a database upgrade, stops at that point and needs attention. To resume, restore a compatible agent with the journal and keys intact.

## Certificate contact

For managed HTTP TLS, the host operator sets `--acme-email operator@example.com` on the agent's `run` command (for systemd, use an inspected `ExecStart` override retaining the existing arguments). An owner must then prepare a plan with host changes approved. The agent persists that reviewed contact in the host route registry. Changing the flag alone does not change running ingress. Public DNS must point at this host and TCP ports 80/443 must be reachable for HTTP-01 validation. The agent waits only for the certificates of the environment being deployed. It reports an unready or untrusted certificate as that deployment's failure and restores the previous routes.

A failed route publication restores the previous route file in place, so other environments on the host keep their connections. The agent recreates the shared proxy only when restoring needs it: when the failed publication changed the proxy's ports, certificate contact or egress setting, when the proxy can't load the previous file, or when a publication that was interrupted, or that accepted drift, left a proxy that differs from the one the agent recorded. A proxy moved onto the dedicated gateway for a coordinated primary keeps running when the host routes nothing, because the primary's private links reach the secondaries through it. An agent from before that change, restored afterwards, removes such a proxy at its next route change, and the primary's private links then refuse until a new plan prepares it again. Recovering a release or preview never recreates the proxy: if it finds a proxy that differs from the recorded one, the publication needs attention and the host keeps reporting drift until a visual reconciliation plan is applied. Route values are written so that the proxy reads exactly the reviewed value, including control characters, and one environment's value can't make the proxy reject the file. A host whose routes hold U+0085 in a header, path prefix or health path, which earlier agents let the proxy read as a space, reports ingress drift once after the upgrade. Apply a reconciliation plan to publish the reviewed value.

## Backup support

Backup recipes, operator filesystem roots, the pinned restic prerequisite, daily/cron/interval schedules, bounded job timeouts and local interruption recovery are documented in [Backups and restoration](backups.md). Use those instructions before enabling backup policies on an enrolled server.

## Container networks

The agent gives every network it creates its own /25 from a container network pool, 10.252.0.0/14 unless you set another. That covers each environment's network, any Compose networks, the ingress network and, on a server that builds, the [build network](git-builds.md#build-network). The default pool holds 2,048 networks, enough for 50 routed environments that each use the most networks a Compose project can declare. The agent doesn't use Docker's `default-address-pools`, whose built-in pools run out after about 30 networks, so the host's own containers keep those pools.

Before creating a network, the agent skips any range that overlaps:

- another Docker network on the host, Astilba's or not
- an IPv4 route in any routing table, including gateway, VPN and `unreachable` routes; routes shorter than /8, such as a VPN's replacement for the default route, are ignored
- a nameserver in `/etc/resolv.conf` or systemd-resolved's upstream list
- the prefix of a managed private network the host holds

A deployment that needs networks the pool can't supply is refused before it changes anything. The refusal names the pool. It says either that the host's routes or managed network prefixes cover all of it, naming the prefixes, which only a different pool or prefix resolves, or how many networks the deployment needs, how many ranges are free and how many networks use the pool.

### Choosing another pool

Set `--network-pool 10.100.0.0/16` on the agent's `run` command when the default overlaps a network the host must reach, for example a VPN or cloud network that routes all of 10.0.0.0/8, a peered network reached only through the default gateway, or a range used on the host's LAN. Use an inspected `ExecStart` override that keeps the existing arguments, as for [certificate contact](#certificate-contact). The pool must be a private IPv4 range of /18 or larger, written as the range itself. Otherwise the agent refuses to start and says why. The pool may contain managed network prefixes, as the default can: the agent places networks around every prefix the host holds.

Changing the pool never moves an existing network. Running environments keep their addresses, including networks created before agents chose them and networks outside the new pool. New networks come only from the new pool, and the old range stays in use until the networks in it are removed. Restart the agent after changing the setting.

The agent checks routes only when it creates a network. If a route appears later, for example when a VPN connects, any container network inside it takes precedence on the host: traffic from the host and its containers to those addresses goes to the containers, not through the VPN. Give the host a persistent route for networks it must reach, or set a pool outside them, before deploying. When choosing a managed network prefix, avoid ranges the workspace's hosts already use for container networks. A host won't join a managed network whose prefix overlaps any of its routes, or any of its local bridge, macvlan or ipvlan L2 networks, routed or not. It changes nothing and the member's note names the conflict. Remove the overlapping network or route on that host, then save the network again. Until then the host still receives removals, and the network's next change tries the whole configuration again. A host reserved for a cross-host placement keeps every network created on it out of that placement's prefix until it joins.

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

Coordinated maintenance additionally requires `maintenanceContinuation` from every
participant before creating its parent or reserving a host. Update the controller
and all idle participants before starting maintenance. Agents advertise support
only after opening their local state and negotiating
`Astilba-Agent-Maintenance-Continuation: v1`. An older or restored older controller
cannot introduce a new maintenance command on these agents. Exact commands
already accepted still reconcile locally while management is unavailable.

A host that refuses a managed network change over a range it already uses says why
in its receipt only while its controller advertises `Astilba-Agent-Network-Refusals:
v1`. Older controllers reject receipt members they don't know, so for them the
command ends needing attention, as it did with older agents. After the agent starts,
such a command waits until management has answered. A refusal recorded just before
a controller downgrade reaches the older controller as a plain failure.

Writer resumptions and maintenance completions retain their original commands and
deadlines. Queued consumer resumptions wait for the matching primary resumption to
succeed; cancellation does not discard them. A delayed first delivery passes through
the existing frozen reservation, retained recovery root, selected target, writer
ownership and readiness checks. An uncertain primary holds consumers and reservations
for reconciliation.

## Protected application releases

Agents advertise `safeReleases` only after a controller response includes `Astilba-Agent-Safe-Releases: v1`. Instance-bearing visual plans require that capability at preparation, apply and dispatch; older payloads retain their original names and hashes. [Application releases](application-releases.md) documents eligibility, command uncertainty, cancellation, retention and recovery. The encrypted `releases` journal bucket records switch and drain state alongside the active `units` mapping; existing observations and metrics keep one logical resource per application.

Named output references use the separately negotiated `serviceOutputs` capability (`Astilba-Agent-Service-Outputs: v1`). Update the agent before deploying a specification with custom outputs or named bindings. Existing PostgreSQL bindings remain compatible; see [service connections and catalog](service-catalog.md).

Preview-capable agents advertise `previews` after feature negotiation, persist the immutable server reservation and reject commands crossing that boundary. Preview builds use `build-preview` and visual plans use recipe 2, which older agents reject. Cleanup receipts survive controller transfer until acknowledged or consumed by authenticated recovery enrollment. See [Pull-request previews](pull-request-previews.md).

Agents advertise `retirement` after feature negotiation. Its visual `retire` action withdraws one unit's routes and removes its containers, networks, schedules and journal records, keeping named volumes. It checks the ownership labels of everything it lists before the first removal and ends in `needs_attention` on any mismatch. It is idempotent and is not cancelled by its admission deadline, so an agent that reconnects late still runs it. See [Retiring an environment](environment-retirement.md).
