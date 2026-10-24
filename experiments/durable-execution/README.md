# PLAT-2: durable execution experiment

A disposable P0 fixture for the PLAT-2 durable-execution milestone. It evaluates River's open-source core, PostgreSQL, bbolt and actual Docker effects before the production application is scaffolded. Code in this repository is AGPL-3.0-only.

## Execution model

The test runner starts a disposable PostgreSQL container, a worker process and a separate agent process. The processes communicate through a private Unix socket. Each case has its own database and bbolt journal. No production endpoints, credentials, host mounts or existing Docker resources are used.

An operation and its River job are inserted in one PostgreSQL transaction. A worker claims an application-level delivery fence and sends the operation's stable identity, frozen plan hash and environment generation. The agent commits acceptance and effect intent to bbolt before creating and starting an operation-labelled container. That container emits one `effect` log line and stays running.

After a crash, the agent inspects the original container. It does not blindly run create or start again. A matching running container establishes the observed result. Missing, stopped or conflicting resources after durable intent require attention and retain the environment's mutation lock. A crash before intent can safely continue after confirming the name is unused.

The worker writes the observed outcome and an application-owned checkpoint in the same transaction. Finalization uses River's `JobCompleteTx` in the transaction that finalizes the operation. Application-level fences prevent a replaced worker from publishing stale results. River retries dispatch; the application and journal determine whether an external effect may run.

Cancellation is a persisted operation request. Cancelling a River job alone does not reconcile an already-applied effect. The cancellation test atomically records that request, cancels the old queue job and inserts a reconciliation delivery. An already-started container remains running; cancellation does not pretend to roll it back.

Once the agent has durably recorded a terminal result, that result wins a later cancellation request even if the controller has not acknowledged it. The request remains recorded separately. Tests cover cancellation before effects, during an unacknowledged effect, after the journaled result and after the controller checkpoint.

## Reproduce

Use a disposable Docker engine and Go 1.27.3. The fixture accepts only an explicit local Unix socket, creates randomly labelled resources and removes only resources carrying its own run label. On macOS, the environment used for development was installed through Homebrew: Go 1.27.3, Docker CLI 29.9.2, Colima 0.10.3 and Lima 2.2.1. No Docker Hub account or Docker Desktop is required.

```sh
colima start --profile astilba-plat-2 --activate=false --cpu 4 --memory 4 --disk 20 --vm-type vz --runtime docker

docker --context colima-astilba-plat-2 pull postgres:18.4-alpine3.23@sha256:996d0920e4ff9df1fc19dacb904492f3c1ec0ec1cc338f0ad7123be7731c5f5e
docker --context colima-astilba-plat-2 pull busybox:1.37.0@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e
docker --context colima-astilba-plat-2 pull traefik:v3.7.16@sha256:22a759fb803abb5c3a7ba5d5a55a201c5fb0fc8993d5c72c47a89075e64f1953

export PLAT2_DOCKER_HOST="$(docker context inspect colima-astilba-plat-2 --format '{{.Endpoints.docker.Host}}')"
bash experiments/durable-execution/verify.sh

colima stop --profile astilba-plat-2
```

Run the script from the repository root. It checks formatting, module checksums, `go vet` and the complete uncached host suite with the race detector, including the [PLAT-3 HTTPS transport fixture](TRANSPORT.md). Ordinary `go test` without the socket explicitly skips the Docker suite and does not establish this experiment's acceptance criteria. The root `make verify` command runs both fixtures. [PLAT-4 host validation](HOST.md) has a separate native Ubuntu setup and `make verify-host` command; it is explicitly skipped without that opt-in. It requires a disposable Docker engine and the explicitly configured Unix socket.

## Failure injection

The test runner waits for an exact barrier before sending `SIGKILL`; it does not rely on guessing when the effect happened. The resumed processes open the same durable stores. Tests use River's real stuck-job rescue without rewriting queue rows to manufacture a retry. Short timeouts are fixture settings, not proposed production defaults.

