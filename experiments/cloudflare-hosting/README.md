# PLAT-6: complete management hosting

Status: **pass for complete management hosting at the tested scale**, 22 October 2026. The portable Go process ran in Cloudflare Containers with external PostgreSQL; the live lifecycle, transport and load checks passed. The cost model does not establish a saving over a small conventional host.

The same disposable Go executable runs behind a conventional HTTPS proxy or the Worker. PostgreSQL holds sessions, operations, River jobs and agent credentials. The Worker serves static assets and forwards API traffic to one named Container; it contains no product state. This is a hosting probe, not the production dashboard or authentication implementation.

## Coverage

- Secure, HttpOnly, SameSite cookies backed by PostgreSQL; Origin/CSRF rejection; authenticated incremental SSE; logout invalidation.
- The PLAT-3 protocol: a 25-second agent poll alongside heartbeat requests, with invalid credentials rejected.
- A delayed River operation and session surviving replacement of the controller's entire filesystem. Duplicate submission has one database effect.
- Live-only checks for natural inactivity sleep, a Cron Trigger waking pending work without browser/agent traffic, and load at 0, 1, 5 and 20 synthetic agents.
- Go process counters for PostgreSQL socket bytes, HTTP requests, process CPU and Go runtime memory. Load records distinguish a new process from continuous measurements.

All identities, tokens and operations are synthetic. Initialization requires an explicit disposable-database flag and refuses a nonempty schema. It never erases or resets an existing database. Runtime secrets are passed at startup; the image has no credentials or durable local state. The Docker context allowlist excludes environment files.

## Pins and licences

| Component | Pin | Licence |
| --- | --- | --- |
| Go | 1.27.3 | BSD-3-Clause |
| Build image | `golang:1.27.3-bookworm@sha256:972b4110ba0f47539a975c31e52c64765e1ff19f2d74469ae4a595c6676e80a5` | Go BSD-3-Clause; Debian component licences |
| Node / pnpm | 24.18.1 / 11.17.0 | MIT |
| Wrangler | 4.151.3 | MIT OR Apache-2.0 |
| Workers types | 5.20261021.1 | MIT OR Apache-2.0 |
| TypeScript | 7.0.2 | Apache-2.0 |

Existing Go dependency and PostgreSQL/Traefik pins remain unchanged. The root `pnpm-lock.yaml` pins JavaScript dependencies, with a one-day release-age policy and only the exact esbuild/workerd install scripts allowed. Local Docker CLI: 29.9.2; Buildx: 0.38.1. Cloudflare uses an amd64 image; the Dockerfile runs its compiler on the native build host and cross-compiles Go.

## Local checks

