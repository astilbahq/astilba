import { describe, expect, it } from "vitest";

import { shouldOpenDesignSystem } from "../../src/scripts/design-system-easter-egg";

describe("design-system Easter egg", () => {
  it("opens only for a right click from a fine pointer", () => {
    expect(shouldOpenDesignSystem({ button: 2, hasFinePointer: true })).toBe(
      true
    );
    expect(shouldOpenDesignSystem({ button: 0, hasFinePointer: true })).toBe(
      false
    );
    expect(shouldOpenDesignSystem({ button: 2, hasFinePointer: false })).toBe(
      false
    );
  });
});
