# Astilba deployment platform

Status: updated 24 October 2026. The initial application and private-alpha acceptance are implemented. The implementation plan now includes operational visibility and staged developer-experience improvements.

The [architecture](platform-architecture.md) and [implementation plan](platform-implementation-plan.md) define the technical boundaries, delivery sequence and acceptance criteria.

## Promise

**Railway with your own servers.**

Astilba lets developers build, connect, deploy, and maintain applications on their own servers through a visual workspace. Run the complete platform yourself, or pay for a hosted dashboard.

## Audience and problem

Solo developers and small trusted teams who want to run their own applications and open-source software without needing to become infrastructure specialists. Resellers and mutually untrusted customer hosting are outside the first release.

The experience should remain approachable when a developer moves beyond a predefined template: adding a worker, connecting a database, configuring storage, or changing how services fit together.

Initial validation uses non-critical deployments and disposable infrastructure. Paid hosting remains optional; self-hosting must provide the complete product.

## Product principles

- **Visual composition.** Create services and meaningful connections between them. Make configuration bindings, persistent storage, domains, and service health understandable.
- **Explicit configuration ownership.** Dashboard-managed projects keep configuration in Astilba. Compose projects keep their files authoritative. For Git-managed Compose, supported canvas and file edits become one reviewed pull or merge request; deployment follows the merged source.
- **Git and build choice.** Support GitHub, GitLab and Gitea, including configured self-hosted GitLab and Gitea. Build on a deployment server or an explicitly selected dedicated runner. Move remote build outputs through a customer-configured OCI registry and deploy immutable image identities.
- **Practical operations.** Bring logs, deployment history, resource usage, scheduled tasks, and service status into the same workspace.
- **Backups and restoration.** Offer reusable backup destinations, schedules, retention, and clear restore operations for supported data types.
- **Reviewable maintenance.** Explain configuration changes and supported upgrade procedures before applying them. Define upgrade support around tested paths.
- **Complete self-hosting.** Publish the new platform under AGPL-3.0-only, with the same product capabilities available through self-hosting and hosted management. Contributors retain copyright and contribute under each component's licence without assigning ownership or granting special commercial relicensing rights.
- **Simple paid hosting.** Charge a monthly workspace subscription with stated server allowances, without separate fees for applications, databases or team members. Choose prices from measured costs. A future paid build service would be separately enabled and metered with spending limits.
- **Optional hosting providers.** Support a portable management installation and evaluate Cloudflare hosting as an alternative. Cloudflare services and custom-domain integrations must remain optional; the management service need not occupy its own VPS.
- **Ownership and continuity.** Existing workloads keep running independently of dashboard availability. Developers retain exportable configuration and data so they can operate or move their applications.

Hosted management can issue privileged commands to enrolled servers and access the deployment secrets it manages. Make that trust relationship explicit. Self-hosting gives customers control over who operates the management service.

## Initial supported experience

Demonstrate one complete deployment journey on a single Docker host:

1. Connect a server.
2. Add an application from a repository or container image.
3. Add a PostgreSQL database and bind its connection details to the application.
4. Configure a domain and deploy the application.
5. Configure a backup destination, create a backup, and demonstrate a restore with disposable data.

Demonstrate a second entry path using a Compose project, preserving its files as the deployment's source of truth. Repository import, deployment triggers and configuration pull or merge requests must work across all three supported Git providers before the paid beta.

Initially each environment has one deployment unit on one Docker host. Keep logical environments separate from placement in the data model. A Compose project remains a single deployment unit even when visual environments eventually gain multiple units.

Use Traefik for application HTTP and TCP ingress. Databases and TCP services are private unless explicitly published. Public applications require customer-provided reachable ingress and suitable DNS. Start with Ubuntu 24.04 LTS, systemd and rootful Docker; advertise each CPU architecture only after testing it.

S3-compatible and configured filesystem backup destinations are included. Restore into fresh storage, verify it, then explicitly switch the application. Support major upgrades only through tested version-specific procedures; the first planned recipe is PostgreSQL 17 to 18.

Validate these journeys on disposable infrastructure, then through the maintainer's own non-critical workloads. Existing containers on an enrolled host stay unmanaged unless a later explicit adoption workflow is implemented.

## Paid beta and operational scope

The paid beta may begin once deployment, Git, build, backup, verified restore, export and management-transfer workflows are reliable. Guided major upgrades can follow independently. Begin with an invitation-only cohort of approximately five workspaces and published asynchronous support hours, without a round-the-clock response commitment.

Astilba operates and supports the management service and remains responsible for defects in it. Customers operate their host OS and application code; the subscription does not include unlimited managed DevOps.

At subscription expiry, stop new hosted management changes, let active work reach a safe stopping point and provide 30 days for export or transfer. Keep the recovery and transfer controls available during that period. Running applications, stored application data and installed agent schedules remain on the customer's servers. Transfer to another management installation requires confirmation from the host owner and fences out the old controller.

Hosted accounts capable of changing servers require MFA. Self-hosted owners control their MFA policy. Agent updates are explicitly initiated through the dashboard and respect compatibility checks and active operations.

## Deferred capabilities

The next increments add operational visibility, a simpler source-to-app workflow, safe stateless releases, general service bindings and a small maintained catalog, then scoped automation and isolated pull-request previews. These extend the initial journey without making every parity feature a paid-beta prerequisite.

Guided host/domain setup, general cron schedules, replicas and cross-host placement, interactive troubleshooting and broader tested maintenance recipes remain later increments in the implementation plan. Kubernetes, live collaboration, generic database administration, existing-workload adoption, Astilba-operated build runners and an Astilba registry require separate scope decisions.

## Technology and next step

Use React, TypeScript, Vite, TanStack Router and Query, React Flow and a platform-specific UI foundation using Panda CSS. Use Go for management and agents, PostgreSQL for management state, and Docker, Compose, BuildKit and Traefik on deployment hosts. River's open-source core and optional Railpack support must pass the initial technical experiments.

P9 adds resource history, disk pressure, operational alerts and bounded retained-log export. The next product increment is P10 source-to-app workflow. Exact prices, the public product name, measured capacity limits and published support hours remain launch decisions informed by non-critical real use.
