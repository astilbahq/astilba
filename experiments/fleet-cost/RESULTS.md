# Fleet cost results

Measured on 5 November 2026 against revision `35aa720` of the application, with synthetic agents and PostgreSQL 18.4. Method and limits are in the [README](README.md). Tables generated from the runs are in [summary.md](results/summary.md) and [cost.md](results/cost.md). The per-run JSON they came from is not in this repository; `run-all.sh` produces it again, as described in the README.

**Read these as sizing estimates, not bills.** Query counts, bytes, rows and pool behaviour are direct measurements. CPU was measured on Apple M5 Pro cores in a shared VM and converted to AWS vCPUs by benchmark ratios. It varied by up to about two times between runs of the same fleet, depending on host load and pool size. Nothing ran on RDS, Fargate or Graviton.

## Findings

1. **On the RDS classes AWS documents for PostgreSQL 18,** one idle server costs management **$1.72 a month at 100 servers, $0.43 at 500, $0.27 at 1,000 and $0.17 at 2,000** in us-east-1 on a single-AZ instance, and $1.88, $0.47, $0.29 and $0.18 in eu-west-1. **On `db.t4g`,** which AWS documents only up to PostgreSQL 17, it would be $0.54, $0.20, $0.18 and $0.14, with eu-west-1 about 5% more. Multi-AZ adds $0.03–0.16 on `db.t4g`. Fixed costs dominate small fleets, because the smallest documented class is far larger than 100 servers need.
2. **The application's database pool, not CPU, caps a deployment first.** The pool is fixed at `max(4, NumCPU)`, so we expect 4 on any Fargate task up to 4 vCPU. With a 0.5 ms database round trip and a 1 ms commit it carries 1,500 agents cleanly and reaches its limit at about 2,000. At 0.8 ms it saturates by 1,500. CPU at that point is only about 0.2–0.3 core each for database and application.
3. **AWS's documentation does not offer PostgreSQL 18 on `db.t4g`.** Its [class support page](https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/Concepts.DBInstanceClass.Support.html), read on 5 November 2026, says a cell naming versions means the class is "supported only for the specified database versions". The `db.t4g` cells name 17 and older, as do those for `db.m8g`, `db.r8g` and `db.m7g`. It lists 18.1 and higher for `db.m9g`, `db.m8gd` and `db.r8gd`, "Yes" for `db.m8a` and `db.r8a`, and "All available versions" for `db.m6i`. The [PostgreSQL 18 release notes](https://docs.aws.amazon.com/AmazonRDS/latest/PostgreSQLReleaseNotes/postgresql-versions.html) and [launch announcement](https://aws.amazon.com/about-aws/whats-new/2025/11/amazon-rds-postgresql-major-version-18) name no classes. The page could lag behind the service, and only an AWS account can confirm with `aws rds describe-orderable-db-instance-options --engine postgres --engine-version 18.6 --db-instance-class db.t4g.medium`. So plan on the documented classes. `db.m6i.large` is the cheapest, if "All available versions" includes 18. If it does not, the cheapest are `db.m9g.large` in us-east-1 and `db.m8gd.large` in eu-west-1, and 100 servers cost $1.76 and $2.11. PostgreSQL 17 on `db.t4g` is the other route to the lower prices, but this measurement ran only on 18.
4. **Replacing the one-second tick removes 86% of queries but only about 10% of the 2,000-server bill on the documented classes and 20% on `db.t4g`, and nothing at 100.** It is worth doing for capacity, not price. Details are [below](#replacing-the-one-second-tick).
5. Operations in progress cost far more than idle polling. An agent with a running command re-polls every 250 ms and the server re-reads and re-sends the whole command each time.

Against the working estimate of $0.30–0.50 a server, the documented classes cost over three times as much at 100 servers, about the same at 500, and less than its low end at 1,000 or more on a single-AZ instance. The bill excludes support, dashboard traffic, builds, email and hosted-only sweeps, so compare it with competitor prices of about $3 to $4.50 a server as a floor rather than a margin.

## Idle and active load

Each row is one 8-minute window with 5 servers a workspace, on the shipped pool of 4 unless the name says `pool12` or `sized-`. `tmpfs-` and `sized-` runs have a 1 ms commit and a 0.41–0.56 ms database ping, except `slow-db` at 0.79 ms. CPU is raw: a guest counts time its vCPU was preempted by the host, so a busier host inflates it.

| Run | Window start, UTC | Agents | q/s | q/s per agent | DB cores | App cores | App MiB | Clients | Heartbeat p50 / p99 | Failed in window | Host load start → end |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- | ---: | --- |
| `idle-0000` | 14:40 | 0 | 4 | – | 0.005 | 0.007 | 18 | 5 | – | – | 17 → 31 |
| `tmpfs-idle-0100` | 18:09 | 100 | 355 | 3.55 | 0.031 | 0.038 | 38 | 5 | 4 ms / 16 ms | 0 | 7 → 5 |
| `tmpfs-idle-0500` | 18:22 | 500 | 1,724 | 3.45 | 0.071 | 0.086 | 85 | 5 | 4 ms / 16 ms | 0 | 3 → 6 |
| `tmpfs-idle-1000` | 20:52 | 1,000 | 3,354 | 3.35 | 0.190 | 0.217 | 134 | 5 | 4 ms / 33 ms | 0 | 7 → 9 |
| `tmpfs-idle-1500` | 18:36 | 1,500 | 5,127 | 3.42 | 0.175 | 0.216 | 185 | 5 | 4 ms / 262 ms | 0 | 4 → 4 |
| `tmpfs-idle-2000` | 20:09 | 2,000 | 6,825 | 3.41 | 0.229 | 0.289 | 243 | 5 | 33 ms / 1.0 s | 3 | 7 → 3 |
| `tmpfs-idle-2000-contended` | 16:18 | 2,000 | 6,162 | 3.08 | 0.379 | 0.437 | 269 | 5 | 262 ms / 4.2 s | 2604 | 18 → 33 |
| `tmpfs-idle-2000-pool12` | 16:33 | 2,000 | 6,811 | 3.41 | 0.372 | 0.426 | 254 | 13 | 4 ms / 1.0 s | 0 | 10 → 11 |
| `tmpfs-idle-1500-slow-db` | 19:54 | 1,500 | 4,977 | 3.32 | 0.192 | 0.239 | 190 | 5 | 1.0 s / 1.0 s | 189 | 3 → 4 |
| `tmpfs-idle-2000-no-tick` | 21:06 | 2,000 | 958 | 0.48 | 0.106 | 0.145 | 244 | 5 | 4 ms / 66 ms | 0 | 6 → 8 |
| `tmpfs-active-1000` | 18:58 | 1,000 | 4,815 | 4.82 | 0.611 | 0.263 | 134 | 5 | 8 ms / 524 ms | 0 | 6 → 6 |
| `tmpfs-active-1000-pool12` | 17:40 | 1,000 | 5,007 | 5.01 | 0.863 | 0.379 | 144 | 13 | 4 ms / 524 ms | 0 | 9 → 6 |
| `sized-0100` | 19:39 | 100 | 357 | 3.57 | 0.020 | 0.023 | 41 | 12 | 4 ms / 8 ms | 0 | 4 → 2 |
| `sized-1000` | 19:26 | 1,000 | 3,436 | 3.44 | 0.120 | 0.100 | 137 | 13 | 4 ms / 8 ms | 0 | 2 → 3 |
| `sized-2000` | 19:12 | 2,000 | 6,840 | 3.42 | 0.290 | 0.218 | 257 | 13 | 4 ms / 66 ms | 0 | 3 → 8 |

Latencies are the upper bound of a power-of-two bucket, so they are within a factor of two. "Failed" counts rejected and timed-out requests in the window. Clients are the pool plus one connection outside it, which we take to be River's listener. No run failed a deployment.

Host contention affects the pool limit and latency tails: the same 2,000-agent configuration reached p50 262 ms and 2,604 failures at load 18 to 33, against p50 33 ms and 3 failures at load 3 to 7.

**Before each window.** Every run from 1,000 agents up, and `idle-0500`, logged timed-out polls before its window: 68 to 5,954, with PostgreSQL busy on 24 hours of seeded metrics at about that time. Those failures are not in the table. Error text in most runs has no timestamps, so that cause is an inference. The two runs repeated with timestamps, `tmpfs-idle-1000` and `tmpfs-idle-2000-no-tick`, show their kept errors over seconds about two minutes before the window.

**Memory.** The application uses about 0.12 MiB per agent over a 20 MiB base. PostgreSQL's private memory was 21–41 MiB with 5 connections and 67–102 MiB with 13. Its shared memory says little about agents, because the harness itself fills it: 221 MiB with no agents after the `pgbench` calibration, then following the seeded table up to the 1 GiB `shared_buffers` limit (375, 672 and 1,078 MiB at 500, 1,000 and 2,000 agents). The window added at most 3 MiB, so the cache agents need was not isolated, and the sizing below uses a planning rule instead.

### Queries by statement

Per agent per second at 2,000 idle agents (`tmpfs-idle-2000-pool12`) and in the active run (`tmpfs-active-1000`, 1,000 agents with about 33 deployments in flight):

| Statement | Idle /s | Idle share of exec time | Active /s | Active share of exec time |
| --- | ---: | ---: | ---: | ---: |
| `AuthenticateAgent` | 1.138 | 44% | 1.381 | 5.8% |
| `ListTroubleshootingOffers` | 1.014 | 21% | 1.110 | 2.6% |
| Poll command query | 1.014 | 12% | 1.110 | 81% |
| `RecordAgentHeartbeat` | 0.067 | 7.6% | 0.068 | 1.0% |
| Poll lease take and release | 0.080 | 10% | 0.336 | 4.9% |
| Metric insert, prune, credential lock | 0.051 | 4.7% | 0.069 | 1.8% |
| Metric ownership check, per service | 0 | 0 | 0.085 | 0.4% |

An idle agent generates 3.41 q/s, and the tick accounts for about 2.9 of them: the first check of each poll remains without it. The poll command query rises from 0.005 ms to 0.2 ms on average once the server has operations. Hydration adds further queries, listed in [summary.md](results/summary.md).

### Traffic, logs and database growth

| Per server, idle | Rate | Per month |
| --- | ---: | ---: |
| Agent to application, including TCP/IP headers | 115 B/s | 300 MB |
| Application to agent | 120 B/s | 316 MB |
| Application to database | 745 B/s | 1.96 GB |
| Database to application | 372 B/s | 0.98 GB |
| WAL | 70–75 B/s | 184–197 MB |
| Request log lines | 15 B/s | 39 MB |

In the active run, application-to-agent traffic rises to 652 B/s and WAL to 320 B/s per server. Compared with idle, each deployment that stays in progress for two minutes adds about 1.9 MB of egress.

Database size under the default retention:

| Data | Bound | Per server |
| --- | --- | ---: |
| Server row | Fixed; 99.8% of updates are HOT | 7.5 KB including bloat |
| Metric history | 24 hours, reached on day one, then flat | 0.63 MB with no services; 2.5 MB with five services |
| Dead metric rows | Autovacuum's default 20% threshold, never reached in a window, so this is an estimate | up to 0.13 MB; up to 0.5 MB |
| Each deployment: operations, plan, revision and audit rows | Rows are kept; steps and notes are cleared after 30 days | 12 KB |
| Build logs, not exercised | 16 KiB tail, cleared after 30 days | up to 16 KiB per build |

So an idle server stops growing after its first day. One deployment a day adds 0.36 MB a month, and hourly deployments add 8.6 MB. Live rows cost under $0.001 a server a month to store. The bill's storage and backup line is larger, from the 20 GB minimum at small fleets ($0.023 a server at 100) and seven days of WAL at large ones ($0.005 at 2,000).

## AWS sizing

Prices come from AWS's [Price List Bulk API](https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonRDS/current/us-east-1/index.json), published between 19 October and 4 November 2026, plus the [VPC page](https://aws.amazon.com/vpc/pricing/) for public IPv4 addresses. All are on demand with no free tier, Savings Plans or reservations, over 730 hours.

| Item | us-east-1 | eu-west-1 |
| --- | ---: | ---: |
| `db.t4g.micro` / `small` / `medium` / `large`, single-AZ, per hour | $0.016 / 0.032 / 0.065 / 0.129 | $0.017 / 0.035 / 0.069 / 0.138 |
| `db.m8g.large`, `db.m8gd.large`, single-AZ, per hour | $0.168, $0.1976 | $0.186, $0.228 |
| `db.m6i.large`, single-AZ, per hour | $0.178 | $0.197 |
| `db.m9g.large`, single-AZ, per hour | $0.183 | No price listed |
| `db.m8a.large`, `db.r8gd.large`, `db.r8a.large`, also documented for 18, single-AZ, per hour | $0.2149, $0.273, $0.3019 | $0.2379, $0.3042, $0.3381 |
| gp3 storage, single-AZ / Multi-AZ, per GB-month | $0.115 / 0.23 | $0.127 / 0.254 |
| Backup storage beyond the free allowance, per GB-month | $0.095 | $0.095 |
| T4g surplus CPU credit, per vCPU-hour | $0.075 | $0.075 |
| Fargate ARM, per vCPU-hour and GB-hour | $0.03238, $0.00356 | $0.03238, $0.00356 |
| Application Load Balancer, per hour and LCU-hour | $0.0225, $0.008 | $0.0252, $0.008 |
| Internet egress, first 10 TB, and cross-AZ each way, per GB | $0.09, $0.01 | $0.09, $0.01 |
| CloudWatch Logs, ingest and stored, per GB | $0.50, $0.03 | $0.57, $0.03 |
| Public IPv4 address, per hour | $0.005 | $0.005 |

**CPU conversion.** One measured core becomes 3.5 Graviton2 vCPUs (`db.t4g`, Fargate ARM, and the Ice Lake hyperthreads of `db.m6i`, conservatively) and 2.25 Graviton4 vCPUs (`db.m8g`, `db.m8gd`, and `db.m9g` for want of a Graviton5 result). The ratios use Geekbench 6 single-core results: [M5 Pro](https://browser.geekbench.com/v6/cpu/17671721) about 4,240 to 4,300, [`c7g.large`](https://browser.geekbench.com/v6/cpu/4651937) 1,467 and [`r8g`](https://browser.geekbench.com/v6/cpu/6855315) 1,882. Graviton2 is estimated at about 1,200 from AWS's claim of up to 25% better compute for Graviton3, and Fargate ARM is treated as Graviton2. These are individual composite scores, not a PostgreSQL benchmark. Raw CPU from a shared VM is likely to overstate use, so these conversions lean conservative.

**Sizing rules.** Sustained CPU stays at or under 60% of a class's vCPUs. RAM is at least 0.5 GiB plus 1.5 times the database. These are planning rules, not measurements. A task's memory is at least twice the application's.

### Burstable classes

RDS runs `db.t4g` in [Unlimited mode](https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/Concepts.DBInstanceClass.Types.html), so an empty credit balance costs money rather than speed. The database used 0.40–0.66 Graviton2 vCPU per 1,000 idle servers across the quieter runs, which sets where each [baseline](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/burstable-credits-baseline-concepts.html) is exceeded:

| Class | Baseline vCPU | Sustained credits run out at about | Measured beyond that | RAM rule stops it at about |
| --- | ---: | ---: | --- | ---: |
| `db.t4g.micro` | 0.2 | 300–500 idle servers | At 500: full balance gone in 103 hours, then $3 a month | 535 servers |
| `db.t4g.small` | 0.4 | 600–1,000 | At 1,000: 36 hours, then $14 | 1,650 |
| `db.t4g.medium` | 0.4 | 600–1,000 | At 2,000: 24 hours, then $22 | 3,900 |
| `db.t4g.large` | 0.6 | 900–1,500 | At 2,000: 71 hours, then $11 | 8,400 |
| `db.m8g.large` | None | Never | 2,000 idle at 26% of 2 vCPUs; the active 1,000 at 69%, over the 60% rule | 8,400 |

Each deployment in flight adds about as much database CPU as 80 idle servers, so bursts drain credits well before these sustained figures. RDS's own overhead is not included. The surcharge is low: until a class hits the 60% rule, paying it is usually cheaper than moving up a size.

### Cost per server per month

Single task with a public address in the database's zone, a two-zone load balancer, seven days of WAL, and all request logs in CloudWatch. "Cross-AZ" is what placing the task in another zone would add. The columns headed `db.t4g` depend on [finding 3](#findings).

| Fleet | Documented for PostgreSQL 18, single-AZ, us / eu | Documented for PostgreSQL 18, Multi-AZ, us / eu | Fargate | `db.t4g` class | `db.t4g`, single-AZ, us / eu | `db.t4g`, Multi-AZ, us / eu | Cross-AZ adds |
| ---: | --- | --- | --- | --- | --- | --- | ---: |
| 100 idle | $1.72 / $1.88 | $3.04 / $3.35 | 0.25 vCPU / 0.5 GB | `micro` | $0.54 / $0.57 | $0.68 / $0.73 | $0.06 |
| 500 idle | $0.43 / $0.47 | $0.70 / $0.76 | 1 / 2 | `micro` | $0.20 / $0.21 | $0.23 / $0.25 | $0.06 |
| 1,000 idle | $0.27 / $0.29 | $0.40 / $0.44 | 2 / 4 | `small` | $0.18 / $0.19 | $0.22 / $0.23 | $0.06 |
| 1,500 idle | $0.20 / $0.22 | $0.29 / $0.31 | 2 / 4 | `small` | $0.14 / $0.14 | $0.16 / $0.17 | $0.06 |
| 2,000 idle, pool at its limit | $0.17 / $0.18 | $0.23 / $0.25 | 2 / 4 | `medium` | $0.14 / $0.14 | $0.17 / $0.18 | $0.06 |
| 1,000, hourly deployments | $0.58 / $0.62 | $0.84 / $0.91 | 2 / 4 | `xlarge` | $0.54 / $0.56 | $0.76 / $0.80 | $0.11 |

On `db.t4g` at 2,000 servers, the $273 monthly bill is $69 database, $58 task, $37 load balancer and addresses, $59 egress, $41 logs and $9 storage and backup. Two tasks would add $58, and `ASTILBA_TROUBLESHOOTING=single-instance` currently requires one.

**Sensitivity,** on `db.t4g`. Scaling the CPU conversion by 0.7 gives $0.54 at 100 servers, $0.13 at 2,000 and $0.44 for the active run. Scaling it by 1.5 gives $0.61, $0.19 and $0.62.

**CPU quota check.** The `sized-` runs limited the database to 0.8 core and the application to 0.1 core at 100 servers, 0.4 at 1,000 and 0.8 at 2,000: the vCPUs of a Fargate task proposed at the time ÷ 3.5 × an assumed 1.4 slowdown. All had no failures and heartbeat p99 under 66 ms. They were set before the final sizing, which proposes 2 vCPU at 1,000 servers, so that fleet was tested at half of it. They used a pool of 12, because quotas do not change `NumCPU`, and they do not test memory limits.

## The pool limit

Every query holds one of the pool's connections for at least its network round trip, so the pool can carry about 4 ÷ round trip queries a second, and an idle agent needs 3.41. That is roughly 1,170 agents divided by the round trip in milliseconds, and less once commits and deployments are added. Measured, with a 1 ms commit:

| Database ping | Pool | Agents | q/s demanded → achieved | Heartbeat p50 | Timed-out polls |
| ---: | ---: | ---: | --- | ---: | ---: |
| 0.56 ms | 4 | 1,500 | 5,130 → 5,127 | ≤ 4 ms | 0 |
| 0.49 ms | 4 | 2,000 | 6,820 → 6,825 | ≤ 33 ms | 3 |
| 0.79 ms | 4 | 1,500 | 5,130 → 4,977 | ≤ 1 s | 189 |
| 0.50 ms | 12 | 2,000 | 6,820 → 6,811 | ≤ 4 ms | 0 |
| 0.50 ms | 4, no tick | 2,000 | 960 → 958 | ≤ 4 ms | 0 |

The limit has little margin. Running 2,000 agents at the same round trip on a busier host (`tmpfs-idle-2000-contended`, load 18 to 33) fell to 6,162 q/s, with heartbeat p50 at 262 ms, 2,095 failed polls and 509 lease conflicts. Polls wait in the queue past the agent's 30-second limit, and the leases they leave behind refuse the retries. Same-AZ placement should help and a Multi-AZ instance's synchronous commit should hurt; neither was measured.

`pool_max_conns` in `ASTILBA_DATABASE_URL` does not help: `migrate` failed with it, and both `migrate` and the startup schema check open that URL through `database/sql`, where pgx sends the parameter to PostgreSQL as a setting. Plan on about 1,000 servers per deployment until the pool is configurable, which leaves room for deployments and a slower link.

## Replacing the one-second tick

A throwaway build with the tick removed ([no-tick.patch](no-tick.patch)) measures the upper bound of a wake-up such as LISTEN/NOTIFY, at 2,000 agents on the pool of 4. It leaves commands waiting up to 25 s, so it is not itself usable. A real wake-up also needs a listener connection, a notification from every path that creates a command or offer, a fallback tick and a re-check after waking.

| 2,000 idle agents | With the tick | Without |
| --- | ---: | ---: |
| Queries per second | 6,825 | 958 |
| Database cores | 0.229 | 0.106 |
| Application cores | 0.289 | 0.145 |
| Application and database traffic, MB per server per month | 2,940 | 545 |
| WAL | Unchanged | Unchanged |
| Internet egress, per server | 124 B/s, within the 117–124 B/s spread between runs | 117 B/s |
| us-east-1 single-AZ on `db.m6i.large`, per month | $334 | $301 |
| us-east-1 single-AZ on `db.t4g`, per month | $273 | $218 |

About $4 of the difference is that run-to-run egress and log spread. An earlier run of the same build measured 0.076 and 0.101 core, which gives the same sizes and bill. So the saving is about:

- **100 servers:** nothing, because the smallest classes already hold the load. This also holds on the documented classes, where the wake-up changes no database size up to 2,000 servers.
- **500 servers:** $0.03–0.05 a server, estimated by scaling rather than run.
- **1,000 servers:** $0.04–0.06 a server, from a smaller task and no surcharge, also estimated.
- **2,000 servers:** $0.02 a server on `db.m6i.large`, about 10%, or $0.03 on `db.t4g`, about 20%. Where task and database cross zones, add $0.05. The estimates above are on `db.t4g`.

Against a price of $3 to $4.50 a server that is under 3% of revenue even across zones, so **it is not worth doing for cost.** Sampling request logs is a simpler saving worth comparing: logs are $2 of the 100-server bill and $41 of the 2,000-server bill.

**It is worth doing for capacity.** The same run shows the pool of 4 carrying 2,000 agents with heartbeat p50 ≤ 4 ms at about one seventh of the query rate. A sensible order:

1. **When a deployment nears 1,000 servers:** make the pool size configurable. It is a small change, and it moves the limit to CPU.
2. **Before a deployment passes about 2,000 servers, or if PostgreSQL needs fewer connections or a smaller class:** build the wake-up. Lengthening the tick to 5 s is a one-line intermediate step, estimated (not measured) to remove about 70% of queries while mean command delivery rises from about 0.5 s to about 2.5 s.

Separately, not re-reading and re-sending an unchanged command every 250 ms would reduce cost in busy fleets, where the tick is not the main expense.

## What was not measured

- RDS, Fargate, Graviton, TLS to the database or the load balancer itself. The commit delay and `netem` round trip stand in for them.
- Real agents beyond their request pattern, browser sessions and server-sent events, builds, backups and schedule reports. Hosted mode's admission checks on delivered commands and its expiry sweep.
- A steady state longer than eight minutes. Metric history is seeded. Autovacuum ran on the metric table in the 2,000-agent and active windows, well short of its dead-row threshold, so seeding may have caused those runs and its long-term cadence is unknown.
- A quiet host throughout. The host was shared with other work, and the load table gives each window and its load. Other sessions reported heavy work from about 17:05 to 17:37 UTC. The two windows that fell in it, for 100 and 500 agents, were discarded and repeated at 18:09 and 18:22, and no published window falls in it. Runs at 2,000 and 1,000 agents with a window ending at load above 15 were also repeated, keeping the first only as `tmpfs-idle-2000-contended`. Runs behind the bill started at load 3 to 7. `idle-0100` overlapped heavy work and is used only for memory. Of the rest, the disk `idle-` series and `tmpfs-idle-2000-pool12` were the busiest.
- One harness version. The harness changed between runs and was uncommitted until afterwards, so results record the application's `35aa720`, as `35aa720-dirty` in `tmpfs-idle-1000` and `tmpfs-idle-2000-no-tick`, the last two run. Changes affected tmpfs and CPU pinning options, generator session handling and error logging, and added the metric call's 5 s send limit, which no earlier window reached; none altered the rest of the request pattern or the counters read. `tmpfs-idle-2000-contended` was the first run of `tmpfs-idle-2000`.

Two side findings:

- The alert sweep scans at most 20 workspaces a minute (`internal/alerts/detect.go`). At five servers a workspace, 2,000 servers means a 20-minute scan cycle rather than the intended 30 seconds.
- One run was refused server creation with `recent_auth_required` seconds after a successful login. It happened once in about 25 runs and was not investigated; the generator now reauthenticates and retries.
