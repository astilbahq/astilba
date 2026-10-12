import { describe, expect, it } from "vitest";

import { setButtonDisabled } from "../src/configurator-button-state";

interface TestButton {
  disabled: boolean;
  readonly attributes: Set<string>;
  toggleAttribute: (name: string, force?: boolean) => boolean;
}

const createButton = (): TestButton => {
  const attributes = new Set<string>();

  return {
    attributes,
    disabled: false,
    toggleAttribute(name, force) {
      const present = force ?? !attributes.has(name);

      if (present) {
        attributes.add(name);
      } else {
        attributes.delete(name);
      }

      return present;
    },
  };
};

describe("configurator copy button state", () => {
  it("keeps native and Base UI state synchronized as configuration validity changes", () => {
    const copyCommand = createButton();
    const copyConfiguration = createButton();
    const buttons = [copyCommand, copyConfiguration];

    for (const disabled of [true, false]) {
      for (const button of buttons) {
        setButtonDisabled(button, disabled);
      }

      expect(copyCommand.disabled).toBe(disabled);
      expect(copyCommand.attributes.has("data-disabled")).toBe(disabled);
      expect(copyConfiguration.disabled).toBe(disabled);
      expect(copyConfiguration.attributes.has("data-disabled")).toBe(disabled);
    }
  });
});
