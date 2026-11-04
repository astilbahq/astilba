# Hosted beta and self-hosted installations

The hosted service and a self-hosted installation run the same Go executable,
dashboard and agent protocol. Billing only controls admission to the hosted
management service. It does not unlock application features on self-hosted
installations. Customer hosts keep running their containers and installed backup
schedules when management is unavailable.

The beta is invitation-only. Before admitting customers, the operator must set a
monthly workspace price, a server allowance, a cohort limit and asynchronous
support hours. The repository does not supply a live price or promise service
availability. Support covers the management service; customers operate their
servers, application code and backup destinations.

## Portable installation

Build the exact revision you intend to run, with its corresponding source and
licence notices:

```sh
docker build --build-arg SOURCE_REVISION="$(git rev-parse HEAD)" -t astilba:local .
```

The image runs as UID/GID 65532, includes its dashboard and source notice, and
contains no Node runtime. It needs a PostgreSQL database, a stable wrapping keyring,
and an HTTPS origin. It does not need access to a Docker socket. Application
builds and deployments run on enrolled agents.

Use [the Compose example](../services/platform/compose.example.yaml), or run the
same image under your preferred container supervisor. Supply configuration through
an owner-only environment file outside source control:

| Setting | Meaning |
| --- | --- |
| `ASTILBA_DATABASE_URL` | PostgreSQL connection URL; use verified TLS for a remote database. |
| `ASTILBA_KEYRING` | Path to a mounted owner-only keyring, readable by UID 65532. |
| `ASTILBA_KEYRING_JSON` | Alternative keyring JSON supplied directly by a secret manager or container environment. Configure exactly one keyring source. |
| `ASTILBA_ORIGIN` | Exact public HTTPS origin, without a path or trailing slash. |
| `ASTILBA_LISTEN` | Listener, normally `0.0.0.0:8080` inside the container. |
| `ASTILBA_TROUBLESHOOTING` | Optional. `single-instance` declares that one instance serves this installation, which [interactive troubleshooting](interactive-troubleshooting.md) requires. Off by default. |
| `ASTILBA_TRUSTED_PROXIES` | Optional comma-separated proxy IP CIDRs, for example `192.0.2.10/32,2001:db8::10/128`. Empty by default; forwarding headers are then ignored. |
| `ASTILBA_SMTP_ADDRESS`, `ASTILBA_SMTP_MODE`, `ASTILBA_SMTP_FROM` | SMTP delivery settings. Use `tls` or `starttls` outside local development. |
| `ASTILBA_SMTP_USERNAME`, `ASTILBA_SMTP_PASSWORD` | Optional SMTP credentials. |

Generate the keyring once with `astilba keygen --out /private/keyring.json` in a
private writable directory, then mount it read-only for normal operation. Preserve
that keyring across container replacement. Creating a new one does not recover
previously encrypted data.

1. Provision an empty PostgreSQL database and the keyring. Do not expose the database publicly.
2. With the same image and configuration as the server, run `astilba migrate`.
   Migrations are explicit; normal startup refuses an incompatible schema.
3. Run `astilba bootstrap --email owner@example.com --name Owner --workspace Personal`.
   Provide the password on stdin or with `--password-file` pointing to an owner-only file.
