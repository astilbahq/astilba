# Fleet capacity measurement

Measures the load connected agents put on the management application. It runs the real server binary against PostgreSQL 18.4 and drives it with synthetic agents over the public HTTP contract. It imports no application code and adds none. Results and the sizing built on them are in [RESULTS.md](RESULTS.md).

## What it runs

`measure` starts three labelled containers on an explicitly named Docker context, then removes only what carries its run label:

- PostgreSQL 18.4 at the repository's pinned digest, with `pg_stat_statements`. A `netem` delay on its replies stands in for the network between a task and a managed database; each run records the resulting ping.
- The application, built from `services/platform` without dashboard assets, in non-development mode behind a declared trusted proxy.
- The generator, whose agents copy the cadence of `internal/agent/client.go`: a poll loop with 250 ms between responses, a heartbeat every 15 s, metrics every 60 s and a report loop every 2 s. A delivered command is re-polled as the real agent does until it is terminal.

The generator enrols through the real API, 5 servers to a workspace. With `--services`, each server gets a real reviewed deployment first, so metric ownership checks and observations touch production tables. With `--deploys-per-day`, draft, plan, resolution and apply repeat as a Poisson process.

Before the window, each server's own report is copied back over the previous 24 hours. The window therefore sees the steady state of one insert and one prune per server-minute against a full table. A short run from empty would never delete anything.

## Run variants

| Prefix | Meaning |
| --- | --- |
| `idle-` | PostgreSQL on the VM's disk. Used for PostgreSQL's memory, because tmpfs pages are charged to its cgroup, and for the zero-agent baseline. |
| `tmpfs-idle-`, `tmpfs-active-` | PostgreSQL's files in tmpfs with `commit_delay=1000` and `commit_siblings=0`. This replaces the host's disk with a fixed 1 ms commit that resembles gp3. |
| `-pool12` | The application sees 12 CPUs, so its pool has 12 connections. |
| `-no-tick` | A throwaway build with [no-tick.patch](no-tick.patch) applied to a temporary copy of the working tree. It bounds what a wake-up could save and is not usable: commands wait up to 25 s. |
| `-slow-db` | A larger `netem` delay, about 0.8 ms round trip. |
| `sized-` | CPU quotas matching proposed sizes. |

`tmpfs-idle-2000-contended` in the published tables is an earlier `tmpfs-idle-2000` on a busy host, kept as evidence of how little margin the pool has. It cannot be repeated by name.

## CPU figures

CPU is what each container's cgroup reports. A guest counts time its vCPU was preempted by a busy host as CPU used, so a busier host inflates it. Each result records host load and a single-core `pgbench` rate, and the sizing in [RESULTS.md](RESULTS.md) uses runs that started at low load. Do not compare CPU between runs without checking both.

## Run it

Use a disposable Docker VM only. The defaults assume 12 CPUs, split 4/4/4 between application, database and generator.

```bash
colima start astilba-fleet-capacity --activate=false --mount none --port-forwarder none --cpu 12 --memory 20 --disk 40
```

```bash
./run-all.sh colima-astilba-fleet-capacity /path/to/new/results
```

```bash
go run . summary /path/to/new/results/*.json
```

[results/summary.md](results/summary.md) came from `summary` over all the runs. `measure` writes one JSON per run. Those files are not kept in this repository, so a repeat produces new measurements rather than the published ones. `run-all.sh` takes run names to repeat one run, skips results that already exist and exits non-zero if any run fails. A full `run-all.sh` takes about four hours. Host load comes from the machine running `measure`, so it describes the Docker host only for a local VM. Repeat runs that overlap heavy work on the same machine.

The application's database pool has `max(4, NumCPU)` connections and cannot be configured: `pool_max_conns` in the URL breaks the `database/sql` migration check. So `--app-cpuset` is how a run selects the pool size.

## Limits

- CPU is measured on Apple silicon in a VM and converted to AWS vCPUs by published single-core benchmark ratios. It has not run on Graviton or RDS. `measure` can repeat on a Graviton Docker host through a Docker context, with `--goarch arm64`. It cannot drive RDS itself.
- The application runs in self-hosted mode. By code inspection, hosted mode adds two short transactions per delivered command and a one-minute notification sweep; the idle path is the same.
- No browser sessions, server-sent events, builds, backups or schedule reports are simulated. No TLS terminates inside the application, because a load balancer would do so.
