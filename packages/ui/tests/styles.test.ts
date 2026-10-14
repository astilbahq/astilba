import { readFile } from "node:fs/promises";
import path from "node:path";

import { describe, expect, it } from "vitest";

describe("compiled component CSS", () => {
  it("leaves semantic variables to @astilba/tokens", async () => {
    const css = await readFile(
      path.resolve(import.meta.dirname, "../dist/styles.css"),
      "utf-8"
    );

    expect(css).toContain(".astilba-control--size_large");
    expect(css).toContain(".astilba-collapsible-panel");
    expect(css).toContain(".astilba-dialog-backdrop");
    expect(css).toContain(".astilba-dialog-viewport");
    expect(css).toContain(".astilba-dialog-popup[data-starting-style]");
    expect(css).toContain(".astilba-dialog-popup[data-ending-style]");
    expect(css).toContain("--astilba-durations-modal-open");
    expect(css).toContain("--astilba-durations-modal-close");
    expect(css).toContain("--astilba-colors-surface-scrim");
    expect(css).toContain(".astilba-field-root");
    expect(css).toContain(".astilba-field-label");
    expect(css).toContain(".astilba-field-description");
    expect(css).toContain(".astilba-field-error");
    expect(css).toContain(".astilba-input");
    expect(css).toContain(".astilba-textarea");
    expect(css).toMatch(
      /@layer astilba\.recipes\s*\{[\s\S]*?\.astilba-field-root\s*\{/u
    );
    expect(css).toContain("--astilba-colors-border-field");
    expect(css).toContain("--astilba-colors-surface-field");
    expect(css).toContain(
      '.astilba-input:is([aria-invalid="true"], [data-invalid])'
    );
    expect(css).toContain(".astilba-input:-webkit-autofill");
    expect(css).toMatch(
      /\.astilba-collapsible-panel\s*\{[^}]*box-sizing: border-box;[^}]*\}/u
    );
    expect(css).toContain("--collapsible-panel-height");
    expect(css).toContain("--astilba-durations-disclosure");
    expect(css).toContain(".astilba-tooltip-popup");
    expect(css).toContain(".astilba-tooltip-popup[data-starting-style]");
    expect(css).toContain(".astilba-tooltip-popup[data-ending-style]");
    expect(css).toContain("--astilba-durations-tooltip-close");
    expect(css).toContain('.astilba-tooltip-labels[data-active="true"]');
    expect(css).toContain(".astilba-menu-popup[data-starting-style]");
    expect(css).toContain(".astilba-menu-popup[data-ending-style]");
    expect(css).toContain("--astilba-durations-menu-open");
    expect(css).toContain("--astilba-durations-menu-close");
    expect(css).toContain(".astilba-menu-item[data-highlighted]");
    expect(css).toContain(".astilba-scroll-area-root");
    expect(css).toContain(".base-ui-disable-scrollbar::-webkit-scrollbar");
    expect(css).toContain('.astilba-scroll-area-viewport[data-fade="block"]');
    expect(css).toContain("--scroll-area-overflow-y-start");
    expect(css).toContain(".astilba-scroll-area-scrollbar[data-scrolling]");
    expect(css).toMatch(
      /\.astilba-scroll-area-scrollbar\[data-orientation="vertical"\]\s*>\s*\.astilba-scroll-area-thumb\s*\{[^}]*min-block-size: 1\.5rem;[^}]*inline-size: 100%;[^}]*\}/u
    );
    expect(css).toMatch(
      /\.astilba-scroll-area-scrollbar\[data-orientation="horizontal"\]\s*>\s*\.astilba-scroll-area-thumb\s*\{[^}]*block-size: 100%;[^}]*min-inline-size: 1\.5rem;[^}]*\}/u
    );
    expect(css).toContain(".astilba-tabs-list");
    expect(css).toContain(".astilba-tabs-tab[data-active]");
    expect(css).toContain(".astilba-tabs-indicator");
    expect(css).toContain("--active-tab-width");
    expect(css).toContain("--astilba-durations-tabs");
    expect(css).toContain("@media (forced-colors: active)");
    expect(css).toContain("@media (prefers-reduced-motion: reduce)");
    expect(css).toContain("transition: none;");
    expect(css).toContain("@layer astilba.utilities");
    expect(css).not.toMatch(/--astilba-colors-[\w-]+\s*:/u);

    const largeControl = css.match(
      /\.astilba-control--size_large\s*\{\s*padding-inline: 0\.875rem;[^}]*\}/u
    );
    const iconOnlyControl = css.match(
      /\.astilba-control--iconOnly_true\s*\{\s*padding-inline: 0;[^}]*\}/u
    );

    expect(largeControl).not.toBeNull();
    expect(iconOnlyControl).not.toBeNull();
    expect(iconOnlyControl?.index).toBeGreaterThan(largeControl?.index ?? -1);
  });
});
