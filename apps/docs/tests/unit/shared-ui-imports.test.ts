import { glob, readFile } from "node:fs/promises";

import { describe, expect, it } from "vitest";

describe("shared UI imports", () => {
  it("uses component entry points without reaching into Base UI", async () => {
    const rootImports: string[] = [];
    const baseUiImports: string[] = [];

    for await (const path of glob("src/**/*.{astro,ts,tsx}")) {
      const source = await readFile(path, "utf8");
      if (/from\s+["']@astilba\/ui["']/.test(source)) {
        rootImports.push(path);
      }
      if (/["']@base-ui\/react(?:\/[^"']*)?["']/.test(source)) {
        baseUiImports.push(path);
      }
    }

    expect(rootImports).toEqual([]);
    expect(baseUiImports).toEqual([]);
  });
});