Use the [disposable Docker setup](../durable-execution/README.md#reproduce), with `PLAT2_DOCKER_HOST` naming its Unix socket. Wrangler needs the same daemon and Docker Buildx.

```sh
pnpm install --frozen-lockfile
export DOCKER_HOST="$PLAT2_DOCKER_HOST"
pnpm verify
```

`TestManagementHosting` uses real Traefik HTTPS, a separate PostgreSQL container and a replaced Go controller container. Scheduled work may wait for River's previous leader lease to expire after an abrupt replacement. The probe allows 90 seconds and checks the database effect; HTTP readiness alone is insufficient.

`pnpm --filter @astilba/cloudflare-spike verify` typechecks the Worker, builds the amd64 image and packages it without deployment. It uses the direct [Durable Object Container API](https://developers.cloudflare.com/containers/api/durable-object-container/) with the `durable_object` scheduling policy.

## Live run

Use a new, dedicated PostgreSQL database reachable from the Container. Use TLS with certificate/hostname verification (`sslmode=verify-full`) and a role limited to that database. The init step needs schema creation. Keep the URL and three random tokens of at least 32 bytes in an ignored environment file; never put them in arguments, committed configuration or chat:

```text
PLAT6_DATABASE_URL
PLAT6_PUBLIC_ORIGIN
PLAT6_LOGIN_TOKEN
PLAT6_INTERNAL_TOKEN
PLAT6_AGENT_SEED
```

`PLAT6_PUBLIC_ORIGIN` is the exact HTTPS origin, without a trailing slash. With those variables loaded, initialize the empty database from `experiments/durable-execution`:

```sh
PLAT6_DISPOSABLE_DATABASE=1 go run ./cmd/hosting init
```

Authenticate Wrangler. Copy `wrangler.jsonc` to the ignored `wrangler.local.jsonc`, assign a fresh unique Worker name and matching public origin, and retain the image, bindings, assets and schedule. Upload the four private values with Wrangler's secret-input mechanism, using stdin or a private temporary JSON file. Deploy with the local configuration. The agent seed derives distinct credentials for 20 synthetic agents.

The secret-protected `/_fixture/stop`, `/_fixture/status` and `/_fixture/metrics` controls are experiment-only; a browser session cannot use them. The minute Cron Trigger calls the protected Go wake endpoint. Remove the complete disposable deployment after measurement, including its schedule and Container application, after checking names against this run's private deployment record. Drop only its dedicated test database.

Set `PLAT6_LIVE_URL` to the deployed origin and load the same runtime secrets and database URL, then run from the Go module:

```sh
go test -count=1 -timeout=15m -v -run '^TestCloudflare(Hosting|Wake|Load)$' .
```

The wake probe observes PostgreSQL directly while waiting for completion, so a test HTTP request cannot create a false wake-up pass. Each load group starts requests for one minute, using 25-second polls and 15-second heartbeats, then drains its in-flight requests before reusing identities. Recorded wall time includes that drain. `PLAT6_MEASUREMENT` records contain counters, not secrets. Store raw deployment output privately and publish only sanitized evidence.

The replacement probe checks the specific operation's recorded completion boot against the new process. A job completed before the old process stopped cannot satisfy that recovery assertion.

## Live evidence

Measured on 22 October 2026 with the complete Go process in the Cloudflare `lite` shape, Worker-hosted static assets and external PostgreSQL 18.4. The external connection uses `sslmode=verify-full`: trusted TLS terminates at the database host's Traefik; its same-host Docker connection to PostgreSQL is plaintext. The runtime login has no superuser, role-creation, database-creation, replication or row-security-bypass privilege.

| Contract | Result |
| --- | --- |
| Same-origin assets, secure cookie, CSRF denial and logout | Pass |
| Incremental authenticated SSE, 25-second polling and concurrent heartbeat | Pass |
| Full container replacement; persisted session and exactly one operation effect under the replacement boot | Pass |
| Natural inactivity sleep and runtime-only secrets | Pass |
| Cron wakes a sleeping process and completes pending work without client traffic | Pass; completion observed 20.85 seconds after the sleep check |
| 0 / 1 / 5 / 20 synthetic agents | Pass; results below |

The first scheduled-wake attempt timed out before the new Cron Trigger propagated. Cloudflare documents [up to 15 minutes for propagation](https://developers.cloudflare.com/workers/configuration/cron-triggers/). Subsequent scheduled invocations succeeded; the final isolated wake check passed in 90.74 seconds, including alignment to the next minute and the sleep observation. Do not run other API probes or leave browser SSE open during that check.

The initial load harness cancelled active polls before immediately reusing agent identities. Live traces showed old poll handlers still active, causing HTTP 409 at the next sample boundary. The corrected harness drains requests instead; it still fails on unexpected responses and timeouts. These are the corrected measurements:

| Agents | Wall seconds, including drain | Completed agent requests | Errors | Go CPU seconds | Go runtime memory, MiB | PostgreSQL socket traffic, KiB |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| 0 | 60.16 | 0 | 0 | Process restarted | 16.84 | Process restarted |
| 1 | 75.78 | 7 | 0 | 1.110 | 17.09 | 525.7 |
| 5 | 77.64 | 35 | 0 | 2.381 | 17.09 | 1,378.7 |
| 20 | 81.01 | 140 | 0 | 2.657 | 21.34 | 2,559.4 |

Positive-agent samples retained the same process. At zero agents the process slept and restarted, so subtracting counters from different boots would be invalid. The table's memory is Go runtime allocation, not resident or provisioned memory. Socket counters include PostgreSQL protocol/TLS traffic. These short, idle-control-plane samples do not exercise deployment commands, build execution, bulk logs or many browser streams, and are not a capacity promise.

Cloudflare's [billing usage endpoint](https://developers.cloudflare.com/analytics/graphql-api/tutorials/querying-container-metrics/) includes micro-VM overhead that Go counters omit. Its delayed/adaptive samples must not be treated as exact per-second accounting. The available API snapshot at 14:37 UTC returned the following values for the matching sample windows:

| Agents | Provider Container CPU seconds | Worker requests | Worker CPU ms | Container resident-memory maximum, MiB |
| --- | ---: | ---: | ---: | ---: |
| 0 | 0.16 | 2 | 1.389 | 20.99 |
| 1 | 1.02 | 8 | 5.995 | 21.43 |
| 5 | 1.28 | 47 | 32.633 | 22.73 |
| 20 | 1.92 | 139 | 102.809 | 24.22 |

These provider samples did not cover every request or the full allocation duration when collected. They are reported as available telemetry, not a complete invoice or a basis for multiplying short-window CPU by a month. The cost bounds below use the declared instance allocation and explicit assumptions instead.

## Cost model

Here, an agent is a connected Astilba server program, not a user. One user may manage several agents.

Published pricing inputs checked on 22 October 2026, in USD before tax. Sources: [Containers](https://developers.cloudflare.com/containers/platform/pricing/), [Workers](https://developers.cloudflare.com/workers/platform/pricing/) and [Durable Objects](https://developers.cloudflare.com/durable-objects/platform/pricing/).

| Component | Included usage and additional price |
| --- | --- |
| Workers Paid | $5/month; 10 million requests and 30 million CPU ms; then $0.30/million requests and $0.02/million CPU ms |
| Container memory | 25 GiB-hours/month; then $0.0000025/GiB-second |
| Container disk | 200 GB-hours/month; then $0.00000007/GB-second |
| Container CPU | 375 vCPU-minutes/month; then $0.000020/vCPU-second |
| Durable Objects | 1 million requests and 400,000 GB-seconds/month; then $0.15/million requests and $12.50/million GB-seconds; excess is rounded to billable units |
| Container egress in Europe | 1 TB/month; then $0.025/GB |
| Conventional VM reference | DigitalOcean Basic: $6/month for 1 GiB, one shared vCPU and 25 GiB disk |

A 30-day model has 2,592,000 seconds. Each agent contributes 276,480 requests at the stated intervals; the minute wake adds 43,200. One continuously active `lite` container provisions 0.25 GiB memory and 2 GB disk, costing $1.395 and $0.31248 above the included allowances. Its CPU ranges from no overage to $2.79 at full 1/16-vCPU utilization. One always-active 0.128-GB Durable Object consumes 331,776 GB-seconds, within the allowance. No application data is stored in its SQLite storage.

The following bounds use those allocations, fresh account allowances, one Worker invocation and one DO request per agent/wake request, and a 1 ms Worker CPU budget per invocation. The bounds vary **container CPU**, not database cost. Zero agents assumes 30 seconds of activity per minute; startup and scheduling jitter can increase that duty cycle. This is a model, not a monthly bill or a confidence interval.

| Agents | Worker / DO requests per month | Cloudflare control-plane model | With illustrative $20 database/backup budget and no network overage |
| --- | ---: | ---: | ---: |
| 0 | 43,200 | $5.72–$6.89 | $25.72–$26.89 |
| 1 | 319,680 | $6.71–$9.50 | $26.71–$29.50 |
| 5 | 1,425,600 | $6.86–$9.65 | $26.86–$29.65 |
| 20 | 5,572,800 | $7.46–$10.25 | $27.46–$30.25 |

Use `Cloudflare model + database/backup allocation + database-provider network/connection fees + retained logs` for the total. The illustrative $20 is an assumption, not a provider quote. A database on an existing host can have no incremental instance charge but still consumes capacity and needs backups. A managed database has its own minimum: check the selected region, storage and backup terms in [DigitalOcean's PostgreSQL prices](https://www.digitalocean.com/pricing/managed-databases), or the chosen provider's quote.

For comparison, the [$6 conventional VM](https://www.digitalocean.com/pricing/droplets) plus that same $20 external-database/backup budget is $26/month before network/log overage. Running PostgreSQL on that VM changes the resilience, backup and operator-work comparison. Neither arrangement was load-tested as a production service, and a 1-GiB VM is a price reference rather than a validated production size.

Linear extrapolation of the measured socket traffic produces about 7.40 / 19.89 / 34.44 GB from PostgreSQL to the container and 11.02 / 27.24 / 49.41 GB in the reverse direction per month at 1 / 5 / 20 agents. The Cloudflare egress side remains below its European allowance in this model; the database provider may bill its outbound side. These fixture queries run frequently and are not an optimized production transport. Price that traffic explicitly instead of assuming that an external database is free to reach.

Cloudflare logging is disabled in this fixture, so paid retained-log usage is zero; add the intended retention budget for a real deployment. Account allowances are shared across deployments. UI traffic, reconnects, other reporting and deployment work add usage. The short sample and coarse provider telemetry do not establish lower customer prices.

## Hosting decision

**Pass:** the complete management fixture works on Cloudflare Containers at 0–20 tested agents, including isolated scheduled wake. Keep the portable Go service and external PostgreSQL as the application boundary, with Cloudflare as an optional deployment adapter. Do not substitute frontend-only hosting for this experiment. The measured footprint is small, but this cost model does not demonstrate a saving over a small conventional host; choose the initial hosted arrangement using production workload and operational requirements rather than a scale-to-zero claim.
