# PLAT-3: HTTPS agent transport experiment

This disposable fixture exercises the agent protocol through Traefik before production transport is built. It shares the PLAT-2 Go module, PostgreSQL harness, command identity, bbolt journal and actual Docker create/start effect. It does not introduce production API or database migrations.

## Topology and scope

The host test process runs the agent. A compiled Linux test helper runs the controller in a BusyBox container, backed by the disposable PostgreSQL instance. Traefik terminates HTTPS and forwards to that controller across a fixture-owned internal Docker network. Only the proxy's HTTPS port is published, on loopback; neither the controller nor the Docker socket is exposed by the proxy. Files are copied into disposable container filesystems, without host mounts.

The certificate and its private key are generated for each run. The client trusts that certificate explicitly and checks its IP identity; an ordinary client without that trust must fail. Traefik's default certificate is configured because an IP-address client does not send DNS SNI. No certificate, key or credential is committed.

Traefik is pinned to `v3.7.16@sha256:22a759fb803abb5c3a7ba5d5a55a201c5fb0fc8993d5c72c47a89075e64f1953`. Its release source is MIT licensed. The image index includes Linux amd64 and arm64; this experiment's execution evidence must be read separately from the production host support matrix. The [proxy configuration](transport_proxy.yml) is the complete static configuration used by the test, with its file-provider routes in `transport_harness_test.go`.

## Identity and delivery contracts

- An enrollment token expires after five minutes, is bound to one server, and is consumed transactionally once. The controller stores token and credential SHA-256 digests. Reissuing enrollment invalidates the prior identity and enrollment token. A lost enrollment response therefore requires a new operator-issued token.
- A server credential contains 256 random bits. Every authenticated endpoint checks the route's server identity and revocation under the same row lock as its mutation. A revoked identity cannot poll, rotate, send heartbeats or report. An open poll checks again while waiting and before selecting work. Already delivered bytes and accepted local work cannot be recalled by revocation.
- Rotation first writes a random candidate into a private, synced local file. The server promotes its digest transactionally, retaining the old digest for two minutes. Retrying the same candidate does not extend that overlap; the previous credential cannot select a different candidate. Recovery first tries the persisted candidate, which may already be active even after the old credential expires. If neither credential is valid, re-enrollment is required.
- The receiving agent and controller both validate envelope version, operation kind, server assignment and role. A build-only identity cannot receive this deployment operation. The fixture has no browser administration routes.
- Polling selects one pending command without deleting it. Only a terminal report removes it from delivery. The controller accepts reports only for the authenticated server's assigned command and frozen identity; identical reports are idempotent and conflicting ones fail.
- Operation execution has its own deadline and journal. Canceling the transport stops polling and reporting, but an accepted local effect continues. Reconnection reports or reconciles the same operation rather than creating another container.

## Limits and failure cases

| Setting | Fixture value |
| --- | --- |
| Long poll | 25 seconds, one command per response |
| Concurrent polls | One per server per controller process; additional poll returns 409 |
| Heartbeat | Every 15 seconds, independent of polling and execution |
| Offline indication | At 45 seconds since the last authenticated contact |
| JSON request / agent response limit | 16 KiB |
| Controller header / request read timeout | 5 / 10 seconds |
| Proxy request read timeout | 10 seconds |
| Controller / proxy response write timeout | 35 seconds |
| Proxy upstream dial / response-header timeout | 5 / 30 seconds |
| Client request timeout | 35 seconds |
| Idle connection timeout | 60 seconds |

The tests cover concurrent enrollment and replay, wrong-server and expired tokens, lost enrollment responses, rotation interrupted before exchange/after exchange/after local promotion, lost rotation responses followed by controller restart, bounded overlap and recovery using the persisted candidate. Revocation covers both credentials and an outstanding poll. Server, role and protocol isolation, oversized bodies and overlapping polls have direct cases.

The live-agent case drops a command-bearing response before journal acceptance, then verifies redelivery. It records heartbeat times during a full empty poll and an effect paused across multiple heartbeat intervals. It disconnects transport during that effect, permits local completion, reconnects and drops the first committed report's acknowledgement. The expected outcome is one journaled result, one controller observation and the original running container with one `effect` log line and no restart.

## Reproduce

Use the disposable engine and pinned PostgreSQL/BusyBox images in the [PLAT-2 instructions](README.md#reproduce), plus the proxy image:

```sh
docker --context colima-astilba-plat-2 pull traefik:v3.7.16@sha256:22a759fb803abb5c3a7ba5d5a55a201c5fb0fc8993d5c72c47a89075e64f1953

export PLAT2_DOCKER_HOST="$(docker context inspect colima-astilba-plat-2 --format '{{.Endpoints.docker.Host}}')"
make verify
```

The shared environment variable intentionally keeps one explicitly chosen disposable engine. For only this spike, run `go test -race -count=1 -timeout=3m -run '^TestAgentHTTPS$' -v .` inside `experiments/durable-execution`. The fixture cross-compiles the controller for the Docker engine's Linux architecture using the pinned Go toolchain. The host test and agent use the race detector; the static controller helper does not. PostgreSQL clock offsets are trusted test controls, never HTTP parameters. They exercise token expiry and the offline cutoff without slowing every test to minutes.

## Boundaries of the result

The targeted checks passed on 22 October 2026 with Go 1.27.3, Traefik 3.7.16, Docker Engine 29.5.2/API 1.54 and Linux arm64 containers, driven from macOS arm64. Verified TLS negotiated TLS 1.3 and HTTP/2. The live case measured a 25.004-second poll and heartbeat gaps of 15.001–15.002 seconds during polling and paused execution. Lost delivery and report responses converged on the original running container, one effect log line, zero restarts and one committed observation.

The complete `make verify` run passes all 17 PLAT-2 durability scenarios, all 11 HTTPS scenarios and the journal-lock test, including formatting, module checksums and `go vet`. The final local test run took 238.663 seconds.

This is one controller and a small number of identities, not a throughput benchmark. The active-poll limit is process-local; production multi-replica admission requires shared coordination. The fixture retries transport failures at a short fixed interval; production reconnect policy still needs bounded exponential backoff and jitter. Installation, persistent artifact distribution, log spooling, secret-bearing payload encryption, full tenant authorization and controller transfer remain later work.

The controller uses a small PostgreSQL delivery table to isolate HTTPS behavior. River's transactional enqueue and stale-worker behavior remain covered by PLAT-2, not reimplemented here. Rotation interruption restores saved state at explicit boundaries; controller restart is a real container restart. This does not prove filesystem behavior after power loss, packet-loss tolerance under every proxy, public certificate renewal, Cloudflare Containers or production Ubuntu host support.

High-risk peer review used three native same-model passes: general, security/privacy/supply chain, and durability/concurrency/proxy compatibility. One test-harness error path could omit a concurrent enrollment result and hang cleanup; three independent verifiers confirmed it. Workers now return status and error to the test goroutine. All three final reruns have no findings or uncovered relevant lenses. Review was read-only by instruction; runtime checks were performed separately.

## References

- [Traefik v3.7.16 release](https://github.com/traefik/traefik/releases/tag/v3.7.16) and [licence](https://github.com/traefik/traefik/blob/v3.7.16/LICENSE.md).
- [Traefik entrypoint timeouts](https://doc.traefik.io/traefik/reference/install-configuration/entrypoints/) and [upstream transport timeouts](https://doc.traefik.io/traefik/reference/routing-configuration/http/load-balancing/serverstransport/).
- [Platform architecture](../../docs/platform-architecture.md) and [P0 implementation plan](../../docs/platform-implementation-plan.md).
