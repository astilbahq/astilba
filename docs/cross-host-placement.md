# Cross-host networking and placement

The control plane, agent and dashboard implement cross-host execution. Rollout requires matching controller/agent versions and the native and browser acceptance gates below. Managed private links remain installed during management outages; revocation is complete only after affected reachable peers acknowledge removal, with disconnected peers shown as pending.

## Outcome and constraints

An owner can enroll deployment servers in an Astilba-managed private network, explicitly place a stateless HTTP application's desired instances on those servers, review every old/new placement and immutable artifact, and deploy the complete group behind the environment's existing primary ingress server. Ingress placement is fixed to that primary in this increment; changing a stateful primary or moving ingress is a separate reviewed migration. Network connectivity and host health are separate observations. Management never carries application traffic. Compose remains one unit/host; PostgreSQL and mounts remain on their owning primary host. No automatic failover, autoscaler, database replication or volume relocation. Preview-only/build-only hosts are ineligible. Same-host and singleton encodings stay compatible until an explicit cross-host conversion.

## Managed private links

Use Linux kernel WireGuard and its wg/ip tools, with no cryptographic implementation or new cloud service. This adds an optional host dependency, documented and checked in supported-host preparation. Direct UDP reachability is required; no NAT traversal rendezvous or relay in this increment. Both public and operator-approved private transport addresses can be explicit literal IP endpoints; no arbitrary DNS lookup in management. The owner reviews the listener and endpoint/firewall changes with recent authentication. Conflicting interfaces, routes, addresses or incompatible firewall configuration reject preparation rather than replacing unrelated host state.

Each workspace network has a stable identity, owner-selected nonoverlapping private prefix and epoch. Each member has one unique /32, public key, machine identity and monotonic configuration generation. The host generates and protects its private key locally; management receives only the public key. Each peer AllowedIPs is exactly its assigned /32: no default route, LAN route or transit forwarding. Persist the network configuration and enabled peer set independently of the management process so established workload traffic survives management and agent restarts. Recover only Astilba-owned interface/configuration state under a host-wide lock. Detect a foreign interface instead of adopting it.

Peer membership is not authorization to reach arbitrary host ports or containers. Freeze endpoint grants naming network epoch, source/destination server and unit, destination instance/resource, transport and port. Export a container port only on the managed private IP and only to named peer addresses through an Astilba-owned firewall chain installed before the endpoint becomes reachable. Inspect Docker DNAT and DOCKER-USER/nft hook behavior on both native architectures. Enforce source-unit egress before Docker SNAT: match the exact owned bridge and container source identity against its frozen destination/port grants; drop every ungranted route into the private prefix. A different unit on the same source host must be denied even if the host peer is authorized. Enforce destination admission using both the managed WireGuard input interface and the authenticated peer /32 plus the endpoint port; a source IP arriving on another interface is never sufficient. Drop decrypted traffic to all ungranted host services, unapproved endpoint ports and non-network destinations. Preserve unrelated Docker/firewall rules; refuse unsupported firewall modes. Bindings/ingress create reviewed endpoint grants, not an unrestricted host mesh.

Membership removal/key replacement is a generation-fenced desired-state operation. Existing server revocation atomically invalidates its membership/key epoch and marks desired peer/grant removal on all remaining members before revoking API credentials; their reconciliation remains deliverable to unaffected members. Renewed enrollment invalidates prior machine/network identity and requires fresh owner network admission, even when the server UUID survives. Credential rotation alone preserves the approved machine identity; key replacement is an explicit distinct action. Remove authorization at each reachable member before claiming completion; unreachable acknowledgements remain pending and visibly incomplete. Existing traffic is not put on a management availability lease. A stale isolated member can retain its previously installed peer permissions; do not describe an unacknowledged revocation as complete. Explicit key replacement may interrupt that member's connections. Controller transfer freezes network mutations and requires renewed owner approval; host private keys do not enter export archives. New controller approval reconciles all member generations before new placement. Old controllers cannot mutate after host transfer generation advances.

## Placement and frozen plans

Extend logical environment identity with a primary unit and additional stateless units. The primary retains current resources, volumes, databases and public ingress ownership. Each application placement lists unit/server and instance count; sum remains the existing desired replica count. Initially at most four deployment servers per environment, ten instances per application and forty per environment. Each distributed application must have HTTP readiness, a route, no mounted/image-declared volumes, no public TCP route and no downstream internal consumer. Remote units contain only their placed eligible application instances. Non-distributed services remain on primary.

