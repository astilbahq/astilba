import { readFile } from "node:fs/promises";

import { describe, expect, it } from "vitest";

const workflow = await readFile(
  new URL("../.github/workflows/release.yml", import.meta.url),
  "utf-8"
);

describe("release workflow contract", () => {
  it("keeps release artifact verification inside the checkout", () => {
    expect(workflow).toContain("path: .artifacts/release");
    expect(workflow).toContain("ASTILBA_ENV_ARTIFACT_DIR: .artifacts/release");
    expect(workflow).toContain('artifact_dir=".artifacts/release"');
    expect(workflow).not.toContain(
      ["path: $", "{{ runner.temp }}/release-assets"].join("")
    );
  });

  it("creates only after a confirmed 404 and fails partial state closed", () => {
    expect(workflow).toContain("gh api --include --silent");
    expect(workflow).toContain(
      'lookup_status="$(grep -E \'^HTTP/[0-9.]+ [0-9]{3}([[:space:]]|$)\' "$lookup_headers" | tail -n 1 || true)"'
    );
    expect(workflow).toContain(
      "if ! grep -Eq '^HTTP/[0-9.]+ 404([[:space:]]|$)' <<< \"$lookup_status\"; then"
    );
    expect(workflow).toMatch(
      /if \[ "\$release_exists" = false \]; then\s+if ! gh release create/u
    );
    expect(workflow).toContain(
      "GitHub release lookup failed without a confirmed 404; refusing creation."
    );
    expect(workflow).toContain(
      "GitHub release state or assets are incomplete or mismatched; manual correction is required before retry."
    );
  });
});
