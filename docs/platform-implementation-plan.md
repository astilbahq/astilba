# Astilba platform implementation plan

Status: updated 24 October 2026. P0–P8 are implemented and the private-alpha acceptance and management update are complete. P9–P13 extend the first complete journey toward routine developer use; P14–P18 remain later increments. Paid admission still requires measured operating limits and launch settings.

Build the [architecture](platform-architecture.md) in vertical increments. Prove the execution model on disposable infrastructure before expanding the editor and service catalogue. Each milestone ends in a demonstrable user journey and the tests needed to support its claims. The [product brief](platform-product-brief.md) remains the guide to product scope.

## Milestones and dependencies

| Milestone | Result | Depends on |
| --- | --- | --- |
| P0 Technical validation | Confirm the risky library and protocol choices with disposable experiments. | Architecture review |
| P1 Application foundation | Authenticate, create a workspace and edit a versioned environment with explicit source ownership and placement. | P0 |
| P2 Server execution | Enroll a deployment server or build runner and execute a durable, observable operation. | P1 |
| P3 Visual deployment | Build and deploy an image-based web application with PostgreSQL, a binding and a domain through the canvas. | P2 |
| P4 Compose workflow | Deploy a file-authoritative Compose project and inspect it through the same product. | P3 |
| P5 Git and build workflows | Import from all three Git providers, propose configuration changes, build an exact commit and deploy its image. | P3 and P4 |
| P6 Backup and restore | Schedule backups and container jobs, and restore into a fresh target. | P3 and P4 |
| P7 Supported major upgrade | Complete a tested PostgreSQL 17 to 18 migration with explicit recovery boundaries. | P6 |
| P8 Hosted beta | Operate the same complete product as a paid management service with export and controller transfer. | P1 through P6; P7 is independent |
| P9 Operational visibility | Inspect resource history, distinguish stale data and receive actionable operational alerts. | P2, P3, P5, P6 |
| P10 Source-to-app workflow | Connect repositories, detect builds and iterate with bounded reusable caches. | P5 |
| P11 Safe application releases | Replace eligible stateless applications behind readiness checks and offer reviewed rollback. | P3, P5 |
| P12 Canvas and service catalog | Connect general service outputs and deploy a small maintained set of recipes. | P3, P4; build on P6/P7 for supported maintenance |
| P13 Automation and previews | Use scoped API/CLI/MCP access and isolated pull-request environments. | P10, P12; previews also need P11 and P14 domains |
| P14 Server and domain onboarding | Guide host preparation and provide optional generated application domains. | P2, P3 |
| P15 General scheduling | Support useful cron/interval schedules with explicit execution semantics. | P6 |
| P16 Replicas and multi-host placement | Add stateless replicas before explicit cross-host placement and routing. | P9, P11; separate storage/network design |
| P17 Interactive troubleshooting | Provide short-lived, audited container sessions and scoped troubleshooting. | P2; separate permission and transport design |
| P18 Application maintenance | Discover updates and expand tested, version-specific maintenance recipes. | P6, P7, P9, P12 |

P0 experiments are disposable and do not establish new production abstractions. Use the results to refine this architecture before scaffolding the main app. The remaining milestones can contain several small changes; each change should serve its stated platform outcome.

The first sustained use is the maintainer's own non-critical workloads after the deployment and restore checks pass. No interviews, pre-sales or commitments from prospective customers are required. P7 and P8 can proceed independently after their shared foundations: major upgrades remain a priority without delaying a reliable paid beta.

## P0 Technical validation

Validate seven concrete risks with the smallest fixtures that exercise them:

