import { describe, expect, test } from "vitest";

import { hostedTelemetry } from "./telemetry";

// Harness self-test — the hosted-sink fake must be flagged hosted, carry the
// per-project salt, and faithfully capture what the kernel delivers (it never
// redacts on its own — that is the kernel's job, which INV-15 verifies).

describe(hostedTelemetry, () => {
  test("is flagged hosted, carries the salt, and captures events", () => {
    const hosted = hostedTelemetry("project-salt");
    expect(hosted.config.hosted).toBeTruthy();
    expect(hosted.config.salt).toBe("project-salt");
    expect(hosted.events).toHaveLength(0);
    // Any CATALOG event name — the fake just captures what arrives; the specific
    // name is immaterial, so a real member stands in for the former off-catalog placeholder.
    hosted.config.sink({ key: "abc", type: "singleflight_option_mismatch" });
    expect(hosted.events).toHaveLength(1);
    expect(hosted.events[0]?.type).toBe("singleflight_option_mismatch");
  });
});
