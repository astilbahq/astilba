# Visual deployments (P3)

Visual environments run container images on one enrolled Ubuntu 24.04 server, using Docker Compose 5.6.1. The agent reports this capability separately from its connection status. Git builds and source-preserving Compose environments have separate milestones.

## Deploy an environment

1. Enroll a deployment or combined-role server. Install the supported Compose plugin locally; the agent does not install Docker or adopt existing workloads.
2. Create a project and environment. Add web applications, workers or PostgreSQL from the visual editor or its equivalent service list. Create database password secrets first. PostgreSQL uses the pinned PostgreSQL 18 image and receives a dedicated data volume.
3. Configure image references, variables, volume attachments and readiness checks in the property forms. Connecting PostgreSQL to an application supplies a `DATABASE_URL` and makes database readiness a prerequisite. Plain variables and immutable secret-version references are distinct inputs.
4. Add HTTP routes or explicit TCP listeners. TCP requires a client-network allowlist; databases otherwise remain private. Managed HTTP certificates require public DNS and a certificate contact configured by the host operator. Unencrypted endpoints require an explicit policy selection.
5. Save the configuration revision. Prepare a plan, review the server, resolved image digests and effects, then apply it. Owners with recent authentication approve new ingress ports and host certificate settings. Preparing a plan only resolves images and observes the host.

Moving or selecting a card never deploys. Canvas positions have their own save action, independent of configuration revisions. The advanced JSON editor remains available for inspecting or editing the same specification. Unsaved edits survive rejected saves and revision conflicts.

Plans expire after fifteen minutes. A changed draft, placement, applied revision or observed runtime state invalidates admission. Repeated application with the same request key returns the same operation. A unit supports twenty services and one hundred distinct secret references. Larger payloads are rejected before admission.

## Observe and recover

Operations persist in PostgreSQL and the agent's encrypted journal. The agent serializes host mutations and checkpoints each phase. It resumes interrupted work from the journal; closing the dashboard does not cancel it. Cancellation stops at a safe checkpoint and retains existing data and any completed effects.

Runtime observations show the actual image, state, memory sample and bounded recent logs for up to forty services, prioritizing the reviewed configuration. Additional retained services remain included in drift checks and can be removed by a reviewed reconciliation. Known environment values are redacted from logs. Treat application output as potentially sensitive: applications may log independently encoded credentials. Observations compare container configuration and the owned proxy against the last successful deployment. Reading the dashboard does not reconcile drift. An explicit reviewed reconciliation is required.

There is no automatic application-image or database rollback. A partially failed deployment can contain both old and new resources; its observations and operation steps explain the remaining state. PostgreSQL initialization settings are reserved against the volume at first deployment admission and remain fixed after cancellation or service removal: changing environment variables does not rotate an initialized database's credentials. Restore into a new volume or perform an explicit database migration outside this milestone.

The agent publishes a complete host route registry and preserves other environments' entries. It writes the registry atomically, verifies its revision and enabled routes through Traefik's private runtime API, and verifies trusted certificates for managed HTTP TLS. A rejected publication restores and verifies the previous registry. Recovery failure remains `needs_attention`; writing a file alone is never success. The management API is bound to container loopback and is not published.

Inspect a `needs_attention` operation locally before using the retry procedure in [the agent guide](agent.md#observe-and-recover). Never delete the journal to clear an operation.

## Data, clones and exports

Deleting a service retains its named volumes. Owners can explicitly delete an unused retained volume after recent authentication and a destructive confirmation. The agent checks ownership and refuses in-use volumes. Existing user containers, networks and volumes remain unmanaged.

Clones receive new environment, unit, service, route and volume identities. Database bindings target the new database. By default, ordinary secret-backed variables are omitted and PostgreSQL receives a fresh random password. Copying secret values requires an explicit choice and recent authentication. Clones do not copy data, and their domains use `.invalid` until changed.

Cloned placement references the new services and retains the selected workspace network and servers. A fresh clone still requires its initial deployment on the primary server before cross-host placement can be applied.

Export uses the last successfully applied plan and produces `compose.json`, `routes.yaml` and instructions. Secret values become required placeholders, including a separate percent-encoded database-password placeholder for connection URLs. Create the documented external network and volumes on a new host, supply the secrets locally, restore data separately, then run the documented Compose command. The bundle needs neither the management application nor an agent. Existing storage names are preserved deliberately.

Standalone Compose export supports single-host environments. Cross-host environments return an explicit unsupported-export error; they cannot be represented by this bundle. Encrypted workspace transfer remains available for moving their configuration and ownership between management installations.

## Validation

`make verify` runs planner contracts, real PostgreSQL admission/authorization and Chromium/WebKit journeys. The browser fixture uses a protocol-level host simulator for review/apply interactions; it does not claim to run Docker in the browser.

`make verify-agent-host` additionally runs the production executor on an explicitly disposable native Ubuntu host. The P3 fixture checks Compose deployment, PostgreSQL binding and retained data, HTTP/TCP ingress, manual drift, a rejected runtime reload, journal restart and standalone export. CI runs the native gate on both supported architectures. Public DNS and live ACME issuance are external prerequisites; local fixture results are not a live certificate-issuance pass.