1. **Editor integration.** Render a representative graph using pinned React Flow, accessible platform controls and real property panels. Exercise dragging, connection creation, zoom, keyboard operation and saving positions separately from configuration. Use 250 nodes as a proposed stress fixture, not a promise about supported production scale. The [PLAT-1 fixture and evidence](../experiments/canvas-editor/README.md) validate React Flow 12.12.0 with React 19.3.1 and Panda 2.3.0, separate browser-local layout/draft saves, typed connections and an equivalent service-list/property path in Chromium and WebKit. The measured 250-node drag updates only the dragged custom node and no property-panel commits; local p95 frame intervals range from 16.7 to 33.3 ms in the two recorded runs. Retain the stack and state boundary. Production persistence, conflict handling and broader accessibility/device evaluation remain P1/P3 work.
2. **Durable execution.** Insert a River job transactionally with an operation. Deliver a stable command ID to a small Go agent backed by bbolt. Force a process exit after an external Docker effect but before its acknowledgement. Restart both sides and prove that the effect is discovered rather than repeated. Include worker lease loss, duplicate delivery and cancellation. The [PLAT-2 fixture](../experiments/durable-execution/README.md) records the commands, failure boundaries and checkpoint findings.
3. **Real transport.** Run enrollment, long polling, reporting, credential rotation and reconnects through the proposed HTTPS proxy. Prove that revocation rejects further requests and that one server cannot claim another's work. The [PLAT-3 fixture](../experiments/durable-execution/TRANSPORT.md) passes these boundaries through pinned Traefik, including lost command and report responses, rotation recovery and heartbeat continuity during polling and local execution. It retains the proposed 25-second poll, 15-second heartbeat and 45-second offline cutoff, with explicit request and proxy budgets; Cloudflare hosting remains a separate experiment.
4. **Container, ingress and export path.** Deploy an application and PostgreSQL on a fresh disposable Ubuntu 24.04 VM with systemd and rootful Docker using the Go SDK and Compose. Exercise Traefik HTTP and TCP routing, project isolation and invalid configuration recovery. Export the configuration and operate it with ordinary Docker tooling while management is offline. Check both amd64 and arm64 before claiming either as supported. The [PLAT-4 host fixture and evidence](../experiments/durable-execution/HOST.md) exercise the complete locked publication/recovery path, private project networks and a relocated standalone Compose export with a retained PostgreSQL volume; Native Ubuntu 24.04.5 amd64 and arm64 CI runs pass all six host scenarios with Docker 29.9.2, Compose 5.6.1, Traefik 3.7.16 and Go 1.27.3. This exact combination is the P0 baseline; broader version ranges and cross-host data transfer remain unvalidated.
5. **Build and data tools.** Build a small Dockerfile fixture locally and on a separate runner, publish the remote output to a test OCI registry and pull its exact digest on the deployment host. Evaluate Railpack separately. Dump and restore a PostgreSQL fixture into a fresh volume. Exercise tool exits, disk exhaustion and supported image layouts. The [PLAT-5 fixture](../experiments/durable-execution/BUILD_DATA.md) tests separate BuildKit stores, authenticated registry transfer, immutable deployment, bounded failure injection and a fresh PostgreSQL 18.4 logical restore. It evaluates Railpack 0.43.0 through pinned frontend/base images independently of the Dockerfile path. Local arm64 and native amd64/arm64 CI checks pass for this fixture.
6. **Optional Cloudflare hosting.** Serve the static dashboard through Workers and run the same Go management fixture in Cloudflare Containers with external PostgreSQL. Test same-origin authentication, long polling, SSE, container sleep and restart, waking pending work and secret injection without durable local files. Compare total measured compute, request and database costs against conventional container hosting at stated agent counts and reporting intervals. The [PLAT-6 fixture](../experiments/cloudflare-hosting/README.md) contains the portable Go process, thin Worker adapter and live probes. **Result: pass at the tested 0–20-agent scale**, including natural sleep, container replacement and Cron waking a pending job without client traffic. The evidence records measured process/database usage and a complete cost model; it does not establish lower prices than a small conventional host. Keep Cloudflare optional and remeasure the production workload before choosing the hosted deployment.
7. **Git and Compose edits.** Exercise minimal authenticated import and configuration pull or merge request creation against GitHub, GitLab and Gitea, including representative self-hosted installations. Test a supported canvas change against Compose fixtures containing comments, anchors and multiple files. Establish which edits can preserve source and which must use the file editor. The [PLAT-7 fixture](../experiments/git-compose/README.md) passes authenticated private-repository import, one two-file configuration request, provider merge and exact re-import on hosted GitHub, GitLab CE 19.5.1 and Gitea 28.2.0. It checks stale bases, create-only branches, explicit interpolation and secret exclusion, including removed diff lines. Local arm64 validation includes ten consecutive GitLab workflows; CI repeats the self-hosted fixtures on Ubuntu amd64. Direct image-scalar edits preserve source bytes; ambiguous targets and external-file resolution remain explicit file-editor/unsupported paths in this spike. Synthetic webhook authentication checks do not establish live hosted-webhook delivery or production replay handling.

Record exact versions, dependency licences, compatibility constraints and the resulting decisions beside the architecture. Check compatibility and redistribution requirements for the AGPL-3.0-only platform and its dependencies. Confirm the toolchain required by oapi-codegen, sqlc and the selected Go libraries. Record exact JavaScript pins when the dashboard is introduced.

**Exit criteria:** the crash test and real HTTPS transport pass; the editor works with the platform UI; the Docker baseline and supported CPU architectures are explicit; Traefik routing recovery, remote image transfer, a fresh restore and standalone Compose operation succeed. Record feasibility for all three Git connectors and source-preserving edits. River remains the default only if its open-source core meets these tests. If it cannot, compare a concrete Temporal implementation before changing the architecture. Railpack failure can leave Dockerfile-only Git deployment as the first supported build path.

