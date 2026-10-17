import { earliestAlarm } from "@astilba/cache/registry";
import { test } from "@fast-check/vitest";
import fc from "fast-check";
import { expect } from "vitest";

test.prop([
  fc.integer({ min: 1, max: 1_000_000 }),
  fc.integer({ min: 1, max: 1_000_000 }),
])(
  "one alarm slot always keeps the nearest deadline, independent of arrival order (B5, D11)",
  (earlier, distance) => {
    const later = earlier + distance;
    expect(earliestAlarm(null, earlier)).toBe(earlier);
    expect(earliestAlarm(earlier, later)).toBe(earlier);
    expect(earliestAlarm(later, earlier)).toBe(earlier);
    expect(earliestAlarm(earlier, earlier)).toBe(earlier);
  }
);
