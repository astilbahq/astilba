# Adapter test boundaries

The deterministic harness models driver behavior under controlled inputs. Local workerd tests exercise platform wiring; they do not certify production propagation timing or a service-level objective.

## Store

- D1: The Cloudflare adapter converts a supplied expiry duration to an integer number of seconds and applies a 60-second minimum. Its read-cache hint depends on the read kind. See `kv.ts` and the integration KV cache tests.
- D2 and D14: Fake-store controls can inject write throttling. Local KV tests cannot establish the error shape of a production throttled write; the adapter has an explicit classification fallback.
- D3: The current KV driver exposes no compare-and-set operation. The conformance suite records that limitation rather than pretending an atomic update occurred.
- D4 and D15: Local KV visibility is not a measurement of cross-location production visibility. The deterministic delayed-store cases test the reader's response to stale observations.

## Registry and bus

- D5 through D12, D16 through D18: Fake hosts and workerd expose different timing and storage controls. Run the registry/lock/bus contracts for controlled behavior and the integration suites for Durable Object wiring. Do not infer production latency from either lane.
- D19: The Coordinator's identity can come from its object name, a configured fallback, or the default. Its journal preserves the chosen identity; integration identity tests exercise those cases and divergence reporting.

The source and tests are the evidence for these statements. Preserve their assertions when relocating this workspace. Production measurements and publication approval are separate work.