Record a separate pass or fail for complete Cloudflare management hosting. A failed container experiment requires a reviewed hosting decision before P1. Keep Go as the agreed baseline while evaluating fixes to the deployment integration; any backend change must be an explicit architecture revision. Do not silently turn the Cloudflare target into static-frontend-only hosting or make Cloudflare services mandatory for everyone.

## P1 Application foundation

Create `apps/platform`, the single Go module under `services/platform`, and the OpenAPI contract under `contracts/platform`. Build a minimal dashboard shell that produces independently deployable static assets and can also be served from Go. Keep domain handlers independent of the packaging mechanism so Go unit tests do not require a frontend build.

Implement database migrations, sqlc queries, structured errors, request IDs, authentication and workspace permissions. Local accounts, sessions, TOTP, invitations and credential lifecycle use maintained primitives and explicit policies. Include a local initial-owner bootstrap and configurable SMTP; keep public registration disabled outside the later hosted signup flow.

Implement projects, logical environments, deployment units, immutable specification revisions and draft revision checks. Separate application source from configuration ownership. Permit one unit per environment initially, without storing the host as the environment's identity. Add secret versions and wrapping-key configuration before any real registry or database credentials are stored. Generate Go API types and the frontend client; validate incoming requests at runtime as well as at compile time.

Retain the repository's AGPL-3.0-only licence, contribution policy and DCO sign-off instructions without copyright assignment or special commercial relicensing rights. Preserve dependency notices. Package the corresponding source and licence notices with a clear source link for distributed and hosted versions.

**Acceptance criteria**

- A fresh installation can create its owner and workspace without an Astilba account, identity provider or billing configuration.
- Owner, operator and viewer requests obey the permission matrix. Cross-workspace IDs, guessed nested IDs and forged secret references fail through the API.
- Concurrent draft edits produce a visible conflict and preserve both users' submitted input for review. A layout edit does not change the deployment draft.
- Login, logout, session rotation, CSRF checks, invitation expiry, MFA and recovery code reuse have direct behaviour tests. Hosted accounts able to change servers require MFA; self-hosted enforcement follows the owner's policy. An unauthenticated bootstrap request cannot seize an installed instance.
- Secret values survive a database round trip without plaintext in stored specifications, audit events or API responses. Wrong-key and swapped-ciphertext tests fail safely.
- Generation is deterministic; regenerating OpenAPI bindings and sqlc output produces no diff. The production image serves the app without Node installed.
- The same dashboard works when bundled with Go or served from Workers with a same-origin API proxy. An ordinary self-hosted installation requires no Cloudflare binding, account or API call.

Add scoped `AGENTS.md` instructions and extend `make verify` to cover the new application surfaces.

The P1 implementation and reproducible checks live in the [application guide](../services/platform/README.md). Database tests exercise bootstrap and credential races, membership/tenant boundaries, immutable drafts, secret storage and layout separation. Browser checks use the real API and PostgreSQL through embedded Go assets and a local same-origin Worker. The container smoke test verifies non-root operation with a read-only filesystem and no Node runtime. These foundations do not establish production deployment readiness or a new live Cloudflare hosting pass.

## P2 Server execution

Implement server enrollment with deployment, build or combined roles, credential rotation and revocation, capability negotiation, bounded HTTPS polling, heartbeat and result reporting. Add host preflight checks and an agent journal with one active process per state directory. Distinguish a server being offline from workloads being unhealthy.

Provide a verified agent download, enrollment instructions and a systemd unit on the supported host image. Detect existing Docker and ingress installations before offering any required host changes.

Implement plans, operations, steps, transactional River enqueueing and typed agent commands. Start with pulling and starting a small test container, inspecting it and removing only its owned disposable resources. Add operation generation checks, per-environment serialization and host-level mutation coordination.

**Acceptance criteria**

- An enrollment token expires, can be used only once and cannot enroll a different server. Rotation interrupted at each exchange boundary can recover without accepting an indefinitely valid old credential.
- Duplicate commands converge to one recorded operation and one intended effect. Two workers or overlapping polls cannot execute conflicting generations.
- Restart after each durable checkpoint, including the gap between an external effect and acknowledgement, resumes through inspection or enters `needs_attention` with actionable information.
- Management loss and agent restart leave existing containers running. An accepted operation reports its final outcome after reconnecting.
- Closing the browser or timing out a polling request does not cancel an accepted operation. Only an explicit cancellation request follows its safe-stop policy.
- An agent cannot read another server's commands, spoof its reports, select its own workspace or call browser administration endpoints. A build-only identity cannot receive runtime production secrets or perform deployment operations.
- Log spooling, request bodies, polling timeouts and concurrent commands have enforced limits. A slow dashboard does not block execution.
- Cleanup cannot remove an unlabelled container, network or volume. Existing workloads remain unmanaged. Port conflicts with an existing deployment tool are reported before ingress changes.
- An explicit dashboard action can update a compatible agent from a verified artifact while protecting active operations. An incompatible or interrupted update leaves an actionable state and existing workloads running. A local update path remains available.

