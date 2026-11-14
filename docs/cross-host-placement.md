# Cross-host networking and placement

The control plane, agent and dashboard implement cross-host execution. Rollout requires matching controller/agent versions and the native and browser acceptance gates below. Managed private links remain installed during management outages; revocation is complete only after affected reachable peers acknowledge removal, with disconnected peers shown as pending.

## Outcome and constraints

An owner can enroll deployment servers in an Astilba-managed private network, explicitly place a stateless HTTP application's desired instances on those servers, review every old/new placement and immutable artifact, and deploy the complete group behind the environment's existing primary ingress server. Ingress placement is fixed to that primary in this increment; changing a stateful primary or moving ingress is a separate reviewed migration. Network connectivity and host health are separate observations. Management never carries application traffic. Compose remains one unit/host; PostgreSQL and mounts remain on their owning primary host. No automatic failover, autoscaler, database replication or volume relocation. Preview-only/build-only hosts are ineligible. Same-host and singleton encodings stay compatible until an explicit cross-host conversion.

## Managed private links

Use Linux kernel WireGuard and its wg/ip tools, with no cryptographic implementation or new cloud service. This adds an optional host dependency, documented and checked in supported-host preparation. Direct UDP reachability is required; no NAT traversal rendezvous or relay in this increment. Both public and operator-approved private transport addresses can be explicit literal IP endpoints; no arbitrary DNS lookup in management. The owner reviews the listener and endpoint/firewall changes with recent authentication. Conflicting interfaces, routes, addresses or incompatible firewall configuration reject preparation rather than replacing unrelated host state.

Each workspace network has a stable identity, owner-selected nonoverlapping private prefix and epoch. Each member has one unique /32, public key, machine identity and monotonic configuration generation. The host generates and protects its private key locally; management receives only the public key. Each peer AllowedIPs is exactly its assigned /32: no default route, LAN route or transit forwarding. Persist the network configuration and enabled peer set independently of the management process so established workload traffic survives management and agent restarts. Recover only Astilba-owned interface/configuration state under a host-wide lock. Detect a foreign interface instead of adopting it.

Peer membership is not authorization to reach arbitrary host ports or containers. Freeze endpoint grants naming network epoch, source/destination server and unit, destination instance/resource, transport and port. Export a container port only on the managed private IP and only to named peer addresses through an Astilba-owned firewall chain installed before the endpoint becomes reachable. Inspect Docker DNAT and DOCKER-USER/nft hook behavior on both native architectures. Enforce source-unit egress before Docker SNAT: match the exact owned bridge and container source identity against its frozen destination/port grants; drop every ungranted route into the private prefix. A different unit on the same source host must be denied even if the host peer is authorized. Enforce destination admission using both the managed WireGuard input interface and the authenticated peer /32 plus the endpoint port; a source IP arriving on another interface is never sufficient. Drop decrypted traffic to all ungranted host services, unapproved endpoint ports and non-network destinations. Preserve unrelated Docker/firewall rules; refuse unsupported firewall modes. Bindings/ingress create reviewed endpoint grants, not an unrestricted host mesh.

