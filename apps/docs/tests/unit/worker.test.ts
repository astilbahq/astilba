import { describe, expect, it, vi } from "vitest";

import {
  CONTENT_SECURITY_POLICY_ASSET_PATH,
  GLOBAL_SECURITY_HEADERS,
} from "../../src/docs/security";
import {
  acceptsMarkdown,
  getMarkdownPath,
  handleRequest,
} from "../../worker/index";

const TEST_CONTENT_SECURITY_POLICY =
  "default-src 'none'; script-src 'self'; style-src 'self'";

const expectGlobalSecurityHeaders = (response: Response): void => {
  for (const [name, value] of Object.entries(GLOBAL_SECURITY_HEADERS)) {
    expect(response.headers.get(name)).toBe(value);
  }
};

const getExpectedPageDiscoveryLink = (markdownPath: string): string =>
  `<${markdownPath}>; rel="alternate"; type="text/markdown", </docs/llms.txt>; rel="describedby"; type="text/plain", </docs/.well-known/api-catalog>; rel="api-catalog"; type="application/linkset+json"`;

const MARKDOWN_FIXTURES = new Map([
  ["/docs/index.md", "# Home"],
  ["/docs/agents/llms-txt.md", "# Overview"],
  ["/docs/agents/mcp.md", "# Overview"],
  ["/docs/cache.md", "# Overview"],
  ["/docs/cache/overview.md", "# Overview"],
  ["/docs/env.md", "# Astilba Env"],
  [
    "/docs/env/overview.md",
    "# Overview\n\n@astilba/env 0.3.0 is a public alpha.",
  ],
]);

const createAssets = ({
  policyBody = `${TEST_CONTENT_SECURITY_POLICY}\n`,
  policyStatus = 200,
}: { policyBody?: string; policyStatus?: number } = {}) => {
  const fetch = vi.fn<Fetcher["fetch"]>(async (input, init) => {
    const request = new Request(input, init);
    const path = new URL(request.url).pathname;

    if (path === CONTENT_SECURITY_POLICY_ASSET_PATH) {
      return new Response(policyBody, {
        headers: { "Content-Type": "text/plain; charset=utf-8" },
        status: policyStatus,
      });
    }

    const markdownFixture = MARKDOWN_FIXTURES.get(path);

    if (markdownFixture !== undefined) {
      if (request.headers.get("If-None-Match") === '"markdown"') {
        return new Response(null, {
          headers: {
            "Content-Type": "text/markdown",
            ETag: '"markdown"',
          },
          status: 304,
        });
      }

      return new Response(markdownFixture, {
        headers: {
          "Content-Signal": "ai-train=yes, search=yes, ai-input=yes",
          "Content-Type": "text/markdown; charset=utf-8",
          ETag: '"markdown"',
        },
      });
    }

    if (path.endsWith("/missing.md") || path.endsWith("/missing/")) {
      return new Response("Not found", {
        headers: { "Content-Type": "text/html; charset=utf-8" },
        status: 404,
      });
    }

    if (request.headers.get("If-None-Match") === '"html"') {
      return new Response(null, {
        headers: {
          "Content-Type": "text/html; charset=utf-8",
          ETag: '"html"',
          Vary: "Accept-Encoding",
        },
        status: 304,
      });
    }

    return new Response("<h1>Overview</h1>", {
      headers: {
        "Content-Type": "text/html; charset=utf-8",
        ETag: '"html"',
        Vary: "Accept-Encoding",
      },
    });
  });
  const assets = {
    connect() {
      throw new Error("The test asset binding does not open sockets.");
    },
    fetch,
  } satisfies Fetcher;

  return { assets, fetch };
};