Production implementation: `services/platform/internal/execution` owns server credentials, immutable probe plans and transactional River dispatch. `internal/agent` journals and reconciles the restricted probe recipe. The [agent guide](agent.md) records installation, resource limits, recovery and explicit signed updates. `make verify-agent-host` is the native systemd/update gate on each supported architecture. P2 does not install ingress, run Compose, fetch Git sources or expose an interactive shell.

## P3 Visual deployment

Implement the initial visual specification and planner: container applications, workers, PostgreSQL, volumes, variable bindings, health checks and HTTP or TCP routes. Resolve image identities, deployment-unit placement and secret versions into frozen plans. Use Compose as the execution representation for the initial visual environment.

Build the canvas and its equivalent service-list view. Add resource property panels, connection validation, staged changes, plan review, operation progress, recent logs and basic resource observations. Use TanStack Query for remote state; keep editor interaction state local and SSE updates as invalidation hints.

Add the deployment-unit network, private database and TCP defaults and Traefik route registry. Provide ordinary route forms and restricted project-scoped advanced configuration. Reserve host-wide settings for owners. Preserve routes belonging to other environments on the same host. Implement configuration export while its source model is still small.

**Demonstration:** enroll a server, add a web image and PostgreSQL, bind `DATABASE_URL`, attach a volume, configure a domain, review the effects and deploy. Change a variable and deploy a second revision, then inspect both attempts.

**Acceptance criteria**

- A card move, selection change or page reload cannot deploy or restart a service. Bindings create the expected concrete environment values without revealing their secret components in plan previews.
- The API rejects invalid mount targets, mismatched connection types, invalid readiness dependencies, duplicate domains and references to another environment.
- Changing an image, secret version or draft after review invalidates the saved plan. Repeating apply with the same idempotency key returns the same operation.
- A failed deployment preserves its diagnostics and truthfully reports which resources and versions are running, including partial changes. Data migrations cannot trigger an unsafe automatic image rollback.
- Concurrent deployments in two environments cannot remove each other's routes. Rejected configuration leaves the running route registry unchanged. Exercise runtime reload errors and verify recovery of previous routes; never report success merely because a configuration file was written.
- Advanced configuration cannot reference another project's backend, arbitrary host files, the proxy's internal API or forbidden providers. Reject templates and test namespace and reference rewriting. HTTP and TCP endpoints respect their domain, port, access and TLS policy.
- Service deletion retains data by default. Explicit volume deletion shows its consequences and requires the corresponding permission and recent authentication.
- A manual Docker change is reported as drift. Merely opening the dashboard cannot overwrite it or remove unrelated resources.
- Cloning a visual environment creates independent configuration and fresh runtime identities and storage. Secret copying and data restoration are explicit choices; internal bindings cannot silently retain the production target.
- Keyboard and screen-reader users can add, configure, connect, review and deploy without dragging. Light and dark themes, focus and reduced motion meet the existing WCAG 2.2 AA product target.
- An exported fixture runs with documented Docker commands after the management application is stopped. Export leaves secret placeholders and preserves the expected service, network and storage mappings.

## P4 Compose workflow

Add file-authoritative environments, the source bundle format, CodeMirror and compose-go validation. Preserve the submitted files and map errors back to their locations. Derive an inspectable graph and map supported canvas edits to precise source changes with a file diff. Leave unsupported edits to the file editor without rewriting unrelated source.

Expose the same deployment review, logs, operation history and storage inspection as visual environments. Keep backups attached to stable runtime resource identities, with warnings when a source edit removes or renames a referenced resource.

Represent Git-backed edits as a draft based on an exact source commit, ready for the provider integration in P5. Batch changes into one explicit submission. Keep a Compose project as one deployment unit; switching ownership modes or splitting it into multiple units requires a later conversion workflow.

**Acceptance criteria**

- A multi-service Compose fixture deploys from its original bundle. Export retains file contents, supported extensions and included supporting files.
- Relative paths resolve inside the submitted bundle. Parent traversal, symlink escape and unapproved remote inclusion fail before execution.
- Interpolation uses supplied values only. A matching variable in the management process cannot accidentally alter a deployment.
- Elevated host permissions are surfaced in the reviewed plan; unsupported features fail with a precise error rather than disappearing on save.
- Supported canvas edits produce the expected minimal source diff while preserving comments, anchors, extensions and untouched files. Ambiguous changes fail with a location and a path to file editing; they cannot silently flatten or drop source.
- File changes appear in the derived graph, while node movement never rewrites source. Ordinary save cannot switch configuration ownership or split a Compose project across servers.
- Restart and stale-plan tests exercise this path through the same operation engine as visual deployments.

