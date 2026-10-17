import { afterEach, beforeEach, expect, vi } from "vitest";

// PLATFORM: workerd's native alarm scheduler uses real time, independent of Vitest's
// Date mock. Give only manual-delivery suites a fixed future adapter clock so the
// production scheduler still arms real alarms, but cannot consume them before the
// manual trigger. Keep I/O timers real and the automatic-delivery witness untouched.
const manualSuites = new Set([
  "do-bus.test.ts",
  "manual-alarm-clock.test.ts",
  "reader-shell.test.ts",
  "regid-identity.test.ts",
  "registry-heartbeat.test.ts",
  "registry-transcript.test.ts",
]);
let controlled = false;

beforeEach(() => {
  const file = expect.getState().testPath?.split("/").at(-1);
  controlled = file !== undefined && manualSuites.has(file);
  if (controlled) {
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(new Date("2100-01-01T00:00:00.000Z"));
  }
});

afterEach(() => {
  if (controlled) {
    vi.useRealTimers();
    controlled = false;
  }
});