describe("Markdown negotiation", () => {
  it("requires an explicit positive text/markdown media range", () => {
    expect(acceptsMarkdown(null)).toBe(false);
    expect(acceptsMarkdown("*/*")).toBe(false);
    expect(acceptsMarkdown("text/html, text/markdown;q=0")).toBe(false);
    expect(acceptsMarkdown("text/html;q=0.5, TEXT/MARKDOWN; q=0.8")).toBe(true);
    expect(acceptsMarkdown("text/markdown;q=0.8invalid")).toBe(false);
    expect(acceptsMarkdown("text/markdown;q=0x1")).toBe(false);
    expect(acceptsMarkdown("text/markdown;q=.8")).toBe(false);
    expect(acceptsMarkdown("text/markdown;q=1.0000")).toBe(false);
  });

  it("honours quality and specificity when HTML is also acceptable", () => {
    expect(acceptsMarkdown("text/html;q=1, text/markdown;q=0.1")).toBe(false);
    expect(acceptsMarkdown("text/html;q=0.1, text/markdown;q=0.8")).toBe(true);
    expect(acceptsMarkdown("application/json;q=1, text/markdown;q=0.8")).toBe(
      true
    );
    expect(acceptsMarkdown("text/html, text/markdown")).toBe(true);
    expect(acceptsMarkdown("*/*;q=1, text/markdown;q=0.8")).toBe(false);
    expect(acceptsMarkdown("text/*;q=0.9, text/markdown;q=0.8")).toBe(false);
    expect(
      acceptsMarkdown("text/*;q=0.9, text/html;q=0.1, text/markdown;q=0.8")
    ).toBe(true);
    expect(acceptsMarkdown("text/*;q=1, text/markdown;q=0")).toBe(false);
  });

  it("matches media parameters before applying their quality", () => {
    expect(
      acceptsMarkdown(
        "text/markdown;q=1, text/markdown;charset=utf-8;q=0, text/html;q=0.5"
      )
    ).toBe(false);
    expect(
      acceptsMarkdown("text/markdown;charset=iso-8859-1;q=1, text/html;q=0.5")
    ).toBe(false);
    expect(
      acceptsMarkdown('text/html;q=0.5, text/markdown;charset="UTF-8";q=0.8')
    ).toBe(true);
    expect(
      acceptsMarkdown('text/markdown;profile="x,y";q=0, text/html;q=1')
    ).toBe(false);
    expect(
      acceptsMarkdown("text/html;q=0.5, text/markdown;q=0.8;charset=utf-8")
    ).toBe(true);
    expect(
      acceptsMarkdown("text/html;q=0.5, text/markdown;q=0.8;charset=iso-8859-1")
    ).toBe(false);
    expect(acceptsMarkdown("text/markdown;q=0.8;broken")).toBe(false);
  });

  it("maps canonical pages to their authored Markdown siblings", () => {
    expect(getMarkdownPath("/docs/")).toBe("/docs/index.md");
    expect(getMarkdownPath("/docs/env/")).toBe("/docs/env.md");
    expect(getMarkdownPath("/docs/env/overview/")).toBe(
      "/docs/env/overview.md"
    );
    expect(getMarkdownPath("/docs/cache/")).toBe("/docs/cache.md");
    expect(getMarkdownPath("/docs/cache/overview/")).toBe(
      "/docs/cache/overview.md"
    );
    expect(getMarkdownPath("/docs/agents/llms-txt/")).toBe(
      "/docs/agents/llms-txt.md"
    );
    expect(getMarkdownPath("/docs/agents/mcp/")).toBe("/docs/agents/mcp.md");
    expect(getMarkdownPath("/docs/cache/overview")).toBeUndefined();
    expect(getMarkdownPath("/docs/missing/")).toBeUndefined();
  });

  it("redirects an unslashed product root to its canonical home", async () => {
    const { assets, fetch } = createAssets();
    const response = await handleRequest(
      new Request("https://astilba.com/docs/cache?ref=product"),
      assets
    );

    expect(response.status).toBe(307);
    expect(response.headers.get("Location")).toBe("/docs/cache/?ref=product");
    expectGlobalSecurityHeaders(response);
    expect(fetch).not.toHaveBeenCalled();
  });

  it.each([
    {
      markdownPath: "/docs/cache/overview.md",
      pagePath: "/docs/cache/overview/",
    },
    {
      markdownPath: "/docs/env/overview.md",
      pagePath: "/docs/env/overview/",
    },
  ])(
    "streams the Markdown asset for $pagePath",
    async ({ markdownPath, pagePath }) => {
      const body = MARKDOWN_FIXTURES.get(markdownPath);
      const { assets, fetch } = createAssets();
      const response = await handleRequest(
        new Request(`https://astilba.com${pagePath}`, {
          headers: { Accept: "text/markdown" },
        }),
        assets
      );

      expect(response.headers.get("Content-Type")).toBe(
        "text/markdown; charset=utf-8"
      );
      expect(response.headers.get("Content-Location")).toBe(markdownPath);
      expect(response.headers.get("Link")).toBe(
        getExpectedPageDiscoveryLink(markdownPath)
      );
      expect(response.headers.get("Vary")).toBe("Accept");
      expect(response.headers.get("ETag")).toBe('"markdown"');
      expect(await response.text()).toBe(body);
      const assetInput = fetch.mock.calls[0]?.[0];
      const assetUrl =
        assetInput instanceof Request ? assetInput.url : assetInput?.toString();
      expect(new URL(assetUrl ?? "").pathname).toBe(markdownPath);
    }
  );

  it.each([
    {
      markdownPath: "/docs/cache/overview.md",
      pagePath: "/docs/cache/overview/",
    },
    {
      markdownPath: "/docs/env/overview.md",
      pagePath: "/docs/env/overview/",
    },
  ])(
    "preserves Markdown revalidation for $pagePath",
    async ({ markdownPath, pagePath }) => {
      const { assets, fetch } = createAssets();
      const response = await handleRequest(
        new Request(`https://astilba.com${pagePath}`, {
          headers: {
            Accept: "text/markdown",
            "If-None-Match": '"markdown"',
          },
        }),
        assets
      );

      expect(response.status).toBe(304);
      expect(response.headers.get("Content-Type")).toBe(
        "text/markdown; charset=utf-8"
      );
      expect(response.headers.get("Content-Location")).toBe(markdownPath);
      expect(response.headers.get("Vary")).toBe("Accept");
      expect(fetch).toHaveBeenCalledTimes(1);
    }
  );

  it.each([
    "/docs/index.md",
    "/docs/cache.md",
    "/docs/cache/overview.md",
    "/docs/env.md",
    "/docs/env/overview.md",
  ])(
    "sets direct Markdown headers for %s without buffering the asset",
    async (path) => {
      const { assets, fetch } = createAssets();
      const response = await handleRequest(
        new Request(`https://astilba.com${path}`),
        assets
      );

      expect(response.status).toBe(200);
      expect(response.headers.get("Content-Type")).toBe(
        "text/markdown; charset=utf-8"
      );
      expect(response.headers.get("X-Content-Type-Options")).toBe("nosniff");
      expectGlobalSecurityHeaders(response);
      expect(response.headers.get("ETag")).toBe('"markdown"');
      expect(await response.text()).toContain("# ");
      expect(fetch).toHaveBeenCalledTimes(1);
    }
  );

  it("preserves direct Markdown revalidation headers", async () => {
    const { assets } = createAssets();
    const response = await handleRequest(
      new Request("https://astilba.com/docs/cache/overview.md", {
        headers: { "If-None-Match": '"markdown"' },
      }),
      assets
    );

    expect(response.status).toBe(304);
    expect(response.headers.get("Content-Type")).toBe(
      "text/markdown; charset=utf-8"
    );
    expect(response.headers.get("X-Content-Type-Options")).toBe("nosniff");
    expectGlobalSecurityHeaders(response);
    expect(response.headers.get("ETag")).toBe('"markdown"');
  });

  it.each(["/docs/missing.md", "/docs/cache/missing.md"])(
    "preserves the HTML 404 for unknown direct Markdown path %s",
    async (path) => {
      const { assets } = createAssets();
      const response = await handleRequest(
        new Request(`https://astilba.com${path}`),
        assets
      );

      expect(response.status).toBe(404);
      expect(response.headers.get("Content-Type")).toBe(
        "text/html; charset=utf-8"
      );
      expect(response.headers.get("Content-Security-Policy")).toBe(
        TEST_CONTENT_SECURITY_POLICY
      );
      expect(response.headers.get("X-Content-Type-Options")).toBeNull();
      expectGlobalSecurityHeaders(response);
      expect(await response.text()).toBe("Not found");
    }
  );

  it.each([
    { markdownPath: "/docs/index.md", pagePath: "/docs/" },
    { markdownPath: "/docs/env.md", pagePath: "/docs/env/" },
    {
      markdownPath: "/docs/env/overview.md",
      pagePath: "/docs/env/overview/",
    },
    { markdownPath: "/docs/cache.md", pagePath: "/docs/cache/" },
    {
      markdownPath: "/docs/cache/overview.md",
      pagePath: "/docs/cache/overview/",
    },
  ])(
    "adds discovery links to GET, HEAD, and revalidation for $pagePath",
    async ({ markdownPath, pagePath }) => {
      const { assets, fetch } = createAssets();
      const expectedLink = getExpectedPageDiscoveryLink(markdownPath);
      const response = await handleRequest(
        new Request(`https://astilba.com${pagePath}`),
        assets
      );

      expect(response.status).toBe(200);
      expect(response.headers.get("Content-Type")).toBe(
        "text/html; charset=utf-8"
      );
      expect(response.headers.get("Link")).toBe(expectedLink);
      expect(response.headers.get("Vary")).toBe("Accept-Encoding, Accept");

      const headResponse = await handleRequest(
        new Request(`https://astilba.com${pagePath}`, {
          method: "HEAD",
        }),
        assets
      );
      expect(headResponse.status).toBe(200);
      expect(headResponse.headers.get("Link")).toBe(expectedLink);
      expect(headResponse.headers.get("Content-Security-Policy")).toBe(
        TEST_CONTENT_SECURITY_POLICY
      );

      const revalidationResponse = await handleRequest(
        new Request(`https://astilba.com${pagePath}`, {
          headers: { "If-None-Match": '"html"' },
        }),
        assets
      );
      expect(revalidationResponse.status).toBe(304);
      expect(revalidationResponse.headers.get("Link")).toBe(expectedLink);
      expect(revalidationResponse.headers.get("Vary")).toBe(
        "Accept-Encoding, Accept"
      );
      expect(revalidationResponse.headers.get("Content-Security-Policy")).toBe(
        TEST_CONTENT_SECURITY_POLICY
      );
      expect(fetch).toHaveBeenCalledTimes(6);
      const policyRequests = fetch.mock.calls
        .map(([input, init]) => new Request(input, init))
        .filter(
          (assetRequest) =>
            new URL(assetRequest.url).pathname ===
            CONTENT_SECURITY_POLICY_ASSET_PATH
        );
      expect(policyRequests).toHaveLength(3);
      expect(
        policyRequests.every(
          (policyRequest) =>
            policyRequest.method === "GET" &&
            policyRequest.redirect === "manual"
        )
      ).toBe(true);
    }
  );

  it.each(["/docs/missing/", "/docs/cache/missing/"])(
    "preserves unknown page behavior for %s",
    async (pagePath) => {
      const { assets, fetch } = createAssets();
      const response = await handleRequest(
        new Request(`https://astilba.com${pagePath}`, {
          headers: { Accept: "text/markdown" },
        }),
        assets
      );

      expect(response.status).toBe(404);
      expect(response.headers.get("Content-Type")).toBe(
        "text/html; charset=utf-8"
      );
      expect(response.headers.get("Link")).toBeNull();
      expect(response.headers.get("Content-Security-Policy")).toBe(
        TEST_CONTENT_SECURITY_POLICY
      );

      const headResponse = await handleRequest(
        new Request(`https://astilba.com${pagePath}`, {
          method: "HEAD",
        }),
        assets
      );
      expect(headResponse.status).toBe(404);
      expect(headResponse.headers.get("Link")).toBeNull();
      expect(fetch).toHaveBeenCalledTimes(4);
    }
  );

  it("does not treat the slash-suffixed MCP path as the protocol endpoint", async () => {
    const { assets, fetch } = createAssets();
    const response = await handleRequest(
      new Request("https://astilba.com/docs/mcp/", { method: "POST" }),
      assets
    );

    expect(response.status).toBe(200);
    expect(response.headers.get("Content-Security-Policy")).toBe(
      TEST_CONTENT_SECURITY_POLICY
    );
    expect(fetch).toHaveBeenCalledTimes(2);
  });

  it("adds global security headers to Worker-generated MCP failures", async () => {
    const { assets } = createAssets();
    const response = await handleRequest(
      new Request("https://astilba.com/docs/mcp"),
      assets
    );

    expect(response.status).toBe(503);
    expectGlobalSecurityHeaders(response);
  });

  it.each([
    { policyBody: "", policyStatus: 200 },
    { policyBody: "missing", policyStatus: 404 },
  ])(
    "fails closed when the generated policy asset is unavailable: $policyStatus",
    async ({ policyBody, policyStatus }) => {
      const { assets } = createAssets({ policyBody, policyStatus });
      const consoleError = vi
        .spyOn(console, "error")
        .mockImplementation(() => undefined);

      try {
        const response = await handleRequest(
          new Request("https://astilba.com/docs/cache/overview/"),
          assets
        );

        expect(response.status).toBe(503);
        expect(response.headers.get("Cache-Control")).toBe("no-store");
        expect(response.headers.get("Content-Security-Policy")).toBeNull();
        expectGlobalSecurityHeaders(response);
        expect(await response.text()).toBe(
          "Documentation is temporarily unavailable."
        );
        expect(consoleError).toHaveBeenCalledWith(
          expect.stringContaining("docs.content_security_policy_unavailable")
        );
      } finally {
        consoleError.mockRestore();
      }
    }
  );

  it("permanently redirects the legacy host path and query", async () => {
    const { assets, fetch } = createAssets();
    const response = await handleRequest(
      new Request(
        "https://docs.astilba.com/cache/overview/?source=legacy&empty="
      ),
      assets
    );

    expect(response.status).toBe(308);
    expect(response.headers.get("Location")).toBe(
      "https://astilba.com/docs/cache/overview/?source=legacy&empty="
    );
    expectGlobalSecurityHeaders(response);
    expect(fetch).not.toHaveBeenCalled();
  });
});