4. Configure an HTTPS reverse proxy for the public origin, forwarding to the
   loopback-bound management port. Forward request bodies and streaming responses;
   permit at least 35 seconds for agent long polls. Use a certificate trusted by
   browsers and enrolled agents. Keep the upstream port private, and set
   [per-address limits](#per-address-limits-at-the-reverse-proxy).
5. Start `astilba serve`, sign in, enable two-factor authentication and configure
   the workspace's MFA policy. Configure SMTP before inviting other users or
   relying on password recovery.
6. Follow [agent installation](agent.md), then exercise a deployment and restore
   on disposable infrastructure before admitting customer workloads.

The served `/source/NOTICE.txt` identifies the corresponding source. Keep the
matching source archive available with every distributed or hosted version.

### Client addresses behind a reverse proxy

Set `ASTILBA_TRUSTED_PROXIES` to the source addresses of your proxies **as seen by
the Go listener**, including each trusted hop when proxies are chained. Use the
narrowest CIDRs available. Do not trust all addresses or a shared container subnet
that includes untrusted workloads. Keep the upstream port reachable only by your
proxy; NAT or a published container port can make unrelated connections appear
to come from one trusted gateway.

The public edge must overwrite `X-Forwarded-For` with its client's address, or
append the actual connecting address to any existing value. Every subsequent
trusted proxy must append its own incoming peer. Astilba walks this list from
right to left, starting at the TCP peer, and stops at the first untrusted address.
User-supplied entries further left cannot select a different authentication
budget. A missing or malformed trusted chain falls back to the TCP peer. Invalid
CIDR configuration stops startup. `Forwarded`, `X-Real-IP` and `CF-Connecting-IP`
are not alternative sources at the Go listener.

For example, a proxy connecting directly from IPv4 loopback uses
`ASTILBA_TRUSTED_PROXIES=127.0.0.1/32`. A containerized proxy will usually have a
different source address; do not copy this example without checking the network.
With no configured proxies, direct-host requests continue to use their TCP peer.
Clients sharing a real public IP still share an IP budget; account and global
authentication limits remain in place.

### Per-address limits at the reverse proxy

Limit connections and requests per client address at your proxy, on every path. Astilba
limits sign-in attempts and a few signed-in actions, and nothing else limits general
requests, before or after sign-in.

Give agent paths their own, larger limits. A host holds a long poll while idle and makes
about 4 requests a second while running an operation. Refusing an agent can delay or cancel
its work, or leave an update waiting for you to retry it on the host. Enrollment needs no
credential, so keep it on the general limits.

These values are starting points, not measurements. Requests over them get a 429, so raise
them if many people, or more than about 20 hosts, share a public address. They count
requests in progress: a WebSocket counts for as long as it stays open, and over HTTP/2 each
concurrent request of a dashboard page counts separately. They do not count connections
that have not finished their headers, so also keep nginx's `worker_connections` above the
total you expect, allowing two for each proxied request.

nginx, where a location's limits replace the server block's:

```nginx
limit_conn_zone $binary_remote_addr zone=astilba_connections:10m;
limit_conn_zone $binary_remote_addr zone=astilba_agent_connections:10m;
limit_req_zone $binary_remote_addr zone=astilba_requests:10m rate=20r/s;
limit_req_zone $binary_remote_addr zone=astilba_agent_requests:10m rate=100r/s;

server {
    # Add these to your existing server block.
    limit_conn_status 429;
    limit_req_status 429;
    limit_conn astilba_connections 64;
    limit_req zone=astilba_requests burst=100 nodelay;

    location /agent {
        # Repeat the proxy settings of your main location here.
        limit_conn astilba_agent_connections 256;
        limit_req zone=astilba_agent_requests burst=200 nodelay;
    }

    # Keeps the server block's limits.
    location = /agent/v1/enroll {
        # Repeat the proxy settings of your main location here.
    }
}
```

Traefik, with three routers that share a service, entry points and TLS:

- your main router, with `astilba-connections` and `astilba-requests`;
- ``Host(`platform.example.com`) && Path(`/agent/v1/enroll`)``, with the same two;
- ``Host(`platform.example.com`) && PathPrefix(`/agent`)``, with the `astilba-agent-` pair.

Keep `Host` in both rules: Traefik prefers the longest rule, so without it your main router
takes these requests.

```yaml
http:
  middlewares:
    astilba-connections:
      inFlightReq:
        amount: 64
        sourceCriterion:
          ipStrategy:
            depth: 0
    astilba-requests:
      rateLimit:
        average: 20
        period: 1s
        burst: 100
    astilba-agent-connections:
      inFlightReq:
        amount: 256
        sourceCriterion:
          ipStrategy:
            depth: 0
    astilba-agent-requests:
      rateLimit:
        average: 100
        period: 1s
        burst: 200
```

Caddy has no per-address limit without a plugin, so use one, or set the limits at a
firewall or load balancer in front of it.

Both examples count the address that connected to the proxy. If another proxy or CDN sits
in front, they would count everyone as one client, so have the proxy take the client
address from its trusted hops, as nginx's `realip` module does with `set_real_ip_from`.
Do not count the raw `X-Forwarded-For` header, whose content a client can choose.

Both also count each IPv6 address separately, and a client usually controls a whole /64,
so changing address avoids these limits. If you publish an AAAA record, also limit each
/64 at a firewall.

### Terminal sessions behind a reverse proxy

[Interactive troubleshooting](interactive-troubleshooting.md) uses WebSockets for both
the browser and the host agent, on the same origin as the dashboard. It needs
`ASTILBA_TROUBLESHOOTING=single-instance`, because both streams of a session must
reach the same instance. Your proxy must:

- forward WebSocket upgrades for `/agent/v1/troubleshooting/` and
  `/api/v1/workspaces/*/troubleshooting-sessions/*/stream`;
- allow an idle timeout longer than 30 seconds, the interval of the stream pings;
- not cap a stream's total duration below 60 minutes.

A proxy that refuses upgrades makes sessions close within about a minute, and a shorter
idle or duration limit ends them early. Nothing else is affected.

## Hosted admission and billing

Set `ASTILBA_HOSTED=true`, plus all of:

- `ASTILBA_HOSTED_COHORT_LIMIT`: total admitted and reserved workspace invitations.
- `ASTILBA_HOSTED_SERVER_LIMIT`: allowance applied to new hosted workspaces.
- `ASTILBA_HOSTED_SUPPORT`: published support hours and scope.

Issue an invitation with `astilba invite --email owner@example.com`. Each emailed
link creates one workspace, expires after 48 hours and verifies that email. The
same admission rule applies to additional workspaces created by existing users.
Hosted writes require both verified email and an MFA-authenticated session.

After signup, `/onboarding?workspace=…` guides authenticator setup, recovery-code
saving and subscription setup. Existing completed steps are skipped; workspace
settings offer a resume link. Recovery confirmation is saved for the current code
generation only. Refreshing before confirmation offers replacement codes; plaintext
codes are never saved in browser storage. Existing accounts retain their previous
completed recovery status until codes are replaced or exhausted.

Stripe is optional. For paid Checkout, configure `ASTILBA_STRIPE_SECRET`,
`ASTILBA_STRIPE_PRICE` and `ASTILBA_STRIPE_WEBHOOK_SECRET` together. Use one recurring
monthly price with quantity one and configure the Stripe customer portal. Pin the
webhook endpoint to API version `2026-09-30.endive`, matching the client, at
`/hooks/stripe`. Subscribe to `checkout.session.completed`,
`customer.subscription.updated` and `customer.subscription.deleted`.

The operator creates and verifies the Stripe product/price outside the application.
Use test mode first. Checkout reserves one durable intent per workspace and reuses
it after network failure. Completion is reconciled before an expired intent is
replaced. Webhooks verify their timestamped raw-body signature, deduplicate events
and retrieve the current subscription instead of trusting event delivery order.
Unrecognized customers and subscriptions never grant access. New checkout success
and cancellation return to the workspace onboarding route. The URL is a navigation
hint; setup completes only when the server reports active management access. If
Stripe is not configured, onboarding explains how to contact the operator.

For an unpaid private evaluation, the operator can issue a bounded grant:

```sh
astilba grant-management --workspace WORKSPACE_UUID --until RFC3339_EXPIRY --servers ALLOWANCE
```

A grant cannot overwrite a Stripe-managed subscription. Published prices and
allowances should be based on measured costs before switching to paid admission.

At expiry, new configuration, builds and deployments are refused. Queued execution
is checked again at dispatch, and the agent receives cancellation for active work.
Cancellation uses the existing checkpoint/recovery rules. It never issues a blanket
container stop, deletes a volume, or disables an installed schedule. Recovery and
encrypted export remain available for 30 days after the entitlement ends. Owners
receive a durable expiry notice; a failed delivery is retried. This first beta does
not automatically delete workspaces. Any later deletion policy needs a separate
announced retention period and a preceding owner notification.

## Operational bounds and retention

Admission permits at most 50 unfinished execution operations in one workspace.
The existing per-server and per-environment exclusion still applies; reaching the
workspace limit returns backpressure before another operation is queued. Archive
operations are serialized per management replica, limited to 32 MiB and 10,000
records, and use fixed 64 MiB Argon2id derivation parameters.

`ASTILBA_LOG_RETENTION_DAYS` controls terminal operation diagnostics and build log
retention, defaulting to 30 days. Cleanup processes at most 100 records from each
category per minute, including historical cross-host receipt logs, and leaves
active operations and current runtime snapshots alone. Public workspace archives
omit application/build logs and free-text operation/upgrade notes, which can contain
captured output; machine states and recovery metadata remain. Internal recovery
checkpoints preserve their original payloads. Configuration revisions,
audit events, artifact identities and backup metadata are retained for recovery;
this is not a claim of constant total database size. Monitor their storage growth
and set the hosted cohort, allowance and price from measured usage. Billing event
IDs are retained for 90 days; processing a later redelivery still retrieves current
provider state and does not create a charge. No automatic workspace deletion runs.

The repeatable `TestHistoryRetentionAcrossRepresentativeFleet` fixture seeds five
workspaces, 25 server records and 2,500 completed operations with four 1 KiB
diagnostic outputs and a 1 KiB note each. It also preserves active and recent
records and checks that build artifact metadata survives log pruning. This is a
synthetic management-data measurement, not a fleet of running customer workloads.
On a local PostgreSQL 18 container, 25 cleanup batches removed approximately
12.96 MB of diagnostic payload in under a second. PostgreSQL retains allocated
space for reuse; this does not promise an immediate reduction in disk usage.
The test prints current timings and database size on every run. Measure sustained
polls, builds, backups and actual provider charges before setting commercial
capacity or availability commitments.

## Installation backup and recovery

A management backup consists of **both** PostgreSQL and the wrapping keyring.
Back them up separately into encrypted storage with independent access controls.
Neither an application backup nor a workspace transfer archive replaces an
installation backup: accounts, authentication configuration and membership belong
to the installation database.

Use the PostgreSQL client's custom archive format and a private output file. For
example, when the database is managed by Compose:

```sh
umask 077
docker compose exec -T database pg_dump --username astilba --format=custom astilba > management.pgdump
```

Use the actual database service/name configured for the installation. Keep database
credentials out of command-line arguments and terminal transcripts. Test restoring
both pieces; a successful upload alone is not a verified backup.

Restore with the matching application version into a clean database and private
keyring mount. Keep the restored management process stopped while fencing every
restored server:

```sh
astilba prepare-recovery --server SERVER_UUID --controller-generation NEW_GENERATION
```

`NEW_GENERATION` must exceed the generation installed on that host, including
transfers that happened after the backup. This invalidates restored credentials,
marks pending work cancelled, clears its delivery records and disables automatic
Git/build deployment policies for the workspace. It preserves configuration,
secrets, runtime ownership and schedule records.

Inspect the installed generation without disclosing credentials with
`sudo astilba-agent inspect-controller`. A controller change also resets its
execution counters; commands from the old controller remain fenced by generation.

Start the recovered dashboard, prepare each server's transfer ticket, stop its
agent, and locally transfer it to the recovered origin with the new generation.
Resolve unfinished local work before transfer. The target compares preserved unit
and schedule identities before admitting commands. Re-enable automatic policies
only after reviewing the recovered state. A stale database or incomplete archive
can fail this comparison; keep the host running and reconcile the discrepancy
instead of overwriting its journal or deleting volumes.

## Transfer a workspace between installations

1. Finish or reconcile active operations and database upgrades. Export the
   workspace from its Workspace page using a unique passphrase of at least 20
   characters. Keep the encrypted file and passphrase separately: the archive
   contains application secrets and provider credentials.
2. Revoke the source server connections after export and avoid further source
   changes. Existing containers, volumes and installed schedules continue running.
3. Sign in as the intended owner at the destination, complete MFA where required,
   and import the archive from Projects. A hosted destination also requires a
   workspace invitation. The importer becomes the owner and author of imported
   history. Accounts, login credentials, billing state and runnable queue jobs
   are not transferred. Automatic Git/build policies require renewed approval.
4. On the destination's Servers page, prepare each local transfer. Independently
   verify the destination's agent release public key with its operator. Save the
   transfer code in an owner-only file on the host, then stop the agent service.
5. Run the displayed command as the host owner, for example:

   ```sh
   sudo astilba-agent transfer --management https://panel.example.com \
     --server SERVER_UUID --transfer-generation NEW_GENERATION \
     --token-file /root/astilba-transfer-token \
     --release-key /etc/astilba/release-public.pem
   sudo systemctl start astilba-agent
   ```

   The journal must be exclusively available, with no unfinished operation,
   active database upgrade or uncertain scheduled run. The command atomically
   replaces controller trust while preserving workload and schedule identities.
   The agent retries enrollment if the destination is unavailable; local
   schedules still run during that retry. An expired ticket can be replaced by
   repeating preparation and the same transfer command with its new code.
6. Confirm connection and inventory at the destination before making changes.
   Remove the temporary token file. Review Git/build policies before enabling
   automation. A previously revoked imported server can be reactivated through
   the same explicit transfer preparation, subject to its server allowance.

Transfer does not redeploy applications or move volumes. Exact unit hashes and
schedule generations must match the destination's records; disagreement stops
enrollment. Take a fresh export or reconcile the source state when possible.
An incomplete or stale backup is not permission to overwrite the host's journal.

Workspace exports use archive version 6 for committed runtime outcomes alongside replica specifications, runtime inventories, schedules and lifecycle metadata. Versions 1–5 remain importable; their runtime provenance stays unknown. Older controllers reject version 6 rather than discard that identity. Imported preview policies and fork/cleanup grants are disabled; review resources and transfer agents before renewed owner approval. See [Pull-request previews](pull-request-previews.md), [application scaling](application-scaling.md) and [cross-host recovery](cross-host-placement.md).