## P5 Git and build workflows

Implement GitHub, GitLab and Gitea connectors, scoped repository selection, provider-specific webhook validation, commit resolution and runner-side cloning. Include configured self-hosted GitLab and Gitea instances. Self-hosted operators supply their own provider credentials without contacting Astilba. Keep credentials and short-lived tokens out of build logs and source exports.

Submit a Git-managed configuration draft as one pull or merge request, then direct the user to the provider for review and merge. Refresh the authoritative source after merge. Detect remote branch changes and conflicts without overwriting them. Runtime secret values must never enter a branch, diff or generated request.

Build Dockerfile repositories using BuildKit on the explicitly selected eligible server or runner. Add Railpack only after its P0 outcome supports the additional path. Surface stages, queue status, cache use, resource failures and the resulting immutable image identity. Queue work when a runner is busy or offline; use a fallback only when explicitly configured. Keep builds separate from deployment approval and make automatic deployment opt-in per environment.

Support customer-configured OCI registries for remote builds, scoped push and pull credentials and deployment by exact digest. Same-host builds can remain local. Promote the same built artifact between environments with destination runtime settings; changed build-time inputs require a new build. Do not add paid shared builders or an Astilba-operated registry in this milestone.

**Acceptance criteria**

- The connector suite exercises import, triggers and configuration pull or merge requests for all three providers. A public and private repository build at their selected commit; a branch moving during a build does not change the source being deployed.
- Batched Git-managed edits create one reviewable request and no deployment before merge. Merge updates the source, then follows the explicit deployment policy. Concurrent remote changes preserve the draft and report a conflict.
- Replayed, reordered or forged webhooks cannot create unbounded or unauthorised deployments. An untrusted fork cannot obtain a private repository's secrets.
- Build secrets do not enter image layers, normal build arguments, logs or exported source bundles in the supported fixtures.
- Declared build secrets are isolated from runtime secret values, and generated configuration commits contain references only. A build-only runner cannot request another runner's work or acquire production deployment credentials.
- An unavailable runner leaves its build queued without starting work on a deployment server. Explicit fallback selection and cancellation are recorded. An expired or cancelled assignment cannot later publish a result as the active build.
- A remote build publishes an artifact that the deployment agent pulls by digest. Wrong architecture, registry authorization failure and interrupted upload fail before replacing the running application. Same-host local builds do not require registry credentials.
- Promotion preserves the built image across environments and resolves each environment's runtime settings. A changed build input cannot be labelled promotion of the original artifact.
- Build cancellation and disk exhaustion preserve active workloads. Cleanup removes only eligible build artifacts and respects storage needed by databases.
- A failed build leaves the applied deployment untouched. A successful build followed by a changed plan requires a fresh review.
- The resulting application joins the same binding, domain, export and operation workflows as an image-based application.

The production workflow and supported limits are recorded in [Git and build workflows](git-builds.md). Dockerfile builds use pinned BuildKit on customer runners; Production Railpack is delivered separately in P10 after its narrow P0 fixture. Provider API/signature fixtures cover GitHub, GitLab and Gitea, while native runner checks cover public/private exact-commit checkout, review-response recovery, local images, TLS registry transfer and bounded build failure. These checks do not claim a live hosted GitHub pass.

## P6 Backup and restore

Implement reusable S3-compatible and filesystem destinations, connection checks, encrypted repository credentials, policy-specific restic repositories and native PostgreSQL logical backups. Add supported quiesced file-volume backups.

Persist backup policies and schedules on the agent. Show retention, next run, last successful snapshot, consistency method, failure and restore verification. Implement restore into a fresh resource and a separate reviewed cutover. Use the same persisted scheduling mechanism for scheduled container jobs, with visible run history, no overlap and an explicit missed-run policy.

**Demonstration:** write representative data, back it up to an external destination, restore a second database and verify its data through the application. Stop management and demonstrate a locally scheduled backup completing.

**Acceptance criteria**

- Snapshot contents restore without access to the management database, given the documented repository credentials, keys and manifest.
- Interrupted uploads, unavailable storage, invalid credentials, full disks and expired operations report failure without marking an incomplete backup usable.
- Retention cannot prune another policy's snapshots, the only verified backup after a failed run, or a backup protected by an active restore or upgrade.
- Native backup fixtures verify roles, schema, extensions and actual records. A generic copy of live database files is never labelled a consistent backup.
- Schedule tests cover restart, reconnect, overlap, timezone changes, DST and forward or backward clock changes. Duplicate reports do not duplicate runs.
- A scheduled container job completes while management is offline, records its exit result and follows its skip-missed-runs policy after an agent restart.
- Restore failure leaves the existing application and data intact. Insufficient capacity for a fresh restore blocks the operation before destructive work. A successful restore shows a distinct verification result before explicit cutover; no in-place restore path is offered.
- Source and destination ownership are rechecked at execution time. Logging and exports reveal neither storage credentials nor repository keys.