Membership removal/key replacement is a generation-fenced desired-state operation. Existing server revocation atomically invalidates its membership/key epoch and marks desired peer/grant removal on all remaining members before revoking API credentials; their reconciliation remains deliverable to unaffected members. Renewed enrollment invalidates prior machine/network identity and requires fresh owner network admission, even when the server UUID survives. Credential rotation alone preserves the approved machine identity; key replacement is an explicit distinct action. Remove authorization at each reachable member before claiming completion; unreachable acknowledgements remain pending and visibly incomplete. A revoked server can never acknowledge anything, and what cuts it off is its peers removing its key, so the network waits on every other member and not on it. Any other member that has not acknowledged the current generation keeps the network pending and new placement refused, including a removed member that has not been revoked. While the network is pending the revoked member reads as pending too, because only an acknowledgement of the current generation proves what a peer holds, and each later generation asks for that again. Whatever the status says, the revoked host may still hold its own configuration, which management cannot confirm or change. A removed member that will not return can be revoked to stop the wait. Renewing a revoked server's enrollment or preparing its transfer clears the revocation, so the network waits on that host again until it acknowledges its removal, is revoked again, or is added back. An acknowledgement made under an earlier controller does not count, so after a restore or import the network stays pending until the owner approves it again and each member answers. Existing traffic is not put on a management availability lease. A stale isolated member can retain its previously installed peer permissions; do not describe an unacknowledged revocation as complete. A failure reconciling one workspace's network costs that workspace its turn and delays each other workspace by no more than that turn's deadline; it raises an operational alert if it continues, unless a held workspace lock is the cause, which only the management log shows. Network generations are ordered by controller generation first, and each controller counts for itself, so what a host holds cannot stand in the way of a restored management, and no live receipt can move its counter on the strength of it. Offline recovery does raise the counter from host exports, within the fixed allowance below. Under one controller, a host's receipt can move the network generation only as far as its unreported work explains. A receipt beyond that moves nothing: the member stays pending with a note, and later removals still reach it. Revocation does not depend on the generation being able to rise. If the generation has already reached the bigint limit, the credential is still cleared and the membership disabled, but a peer that already holds that generation cannot be told. Those members stay pending with a note, and network configuration and new placement are refused. Agents generate a key per membership, so management refuses a key that another member of the network holds, and that member stays pending with a note until it reports its own. It isn't asked again until the network changes, for example when the member holding that key is removed or revoked, or is saved. A host that can't run a network command at all, because it isn't prepared for managed networking or lacks the tools, changes nothing. Its member is noted, and nothing more is sent to it until its heartbeat reports it ready or the network changes. Explicit key replacement may interrupt that member's connections. Controller transfer freezes network additions and requires renewed owner approval, while removals still reach a transferred host; host private keys do not enter export archives. New controller approval reconciles all member generations before new placement. Old controllers cannot mutate after host transfer generation advances.

## Placement and frozen plans

Extend logical environment identity with a primary unit and additional stateless units. The primary retains current resources, volumes, databases and public ingress ownership. Each application placement lists unit/server and instance count; sum remains the existing desired replica count. Initially at most four deployment servers per environment, ten instances per application and forty per environment. Each distributed application must have HTTP readiness, a route, no mounted/image-declared volumes, no public TCP route and no downstream internal consumer. Remote units contain only their placed eligible application instances. Non-distributed services remain on primary.

Version cross-host plans and journal/network records so old agents reject before effects. Freeze the full logical specification, primary and remote unit identities, local resource projections, old/new instance identities, image digest per architecture, network epoch/public identities/endpoint grants, capacity reservations, release options, routing placement and expected per-unit applied inventories. The planner resolves on every selected architecture. Locally loaded artifacts are eligible only on their owning server; remote placement requires an existing immutable registry artifact of the appropriate architecture. No implicit copying/rebuilding or mutable-tag resolution during apply. Admission rechecks all server roles, capabilities, live capacity, approved network/controller generations and expected applied state. Reserve capacity for both old/new pools, accounting for all concurrent reservations. Exhaustion and unknown capacity reject placement; never oversubscribe silently. Existing counts/limits are not a claim of available RAM/CPU. The owner configures Astilba-allocatable CPU/RAM below observed physical capacity with an explicit OS, unmanaged-workload and unbounded-managed-workload reserve; distributed instances require reviewed CPU/RAM requests enforced as Docker limits. An existing managed container without enforced CPU/RAM limits, including primary PostgreSQL, is never counted as zero: the owner must explicitly include it in the non-allocatable reserve or apply reviewed limits before the host is eligible. Unknown/unaccounted inventory rejects eligibility; freeze the acknowledged unbounded-workload inventory so a newly introduced unbounded service invalidates admission. Admission sums all active allocations and replacement reservations against those host budgets in one transaction; do not infer free CPU from a utilization sample or promise isolation from unmanaged host workloads.

