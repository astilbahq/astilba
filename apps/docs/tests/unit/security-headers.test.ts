import { createHash } from "node:crypto";

import { describe, expect, it } from "vitest";

import {
  createContentSecurityPolicy,
  getInlineScriptHashes,
} from "../../scripts/security-headers.mjs";

const hash = (source: string): string =>
  `'sha256-${createHash("sha256").update(source, "utf8").digest("base64")}'`;

describe("security headers", () => {
  it("hashes unique inline scripts without treating data-src as src", () => {
    expect(
      getInlineScriptHashes([
        '<script>console.log("one")</script><script src="/app.js"></script>',
        '<script data-src="src=/external.js > still inline">console.log("two")</script><script>console.log("one")</script>',
      ])
    ).toEqual([hash('console.log("one")'), hash('console.log("two")')].sort());
  });

  it("creates a strict executable policy with narrow framework exceptions", () => {
    const inlineHash = hash('console.log("safe")');
    const policy = createContentSecurityPolicy([inlineHash]);

    expect(policy).toBe(
      [
        "default-src 'none'",
        "base-uri 'none'",
        "connect-src 'self'",
        "font-src 'self' data:",
        "form-action 'none'",
        "frame-ancestors 'none'",
        "frame-src 'none'",
        "img-src 'self' data:",
        "manifest-src 'none'",
        "media-src 'none'",
        "object-src 'none'",
        `script-src 'self' 'wasm-unsafe-eval' ${inlineHash}`,
        "script-src-attr 'none'",
        "style-src 'self' 'unsafe-inline'",
        "worker-src 'self'",
      ].join("; ")
    );
  });

  it("hashes the line endings produced by the HTML parser", () => {
    expect(
      getInlineScriptHashes(["<script>one\r\ntwo\rthree</script>"])
    ).toEqual([hash("one\ntwo\nthree")]);
  });

  it("rejects empty or malformed inline-script sources", () => {
    expect(() => createContentSecurityPolicy([])).toThrow(
      "At least one inline script hash is required"
    );
    expect(() => createContentSecurityPolicy(["sha256-not-quoted"])).toThrow(
      "quoted SHA-256 sources"
    );
    expect(() => createContentSecurityPolicy(["'sha256-A'"])).toThrow(
      "quoted SHA-256 sources"
    );
    expect(() =>
      createContentSecurityPolicy([`'sha256-${"A".repeat(42)}=='`])
    ).toThrow("quoted SHA-256 sources");
  });
});