The production workflow and supported recipes are recorded in [Backups, scheduled jobs and fresh restores](backups.md). The agent owns durable schedules and per-policy restic repositories; PostgreSQL and quiesced file restores always target new volumes. Native fixtures cover filesystem and S3-compatible storage, PostgreSQL records/schema/roles/extensions, file metadata, offline jobs and interrupted execution. API and browser fixtures cover tenant scoping, encrypted credentials, report replay and a separate cutover draft. Only the documented recipes are supported; these checks do not establish application-wide transactional consistency or a generic database upgrade path.

Completing P5 and P6, including the repository and Compose demonstrations, satisfies the first full product journey in the brief and permits a private alpha using the maintainer's non-critical workloads. Record restore evidence and operational limitations rather than implying that every deployed application is supported. External interviews and purchase commitments are not entry or exit criteria.

## P7 Supported major upgrade

Implement the PostgreSQL 17 to 18 logical migration recipe described in the architecture. This milestone can follow the paid beta and is not one of its prerequisites. Establish exact supported source and target image digests, data layouts, extensions and client tool versions. Reject unsupported source instances with specific preconditions instead of offering a generic upgrade button.

Add the upgrade plan UI: prerequisites, expected downtime, disk requirement, backup, ordered steps, cutover and the boundary after which returning to the old volume would lose new writes. Protect retained volumes and snapshots from cleanup.

**Acceptance criteria**

- A complete fixture migrates representative records, constraints, roles and supported extensions, and the application passes functional checks afterward.
- Each step has a restart and lost-acknowledgement scenario. A step with an unverifiable outcome pauses rather than being blindly repeated.
- Unsupported versions, insufficient disk, unknown writers, extension mismatch, failed backup and failed restore block cutover.
- Failures before new writes can restore the old service. Tests after new writes prove that an old-volume restart is not presented as lossless rollback.
- A failed route or binding switch is reconciled with actual state before any writer resumes. The operation history identifies what is serving traffic.
- Preserved data is deleted only by an explicit later action. The documented manual completion path works with ordinary PostgreSQL and Docker tools.

The production recipe and recovery boundary are described in [Supported PostgreSQL major upgrades](supported-upgrades.md). Additional application recipes are separate later increments, each with its own compatibility matrix and data assertions.

## P8 Hosted beta

Operator installation and hosting guidance: [Hosted beta and self-hosted installations](hosting.md).

Add hosted signup, verified account email, workspace onboarding and the optional Stripe integration. Keep subscription state outside core feature selection. Use monthly workspace subscriptions with stated server allowances and no per-application, per-database or per-member fee. Define actual prices, operational quotas and retention from measured storage and execution costs. Start with an invitation-only cohort of approximately five workspaces, publish asynchronous support hours and distinguish platform support from operation of customer hosts and application code.

Package a documented self-hosted installation using the same artifacts. Include HTTPS setup, explicit database migration, installation backup and restore, dashboard-initiated agent updates, server disconnect and controller transfer. Test installation from a clean machine rather than only the development checkout. Include licence notices and accessible corresponding source for the exact hosted and distributed version.

Document the Cloudflare Containers deployment adapter if P0 validated it, alongside the portable installation. Keep custom-domain integration optional. Choose the paid hosting arrangement using measured operating costs, without changing the self-hosted feature set.

**Acceptance criteria**

- Every supported product capability works in a self-hosted installation without a licence key, billing service or Astilba-hosted API.
- Tenant isolation tests cover API reads and writes, logs, events, operation dispatch, secrets, backups, builds and webhook processing.
- Billing webhook replay and subscription changes cannot duplicate charges in our processing or cause destructive actions on a customer server.
- Management downtime leaves customer applications and installed schedules running. Restart reconciles operation history and current runtime state.
- At subscription expiry, new management changes stop, active operations stop safely and export, recovery and transfer remain available for 30 days. A notification precedes hosted workspace deletion. Running workloads, volumes and installed schedules remain on customer servers; queued work cannot bypass expiry restrictions.
- A host owner can transfer a server between hosted and self-hosted management without redeploying its workloads or moving its volumes. Encrypted secret transfer and schedule identity preservation work; the old controller is revoked and fenced before new commands are admitted. Stale pending commands cannot execute after transfer.
- Restore the management database and wrapping keys into a clean installation; reconnect agents and recover secret access without executing stale work.
- Backpressure and retention keep a representative fleet within measured resource budgets. Publish only the capacity and availability commitments supported by those measurements.
- Complete deployment, all three Git provider workflows, local and remote builds, backup, verified restore and export from the production UI. Guided upgrades are required only if offered as a supported capability. Run the full repository verification before proposing the release change.

