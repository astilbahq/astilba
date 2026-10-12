import { describe, expect, it } from "vitest";

import { check as checkDocsBuild } from "../../.astilba/env/docsBuild.server.ts";
import { check as checkSiteBuild } from "../../.astilba/env/siteBuild.server.ts";
import { resolveCanonicalOrigin } from "../../src/env/origin.ts";

const docsSource = "ASTILBA_DOCS_SITE";
const siteSource = "ASTILBA_SITE";
const invalidOriginShapes = [
  "http://astilba.com",
  "https://user@astilba.com",
  "https://astilba.com/docs",
  "https://astilba.com?preview=true",
  "https://astilba.com#fragment",
  "not a URL",
];

describe("public build origins", () => {
  it("keeps both optional origins absent for local work", () => {
    const docs = checkDocsBuild({});
    const site = checkSiteBuild({});

    expect(docs).toEqual({ ok: true, value: {} });
    expect(site).toEqual({ ok: true, value: {} });
    expect(
      resolveCanonicalOrigin(docsSource, docs, "docsOrigin")
    ).toBeUndefined();
    expect(
      resolveCanonicalOrigin(siteSource, site, "siteOrigin")
    ).toBeUndefined();
  });

  it("keeps target entry keys type-specific", () => {
    const docs = checkDocsBuild({});
    const site = checkSiteBuild({});

    // @ts-expect-error Docs target has no siteOrigin entry.
    resolveCanonicalOrigin(docsSource, docs, "siteOrigin");
    // @ts-expect-error Apex target has no docsOrigin entry.
    resolveCanonicalOrigin(siteSource, site, "docsOrigin");
  });

  it("normalizes the canonical docs origin", () => {
    const docs = checkDocsBuild({ [docsSource]: "https://astilba.com/" });

    expect(docs).toEqual({
      ok: true,
      value: { docsOrigin: "https://astilba.com" },
    });
  });

  it.each(invalidOriginShapes)(
    "rejects an invalid docs origin shape",
    (value) => {
      expect(checkDocsBuild({ [docsSource]: value })).toMatchObject({
        diagnostics: [
          {
            code: "ENV_INVALID_VALUE",
            consumer: "docs",
            entry: "docsOrigin",
          },
        ],
        ok: false,
      });
    }
  );

  it.each(invalidOriginShapes)(
    "rejects an invalid apex origin shape",
    (value) => {
      expect(checkSiteBuild({ [siteSource]: value })).toMatchObject({
        diagnostics: [
          {
            code: "ENV_INVALID_VALUE",
            consumer: "site",
            entry: "siteOrigin",
          },
        ],
        ok: false,
      });
    }
  );

  it("retains the application-owned canonical origin boundary", () => {
    const docs = checkDocsBuild({ [docsSource]: "https://example.com" });

    expect(docs).toMatchObject({ ok: true });
    expect(() =>
      resolveCanonicalOrigin(docsSource, docs, "docsOrigin")
    ).toThrow("ASTILBA_DOCS_SITE must use the canonical deployed origin");
  });

  it("retains the application-owned apex canonical origin boundary", () => {
    const site = checkSiteBuild({ [siteSource]: "https://example.com" });

    expect(site).toMatchObject({ ok: true });
    expect(() =>
      resolveCanonicalOrigin(siteSource, site, "siteOrigin")
    ).toThrow("ASTILBA_SITE must use the canonical deployed origin");
  });

  it("uses value-free diagnostics for rejected source values", () => {
    const rejectedValue = "https://astilba.com/rejected-value";
    const docs = checkDocsBuild({ [docsSource]: rejectedValue });

    let message = "";
    try {
      resolveCanonicalOrigin(docsSource, docs, "docsOrigin");
    } catch (error) {
      message = error instanceof Error ? error.message : String(error);
    }

    expect(message).toContain(docsSource);
    expect(message).not.toContain(rejectedValue);
  });

  it("keeps docs and apex source targets isolated", () => {
    const docs = checkDocsBuild({
      [docsSource]: "https://astilba.com",
      [siteSource]: "https://astilba.com/not-an-origin",
    });
    const site = checkSiteBuild({
      [docsSource]: "https://astilba.com/not-an-origin",
      [siteSource]: "https://astilba.com",
    });

    expect(docs).toEqual({
      ok: true,
      value: { docsOrigin: "https://astilba.com" },
    });
    expect(site).toEqual({
      ok: true,
      value: { siteOrigin: "https://astilba.com" },
    });
  });
});