Version cross-host plans and journal/network records so old agents reject before effects. Freeze the full logical specification, primary and remote unit identities, local resource projections, old/new instance identities, image digest per architecture, network epoch/public identities/endpoint grants, capacity reservations, release options, routing placement and expected per-unit applied inventories. The planner resolves on every selected architecture. Locally loaded artifacts are eligible only on their owning server; remote placement requires an existing immutable registry artifact of the appropriate architecture. No implicit copying/rebuilding or mutable-tag resolution during apply. Admission rechecks all server roles, capabilities, live capacity, approved network/controller generations and expected applied state. Reserve capacity for both old/new pools, accounting for all concurrent reservations. Exhaustion and unknown capacity reject placement; never oversubscribe silently. Existing counts/limits are not a claim of available RAM/CPU. The owner configures Astilba-allocatable CPU/RAM below observed physical capacity with an explicit OS, unmanaged-workload and unbounded-managed-workload reserve; distributed instances require reviewed CPU/RAM requests enforced as Docker limits. An existing managed container without enforced CPU/RAM limits, including primary PostgreSQL, is never counted as zero: the owner must explicitly include it in the non-allocatable reserve or apply reviewed limits before the host is eligible. Unknown/unaccounted inventory rejects eligibility; freeze the acknowledged unbounded-workload inventory so a newly introduced unbounded service invalidates admission. Admission sums all active allocations and replacement reservations against those host budgets in one transaction; do not infer free CPU from a utilization sample or promise isolation from unmanaged host workloads.

## Data and readiness

Each application gets a per-unit Compose projection with local instances, no copies of primary volumes/databases, and secrets hydrated only for that assigned unit's applications and bound source outputs. Cross-host bindings use frozen private destination endpoints and preserve separate address/port/URL semantics, secret provenance and readiness dependencies. A drawing alone creates no link or grant. Do not distribute an application with a binding/readiness dependency unless the explicit path and readiness probe are proven. The application retains its logical ID, but each local plan and observation has its unit/server identity. Logs and metrics expose partial/missing hosts as unknown rather than presenting one host's state as the whole group.

## Coordination and failure semantics

Keep one active environment mutation. A durable parent operation reserves the environment and all involved servers, and records immutable child commands and phase receipts. Child execution retains assigned-server authentication, monotonic generations, local journal acceptance and idempotent report sequence. Do not implement coordination by bypassing existing active-environment or host serialization guards. A migration must explicitly relate children to the parent; ordinary operations cannot enter the reserved environment/hosts. Deterministic lock ordering prevents opposing placements from deadlocking. Before acknowledging prepare, each agent durably records the parent ID, environment, controller generation, participating units, frozen manifest hash and phase. This local barrier survives child acknowledgement/restart and blocks ordinary command acceptance, due schedules, writer resumption, updates and controller transfer until the same parent supplies an authenticated durable completion/abort receipt. Unknown outcomes retain the barrier. An operator may inspect and reconcile the frozen parent locally; there is no timeout-based barrier release. Legacy journal records contain an uncertainty sentinel so an older agent also refuses mutations/schedules while the barrier exists. Old binaries may keep workloads serving but cannot resume distributed mutation or local-only maintenance.

Use prepare, publish and retire phases. Prepare complete new pools on all targets while old pools serve. Each host verifies resources/image ownership, rejects image-declared volumes, applies resource limits, starts candidates, verifies local readiness and authorized private endpoints, and acknowledges a durable prepared receipt. All prepare commands are frozen together; the primary completes preparation before consumer units are first dispatched, so primary-owned bindings exist before consumers probe them. Failed or cancelled primary preparation cancels only consumer commands that have never been dispatched. A queued consumer prepare restored after its parent has advanced is also cancelled without dispatch; uncertain or already-started work retains its recovery barrier. A release command runs once on the designated candidate host, with uncertainty fenced. Only after every required receipt is known may the ingress host publish the frozen complete backend vector under its existing registry lock, verify the switch and acknowledge it. Only after verified publication may old pools drain and stop. Removed network grants are retired after their users are removed. Mark the revision applied only when all required receipts match; otherwise expose mixed/needs-attention state and retain known serving pools. Partial candidate setup may be cleaned only with exact ownership checks and a frozen cleanup receipt.