## P9 Operational visibility

Deliver three independently reviewable increments: resource history and disk pressure; operational alerts; bounded redacted log export. Observe host and managed-service CPU/memory and host disk capacity without collecting application payloads, secrets, process arguments or arbitrary labels. Collection runs through the outbound agent connection and never waits for dashboard traffic. An unavailable collector must not block heartbeats, deployment or local schedules.

Start resource history with one accepted sample per server per minute, a rolling 24-hour window and at most 40 managed services per sample. Show gaps and stale or unsupported collection explicitly. The initial disk indicator reports the Docker data-root filesystem, falling back to the agent filesystem only when Docker's root is unavailable and labelling that fallback. It is a capacity warning, not an automatic cleanup action. Keep older agents usable and new agents able to talk to older controllers.

Add durable alerts for host disconnection, failed deployment/build/backup, unhealthy managed services and sustained disk pressure. Deduplicate transitions, show recovery and acknowledge status, and bound retries and history. Begin with an in-product inbox and verified-owner email through the existing mailer; add optional webhook delivery with explicit destination validation before claiming integration support. Notifications contain resource identities and safe summaries, never captured logs or secrets. Define which alerts can be detected during management downtime; do not imply an external uptime guarantee.

Log export reuses resource authorization and secret redaction, caps volume and duration, and reports truncation. Historical metrics and diagnostic logs are disposable observations, excluded from workspace transfer; configured alert policies require explicit transfer semantics before export support.

**Acceptance criteria**

- Cross-workspace reads and cross-server metric submissions fail; only registered resources on the authenticated server enter service history.
- Duplicate and excessive reports cannot grow history beyond the per-server sampling bound. Retention and query limits remain bounded under the representative fleet fixture.
- Missing, stale, truncated and failed observations remain distinguishable from zero usage and healthy state. Charts have equivalent textual data and keyboard controls.
- Collection remains read-only and bounded during container churn, Docker failure and slow stats reads; old/new agent-controller combinations retain deployment compatibility.
- Alert failures do not block operations; repeated delivery and restarts do not create unbounded duplicates. Test recovery, suppression and recipient authorization.
- A disposable native-host journey exercises collection and the dashboard; integration tests cover isolation, credential revocation, expiry and retention.

## P10 Source-to-app workflow

Add GitHub App installation and repository selection while preserving the existing GitLab/Gitea and manually scoped credential paths. Add production Railpack only with a documented supported-language matrix, reproducible image pins and build-secret isolation. Retain Dockerfile builds and explicit overrides.

Deliver this milestone in reviewable changes: build resource budgets and monorepo triggers; bounded project-scoped caches; a validated Railpack path; and GitHub App installation/repository onboarding. Keep the milestone open until all four pass their acceptance checks.

Introduce per-project or per-trust-domain reusable BuildKit caches with bounded storage and safe eviction, configurable build resource budgets, and monorepo watch paths. Prove cache isolation, cancellation, changed input invalidation and exact-commit behavior. Use a representative existing project to measure onboarding and repeated-build latency; no cache hit or universal framework support is promised.

