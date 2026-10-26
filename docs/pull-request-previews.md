# Pull-request previews

Previews build and deploy verified pull-request commits from GitHub, GitLab or Gitea into separate visual environments. An owner explicitly enables a saved template policy. Fork commits require separate approval for the displayed commit and source repository; a new fork commit invalidates that approval.

## Set up

1. Enroll a fresh **Deployment and build** server with **Preview-only server** selected. Reservation is immutable. Enrollment refuses a host with existing containers. Update its agent to a release advertising preview support. Keep this host off production private networks.
2. Create a **Preview template** environment. Configure preview-only secrets and a saved visual specification with one stateless Git-built service and any pinned-image dependencies. PostgreSQL passwords are regenerated per preview. Templates cannot deploy; ordinary environments cannot be converted into templates.
3. Configure the repository under Integrations. A token-backed connection needs read access to repository contents and pull/merge requests. GitHub App preview access uses repository-scoped installation tokens with contents and pull-request read permissions. Existing connections may need permission approval on GitHub.
4. Open **Pull-request previews**, select the service, connection, target branch and reserved host. Choose build/runtime budgets, concurrency and closed-preview retention. Confirm identity in Account security and explicitly approve automatic execution and cleanup before enabling.
5. For generated HTTPS URLs, complete that host's [application-domain setup](server-onboarding.md#optional-generated-names) first and enable generated domains in the policy. Otherwise routes are omitted. Preview names point directly at the reserved host.

The initial increment supports one stateless Git-built service, at most five services, five volumes and five HTTP routes per preview. All template images must use immutable SHA-256 digests. Compose previews, stateful Git-built targets, registry publication, fallback runners, watched paths and build-time secrets are unavailable. Dockerfile and Railpack builds use the existing pinned build machinery.

## Isolation and limits

Each preview receives a new environment, deployment unit, service/volume/binding identities and scoped secret versions. Only explicitly configured template secrets are copied; production environment secrets are unavailable. PostgreSQL passwords are independently random. Generated names include the new environment/service identities. Preview layer caches are scoped to the preview request and environment, separate from ordinary project caches.

The controller and agent enforce the reserved-host boundary in both directions. Preview plans use a distinct recipe and build action so older agents reject them. Preview resources cannot be edited, cloned, restored, upgraded or deployed through ordinary environment APIs. Scoped automation can read them; owners manage their lifecycle through the template.

A reserved server is a trust boundary, **not a hostile-code sandbox**. BuildKit runs privileged with host networking; containers share the host kernel. Approved code can reach networks available to that host and receives the template's runtime secrets. Do not use production credentials in a template or connect a preview host to production private services. Fork approval explicitly accepts that commit's execution and access.

Policies allow 1–10 active previews and 1–3 concurrent builds, with builds no higher than active previews. Workspace admission caps allocated previews at 20 and each server at 10; unallocated request records are capped at 50 per workspace. Limits are checked while holding the workspace mutation lock. Closing, blocked or offline previews keep their resource slots until the agent confirms removal.

Every service receives limits of 1–4 CPU cores, 128–8192 MiB memory and 32–512 processes. Builds require the existing explicit CPU, memory, source/builder storage and time budget. These limits do not provide a hard Docker-volume or image-storage quota. Monitor disk capacity on the reserved host. Runtime containers drop capabilities and prohibit new privileges; PostgreSQL receives only the startup capabilities needed to initialize its own data directory.

## Lifecycle and failures

Signed provider events are hints. Reconciliation reads the authoritative request state, base repository, target branch, head repository and exact commit before allocating work. The agent fetches the provider's canonical pull/merge-request ref and checks its commit against the assignment. A paginated provider sweep discovers requests even when their first webhook is missed; existing requests are periodically rechecked. Provider errors are visible and retain existing resources.

Duplicate delivery, changed commits, closing and reopening reconcile through durable operations. A new commit cancels stale work and waits for acknowledgement before replacement. Fork approval binds the commit, source repository and policy generation. Reopening during the retention period preserves data; reopening after confirmed deletion allocates new identities and storage. Explicit owner cleanup keeps the request deleted until an owner requests refresh.

Closed-preview retention defaults to **24 hours** in the dashboard, configurable from 0 to 168 hours. The API also permits `null` for manual owner cleanup. An owner can remove a preview immediately after recent identity confirmation. Cleanup cancels outstanding work, waits for acknowledgement, withdraws only that preview's routes, removes owned containers/volumes/network, verifies absence and retires active runtime inventory. Shared ingress and other workloads remain intact. Copied preview secret values are erased only after confirmed cleanup. Immutable operation/revision metadata, completed images and bounded build-layer caches follow their existing retention; cleanup is not a backup-erasure mechanism.

Offline agents leave cleanup pending, retaining the resource slot. An interrupted agent replays its journal. If cleanup succeeded but its report was lost before controller recovery, the agent retains an operation/digest receipt across transfer. The destination validates it against the frozen cleanup command before retiring inventory. Unknown receipts cannot authorize removal. There is no forced release of an unconfirmed slot.

Pausing stops new builds and cancels outstanding work; existing runtime/data remains. The same valid owner grant can resume its frozen policy after cancellation acknowledgement. While a request stays open at the same commit, time-budget expiry and explicit operation cancellation stay stopped until an owner refreshes it. A new commit or a later close/reopen lifecycle can create fresh work; the cancelled operation remains cancelled. Pause the preview policy to prevent automatic execution across those events. Clean up active previews before changing the saved policy. Owner/authentication or controller changes invalidate automatic execution and fork approvals; cleanup can be renewed explicitly by a current owner. Workspace import and management recovery disable policies. Review imported resources and transfer agents before granting new execution authority.

## Verification

`make verify` covers real PostgreSQL lifecycle, authorization, provider API/signature fixtures, workspace transfer compatibility and Chromium/WebKit journeys. `make verify-agent-host` includes actual Git builds, preview PostgreSQL, runtime limits, interruption during volume removal, preservation of other workloads and cleanup receipts across controller transfer. Native CI runs both Linux architectures. These fixtures use disposable resources and do not update a live installation.
