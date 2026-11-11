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
| `ASTILBA_DATABASE_POOL_SIZE` | Optional. Connections each process may hold, 4 to 1000. Defaults to 12. See [database connections](#database-connections). |
| `ASTILBA_TROUBLESHOOTING` | Optional. `single-instance` declares that one instance serves this installation, which [interactive troubleshooting](interactive-troubleshooting.md) requires. Off by default. |
| `ASTILBA_AGENT_FORK_FENCING` | Optional `true` or `false` (default). With `true`, proof that a second copy of an agent uses a server's key also clears that server's credential, stopping both copies until it is enrolled again. Either way it raises an identity conflict alert. See [the agent's key](agent.md#identity-conflicts). |
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

To move to a newer version later, follow [upgrading management](management-upgrades.md).

The served `/source/NOTICE.txt` identifies the corresponding source. Keep the
matching source archive available with every distributed or hosted version.

### Database connections

Each `astilba serve` process holds up to `ASTILBA_DATABASE_POOL_SIZE` connections,
plus one for the job listener. The default of 12 suits a database about a millisecond
away and most self-hosted installations need not change it.

**Size it from the round trip.** Every query holds a connection for at least one round
trip between the service and PostgreSQL, so a pool carries about pool size ÷ round
trip queries a second. An idle server's agent makes about 3.4. To keep about a third
of the pool spare:

```text
pool size = servers x round trip in milliseconds / 200
```

So the default suits about 800 servers at 3 ms and 480 at 5 ms. Use 130 in place of 200
if servers deploy about hourly. Measure the round trip from where the service runs.
The [capacity measurement](../experiments/fleet-capacity/RESULTS.md#a-configurable-pool)
tested this up to a pool of 24 at about 3.5 ms and up to 2,000 servers, on a local VM
with a simulated network rather than a managed database.

**Stay within PostgreSQL's limit.** Budget the whole pool: at 100 servers a pool of 12
had already opened nearly all its connections. Count every process that can run at once, including the overlap
during a rolling replacement, and leave room for migrations, one-off commands and
your own monitoring:

```text
processes x (pool size + 1) + other clients <= max_connections - superuser_reserved_connections - reserved_connections
```

A `CONNECTION LIMIT` on the role or database also applies, and is the lower bound if it
is smaller. `astilba serve` refuses to start if one process alone could never fit within
any of these, but it cannot see other processes or clients. PostgreSQL's `max_connections` is usually 100 and changing it requires a restart; a
small managed instance may set it lower, so check yours.

**Do not make it larger than you need.** When the pool is too small, work queues in the
service, which is bounded and visible. When the total passes a limit, new connections
fail outright. Measured under idle polling, each connection had added about 4 to 6 MiB
of PostgreSQL private memory after ten minutes, still rising slowly on larger pools. A
pool of 48 also appeared to use more database CPU than a pool of 4 for the same queries,
though one pair of runs on a shared host cannot say how much. Past the point where PostgreSQL's CPUs are busy,
more active connections reduce throughput rather than add to it
([PostgreSQL wiki](https://wiki.postgresql.org/wiki/Number_Of_Database_Connections)).
The minimum of 4 leaves no room to spare: the four background job workers can hold all
of them while an application DNS provider is slow.

**Connections must keep their session.** Job notifications use `LISTEN`, and offline
recovery relies on a session advisory lock on each connection. Connect directly or
through session pooling, not transaction pooling. Set the size with this setting, not
`pool_max_conns` in `ASTILBA_DATABASE_URL`, which the startup schema check sends to
PostgreSQL as an unknown setting and fails.

**Reading the log.** Each process logs one line a minute, with counts for that minute:

```text
INFO database pool size=12 open=12 inUse=7 acquires=117621 waited=4366 waitSeconds=3.08 canceled=0
```

Divide `waitSeconds` by `acquires` for the mean wait for a connection. With a third of
the pool spare it was usually under 3 ms, near the limit 2 to 12 ms, and past it over
400 ms with `waited` equal to `acquires`. The share that `waited` alone is a poor signal,
since a pool of 4 waited half the time with a third spare. `canceled` alone is too,
because it also counts queries started after their client disconnected, however idle the
pool. The line becomes a warning when the mean wait passes 50 ms, or when `canceled`
exceeds `acquires`, as it does when the pool is too stuck to complete work. If warnings
persist, raise the pool size or check that PostgreSQL itself has not slowed down, because
a slower database fills a pool of any size.

**Upgrading.** Earlier versions used the larger of 4 and the host's CPU count. A host with
fewer than 12 CPUs now opens more connections, so recheck the budget above. A host with
more than 12 opens fewer, so set the size if it carries many servers.

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
Clients sharing a real public IP still share an IP budget. See
[sign-in under attack](#sign-in-under-attack) for how the budgets work together.

### Sign-in under attack

Astilba applies these limits itself, in PostgreSQL, so they hold across replicas. Each is
per 15 minutes and counts only attempts that reach a password check. A refused attempt costs
nothing.

| Who | Limit |
| --- | --- |
| Each IPv4 address or IPv6 /64 | 50 sign-in attempts |
| Each IPv6 /48 | 200 sign-in attempts |
| One account from one address | 5 attempts |
| One account from all addresses | 10 attempts |
| A browser that has signed in to the account before | 5 attempts, on its own budget |
| One account from all such browsers | 20 attempts, correct or not |

A successful sign-in from a new browser clears that account's shared limit and the one for its
own address. A browser that completes sign-in, including any second factor, holds a cookie sent
only to the sign-in path. The cookie names no account and lasts 90 days from its last sign-in. It
stops counting when anything ends the account's sessions, such as a password change, turning
two-factor authentication on or off, or using or regenerating recovery codes. A browser holds
one, for the account it signed in to last. The cookie skips no check, and
an attacker without it cannot spend its budget, so guessing at an account does not lock its owner
out of the browser they last used for it.

Someone on another browser is refused while the account's limit of 10 is spent, which takes two
addresses and the email address. Password reset keeps working during an attack: anyone can make
Astilba send the link, up to 3 times per 15 minutes per email, and a request beyond that is
answered as usual and sends nothing. A delivery that fails does not count, and a request for an
email with no account counts for nothing. Each link replaces the one before, so use the newest.
Completing a reset clears the shared limit and makes that browser a known one.

There is no installation-wide limit, because anyone could spend it to refuse everyone. Trying a
password across many accounts is bounded by each account's limit, each address's limit and how
fast a replica can hash.

Each replica runs two password hashes at once, and one IPv4 address or IPv6 /48 runs one at a
time. It can have four requests in progress, and more are refused at once. Others wait up to 5
seconds, with up to 32 waiting. Browsers with the cookie, valid links and signed-in users have
their own queue, which goes first. A request that cannot be served gets a 503 with
`Retry-After: 5` and spends no attempts.

Your proxy needs to do three things for this to hold:

- **Limit requests per address.** The [general limits](#per-address-limits-at-the-reverse-proxy)
  are enough: an address that has spent its limits is refused after one database read and never
  waits for a hash. Those reads are what the proxy's limit bounds.
- **Group IPv6 clients.** Traefik counts each IPv6 address separately unless you set
  `sourceCriterion.ipStrategy.ipv6Subnet: 64`. nginx's `$binary_remote_addr` also counts each
  address and has no such setting, so a client with a /64 is not limited by it; if that matters,
  group addresses at a firewall or CDN in front of nginx, or do not publish an IPv6 address.
  Astilba's own limits group IPv6 at /64 either way, and sign-in and reset requests also at /48.
- **Pass on the client's address**, as [described above](#client-addresses-behind-a-reverse-proxy).
  Without it every client shares one address's limits, so one guesser refuses everyone, and only
  one hash runs at a time for people without the cookie.

No per-address limit stops a flood from hundreds of addresses, each staying within its limits.
That delays or refuses sign-in on browsers without the cookie, with a 503, for as long as it
runs, while live sessions and known browsers keep working. Stop it upstream, with a CDN or
firewall that can challenge or block that traffic.

### When password reset mail fails

A reset request answers the same way for every email and sends its mail afterwards, in a
background job, so its response cannot show whether an account exists. That also means the
person asking is never told about a failed send. They see "a password reset link will arrive
shortly", and then no mail arrives.

Astilba retries for about 15 minutes, starting after 15 seconds and at most 5 minutes apart. A
request waiting in the queue or being retried survives a restart. Any earlier link keeps working
until a new one has been accepted by the relay, and a send the relay refuses or that never reached
it does not count towards the limit of 3. A send the relay read but never answered does count,
because that mail may still arrive. No mail goes out for a request more than 15 minutes old, so
mail never arrives hours later, and the person has to ask again. A retry is also dropped if a
newer link has gone out by then, so it cannot replace a link someone is using. Should the account's
password have changed in the meantime, though, a retry still sends a link, as a new request would.

Only you can see the failure, in the server log, and no log line names the email:

| Log message | Level | Meaning |
| --- | --- | --- |
| `password reset mail could not be sent` | warning | One attempt failed and will be retried. |
| `password reset mail could not be prepared` | warning | The database stopped one attempt before sending, and it will be retried. |
| `password reset mail abandoned` | error | The request ran out of time, so whoever asked got no mail. It appears at the next attempt, normally within 20 minutes of the request, or about an hour after a server was killed during the send. |
| `password reset link could not be saved after sending` | error | Mail went out with a link that does not work. A retry sends another, unless the limit of 3 is spent. |
| `password reset charge could not be refunded` | error | That email can send fewer links until its 15 minutes end. |
| `password reset request could not be opened` | error | The request cannot be read, for example because its key was removed, so it was dropped. |
| `password reset request could not be queued` | warning | The request was lost before it was queued, though it was answered as usual. |

Without SMTP, every request for an account ends in `password reset mail abandoned`. A request for
an email with no account logs nothing, so test with a real account. If the server is killed during
a send rather than stopped, that one request is dropped, because Astilba finds it only about an
hour later, and it counts towards the limit until its 15 minutes end. Asking again works straight
away, because each request is its own job. A flood of requests from thousands of addresses can
delay this mail, and requests that wait over 15 minutes are dropped. Deployments and other
background work have their own queue and are not delayed by it.

### Per-address limits at the reverse proxy

Limit connections and requests per client address at your proxy, on every path. Astilba
has its own budgets for sign-in, password reset, invitation, signup and enrollment attempts
and for a few signed-in actions, which proxy settings do not change: for example, 30
enrollments per address every 2 minutes. Nothing else limits general requests, before or
after sign-in.

Give `/agent/` its own, larger limits. A host holds a long poll while idle and makes about 4
requests a second while running an operation, and refusing an agent can delay or cancel its
work. Enrollment stays on the general limits, because each attempt writes to the database
before its credential is checked. So do downloads under `/agent-artifacts/`, which serve
large files without a credential. A refused download leaves that update waiting for you to
retry it on the host, so update hosts that share an address a few at a time.

These values are starting points, not measurements. Requests over them get a 429, so raise
them if many people, or more than about 20 hosts, share a public address. The request limits
count arrivals. The connection limits count requests in progress: a WebSocket counts for as
long as it stays open, and over HTTP/2 each concurrent request of a dashboard page counts
separately. They do not count connections that have not finished their headers, so also keep
nginx's `worker_connections` above the total you expect, allowing two for each proxied
request.

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

    location /agent/ {
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
- ``Host(`platform.example.com`) && PathPrefix(`/agent/`)``, with the `astilba-agent-` pair.

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
in front, they would count everyone as one client. Have nginx take the client address from
its trusted hops with the `realip` module and `set_real_ip_from`. On Traefik, list those hops
in the entry point's `forwardedHeaders.trustedIPs`, and set `sourceCriterion.ipStrategy.depth`
to their number on all four middlewares. Do not count the raw `X-Forwarded-For` header, whose
content a client can choose.

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
- allow an idle timeout of at least 35 seconds, because the streams ping every 30 seconds
  and a proxy limit of exactly 30 seconds ends them;
- not cap a stream's total duration below 60 minutes.

A proxy that refuses upgrades closes a session within about a minute, without starting
a shell. A shorter idle or duration limit ends a running session early, and its shell
with it. Nothing else is affected.

These configurations carry a session through each proxy. `make verify-agent-host-core`
reads them from this page and runs them with pinned nginx 1.30.6, Caddy 2.11.8 and
Traefik 3.7.16 images.

nginx needs the upgrade headers, and its `proxy_read_timeout` is the idle limit:

```nginx
map $http_upgrade $connection_upgrade {
    default upgrade;
    ''      close;
}

server {
    listen 443 ssl;
    server_name platform.example.com;
    ssl_certificate /etc/ssl/platform/fullchain.pem;
    ssl_certificate_key /etc/ssl/platform/privkey.pem;

    # The largest request is a workspace import.
    client_max_body_size 45m;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $remote_addr;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $connection_upgrade;
        proxy_buffering off;
        proxy_read_timeout 120s;
        proxy_send_timeout 120s;
    }
}
```

Caddy forwards upgrades and sets no stream limit by default:

```caddyfile
platform.example.com {
    reverse_proxy 127.0.0.1:8080
}
```

Use Caddy 2.11.8 or later. 2.11.7 can read the rest of a large request that was refused
early as a new request, and answer the client's next request on that HTTP/1.1 connection
with a 431.

Traefik does too. This is its static configuration, followed by the file it loads:

```yaml
entryPoints:
  websecure:
    address: ":443"
providers:
  file:
    filename: /etc/traefik/dynamic.yml
```

```yaml
http:
  routers:
    platform:
      rule: Host(`platform.example.com`)
      entryPoints: [websecure]
      service: platform
      tls: {}
  services:
    platform:
      loadBalancer:
        servers:
          - url: http://127.0.0.1:8080
tls:
  certificates:
    - certFile: /etc/ssl/platform/fullchain.pem
      keyFile: /etc/ssl/platform/privkey.pem
```

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
records, and use fixed 64 MiB Argon2id derivation parameters. An account can make
five exports and five imports an hour, and each request is charged before its body is
read, so one whose body never finishes, or does not parse, still pays. An import's
account is checked for recent sign-in, and in hosted mode a second factor and verified
email, before its body is read, and its body then waits for one of two places per
replica, so a replica holds at most two import bodies. An export's authority is checked
before it waits. Either then waits for the replica's archive turn, in arrival order,
for up to ten seconds each time, and gets a 503 asking it to retry in a few seconds if
the wait runs out. Neither wait shortens the time it has to send its body or to
finish once its turn comes. An account has one export or import in progress on a replica at a time, and
another gets a 503 at once without being charged.

The database keeps every operation, but an export and a cross-host recovery checkpoint
carry only the operation history recovery and the app need. That is unfinished work,
every operation that applied or removed a runtime, every standalone private network
removal, every operation another archived record names, and the newest 100. Older
finished housekeeping, such as other private network, agent update and single-host
backup commands, stays out, so a long history doesn't stop exports or cross-host
deployments. A workspace imported from an export, or recovered from a management
backup older than that housekeeping, doesn't get it back. Deployment history,
configuration revisions and audit events are all carried, so a very active workspace
can still reach the record limit.

Work that Git webhooks start runs in a background queue of its own, with four workers
per replica: source refreshes, pull-request previews and GitHub App events. A burst of
it never delays deployments, DNS or the sweeps. A signed push starts a refresh only for
a branch that one of the workspace's sources or automatic build targets tracks, and
pushes for a branch with a refresh pending join it, so a workspace has at most one
refresh per tracked branch. A pull-request event adds nothing when the same preview job
is about to run at the same priority or sooner. Once a workspace has 50 jobs for request
events waiting, further events add nothing, and discovery, which finds only the
provider's open requests, picks them up on its cycle of about five minutes, a page of 50
requests at a time. Discovery itself waits for its next cycle while the workspace has 100
request jobs waiting.

The queue takes jobs oldest first, except that a workspace's jobs queued while it already
has four waiting go behind every other workspace's first four. A workspace uses at most
three of the four workers, and one with jobs running lets a workspace with none go first.
A workspace with nothing running therefore starts each of its first four waiting jobs on
the free worker, or after at most a few other jobs, however much another has queued; jobs
it queues beyond those wait, oldest first, with other workspaces' backlogs. Replicas that
start jobs at the same moment can briefly exceed these shares. Waiting on a provider holds a worker but not a database connection.

The private network, coordinated backup, operational alert and expiry notice sweeps take one workspace
at a time, each under its own deadline, and carry on past a failure. A failure costs that
workspace its turn, and each failing workspace delays the others by no more than that
deadline, though many failing at once add up because sweep capacity is shared. Each is logged
as `background sweep item failed` with the sweep name and the workspace; the log keeps the
failure's category rather than its text, which can contain stored values. Alert on that
message. The workspace's owner also sees an [operational alert](operational-alerts.md) if a
network or coordinated backup stays blocked, unless a held workspace lock is the cause: that
stops alert evaluation for the workspace too, so only the log shows it.

Each class of retention cleanup commits separately, so one that fails does not stop the
others, and logs `retention cleanup failed` with the class. The preview sweep takes every workspace's oldest due work before any
workspace's second, and its jobs run in the Git queue described above, sharing it by workspace.

`ASTILBA_LOG_RETENTION_DAYS` controls terminal operation diagnostics and build log
retention, defaulting to 30 days. Cleanup processes at most 100 records from each
category per minute, including historical cross-host receipt logs, and leaves
active operations and current runtime snapshots alone. Receipt cleanup reads only operations
whose receipts still carry logs, so it doesn't slow as cleaned history grows. Public workspace archives
omit application/build logs and free-text operation/upgrade notes, which can contain
captured output; machine states and recovery metadata remain. Internal recovery
checkpoints preserve their original payloads. Configuration revisions (a save
that leaves the draft unchanged adds none), audit events, artifact identities and backup metadata are retained for recovery;
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
[Management backups](management-backups.md) has a reference script and timer that
encrypt and upload the database nightly to storage the server cannot erase, with an
alert when it fails, and the full restore procedure.

Restore with the matching application version, or a newer one after `astilba migrate`,
into a clean database and private keyring mount. Keep the restored management process stopped while fencing every
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

Private networks take only removals until you approve them again. That approval
replaces what each host holds, including peers the backup does not know about. See
[cross-host placement](cross-host-placement.md).

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