| Case | Required observation |
| --- | --- |
| Enqueue commit and rollback | Operation and job are both present or both absent. |
| Crash after acceptance | Resume safely before effect intent; one effect. |
| Crash after intent, before create | `needs_attention`; no speculative replay. |
| Crash after create, before start | `needs_attention`; no speculative start. |
| Crash after start, before acknowledgement | Recover the same container; one effect log line, zero restarts. |
| Crash after journaled result | Return the stored result; retain the same container. |
| Crash before checkpoint transaction commit | Both observation and checkpoint roll back, then commit once on retry. |
| Crash after checkpoint commit | Resume after that step; one observation and one finalization. |
| Database connection lost before checkpoint commit | Roll back the observation and checkpoint, then reconcile and record them once on retry. |
| Crash before/after final completion commit | Operation finalization and River completion both roll back or both persist. |
| Duplicate delivery and stale identity | Reuse the original outcome; reject changed plans, stale generations and stale delivery fences. |
| Cancellation before effect | No container is created. |
| Cancellation after effect | Reconcile the original container and retain it; cancellation is not rollback. |
| Cancellation after a durable result | Preserve the original success and the separate cancellation request, before or after the controller checkpoint. |
| Live stale worker | Suspend a worker, let another worker rescue the job, then resume the first; its stale result cannot overwrite the replacement. |
| Concurrent journal owners | bbolt rejects the second process opening the same state file. |

## Checkpoint finding

The initial implementation used River 0.49.0's `ResumableSetStepTx`. Killing the worker before or after its database transaction behaved as expected. Terminating the PostgreSQL connection before commit exposed a different outcome: the application update rolled back, but the still-running worker retained the helper's in-memory progress. River persisted that progress while handling the error and skipped the effect step on retry. The observed result was a completed job with zero operation observations and one finalization.

The fixture therefore uses ordinary River jobs with application-owned intermediate checkpoints and transactional job completion. `checkpoint_connection_loss` retains the failed scenario as a regression check against this implementation. It terminates the actual PostgreSQL transaction connection; it does not mock a commit failure. This finding narrows the approved use of River's resumable helpers; it does not require replacing the queue with Temporal.

## Verified result: 22 October 2026

All 17 integration scenarios and the journal-lock test pass with `go test -race -count=1`, through `verify.sh`. The complete run took 168 seconds; formatting, module checksum verification and `go vet` also pass. The subprocess helper's skipped top-level test is an entry point invoked by the integration cases, not omitted coverage.

For recovered effects, the original container ID is preserved, the effect appears once in its logs and Docker reports zero restarts. Every completed operation has one committed observation and one finalization. Ambiguous intent/create outcomes stop in `needs_attention` and reject conflicting generations. Transaction rollback, connection loss, final acknowledgement, late cancellation and resumed stale workers have direct cases.

Keep River's open-source core for the next P0 experiments, with application-owned intermediate checkpoints and `JobCompleteTx` for final completion. These results do not establish universal exactly-once execution; each future external effect still needs its own inspection and ambiguity policy.

The High-risk review used three native same-model passes: general, durability/concurrency, and security/permissions/dependencies. A candidate concerning late cancellation was independently refuted by three verifiers against the terminal-result contract. Final reruns have no findings or uncovered relevant lenses. Read-only review was enforced by instructions; runtime verification was performed separately.

## Versions and scope

`go.mod` and `go.sum` pin the Go dependencies. Principal dependencies are River/riverpgxv5 0.49.0 (MPL-2.0), bbolt 1.5.0 (MIT), pgx 5.11.0 (MIT), and Moby client 0.6.2/API 1.56.1 (Apache-2.0). These licence names were checked against the downloaded source notices; retain those notices if distributing their source or binaries. This fixture does not redistribute an image or a compiled application.

The pinned database image is PostgreSQL 18.4 on Alpine 3.23; the effect image is BusyBox 1.37.0. The worker and agent processes run on macOS arm64; their Docker effects run in Colima's Linux arm64 VM using Docker Engine 29.5.2, API 1.54. SDK API negotiation is exercised against that engine. This does not establish the Ubuntu/systemd host matrix or amd64 support; P0 experiment 4 owns those checks.

The fixture tests process crashes, not power loss, disk corruption or loss of the journal itself. It exercises one create/start effect, not every deployment or destructive operation. It has one serialized agent and per-environment generations; it is not a production scheduler. HTTPS, enrollment, identity/credential rotation, controller transfer and host-wide coordination remain separate planned work. The private socket is not the production agent protocol.

## References

- [River transactional enqueueing](https://riverqueue.com/docs/transactional-enqueueing).
- [River durable resumable checkpoints](https://riverqueue.com/docs/resumable-jobs#durable-checkpoints): default in-memory progress can be lost on a crash; transactional checkpoints do not make external effects transactional.
- [River stuck jobs](https://riverqueue.com/docs/stuck-jobs) and [cancellation](https://riverqueue.com/docs/cancelling-jobs).
- [Platform architecture](../../docs/platform-architecture.md) and [P0 implementation plan](../../docs/platform-implementation-plan.md).