Build resource budgets and monorepo triggers are implemented. Bounded project layer caches add runner-local OCI exports, compatibility keys, fixed retention/eviction limits and native BuildKit reuse/invalidation checks; see [Git and build workflows](git-builds.md#reusable-layer-caches). Production Railpack adds an explicit builder choice, source-preserving command overrides, version-negotiated runners, journaled prepared plans and pinned image/tool inputs. Its supported Node/npm, static Go and shell matrix and limits are documented in [Git and builds](git-builds.md#railpack-builds). GitHub App onboarding remains the final separate increment.

## P11 Safe application releases

Start with explicitly eligible stateless HTTP services. Create revision-specific instances, wait for meaningful readiness, atomically switch routes and drain the old revision. A failed replacement leaves the serving revision available. Persist traffic-switch and cleanup state so interruption cannot silently choose the wrong revision.

Retain immutable images and effective configuration for an explicit rollback plan. Show changed settings and image availability before rollback. Add bounded pre-deployment commands with explicit failure and uncertain-outcome semantics. Database writes, schema migrations, mounted storage, fixed host ports and background workers need separate compatibility decisions; image rollback must never claim to rewind data.

## P12 Canvas and service catalog

Extend PostgreSQL-specific bindings into typed service outputs and references, including internal addresses, ports and generated connection values. Preserve secret provenance, cycle detection, revision hashing, export and source ownership. Existing shared variables remain supported.

Add a small maintained catalog, initially approximately 5–10 recipes, plus selected cache/database types where useful. Validate every recipe against the supported Compose subset. For each recipe publish separate deployment, binding, backup, restore and upgrade support; an arbitrary image running successfully does not establish maintenance support. Avoid importing a large unverified catalog or rewriting Git-owned source behind the user's back.

## P13 Automation and previews

Introduce revocable, scoped automation credentials with explicit workspace/environment capabilities and audit attribution. A general CLI and MCP adapter use the same API admission, plan/apply separation and redaction as the dashboard. They do not automate password/MFA entry or bypass owner-only grants. Keep secrets out of output by default.

Then add isolated pull-request environments with explicit fork approval, independent secrets/storage, optional generated domains, resource and concurrency limits, and idempotent cleanup on closure. A preview cannot inherit production secrets or address a production resource. GitHub App onboarding, useful build caching, generic bindings and stateless release handling are prerequisites. Verify webhook replay, reopened PRs, offline agents and cleanup after lost acknowledgements.

## P14–P18 Later increments

- **P14 Server/domain onboarding:** a reviewable supported-host bootstrap, signed agent enrollment and optional per-installation wildcard application domains. Preserve outbound connections and explicit approval of host/network changes; no mandatory DNS provider.
- **P15 General scheduling:** useful cron/interval expressions, with documented timezone/DST, overlap, timeout and missed-run semantics that continue when management is offline.
- **P16 Replicas and placement:** begin with stateless same-host replicas. Define cross-host routing, private networking, storage restrictions and failure domains before multi-host scheduling. Multiple enrolled servers alone do not provide high availability; Kubernetes remains a separate scope decision.
- **P17 Troubleshooting:** dedicated short-lived resource-scoped container sessions, permissions and audit. Add filesystem access or private-service tunnels only with explicit boundaries; do not place arbitrary shell commands in the deployment API.
- **P18 Maintenance:** update discovery and review windows, then additional tested version-specific upgrade recipes. Preserve backup, verified restore and new-write boundaries. Do not label an arbitrary image-tag change a supported major upgrade.

Prioritize P9, the source-to-app journey and safe stateless releases alongside actual non-critical use. The later parity milestones do not all become prerequisites for the first limited paid cohort. Record measured cost and capacity before setting allowances, pricing or availability commitments.

## Verification strategy

| Layer | Required evidence |
| --- | --- |
| Pure domain logic | Deterministic validation, plan diffs, binding resolution, permission decisions and recipe preconditions. |
| Database | Real PostgreSQL migrations, tenant scoping, environment and placement identities, revision conflicts, operation uniqueness and transactional enqueueing. |
| Agent | Real Docker effects, process restart, duplicate commands, stale generations, ownership and bounded resource use. |
| HTTP and contracts | Runtime input validation, generated client compatibility, auth, CSRF, credential lifecycle and denied actions. |
| Git and builds | All three provider adapters, source-preserving changes, webhook replay, runner isolation, explicit fallback and digest-based artifact transfer. |
| Ingress | HTTP and TCP routing, private defaults, advanced configuration restrictions, cross-project references and reload recovery. |
| Browser | Playwright journeys, keyboard equivalents, accessibility, plan review and reconnect behaviour. |
| Data maintenance | Real dump, restore, upgrade fixtures and failure injection using disposable data. |
| Distribution | Clean installation, corresponding source, standalone export, management restore, controller transfer and supported agent compatibility. |

Run Go formatting, vet, static analysis and tests with the race detector where supported. Test selected input parsers and plan invariants with fuzzing. Use Vitest for frontend behaviour and Playwright with axe for browser contracts. Unit tests should not reproduce an implementation line by line; exercise observable guarantees and meaningful failure cases.

Use fake clocks, controlled identifiers and explicit injected inputs for domain tests. Production credentials use a cryptographically secure random source. Keep all destructive integration work on explicitly created disposable resources; never point the harness at existing deployments or real customer data.

Expose fast local checks and one complete `make verify` entry point, retain exact dependency and action pins, and extend CI without relaxing existing gates. Generated-code checks must fail on stale output. Run `make verify` before proposing a commit, as required by the root instructions.

## Current implementation increment

P0–P8 and private-alpha acceptance establish the implementation baseline. P9 includes resource history, disk-pressure visibility, [operational alerts](operational-alerts.md) and [bounded retained-log export](operational-visibility.md#export-retained-logs). Log export preserves the existing sampled data and redaction boundaries; it does not introduce a full log archive. P10 source-to-app workflow is the next product increment. Keep later parity work at milestone level until its prerequisites pass. The disposable experiments remain evidence, not production scaffolding.
