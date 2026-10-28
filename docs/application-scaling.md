# Application replicas

Visual web applications can run 1–10 instances on their environment's assigned server, with at most 40 desired instances per visual unit. Replicas distribute HTTP requests and tolerate an individual unhealthy instance; they do not survive the loss of that server or its ingress. There is no autoscaler.

An application with multiple instances needs an HTTP route and readiness check. It cannot mount storage, declare image volumes, publish TCP, or supply internal bindings/readiness dependencies to another application. PostgreSQL, workers, Compose-authoritative projects and pull-request previews keep their existing single-instance behavior. Each replica receives the same configuration and secrets. Keep sessions, uploaded files and other shared state outside the containers.

## Review and apply

Stage the count in the visual application inspector, save the draft and prepare a deployment plan. The review shows desired counts and the chosen deployment strategy. Exactly one eligible effective application change uses the [protected release](application-releases.md) workflow: start a complete replacement pool, run the release command once, require readiness from every member, verify the proxy switch, then drain and stop the old pool. Scale-down also replaces the complete pool. Budget capacity for both old and new pools during a release.

Initial deployment, shared changes, multiple changed applications and drift reconciliation use the standard deployment path, which can restart services and does not protect serving continuity. Requiring protected release options rejects an incompatible change. A missing agent capability never falls back to a single instance.

Traefik sends traffic to private instance endpoints and checks the declared HTTP readiness path every five seconds with a two-second timeout. Health convergence takes time and does not guarantee that every failed request is avoided. A recovered instance rejoins the pool. The dashboard shows individual instance states under one logical application; resource usage sums installed active instances. Missing samples remain unknown/partial rather than reporting one member's usage as the total.

Rollback restores the historical desired count, effective image and configuration into a new reviewed draft and replacement pool. It does not restore external data. After draining, only one stopped primary per retired generation pins its image, with the existing two-generation retention limit; retired extra instances are removed without volume or daemon pruning.

## Compatibility, export and recovery

Replica plans use visual recipe 3 and require the negotiated `replicas` and `safeReleases` agent capabilities. Existing singleton plans retain their encoding and hashes. Once a unit uses recipe 3, subsequent plans retain it even after scaling to one instance. Workspace archive version 4 preserves the complete inventory and imports versions 1–3; older controllers reject version 4.

Every routed service in a recipe-3 unit uses the new backend-vector encoding, including singleton databases. Pending publication uses a two-snapshot tuple that older journal readers reject, including the first transition from legacy or empty routes. Previous agents therefore refuse publication before changing the host registry, including when operating on an unrelated legacy unit or recovering pending routes. Workloads can keep running during a downgrade, but host routing mutations are blocked. Restore a compatible agent to resume management; restoring the previous executable alone does not make the journal compatible. Keep its key and journal intact.

Compose/route export includes every frozen instance and health check. Controller transfer preserves active runtime identities and operation fences without deploying implicitly. Backup and supported-upgrade writer quiescence includes every active replica.

## Cross-host placement: next stage

Cross-host execution is not implemented. Logical environment/application identity stays separate from deployment units, which own server placement, local volumes, private endpoints and operation fences. A Compose project remains one unit on one host. Placement must be explicit rather than inferred from a canvas connection.

The [proposed cross-host design](cross-host-placement.md) covers private-network enrollment and revocation, authenticated endpoint discovery, ingress placement, partition behavior, failure domains and capacity admission. Installed private links remain available during management outages; unacknowledged revocation remains visibly incomplete. Each host needs compatible immutable images/artifacts. Never expose a private database to make a binding work; replicas do not imply database replication or live volume migration.

Multi-server changes cannot promise an atomic transaction. Plans must identify every old/new placement, retain known serving pools when coordination is uncertain and expose reconciliation. Host loss cannot authorize unfenced writers or automatic stateful relocation. A single front door and DNS remain failure boundaries until a separately implemented routing design removes them. Kubernetes remains a separate scope decision.

## Verification

Disposable native acceptance exercises HTTP distribution, health exclusion/recovery, replacement pools, interrupted publication/commit, failed candidates, rollback and group writer quiescence. API/PostgreSQL checks cover capability loss before apply/redelivery and exact successful inventory. Chromium and WebKit exercise staging, failed/conflicting saves, plan review and per-instance status. The preceding agent's actual routing code is also checked against a mixed legacy/replica registry and pending recovery. These checks do not deploy the live alpha.
