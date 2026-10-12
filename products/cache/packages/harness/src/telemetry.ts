import type { TelemetryConfig, TelemetryEvent } from "@astilba/cache";

// A minimal HOSTED telemetry sink (§10, MI4). The `config` carries `hosted:true`
// so the kernel pseudonymizes every string field before delivery — per-project
// HMAC-salted key/tag hashes, redacted details — and the sink just captures what
// actually arrives. Redaction is the KERNEL's responsibility: a wrong kernel that
// forwards raw keys/tags/details would fail the invariant-15 assertion. The fake
// itself never redacts and never throws.

export interface HostedTelemetryFake {
  readonly config: TelemetryConfig;
  readonly events: readonly TelemetryEvent[];
}

export function hostedTelemetry(salt: string): HostedTelemetryFake {
  const events: TelemetryEvent[] = [];
  const config: TelemetryConfig = {
    hosted: true,
    salt,
    sink: (event) => {
      events.push(event);
    },
  };
  return { config, events };
}
