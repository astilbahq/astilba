# Fleet capacity results

Measured on 5 November 2026 against application revision `35aa720`, with synthetic agents and PostgreSQL 18.4. Method and limits are in the [README](README.md). Tables generated from the runs are in [summary.md](results/summary.md). The per-run JSON they came from is not in this repository; `run-all.sh` produces it again, as described in the README.

**Read the CPU and sizing as estimates.** Query counts, bytes, rows and pool behaviour are direct measurements. CPU was measured on Apple M5 Pro cores in a shared VM, and sizing converts it to AWS vCPUs by benchmark ratios. CPU varied by up to about two times between runs of the same fleet, depending on host load and pool size. Nothing ran on RDS, Fargate or Graviton.

## Findings

1. **Load is close to linear per agent.** An idle agent makes about 3.4 queries a second and exchanges about 235 B/s with the application and 1.1 KB/s of database traffic. On a quiet host, 1,000 idle agents used about 0.15 core of PostgreSQL and 0.19 core of the application.
2. **The application's database pool, not CPU, limits a deployment first.** The pool is fixed at `max(4, NumCPU)`, so we expect 4 on any Fargate task up to 4 vCPU. It can carry about 4 ÷ round trip queries a second, so its ceiling is roughly 1,170 idle agents ÷ the round trip in milliseconds. On a held host it carried 2,000 agents at 0.56 ms and 1,500 at 0.75 ms with no failures, at about 93% of that ceiling. Slightly more delay or host load tipped similar runs into timeouts. CPU at that point is only about 0.2–0.3 core each.
3. **Replacing the one-second tick removes 86% of queries and about half of CPU, and it halves the task at 2,000 servers.** It is most useful for capacity. Details are [below](#replacing-the-one-second-tick).
4. **Operations in progress are far heavier than idle polling.** An agent with a running command re-polls every 250 ms, and the server re-reads and re-sends the whole command each time.

## Idle and active load

Each row is one 8-minute window with 5 servers a workspace, on the shipped pool of 4 unless the name says `pool12` or `sized-`. `tmpfs-` and `sized-` runs have a 1 ms commit and a 0.41–0.56 ms database ping, except `slow-db` at 0.75 ms. CPU is raw: a guest counts time its vCPU was preempted by the host, so a busier host inflates it.

| Run | Window start, UTC | Agents | q/s | q/s per agent | DB cores | App cores | App MiB | Clients | Heartbeat p50 / p99 | Failed in window | Host load start → end |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- | ---: | --- |
| `idle-0000` | 14:40 | 0 | 4 | – | 0.005 | 0.007 | 18 | 5 | – | – | 17 → 31 |
| `tmpfs-idle-0100` | 18:09 | 100 | 355 | 3.55 | 0.031 | 0.038 | 38 | 5 | 4 ms / 16 ms | 0 | 7 → 5 |
| `tmpfs-idle-0500` | 18:22 | 500 | 1,724 | 3.45 | 0.071 | 0.086 | 85 | 5 | 4 ms / 16 ms | 0 | 3 → 6 |
| `tmpfs-idle-1000` | 22:55 | 1,000 | 3,351 | 3.35 | 0.149 | 0.185 | 134 | 5 | 4 ms / 16 ms | 0 | 4 → 4 |
| `tmpfs-idle-1500` | 18:36 | 1,500 | 5,127 | 3.42 | 0.175 | 0.216 | 185 | 5 | 4 ms / 262 ms | 0 | 4 → 4 |
| `tmpfs-idle-2000` | 22:28 | 2,000 | 6,670 | 3.33 | 0.231 | 0.293 | 241 | 5 | 4 ms / 262 ms | 0 | 5 → 6 |
| `tmpfs-idle-2000-contended` | 16:18 | 2,000 | 6,162 | 3.08 | 0.379 | 0.437 | 269 | 5 | 262 ms / 4.2 s | 2604 | 18 → 33 |
| `tmpfs-idle-2000-pool12` | 16:33 | 2,000 | 6,811 | 3.41 | 0.372 | 0.426 | 254 | 13 | 4 ms / 1.0 s | 0 | 10 → 11 |
| `tmpfs-idle-1500-slow-db` | 22:41 | 1,500 | 4,990 | 3.33 | 0.225 | 0.275 | 185 | 5 | 8 ms / 524 ms | 0 | 6 → 9 |
| `tmpfs-idle-2000-no-tick` | 23:08 | 2,000 | 957 | 0.48 | 0.104 | 0.143 | 243 | 5 | 4 ms / 16 ms | 0 | 3 → 3 |
| `tmpfs-active-1000` | 18:58 | 1,000 | 4,815 | 4.82 | 0.611 | 0.263 | 134 | 5 | 8 ms / 524 ms | 0 | 6 → 6 |
| `tmpfs-active-1000-pool12` | 17:40 | 1,000 | 5,007 | 5.01 | 0.863 | 0.379 | 144 | 13 | 4 ms / 524 ms | 0 | 9 → 6 |
| `sized-0100` | 19:39 | 100 | 357 | 3.57 | 0.020 | 0.023 | 41 | 12 | 4 ms / 8 ms | 0 | 4 → 2 |
| `sized-1000` | 19:26 | 1,000 | 3,436 | 3.44 | 0.120 | 0.100 | 137 | 13 | 4 ms / 8 ms | 0 | 2 → 3 |
| `sized-2000` | 19:12 | 2,000 | 6,840 | 3.42 | 0.290 | 0.218 | 257 | 13 | 4 ms / 66 ms | 0 | 3 → 8 |

Latencies are the upper bound of a power-of-two bucket, so they are within a factor of two. "Failed" counts rejected and timed-out requests in the window. Clients are the pool plus one connection outside it, which we take to be River's listener. No run failed a deployment.

**Before each window.** Every run from 1,000 agents up, and `idle-0500`, logged timed-out polls before its window: 68 to 5,954, with PostgreSQL busy on 24 hours of seeded metrics at about that time. Those failures are not in the table. Error text in most of the runs has no timestamps, so that cause is an inference. Runs repeated with timestamps show their kept errors over seconds about two minutes before the window.

**Memory.** The application uses about 0.12 MiB per agent over a 20 MiB base. PostgreSQL's private memory was 21–41 MiB with 5 connections and 67–102 MiB with 13. Its shared memory says little about agents, because the harness itself fills it: 221 MiB with no agents after the `pgbench` calibration, then following the seeded table up to the 1 GiB `shared_buffers` limit (375, 672 and 1,078 MiB at 500, 1,000 and 2,000 agents). The window added at most 3 MiB, so the cache agents need was not isolated, and the sizing below uses a planning rule instead.

### Host overlap

The host was shared, and other work moved results. Sessions reported:

| Period, UTC | Work | What happened to overlapping runs |
| --- | --- | --- |
| About 17:05–17:37 | Other sessions' tests, a 4 CPU VM and a full integration comparison | The 100 and 500 agent windows that fell in it were discarded and repeated at 18:09 and 18:22 |
| 19:48–20:17 | Two full integration runs, one with the race detector, and a 4 CPU VM | `tmpfs-idle-1500-slow-db` and `tmpfs-idle-2000` overlapped and were repeated |
| From about 20:30, exact times unknown | Test suites on two other VMs, including four integration lanes | `tmpfs-idle-1000` and `tmpfs-idle-2000-no-tick` may have overlapped and were repeated |

The four repeats ran with other project sessions holding heavy work from 22:22 to 23:17 UTC. Work outside those sessions was not held, so each result still records host load. First versions against repeats:

| Run | First window, UTC | First: q/s, DB / app cores, p50 / p99, failed, load | Repeat: q/s, DB / app cores, p50 / p99, failed, load |
| --- | --- | --- | --- |
| 2,000 agents | 20:09 | 6,825, 0.229 / 0.289, 33 ms / 1.0 s, 3, 7 → 3 | 6,670, 0.231 / 0.293, 4 ms / 262 ms, 0, 5 → 6 |
| 1,500 agents, slower database | 19:54 | 4,977, 0.192 / 0.239, 1.0 s / 1.0 s, 189, 3 → 4 | 4,990, 0.225 / 0.275, 8 ms / 524 ms, 0, 6 → 9 |
| 1,000 agents | 20:52 | 3,354, 0.190 / 0.217, 4 ms / 33 ms, 0, 7 → 9 | 3,351, 0.149 / 0.185, 4 ms / 16 ms, 0, 4 → 4 |
| 2,000 agents, no tick | 21:06 | 958, 0.106 / 0.145, 4 ms / 66 ms, 0, 6 → 8 | 957, 0.104 / 0.143, 4 ms / 16 ms, 0, 3 → 3 |

So host contention mostly moved latency tails and pool failures. The load figure alone did not reveal it: the 1,500-agent first version read load 3 to 4 while other VMs were busy. The tables here use the repeats. `tmpfs-idle-2000-contended`, an earlier 2,000-agent run at load 18 to 33, is kept as evidence of how little margin the pool has.

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

In the active run, application-to-agent traffic rises to 652 B/s and WAL to 320 B/s per server. Compared with idle, each deployment that stays in progress for two minutes adds about 1.9 MB of traffic to the agent.

Database size under the default retention:

| Data | Bound | Per server |
| --- | --- | ---: |
| Server row | Fixed; 99.8% of updates are HOT | 7.5 KB including bloat |
| Metric history | 24 hours, reached on day one, then flat | 0.63 MB with no services; 2.5 MB with five services |
| Dead metric rows | Autovacuum's default 20% threshold, never reached in a window, so this is an estimate | up to 0.13 MB; up to 0.5 MB |
| Each deployment: operations, plan, revision and audit rows | Rows are kept; steps and notes are cleared after 30 days | 12 KB |
| Build logs, not exercised | 16 KiB tail, cleared after 30 days | up to 16 KiB per build |

So an idle server stops growing after its first day. One deployment a day adds 0.36 MB a month, and hourly deployments add 8.6 MB.

## Sizing

**CPU conversion.** One measured core becomes 3.5 Graviton2 vCPUs (`db.t4g`, Fargate ARM) and 2.25 Graviton4 vCPUs. The ratios use Geekbench 6 single-core results: [M5 Pro](https://browser.geekbench.com/v6/cpu/17671721) about 4,240 to 4,300, [`c7g.large`](https://browser.geekbench.com/v6/cpu/4651937) 1,467 and [`r8g`](https://browser.geekbench.com/v6/cpu/6855315) 1,882. Graviton2 is estimated at about 1,200 from AWS's claim of up to 25% better compute for Graviton3, and Fargate ARM is treated as Graviton2. These are individual composite scores, not a PostgreSQL benchmark. Raw CPU from a shared VM is likely to overstate use, so the conversions lean conservative.

**Rules.** Sustained CPU stays at or under 60% of the vCPUs. Database RAM is at least 0.5 GiB plus 1.5 times the database. A task's memory is at least twice the application's. These are planning rules, not measurements.

| Fleet | Database, Graviton2 vCPU used | Database RAM | Fargate task, vCPU / GB |
| ---: | ---: | ---: | --- |
| 100 idle | 0.11 | 0.6 GiB | 0.25 / 0.5 |
| 500 idle | 0.25 | 1 GiB | 1 / 2 |
| 1,000 idle | 0.52 | 1.4 GiB | 2 / 4 |
| 1,500 idle | 0.61 | 1.9 GiB | 2 / 4 |
| 2,000 idle | 0.81 | 2.3 GiB | 2 / 4 |
| 1,000, hourly deployments | 2.14 | 4 GiB | 2 / 4 |

**Burstable baselines.** The database used 0.40–0.52 Graviton2 vCPU per 1,000 idle servers, which sets where each [baseline](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/burstable-credits-baseline-concepts.html) is exceeded. A class above its baseline draws down its credit balance:

| Class | Baseline vCPU | Sustained use exceeds it at about | A full balance lasts | RAM rule stops it at about |
| --- | ---: | ---: | --- | ---: |
| `db.t4g.micro` | 0.2 | 400–500 idle servers | 103 hours at 500 | 535 servers |
| `db.t4g.small` | 0.4 | 800–1,000 | 80 hours at 1,000 | 1,650 |
| `db.t4g.medium` | 0.4 | 800–1,000 | 23 hours at 2,000 | 3,900 |
| `db.t4g.large` | 0.6 | 1,150–1,500 | 69 hours at 2,000 | 8,400 |

Each deployment in flight adds about as much database CPU as 80 idle servers, so bursts drain credits well before these sustained figures. RDS's own overhead is not included.

**CPU quota check.** The `sized-` runs limited the database to 0.8 core and the application to 0.1 core at 100 servers, 0.4 at 1,000 and 0.8 at 2,000: the vCPUs of a Fargate task proposed at the time ÷ 3.5 × an assumed 1.4 slowdown. All had no failures and heartbeat p99 under 66 ms. They were set before the final sizing, which proposes 2 vCPU at 1,000 servers, so that fleet was tested at half of it. They used a pool of 12, because quotas do not change `NumCPU`, and they do not test memory limits.

## The pool limit

Every query holds one of the pool's connections for at least its network round trip, so the pool can carry about 4 ÷ round trip queries a second. An idle agent needs 3.41, and commits and deployments need more. Measured, with a 1 ms commit:

| Database ping | Pool | Agents | Host | q/s demanded → achieved | Heartbeat p50 | Failed polls |
| ---: | ---: | ---: | --- | --- | ---: | ---: |
| 0.56 ms | 4 | 1,500 | Load 4 | 5,130 → 5,127 | ≤ 4 ms | 0 |
| 0.56 ms | 4 | 2,000 | Held, load 5 to 6 | 6,820 → 6,670 | ≤ 4 ms | 0 |
| 0.75 ms | 4 | 1,500 | Held, load 6 to 9 | 5,130 → 4,990 | ≤ 8 ms | 0 |
| 0.79 ms | 4 | 1,500 | Overlapped | 5,130 → 4,977 | ≤ 1 s | 189 |
| 0.49 ms | 4 | 2,000 | Overlapped | 6,820 → 6,825 | ≤ 33 ms | 3 |
| 0.53 ms | 4 | 2,000 | Load 18 to 33 | 6,820 → 6,162 | ≤ 262 ms | 2,095 |
| 0.50 ms | 12 | 2,000 | Load 10 to 11 | 6,820 → 6,811 | ≤ 4 ms | 0 |
| 0.53 ms | 4, no tick | 2,000 | Held, load 3 | 960 → 957 | ≤ 4 ms | 0 |

The limit has little margin. Held runs at about 93% of the ceiling had no failures but were already 2–3% short of demand. In the overlapped 0.79 ms run, demand was about 101% of the ceiling, and heartbeats took up to 1 s. At load 18 to 33, polls waited in the queue past the agent's 30-second limit, and the leases they left behind refused the retries, producing 509 lease conflicts. Same-AZ placement should help and a Multi-AZ instance's synchronous commit should hurt; neither was measured.

`pool_max_conns` in `ASTILBA_DATABASE_URL` does not help: `migrate` failed with it, and both `migrate` and the startup schema check open that URL through `database/sql`, where pgx sends the parameter to PostgreSQL as a setting. Plan on about 1,000 servers per deployment until the pool is configurable, which leaves room for deployments and a slower link.

## Replacing the one-second tick

A throwaway build with the tick removed ([no-tick.patch](no-tick.patch)) measures the upper bound of a wake-up such as LISTEN/NOTIFY, at 2,000 agents on the pool of 4. It leaves commands waiting up to 25 s, so it is not itself usable. A real wake-up also needs a listener connection, a notification from every path that creates a command or offer, a fallback tick and a re-check after waking.

| 2,000 idle agents | With the tick | Without |
| --- | ---: | ---: |
| Queries per second | 6,670 | 957 |
| Database cores | 0.231 | 0.104 |
| Application cores | 0.293 | 0.143 |
| Database, Graviton2 vCPU used | 0.81 | 0.36 |
| Fargate task, vCPU / GB | 2 / 4 | 1 / 2 |
| Application and database traffic, MB per server per month | 2,940 | 545 |
| WAL and traffic to agents | Unchanged | Unchanged |

So at 100 servers the smallest sizes already hold the load, and it changes nothing. At 2,000 the database falls below every `db.t4g` baseline above `micro`, and the task halves.

**It is worth doing for capacity.** The same run shows the pool of 4 carrying 2,000 agents at about one seventh of the query rate, far below its ceiling. A sensible order:

1. **When a deployment nears 1,000 servers:** make the pool size configurable. It is a small change, and it moves the limit to CPU.
2. **Before a deployment passes about 2,000 servers, or if PostgreSQL needs fewer connections or a smaller class:** build the wake-up. Lengthening the tick to 5 s is a one-line intermediate step, estimated (not measured) to remove about 70% of queries while mean command delivery rises from about 0.5 s to about 2.5 s.

Separately, not re-reading and re-sending an unchanged command every 250 ms would reduce load in busy fleets, where the tick is not the main contributor.

## What was not measured

- RDS, Fargate, Graviton, TLS to the database or the load balancer itself. The commit delay and `netem` round trip stand in for them.
- Real agents beyond their request pattern, browser sessions and server-sent events, builds, backups and schedule reports. Hosted mode's admission checks on delivered commands and its expiry sweep.
- A steady state longer than eight minutes. Metric history is seeded. Autovacuum ran on the metric table in the tmpfs 2,000-agent and active windows, well short of its dead-row threshold, so seeding may have caused those runs and its long-term cadence is unknown.
- One harness version. The harness changed between runs and was uncommitted for most of them, so `summary.md` shows a mix of revisions for the same application code. Changes affected tmpfs and CPU pinning options, generator session handling and error logging, and added the metric call's 5 s send limit, which no earlier window reached; none altered the rest of the request pattern or the counters read. `tmpfs-idle-2000-contended` was the first run of `tmpfs-idle-2000`.

Two side findings:

- The alert sweep scans at most 20 workspaces a minute (`internal/alerts/detect.go`). At five servers a workspace, 2,000 servers means a 20-minute scan cycle rather than the intended 30 seconds.
- One run was refused server creation with `recent_auth_required` seconds after a successful login. It happened once in about 30 runs and was not investigated; the generator now reauthenticates and retries.