Each unit retains its two most recent stopped application pools. Their containers keep old images referenced. Retirement records exact Docker IDs before stopping containers, and replay preserves those identities after a process restart. Eviction refuses a pool that was restarted, paused or replaced; it never removes volumes. Retained pools are recovery evidence, not automatic database rollback.

If a peer disappears before publication, preserve old routing and candidate uncertainty; do not mark successful or schedule an unfenced replacement. If ingress acknowledgement is lost, reconcile its actual registry before retrying or retiring old pools. A management or agent restart resumes the same parent and children. Cancellation stops at safe checkpoints and never interprets a missing response as failure. Partition after publication uses Traefik backend readiness to exclude unreachable endpoints, with a convergence window; it does not relocate state or promise uninterrupted requests. The single ingress host and DNS remain failure boundaries. A rollback is a new complete reviewed group deployment; it does not roll back external data.

## Maintenance and compatibility

Cross-host binding introduces remote writers. Existing backup/upgrade quiescence must use the same durable parent barrier to stop and confirm every active writer across units, preserving stop/restart receipts and unknown-outcome fences. No supported upgrade, restore cutover or quiescing backup may proceed with an unreachable writer. Logical database dumps may retain their existing consistency guarantee when no stop is needed. Installed schedules must not silently run a local-only maintenance procedure after distribution; network-independent jobs remain local, but cross-host quiescing maintenance needs an explicit supported policy or is rejected before installation. Transfer/export includes unit identities, parent/child receipts and frozen public network configuration, but no host private keys; imports freeze network/build automation until renewed owner approval.

UI exposes managed-network preparation/health, explicit per-server counts, ingress placement, old/new placement review, capacity/architecture errors and per-host execution progress. Existing save/conflict/draft semantics and owner/recent-auth requirements remain. Use existing components and accessible keyboard/list paths, preserving the parallel UI work. Source-authoritative Compose and previews reject cross-host fields. CLI/MCP uses the same reviewed API authority and cannot configure network membership through a build token.

## Recovering management from an older backup

An interrupted distributed operation can be newer than the management backup.
The primary retains encrypted management checkpoints before each effect phase;
every participant retains exact command headers and sequenced outcomes. Recovery
reconstructs the frozen operation from those checkpoints and the original
management keyring. It does not resolve tags, regenerate plans or infer an outcome
from a missing container or report. Checkpoint sizes and retained histories are
bounded; an incomplete or incompatible evidence set refuses recovery.

Stop the old and destination management processes and all participating agents.
Preserve their journals, host keys and current workload containers. Restore the
management database and its original keyring, and ensure the importing account
already owns the workspace. Keep the agents stopped while collecting evidence:

```sh
sudo astilba-agent inspect-cross-host
sudo astilba-agent export-recovery --recovery-export /srv/recovery/host-a
```

Use a new private directory on each host and copy the complete exports privately
to management. Include every required participant, even one whose completed drain
removed its unit from the current inventory. That absent inventory is accepted
only with an authenticated, successful completion proof for the exact frozen
unit. Empty inventory alone grants no authority.

Inspect with the normal management database/keyring configuration and repeat
`--host-export` for all hosts:

```sh
astilba recover-cross-host --workspace WORKSPACE_UUID --owner OWNER_UUID \
  --host-export /srv/recovery/host-a --host-export /srv/recovery/host-b
```

Review the reported state hash and required participants. To stage exactly that
state, repeat the command with `--expected-state STATE_HASH` and a new private
`--out /srv/recovery/handovers` directory. Management must remain stopped; the
command requires exclusive database access. It retains stronger compatible
management knowledge, cancellation, revocation and deletion state. Conflicting
machine identities, epochs, plans or proofs require inspection rather than a
counter reset. Missing imported records are attributed to the importing owner.

Start destination management and use its normal authenticated transfer workflow
for each server. On each stopped agent, supply its matching handover JSON through
`transfer --recovery-handover FILE`, together with the normal management origin,
server ID, target release key, transfer generation and private token file. Keep
the same state directory. Ordinary settled-host transfer does not bypass an
active distributed reservation.