## Data and readiness

Each application gets a per-unit Compose projection with local instances, no copies of primary volumes/databases, and secrets hydrated only for that assigned unit's applications and bound source outputs. Cross-host bindings use frozen private destination endpoints and preserve separate address/port/URL semantics, secret provenance and readiness dependencies. A drawing alone creates no link or grant. Do not distribute an application with a binding/readiness dependency unless the explicit path and readiness probe are proven. The application retains its logical ID, but each local plan and observation has its unit/server identity. Logs and metrics expose partial/missing hosts as unknown rather than presenting one host's state as the whole group.

## Coordination and failure semantics

Keep one active environment mutation. A durable parent operation reserves the environment and all involved servers, and records immutable child commands and phase receipts. Child execution retains assigned-server authentication, monotonic generations, local journal acceptance and idempotent report sequence. Removals are delivered ahead of other commands, so a host can take one sent after a deployment's links command, at a later generation, before it takes the links command. It then ends the links command unrun, the deployment aborts and releases its hosts, and the owner can apply again. After an unclean agent stop, or once an earlier agent has opened its journal, the host can't tell that it never ran a links command it took a removal ahead of, so it refuses it. Cancelling the deployment then ends it, and the deployment aborts and releases its hosts. Do not implement coordination by bypassing existing active-environment or host serialization guards. A migration must explicitly relate children to the parent; ordinary operations cannot enter the reserved environment/hosts. Deterministic lock ordering prevents opposing placements from deadlocking. Before acknowledging prepare, each agent durably records the parent ID, environment, controller generation, participating units, frozen manifest hash and phase. This local barrier survives child acknowledgement/restart and blocks ordinary command acceptance, due schedules, writer resumption, updates and controller transfer until the same parent supplies an authenticated durable completion/abort receipt. Unknown outcomes retain the barrier. An operator may inspect and reconcile the frozen parent locally; there is no timeout-based barrier release. The current agent-local key envelope makes previous agents refuse journal opening before initialization, pruning, replay or maintenance effects. Legacy active journals must finish with their previous compatible agent before migration; migration preserves pending signed agent updates and idle distributed-runtime fences. This changes neither the recovery capsule format nor the independent network boot-restore helper. See the [agent upgrade boundary](agent.md#explicit-updates).

Use prepare, publish and retire phases. Prepare complete new pools on all targets while old pools serve. Each host verifies resources/image ownership, rejects image-declared volumes, applies resource limits, starts candidates, verifies local readiness and authorized private endpoints, and acknowledges a durable prepared receipt. All prepare commands are frozen together; the primary completes preparation before consumer units are first dispatched, so primary-owned bindings exist before consumers probe them. Failed or cancelled primary preparation cancels only consumer commands that have never been dispatched. A queued consumer prepare restored after its parent has advanced is also cancelled without dispatch; uncertain or already-started work retains its recovery barrier. A release command runs once on the designated candidate host, with uncertainty fenced. Only after every required receipt is known may the ingress host publish the frozen complete backend vector under its existing registry lock, verify the switch and acknowledge it. Only after verified publication may old pools drain and stop. Removed network grants are retired after their users are removed. Mark the revision applied only when all required receipts match; otherwise expose mixed/needs-attention state and retain known serving pools. Partial candidate setup may be cleaned only with exact ownership checks and a frozen cleanup receipt.

Each unit retains its two most recent stopped application pools. Their containers keep old images referenced. Retirement records exact Docker IDs before stopping containers, and replay preserves those identities after a process restart. Eviction refuses a pool that was restarted, paused or replaced; it never removes volumes. Retained pools are recovery evidence, not automatic database rollback.

If a peer disappears before publication, preserve old routing and candidate uncertainty; do not mark successful or schedule an unfenced replacement. If ingress acknowledgement is lost, reconcile its actual registry before retrying or retiring old pools. A management or agent restart resumes the same parent and children. Stopping an agent records no outcome for a coordinated or private-link phase: it reruns when the agent restarts, exactly as after a crash. Reservation, storage, private links, candidate startup and probes repeat against the same frozen identities, publication first recovers its pending route attempt, retirement resumes its recorded drain deadline and exact containers, candidate cleanup removes only what is still owned, and a release command keeps its run-once state. A data effect under a parent keeps its own interruption rules. Cancellation stops at safe checkpoints and never interprets a missing response as failure. Partition after publication uses Traefik backend readiness to exclude unreachable endpoints, with a convergence window; it does not relocate state or promise uninterrupted requests. The single ingress host and DNS remain failure boundaries. A rollback is a new complete reviewed group deployment; it does not roll back external data.

A deployment phase's admission deadline decides only whether its host may start something new, as maintenance's does below. Reservation, private links, candidate preparation, the release command, probes and publication each start something, so none starts after its deadline. One that misses it has changed nothing, which is a known outcome: the parent removes the candidates on every host and fails, releasing every host so the owner can apply again. That includes publication, which would otherwise switch ingress to candidates probed long before. The primary ends a publication failed or cancelled only before it starts changing ingress; once it has started, any outcome but success needs attention, so no host removes candidates the routes use. Retirement, completion and candidate cleanup finish what the hosts already began, and nothing can issue them again, so an agent runs them however late they arrive, but only while its own journal holds that deployment's reservation, and then through the same frozen-reservation checks. Cleanup also acknowledges a host that holds no reservation at all, changing no reservation or runtime there, since refusing it would hold every other host. Lateness never lets a command start or widen anything. The agent decides this from one closed list of command kinds, deployment and network actions and coordinated phases, and doesn't run a phase that isn't on it. A retirement is on it: it runs late only on a host whose journal holds the environment there (see [Retiring an environment](environment-retirement.md)). Every integration test checks, when it ends, that each coordinated phase management issued is on that list, so a phase management issues in any tested flow fails until the agent lists it. An agent without this rule cancels late retirement, completion and cleanup instead, and the parent holds the reservation for reconciliation, as it did before.

## Moving off a revoked server

Revoking a server ends what it was doing and lets you plan around it. Nothing the server reports affects either, and renewing it later doesn't reopen anything. Renewal, and a transfer that would lift the revocation, are refused while an operation that names the server is still running.

Whatever you do next, a revoked server keeps what it was given. It may keep running containers and it still holds its secrets, registry access and TLS material, so rotate those. Management can't confirm or change any of it.

Renewal is a poor way back. It accepts only the machine that was enrolled, with its journal, so a rebuilt or replaced machine needs a new server record. It doesn't undo a mistaken revocation either. Work already settled stays settled. A host that held a reservation for an operation that has since ended keeps refusing ordinary work, and nothing online can release it. A mistaken revocation of such a host may only be recoverable offline, through [recovering management](#recovering-management-from-an-older-backup), and we have not established that this works, so plan to replace the host.

### Work in flight

Revocation cancels the server's unfinished commands in the same transaction, with a note that what it did is unknown. An operation that involved other hosts then carries on without it:

| When the server was revoked | What happens |
| --- | --- |
| A deployment, before publication | The remaining hosts finish what they're doing, then remove their candidates and keep their previous pools. A release command that hasn't started is cancelled. A secondary held in preparation, for example because a service it binds to was on the revoked server, is released. The deployment fails. A primary stays held, because its preparation may have recreated a shared service: inspect it and run `astilba-agent retry`, which ends the preparation. |
| A deployment, while publishing | If the publication hadn't been sent, or the primary was revoked before reporting it, the same as above. Otherwise the publication finishes first. |
| A deployment, after publication | The remaining hosts finish. The deployment succeeds, with a note, and its plan still names the revoked server until you plan it away. |
| Maintenance, before its data effect is sent | The effect doesn't start, because a stopped writer can't be confirmed. The remaining writers resume and the operation fails. |
| Maintenance, after its data effect is sent | A consumer's loss doesn't stop the effect. If the primary was revoked before reporting it, the remaining writers resume and the outcome of the effect is unknown. |
| An upgrade waiting for your choice | If the primary was revoked, the upgrade is rejected with a note and the remaining writers resume. If a consumer was, your choice still stands. |

Another case also needs you. If the primary is revoked after publication and a healthy secondary's application then exits or turns unhealthy, because it depended on that primary, the secondary reports `needs_attention` when asked to complete, and the deployment holds. Nothing has been lost. Fix the application and run `astilba-agent retry` on that host, or revoke that host too.

Consumers normally wait for their provider before resuming. They don't wait for a revoked one, so a consumer whose readiness depends on that provider can come back unready. The reservation is then held as for any other unready writer, and `astilba-agent accept-unready-writers` releases it.

### A revoked secondary

A secondary holds no durable data, because a distributed application can't have volumes, so nothing durable is lost. Save a placement that no longer targets the server and deploy it. The plan leaves the server out and says so in its effects. It sends the server nothing, and the server's runtime record goes when the plan is applied. If the server is renewed before you apply the plan, the plan is refused, because that server could now answer. To remove placement, consolidate on the primary first, as usual.

A member removed from the network but not revoked can still answer, so it keeps blocking the plan until it answers or you revoke it.

### A revoked primary

The primary holds the environment's volumes and databases, so moving off it loses their data. Nothing is copied from a revoked server.

The deployment panel says when the environment's primary was revoked. Choose a healthy server and plan a deployment to it. The plan is refused until you name every volume on the revoked primary, and the panel asks you to accept each one. The plan's effects list what you're giving up. Only an owner can do this. Applying it:

- makes the chosen server the primary. If it already holds this environment's unit, that unit takes the role, so the host keeps a single unit. Otherwise the server gets a new unit. The former primary's unit is detached and never reused, so its old volumes on that host can't come back by accident
- starts the volumes the revision declares empty, with the same identities, and drops the others
- moves the route claims, so point custom domains at the new server, and change generated domains, which name their server
- leaves the environment's backup policies with the revoked server, where they can't run. Create new ones.

The plan applies only while the old primary is still revoked. If the deployment doesn't finish, the environment is already on its way to that server, and the panel continues there. Other servers are refused while that server is healthy, because the first attempt may have left containers and volumes on it. If it's revoked too, choose any healthy server.

To get the data back, restore a snapshot by hand, as [Backups](backups.md#recovery-without-the-management-database) describes, into the new empty volumes. Filesystem backup destinations on the revoked server are lost with it.

The new server needs an agent from this release. An older agent refuses the plan before changing anything. Deploying back onto a renewed former primary fails, because its old volumes are labelled for the detached unit. Replace that host rather than reuse it.

Offline recovery keeps whichever side knows of the replacement, whether the management backup or a host's capsule is the newer.

### A primary's ingress

A primary's ingress proxy reaches the secondaries over private links, which need the proxy to use a dedicated network as its gateway. A server that has only run secondaries, or only published routes without host changes, has no such proxy. When a coordinated plan, including a replacement, makes it the primary, its agent moves its proxy onto that gateway while the hosts reserve capacity, before any application changes. A server that routes nothing gets a proxy with no public ports.

Moving the proxy restarts it, so the plan names the environments, previews and the planned environment included, whose traffic through that server drops for a few seconds. When there are too many to list, it names as many as fit and counts the rest. Choose a quiet time to apply it. A server whose proxy already uses the gateway keeps it running untouched, and a primary with no secondaries needs no gateway.

If the move fails, the proxy goes back to the routes it served and the deployment needs attention on that server. An interrupted move is put back the same way before it's tried again. Once moved, the proxy stays on the gateway, even if the deployment is then cancelled or fails.

The server's agent must advertise `primaryIngress` (`Astilba-Agent-Primary-Ingress: v1`). Otherwise the plan is refused before anything changes, asking you to update that agent.

## Maintenance and compatibility

Cross-host binding introduces remote writers. Existing backup/upgrade quiescence must use the same durable parent barrier to stop and confirm every active writer across units, preserving stop/restart receipts and unknown-outcome fences. No supported upgrade, restore cutover or quiescing backup may proceed with an unreachable writer. Logical database dumps may retain their existing consistency guarantee when no stop is needed. Installed schedules must not silently run a local-only maintenance procedure after distribution; network-independent jobs remain local, but cross-host quiescing maintenance needs an explicit supported policy or is rejected before installation. Every participant must explicitly advertise `maintenanceContinuation` before a new maintenance parent or reservation is created. Freeze every resumption child together, but deliver queued consumer `resume-source` and `resume-target` commands only after the primary succeeds in that exact parent phase. Already-dispatched children retain reconciliation delivery. Primary failure, cancellation or uncertainty keeps consumers stopped and surfaces `needs_attention` before all-child completion. Terminal outcomes remain terminal.

A maintenance command's admission deadline decides whether its host may start a new effect, never whether its reservation ends. Resumption and completion change no data and nothing can issue them again, so an agent reconciles them through its stateful reservation and recovery checks however late they arrive, without replacing the original command or deadline. Lateness is not evidence: completion releases a reservation only once it has confirmed the writers, and otherwise holds it for `astilba-agent retry` or `accept-unready-writers`. A data effect does not start after its deadline. A backup, restore or upgrade action that misses its deadline has changed nothing, which is a known outcome, so the parent resumes the source writers on every host and completes as failed, leaving the owner to request it again, or, once a prepare has run, returns the upgrade to the owner to approve again. Should a host have begun restoring, its journal refuses to resume the source, so the reservation holds and no outcome is assumed.

A backup leaf's deadline is its run's timeout plus ten minutes, so a long upload fails by that timeout and the parent resumes the writers. A backup leaf stopped while its agent runs on, by the owner's cancel or by a deadline it reaches because it started late, settles by what its run did. Stopped before its first effect, the leaf is cancelled and its run failed, so the parent resumes the source writers and completes as failed. Stopped after it, part way through its upload, the leaf needs attention, as its run does, and the hosts stay reserved with their writers stopped. Inspect the primary for containers with the run's `com.astilba.run` label, then run `astilba-agent reject-cross-host-effect --operation LEAF_UUID` there: it records the leaf and its run as failed, and the parent then resumes the writers on every host. A restore phase that fails its checks before it records its cutover intent has stopped nothing more than the pause did, so it fails and the parent resumes the source.

Maintenance leaves an application as it found it rather than refusing it, because a backup or restore is most wanted when an application is down, and a refused one protects nothing. An instance already stopped when its host pauses stays stopped; once the reservation is released, starting it by hand is not drift, though the next maintenance refuses once until management has observed the change. Starting it while the reservation is held is refused until it is stopped again. A restored database is the exception: it starts, because serving it is what the cutover is for. A running instance failing its Docker health check or plan readiness when its host reserves is stopped and restarted like any other writer, or left running if it is a database, but neither resumption nor completion waits for it, and it stays stopped if it cannot restart, so it does not keep other hosts' writers stopped. Probing happens at reservation because a later probe could blame a writer for a dependency another host has paused. Failing to run a probe at all refuses the reservation instead. The completion note counts the instances excused from readiness, and the receipt reports their state.

Changed configuration or identity is not a state to preserve, so reservation refuses it, and so does the pause if it appears later, along with a writer stopped since reserving. Nothing has stopped on that host, so the parent resumes every other host and completes as failed. A host that stopped nothing, because it refused or never reached its pause, completes with whatever it observes, drift included.

A writer that passed its checks at reservation and does not after resumption is an unknown outcome, so it retains the reservation and no timeout releases it. `astilba-agent retry` reruns the same command once the writer recovers. When the fix needs the reservation released, inspect the host and accept the writers as they are, giving the resumption or completion operation that needs attention:

```sh
sudo systemctl stop astilba-agent
sudo astilba-agent accept-unready-writers --operation OPERATION_UUID
sudo systemctl start astilba-agent
```

This changes no container and leaves a writer that could not start stopped. It refuses changed configuration or identity, does not excuse a database, and does not decide a data effect's outcome, which resumption still checks. The accepted command then finishes, and the later completion releases the reservation. A corrected deployment can then replace a writer that is running but not ready. A stopped writer reads as drift, which refuses a cross-host deployment's resolution until the writer runs again.

Transfer/export includes unit identities, parent/child receipts and frozen public network configuration, but no host private keys; imports freeze network/build automation until renewed owner approval.

UI exposes managed-network preparation/health, explicit per-server counts, ingress placement, old/new placement review, capacity/architecture errors and per-host execution progress. Existing save/conflict/draft semantics and owner/recent-auth requirements remain. Use existing components and accessible keyboard/list paths, preserving the parallel UI work. Source-authoritative Compose and previews reject cross-host fields. CLI/MCP uses the same reviewed API authority and cannot configure network membership through a build token.

## Recovering management from an older backup

An interrupted distributed operation can be newer than the management backup.
The primary retains encrypted management checkpoints before each effect phase;
every participant retains exact command headers and sequenced outcomes. Recovery
reconstructs the frozen operation from those checkpoints and the original
management keyring. It does not resolve tags, regenerate plans or infer an outcome
from a missing container or report. Checkpoints are compressed with zstd. Checkpoints sealed
before that were compressed with gzip, and recovery still reads them, alone or mixed with newer
ones. A release can't read checkpoints sealed by a newer one, so run recovery with the release
that sealed the newest checkpoint or a newer one, even when the management backup is older.
Checkpoint sizes and retained histories are bounded; an incomplete or incompatible
evidence set refuses recovery.

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

Recovery needs an export from every enrolled server except a revoked one, which may be gone or untrusted. Revoke a server in the restored management before recovering if you can't trust it or reach it. An export supplied for a revoked server must still match its machine, but nothing it claims counts: not its proofs, inventory, roots, private links or generation claims, which it could make up. Only its sealed capsules can count, for an interrupted root it was primary of, beyond the checkpoints management kept, because management sealed them and the server can only replay them. An export that can't be read at all still stops recovery, so leave it out; a revoked server's export is never required. Recovery keeps the revocation until an owner lifts it, keeps the server out of the network whichever membership record it selects, and ends its work as revoking it does, including any standalone removal still waiting on it.

A revoked server that took part in an interrupted root gets no authority back. The root opens once the remaining participants prove themselves, and they end it as they would after any revocation, following the table in [Moving off a revoked server](#moving-off-a-revoked-server). When the revoked server was the root's primary, whose export normally carries the root's checkpoints, recovery uses the checkpoints management sealed for it instead. If the revoked server's export holds later checkpoints, recovery takes them too, but only in order, each following the last, and only while each opens under management's key, so the server can replay progress management recorded but can't invent, reorder or withhold any below what management kept. Where it holds two different checkpoints for the same step, recovery takes neither. If the root is newer than anything management kept, include the revoked server's export if you can, since its sealed capsules are then the only copy of the root. Without them nothing holds its frozen plan, and recovery refuses with what to do: revoke the root's other servers too, leave out their exports, then deploy the environment to healthy servers.

Host exports are unsigned, so recovery refuses, on the preview and before writing
anything, an export that claims a network, environment, update or controller
generation more than a fixed allowance above what current management and the
sealed capsules record. The error names the host. Inspect that host before
including its export.

A host can hold a later network generation than a management restored from a
backup, and peers that backup does not know about. Neither needs any special
step. Recovery moves the host to a new controller generation, and only its local
transfer can do that. Removals reach it straight away if management has a record
of what the host holds, and otherwise with the approval. After you approve the
network again, each free member receives the whole approved manifest for that
controller. It replaces what the host holds, whatever generation the host has
reached, and removes those unknown peers. A busy member receives only removals
until it is free, and the whole manifest follows. A manifest already on its way
carries any removal at its generation, so that removal goes separately only if the
manifest has not landed within 30 seconds, or ended without landing.

If management was restored without fencing a host, the host stays on the same
controller and ahead of management. It may end a `configure` unrun as stale, in
which case removals go to it first and each later `configure` is tried again until
management's counter passes the host's, or refuse it without changing anything, in
which case the member is noted and receives only removals until the network is
saved. Management
needs a record of what the host holds to send a removal, and it drops that record if
the host refuses one. Nothing the host reports moves the counter by more than its
unreported work explains. Stop management, run `astilba prepare-recovery`
for that server with a generation above the one it has installed, and start
management again. Then prepare its transfer, transfer it locally and approve the
network again.

A member noted as reporting a generation far beyond what this installation issued
has the same way out. A modified agent can also cause that note, so revoke the
server instead if you do not trust it.

A host also refuses a network change, before changing anything, when the prefix
overlaps a route or local container network it already uses. While it holds no
enabled network for it, it checks Docker's record as well as its routes, because a bridge has no route
until Docker starts, or at all in isolated gateway mode. A coordinated `links`
phase then fails, and its deployment aborts and releases its reservations.
Otherwise the member is noted with the remedy and receives only removals at that
generation, and the next generation tries the whole manifest again. The refusal
says what the host holds for its membership, so management can scope those removals
even when it had no record of the host, and a revoked peer still leaves it.
Management refuses a refusal claiming a later generation than it sent. It keeps a
refusal naming a peer or grant it never made for that host, which an honest host
can hold after management is restored from an older backup, but it doesn't store
that description. Restrictions never check
routes: they keep the prefix the host holds, and taking authority away must not
wait on a conflict. A host that never joined holds nothing, so it acknowledges each
removal without applying it. A whole disabled manifest for a host that holds the
network, which removes a member management has no record of, checks routes but not
Docker's record. So a host removed, added again behind a route it can't join over,
and removed again can't confirm that removal until the route goes or the server is
revoked, and the network waits on it until then.

Preserve private recovery exports and handovers until recovery is confirmed.

Recovery orders runtime changes by the operation that actually committed them,
not revision creation time or the execution counter used by reads and prepares.
Each operation records an applied immutable plan or confirmed removal; the current
runtime retains that operation even when cleanup removes its projection. Recovery
checks each original authenticated base before combining histories. It identifies an
operation by a fixed, versioned list of its command's fields as stored, leaving out those that
change after it is created (cancellation, controller transfer and its attached checkpoint), so a
checkpoint sealed by one release is compared the same way by a later one. A primary that fails a checkpoint
phase is still sent the root's closure, because it may have kept the capsule, and a
host keeps an unclosed capsule until its root is closed. Equivalent
cleanup acknowledgements remain valid across report and controller transfer.
Archive version 6 and later preserve these outcomes. Versions 1–5 remain importable with
unknown provenance: their nonempty runtime cannot establish recovery ancestry or
be adopted without a fresh successful application and checkpoint. Unchanged
unrelated legacy state may be preserved, including legacy state with no runtime.
A selected preview removal reapplies the existing cleanup finalizer after the
history merge, clearing live runtime, routes and secrets and marking its request
deleted. Immutable history and retained checkpoint bytes remain intact.

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
| `TestNativeCrossHostDeployment` | Two isolated Docker hosts with real private links, ingress, process interruption, writer barriers, backups, restore cutovers and a supported PostgreSQL upgrade. Quiesced backups stop a primary file-serving provider used by remote health checks, then deliver the frozen resumptions after their admission deadlines. |
| `TestNativeCrossHostTakeover` | A host that has only run a secondary becomes primary on the same two isolated hosts: its real reserve moves a proxy it never had onto the dedicated gateway, it reports its ingress ready, its private links install, and its ingress then reaches both hosts. The former primary's host stands in for a remaining secondary, since a replacement with one would take a third host. |
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
