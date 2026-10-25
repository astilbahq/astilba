# Application releases

A deployment plan selects a protected release automatically when the assigned agent supports `safeReleases` and exactly one effective stateless HTTP application changes. Effective identity includes resolved image digests and artifact identities: moving a mutable tag counts even without a saved configuration edit. Initial creation has no previous serving revision to protect.

Both versions must have an HTTP readiness check and an existing managed HTTP route. Shared configuration, other applications and their resolved images must stay unchanged. Compose projects, mounted applications, image-declared volumes, workers, database replacements, applications with internal dependants, host changes and drift reconciliation use the standard reviewed deployment path. A fixed TCP listener cannot target the replaced application. Review the strategy and effects before applying any plan. Requiring protected release options rejects an incompatible change rather than silently falling back.

## Candidate and traffic

The candidate has its own container, Compose project, Compose service key and private DNS identity; logical application IDs remain stable. It joins the existing owned unit network without publishing another host port. The serving instance remains available while the candidate starts (after the optional release command) and its mandatory HTTP readiness check runs. A failed check vetoes the switch, removes only the owned candidate and leaves the previous revision serving.

The encrypted host journal records candidate, command, switch and drain state. Traefik's atomic file update and runtime acknowledgement precede the active-instance journal commit. That commit stores the effective plan and observed fingerprints with the switch phase in one transaction. Pending route recovery reuses an unchanged proxy. A restarted agent reconciles these states with both containers retained; it does not guess which container can be deleted.

After verified switching, existing connections have the reviewed drain budget (1–120 seconds; default 10), followed by a graceful stop with a 10-second termination timeout. Requests exceeding those bounds can be interrupted. Cancellation before switch intent removes the candidate. After durable switch intent, cancellation completes reconciliation and drain, so reports identify the revision actually serving.

## Release command

Protected release options may include an explicit argument array, up to 32 arguments of 4096 bytes each. No implicit shell is added. The target image runs the command before candidate startup in a separate container with the application's effective environment and private network, no persistent mounts, a read-only root filesystem and a 64 MiB `/tmp`. Its limits are one CPU, 256 MiB memory, 64 processes and a rotating 1 MiB log. The reviewed timeout is 1–300 seconds (default 60).

The agent persists start intent and an absolute deadline before starting the command. Accepted work retries transient local failures independently of management polling, so a controller outage does not suspend command reconciliation. A restart inspects that same container rather than launching another. Missing or merely created containers after an ambiguous start require inspection; nonzero exit, memory termination or completion after the deadline vetoes traffic switching. The agent enforces the timeout while running; during an agent or Docker-control outage a command may continue until reconciliation. Its external writes remain uncertain and are never declared rolled back.

For `needs_attention`, inspect the operation and host before using the documented local `astilba-agent retry --operation` workflow. To abandon a pre-switch command, request cancellation and reconcile locally; command effects may already exist. Do not erase the journal. Post-switch reconciliation retains both instances until routing and active state are known.

## History and rollback

The dashboard lists the latest 20 successfully applied deployment plans. Compatible targets can create a new rollback draft, attributed to the requesting operator. This requires an unchanged applied draft, the full workspace/project/environment scope and a supported agent. A stale base revision fails rather than overwriting another edit. Shared and other-application identities must still be compatible with the historical target.

The draft restores one application's historical configuration, immutable image and secret-version references. It does not deploy, rebuild Git source, copy a previous release command or restore database data. Review the effective configuration and prepare a new protected plan to check image availability before applying. Unavailable local artifacts or registry images fail before traffic changes. API clients should require protected release options for rollback plans as the dashboard does.

Two inactive stopped instances per application pin recent image references while that application remains managed. This is bounded retention, not a permanent image archive. Cleanup verifies ownership and stopped state and never deletes persistent volumes or prunes the daemon. Normal deployment, backup, upgrade, metrics and export paths use the active instance mapping; retained candidates are not separate logical applications. Controller transfer retains these journal identities and hashes. Downgrading to an agent without instance support blocks new instance-bearing plans.

## Verification

The native visual fixture samples HTTP independently through failed readiness, command failure, traffic-publication interruptions and durable-switch interruptions, then exercises retained-image rollback and ordinary reconciliation. The same application and PostgreSQL unit enters the existing major-upgrade and restore checks. API tests cover resolved-image selection, idempotency, nested scope, stale rollback drafts and capability downgrade; real browser journeys cover review, options and rollback draft creation. These are disposable tests, not a live-alpha rollout.