Each host verifies the exact evidence and new controller authority before
enrollment. Execution remains held until every participant enrolls with its full
captured inventory and every host acknowledges opening the same recovery root.
Old-controller commands are rejected. Network/build automation remains subject
to renewed owner approval; the handover does not export WireGuard private keys.

Resume the recorded operation, inspecting any uncertain data effect. An
interrupted PostgreSQL cutover reports a failed leaf execution with upgrade
status `attention`; once target writes are possible, returning to the source is
forbidden. Explicit target reconciliation preserves that boundary. The globally
applied revision changes only after all hosts confirm their target resumption.
Preserve private recovery exports and handovers until recovery is confirmed.

## Implementation sequence and evidence

1. Schema/domain/contracts for network membership, grants, explicit units/placement, immutable manifests and coordinated operation receipts; compatibility rejection and transfer versions.
2. Host link preparation and generation-fenced reconciliation, ownership and private-key handling, endpoint grants and restart behavior. Include an owner-approved optional systemd drop-in adding AF_NETLINK to the existing address-family allowlist; retain the other service restrictions and verify the upgrade/install path. Prove two disposable Ubuntu hosts (both supported architectures) can reach only granted ports and fail closed on forged identities, a denied source unit on an otherwise allowed peer host, wrong ingress interface, conflicting routes and revoked grants. Execute through the installed systemd unit, not only an unrestricted test shell.
3. Planner and coordinator using durable reservations and assigned-server child commands. Prove role/capability changes, tenant isolation, capacity contention, idempotent apply, lost reports, parent restart and cancellation at each boundary against real PostgreSQL.
4. Local pool preparation, endpoint publication and protected ingress switch. Prove distribution, readiness failure, denied/unknown peer, lost prepare/publish/retire receipts, restart, rollback and unrelated route preservation on disposable native hosts.
5. Remote writer maintenance barriers, schedules and export/transfer recovery. Prove no stateful effect starts with an unconfirmed remote writer and no cross-host data is silently omitted.
6. Accessible placement/network UI and real API Chromium/WebKit journeys; load-bearing focused tests only. Run required make verify, both native architectures, secret/public-source scan and final high-risk peer review before PR. No live-alpha mutation or paid infrastructure purchase is included.

The acceptance fixtures exercise different boundaries:

| Fixture | Evidence |
| --- | --- |
| `TestNativeManagedLinks` | Real WireGuard and firewall rules through the installed systemd network service, including denied sources, host ports, ingress interfaces and revocation. |
| `TestCrossHostCoordinatedPlacement` | Real PostgreSQL/River admission, capacity, cancellation and deployment/maintenance coordination with controlled agent protocol peers. Host effects are separate. |
| `TestNativeCrossHostDeployment` | Two isolated Docker hosts with real private links, ingress, process interruption, writer barriers, backups, restore cutovers and a supported PostgreSQL upgrade. |
| `TestCrossHostRetainedPools` | Docker container identity, crash/replay, bounded stopped-pool eviction and image retention. |
| `TestNativeCrossHostOlderBackupRecovery` | SQL reconstruction, journal transfer, disjoint parents and preservation of newer management knowledge. Its network receipts are controlled fixture inputs. |
| `TestNativeCrossHostDrainedRecovery` / `TestNativeCrossHostUpgradeRecovery` | Two-host SQL/journal/private-link recovery after a secondary has completed, or after an interrupted PostgreSQL cutover with a distinct write on the new database. |
| `tests/cross-host.spec.ts` | Chromium/WebKit network setup, failed-save and draft-conflict preservation, per-host plan review, keyboard access and responsive reflow through the real API. Authenticated protocol peers provide observations; this journey does not simulate a successful cross-host deployment. |

Run these with the repository's existing application and native gates. A protocol
fixture does not establish kernel isolation or data recovery; the native cases
provide that evidence. Neither replaces the required second-architecture run.

## Transport references

- [WireGuard configuration and endpoint requirements](https://www.wireguard.com/quickstart/)
- [WireGuard routing and network namespaces](https://www.wireguard.com/netns/)
- [Docker port publishing and masquerading](https://docs.docker.com/engine/network/port-publishing/)
- [Docker firewall integration](https://docs.docker.com/engine/network/packet-filtering-firewalls/)
- [Docker iptables packet ordering](https://docs.docker.com/engine/network/firewall-iptables/)
