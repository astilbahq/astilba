import { describe, expect, it, vi } from "vitest";

import { MCP_SERVER_INFO } from "../../src/docs/agent-discovery";
import { docsProducts } from "../../src/docs/catalog";
import { parseDocsCorpus } from "../../src/docs/mcp-corpus";
import { siteDocsPages } from "../../src/docs/site-pages";
import { ASTILBA_ORIGIN, docsUrl, withDocsBase } from "../../src/docs/urls";
import {
  handleDocsMcpRequest,
  readDoc,
  searchDocs,
} from "../../worker/docs-mcp";

const docsOrigin = ASTILBA_ORIGIN;

const getSitePageFixture = (
  id: string
): { content: string; description: string; title: string } => {
  switch (id) {
    case "index":
      return {
        content: "# Astilba documentation\n\nBrowse the public product docs.",
        description: "Public documentation for Astilba products.",
        title: "Astilba documentation",
      };
    case "cache":
      return {
        content: "# Cache\n\nExplore the public Cache documentation.",
        description: "A portable server-side TypeScript cache.",
        title: "Cache",
      };
    case "env":
      return {
        content: "# Env\n\nExplore the public Env documentation.",
        description: "A local-first configuration contract compiler.",
        title: "Env",
      };
    case "agents/llms-txt":
      return {
        content: "# LLMs.txt\n\nChoose a generated documentation corpus.",
        description: "Choose an Astilba documentation corpus for an agent.",
        title: "LLMs.txt",
      };
    case "agents/mcp":
      return {
        content: "# MCP Server\n\nConnect to the public documentation server.",
        description: "Connect an MCP client to Astilba documentation.",
        title: "MCP Server",
      };
    default:
      throw new Error(`Missing site documentation fixture for ${id}.`);
  }
};

const createRateLimiter = (success = true) => {
  const limit = vi.fn(async () => ({ success }));
  const rateLimiter = { limit } satisfies RateLimit;

  return { limit, rateLimiter };
};

const createCorpusValue = () => ({
  schemaVersion: 1,
  pages: [
    ...siteDocsPages.map(({ canonicalPath, id, markdownPath, productId }) => {
      const fixture = getSitePageFixture(id);
      const product = productId
        ? docsProducts.find((candidate) => candidate.id === productId)
        : undefined;

      return {
        canonicalUrl: new URL(canonicalPath, docsOrigin).href,
        content: fixture.content,
        description: fixture.description,
        markdownPath,
        ...(product
          ? {
              product: product.label,
              productId: product.id,
            }
          : {}),
        title: fixture.title,
        uri: `${docsOrigin}${markdownPath}`,
      };
    }),
    ...docsProducts.flatMap((product) =>
      product.versions.flatMap((version) =>
        version.sections.flatMap((section) =>
          section.items.map((page) => {
            const markdownPath = withDocsBase(
              `/${version.basePath}/${page.slug}.md`
            );
            const isOverview = page.slug === "overview";
            const isInvalidation = page.slug === "tags-and-invalidation";
            const content = isOverview
              ? `A😀B ${product.label} overview and public boundaries.`
              : isInvalidation
                ? "# Invalidate cached data\n\nTag invalidation supports related values."
                : `# ${page.label}\n\nPublic ${product.label} documentation for ${page.label}.`;

            return {
              canonicalUrl: docsUrl(`/${version.basePath}/${page.slug}/`),
              content,
              description: isInvalidation
                ? "Invalidate related cached values with tags."
                : `Learn about ${page.label}.`,
              docsVersion: version.label,
              docsVersionId: version.id,
              lifecycle: version.lifecycle,
              markdownPath,
              product: product.label,
              productId: product.id,
              title: page.label,
              uri: `${docsOrigin}${markdownPath}`,
            };
          })
        )
      )
    ),
  ],
});

const createAssets = () => {
  const corpus = JSON.stringify(createCorpusValue());
  const fetch = vi.fn<Fetcher["fetch"]>(async (input) => {
    const url = new URL(
      input instanceof Request ? input.url : input.toString()
    );

    if (url.pathname === withDocsBase("/_mcp/docs.json")) {
      return new Response(corpus, {
        headers: { "Content-Type": "application/json; charset=utf-8" },
      });
    }

    return new Response("Not found", { status: 404 });
  });
  const assets = {
    connect() {
      throw new Error("The test asset binding does not open sockets.");
    },
    fetch,
  } satisfies Fetcher;

  return { assets, fetch };
};

const createRpcRequest = (
  method: string,
  params?: Record<string, unknown>,
  options: {
    clientIp?: string;
    id?: number;
    origin?: string;
    protocolVersion?: string;
  } = {}
) =>
  new Request(docsUrl("/mcp"), {
    body: JSON.stringify({
      id: options.id ?? 1,
      jsonrpc: "2.0",
      method,
      ...(params ? { params } : {}),
    }),
    headers: {
      Accept: "application/json, text/event-stream",
      "Content-Type": "application/json",
      "MCP-Protocol-Version": options.protocolVersion ?? "2025-11-25",
      ...(options.clientIp ? { "CF-Connecting-IP": options.clientIp } : {}),
      ...(options.origin ? { Origin: options.origin } : {}),
    },
    method: "POST",
  });

describe("generated MCP corpus", () => {
  it("accepts exactly the allowlisted site and product pages", () => {
    const value = createCorpusValue();
    const corpus = parseDocsCorpus(value);

    expect(corpus.pages).toHaveLength(value.pages.length);
    expect(corpus.pages[0]?.uri).toBe(`${docsOrigin}/docs/index.md`);
  });

  it("rejects metadata that differs from the typed catalog", () => {
    const value = createCorpusValue();
    const overview = value.pages.find(
      (page) => page.markdownPath === "/docs/cache/overview.md"
    );

    if (!overview || !("productId" in overview)) {
      throw new Error("Missing overview fixture.");
    }

    overview.productId = "different";
    expect(() => parseDocsCorpus(value)).toThrow(
      "differs from the public documentation catalog"
    );
  });

  it("rejects empty and overlong titles and descriptions", () => {
    const emptyTitle = createCorpusValue();
    emptyTitle.pages[0].title = "";

    expect(() => parseDocsCorpus(emptyTitle)).toThrow("non-empty string");

    const overlongTitle = createCorpusValue();
    overlongTitle.pages[0].title = "a".repeat(257);

    expect(() => parseDocsCorpus(overlongTitle)).toThrow(
      "at most 256 characters"
    );

    const emptyDescription = createCorpusValue();
    emptyDescription.pages[0].description = "";

    expect(() => parseDocsCorpus(emptyDescription)).toThrow("non-empty string");

    const overlongDescription = createCorpusValue();
    overlongDescription.pages[0].description = "a".repeat(1025);

    expect(() => parseDocsCorpus(overlongDescription)).toThrow(
      "at most 1024 characters"
    );
  });

  it("requires the complete catalogue metadata tuple on product pages", () => {
    const value = createCorpusValue();
    const overview = value.pages.find(
      (page) => page.markdownPath === "/docs/cache/overview.md"
    );

    if (!overview || !("productId" in overview)) {
      throw new Error("Missing overview fixture.");
    }

    for (const field of [
      "docsVersion",
      "docsVersionId",
      "lifecycle",
      "product",
      "productId",
    ]) {
      Reflect.deleteProperty(overview, field);
    }

    expect(() => parseDocsCorpus(value)).toThrow(
      "differs from the public documentation catalog"
    );
  });

  it("rejects version metadata on an unversioned product home", () => {
    const value = createCorpusValue();
    const cacheHome = value.pages.find(
      (page) => page.markdownPath === "/docs/cache.md"
    );

    if (!cacheHome) {
      throw new Error("Missing Cache product home fixture.");
    }

    Object.assign(cacheHome, {
      docsVersion: "Unreleased",
      docsVersionId: "unreleased",
      lifecycle: "unreleased",
    });

    expect(() => parseDocsCorpus(value)).toThrow(
      "differs from the public documentation catalog"
    );
  });

  it("rejects empty and overlong catalogue metadata", () => {
    const emptyMetadata = createCorpusValue();
    const emptyOverview = emptyMetadata.pages.find(
      (page) => page.markdownPath === "/docs/cache/overview.md"
    );

    if (!emptyOverview || !("productId" in emptyOverview)) {
      throw new Error("Missing overview fixture.");
    }

    emptyOverview.productId = "";
    expect(() => parseDocsCorpus(emptyMetadata)).toThrow("non-empty string");

    const overlongMetadata = createCorpusValue();
    const overlongOverview = overlongMetadata.pages.find(
      (page) => page.markdownPath === "/docs/cache/overview.md"
    );

    if (!overlongOverview || !("productId" in overlongOverview)) {
      throw new Error("Missing overview fixture.");
    }

    overlongOverview.productId = "a".repeat(129);
    expect(() => parseDocsCorpus(overlongMetadata)).toThrow(
      "at most 128 characters"
    );
  });

  it("rejects product metadata on the catalogue-independent homepage", () => {
    const value = createCorpusValue();
    Object.assign(value.pages[0], {
      docsVersion: "Unreleased",
      docsVersionId: "unreleased",
      lifecycle: "unreleased",
      product: "Cache",
      productId: "cache",
    });

    expect(() => parseDocsCorpus(value)).toThrow(
      "is not present in the public documentation catalog"
    );
  });

  it("rejects site-wide resources outside the explicit allowlist", () => {
    const value = createCorpusValue();
    const sitePage = value.pages.find(
      ({ markdownPath }) => markdownPath === "/docs/agents/mcp.md"
    );

    if (!sitePage) {
      throw new Error("Missing site-wide MCP fixture.");
    }

    sitePage.canonicalUrl = `${docsOrigin}/docs/agents/private/`;
    sitePage.markdownPath = "/docs/agents/private.md";
    sitePage.uri = `${docsOrigin}/docs/agents/private.md`;

    expect(() => parseDocsCorpus(value)).toThrow(
      "is not present in the public documentation catalog"
    );
  });

  it("rejects resources outside the public docs origin", () => {
    const value = createCorpusValue();
    value.pages[0].uri = "https://example.com/index.md";

    expect(() => parseDocsCorpus(value)).toThrow("is not canonical");
  });

  it("ranks and filters deterministic lexical search results", () => {
    const corpus = parseDocsCorpus(createCorpusValue());
    const results = searchDocs(corpus, {
      productId: "cache",
      query: "tag invalidation",
      versionId: "unreleased",
    });

    expect(results).toHaveLength(1);
    expect(results[0]).toMatchObject({
      productId: "cache",
      title: "Invalidate cached data",
      uri: `${docsOrigin}/docs/cache/tags-and-invalidation.md`,
      versionId: "unreleased",
    });
    expect(
      searchDocs(corpus, { productId: "missing", query: "cache" })
    ).toEqual([]);
    expect(
      searchDocs(corpus, { query: "related cached values" })[0]
    ).toMatchObject({ title: "Invalidate cached data" });
    expect(
      searchDocs(corpus, {
        productId: "cache",
        query: "Explore public Cache documentation",
      })[0]
    ).toMatchObject({
      productId: "cache",
      title: "Cache",
      uri: `${docsOrigin}/docs/cache.md`,
      versionId: null,
    });
    expect(
      searchDocs(corpus, {
        productId: "env",
        query: "Cloudflare Workers",
        versionId: "0.3",
      })[0]
    ).toMatchObject({
      productId: "env",
      title: "Cloudflare Workers",
      uri: `${docsOrigin}/docs/env/cloudflare-workers.md`,
      versionId: "0.3",
    });
  });

  it("reads only allowlisted resources and avoids splitting surrogate pairs", () => {
    const corpus = parseDocsCorpus(createCorpusValue());
    const result = readDoc(corpus, {
      limit: 2,
      offset: 0,
      uri: "/docs/cache/overview.md",
    });

    expect(result).toMatchObject({
      content: "A",
      nextOffset: 1,
      returnedChars: 1,
    });
    expect(
      readDoc(corpus, { uri: "https://example.com/private.md" })
    ).toBeUndefined();
    expect(
      readDoc(corpus, { uri: `${docsOrigin}/docs/cache/overview/?draft=1` })
    ).toBeUndefined();
    expect(
      readDoc(corpus, {
        offset: 10_000,
        uri: `${docsOrigin}/docs/cache/overview.md`,
      })
    ).toBeUndefined();
    expect(
      readDoc(corpus, {
        offset: 2,
        uri: `${docsOrigin}/docs/cache/overview.md`,
      })
    ).toBeUndefined();
    expect(
      readDoc(corpus, {
        limit: 1,
        offset: 1,
        uri: `${docsOrigin}/docs/cache/overview.md`,
      })
    ).toMatchObject({
      content: "😀",
      nextOffset: 3,
      returnedChars: 2,
    });
    expect(
      readDoc(corpus, {
        limit: 12,
        uri: `${docsOrigin}/docs/env/cloudflare-workers.md`,
      })
    ).toMatchObject({
      offset: 0,
      returnedChars: 12,
      uri: `${docsOrigin}/docs/env/cloudflare-workers.md`,
    });
  });
});

describe("documentation MCP transport", () => {
  it("initializes with read-only tool and resource capabilities", async () => {
    const { assets } = createAssets();
    const { limit, rateLimiter } = createRateLimiter();
    const response = await handleDocsMcpRequest(
      createRpcRequest(
        "initialize",
        {
          capabilities: {},
          clientInfo: { name: "test-client", version: "1.0.0" },
          protocolVersion: "2025-11-25",
        },
        { origin: docsOrigin }
      ),
      assets,
      rateLimiter
    );
    const body = await response.json();

    expect(response.status).toBe(200);
    expect(response.headers.get("Cache-Control")).toBe("no-store");
    expect(response.headers.get("Access-Control-Allow-Origin")).toBe(
      docsOrigin
    );
    expect(body).toMatchObject({
      result: {
        capabilities: { resources: {}, tools: {} },
        serverInfo: {
          name: MCP_SERVER_INFO.name,
          version: MCP_SERVER_INFO.version,
        },
      },
    });
    expect(limit).toHaveBeenCalledExactlyOnceWith({
      key: "client:anonymous",
    });
  });

  it.each(["ping", "resources/list", "tools/list"])(
    "charges one request unit for %s",
    async (method) => {
      const { assets } = createAssets();
      const { limit, rateLimiter } = createRateLimiter();
      const response = await handleDocsMcpRequest(
        createRpcRequest(method, undefined, { clientIp: "203.0.113.7" }),
        assets,
        rateLimiter
      );

      expect(response.status).toBe(200);
      expect(limit).toHaveBeenCalledExactlyOnceWith({
        key: "client:203.0.113.7",
      });
    }
  );

  it("lists resources and invokes both bounded read-only tools", async () => {
    const { assets, fetch } = createAssets();
    const { limit, rateLimiter } = createRateLimiter();
    const resourcesResponse = await handleDocsMcpRequest(
      createRpcRequest("resources/list"),
      assets,
      rateLimiter
    );
    const resourcesBody = (await resourcesResponse.json()) as {
      result: { resources: Array<Record<string, unknown>> };
    };

    expect(resourcesBody.result.resources).toHaveLength(
      createCorpusValue().pages.length
    );
    expect(resourcesBody.result.resources[0]).toMatchObject({
      mimeType: "text/markdown",
      uri: `${docsOrigin}/docs/index.md`,
    });
    expect(fetch).toHaveBeenCalledTimes(1);

    const [searchResponse, readResponse] = await Promise.all([
      handleDocsMcpRequest(
        createRpcRequest("tools/call", {
          arguments: { query: "tag invalidation" },
          name: "search_docs",
        }),
        assets,
        rateLimiter
      ),
      handleDocsMcpRequest(
        createRpcRequest("tools/call", {
          arguments: {
            limit: 8,
            uri: `${docsOrigin}/docs/cache/overview.md`,
          },
          name: "read_doc",
        }),
        assets,
        rateLimiter
      ),
    ]);
    const searchBody = (await searchResponse.json()) as {
      result: {
        structuredContent: {
          results: Array<Record<string, unknown>>;
        };
      };
    };
    const readBody = (await readResponse.json()) as {
      result: { structuredContent: Record<string, unknown> };
    };

    expect(searchBody.result.structuredContent.results[0]).toMatchObject({
      title: "Invalidate cached data",
      uri: `${docsOrigin}/docs/cache/tags-and-invalidation.md`,
    });
    expect(readBody.result.structuredContent).toMatchObject({
      offset: 0,
      returnedChars: 8,
      uri: `${docsOrigin}/docs/cache/overview.md`,
    });
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(limit).toHaveBeenCalledTimes(5);
    expect(limit.mock.calls).toEqual(
      Array.from({ length: 5 }, () => [{ key: "client:anonymous" }])
    );
  });

  it("charges an additional unit for direct resource reads", async () => {
    const { assets } = createAssets();
    const { limit, rateLimiter } = createRateLimiter();
    const overviewUri = `${docsOrigin}/docs/cache/overview.md`;
    const response = await handleDocsMcpRequest(
      createRpcRequest("resources/read", { uri: overviewUri }),
      assets,
      rateLimiter
    );
    const body = (await response.json()) as {
      result: { contents: Array<Record<string, unknown>> };
    };

    expect(response.status).toBe(200);
    expect(body.result.contents[0]).toMatchObject({
      mimeType: "text/markdown",
      uri: overviewUri,
    });
    expect(limit).toHaveBeenCalledTimes(2);
    expect(limit).toHaveBeenNthCalledWith(1, { key: "client:anonymous" });
    expect(limit).toHaveBeenNthCalledWith(2, { key: "client:anonymous" });
  });

  it("keeps pre-2025-06-18 tool definitions and results compatible", async () => {
    const { assets } = createAssets();
    const { rateLimiter } = createRateLimiter();
    const options = { protocolVersion: "2025-03-26" };
    const toolsResponse = await handleDocsMcpRequest(
      createRpcRequest("tools/list", undefined, options),
      assets,
      rateLimiter
    );
    const toolsBody = (await toolsResponse.json()) as {
      result: { tools: Array<Record<string, unknown>> };
    };
    const callResponse = await handleDocsMcpRequest(
      createRpcRequest(
        "tools/call",
        {
          arguments: { query: "tag invalidation" },
          name: "search_docs",
        },
        options
      ),
      assets,
      rateLimiter
    );
    const callBody = (await callResponse.json()) as {
      result: {
        content: Array<{ type: string }>;
        structuredContent?: unknown;
      };
    };

    expect(toolsBody.result.tools.some((tool) => "outputSchema" in tool)).toBe(
      false
    );
    expect(callBody.result.structuredContent).toBeUndefined();
    expect(callBody.result.content).toEqual([
      expect.objectContaining({ type: "text" }),
    ]);
  });

  it("supports bounded legacy JSON-RPC batches", async () => {
    const { assets } = createAssets();
    const { limit, rateLimiter } = createRateLimiter();
    const response = await handleDocsMcpRequest(
      new Request(`${docsOrigin}/docs/mcp`, {
        body: JSON.stringify([
          { id: 1, jsonrpc: "2.0", method: "ping" },
          { id: 2, jsonrpc: "2.0", method: "resources/list" },
        ]),
        headers: {
          Accept: "application/json, text/event-stream",
          "Content-Type": "application/json",
          "MCP-Protocol-Version": "2025-03-26",
        },
        method: "POST",
      }),
      assets,
      rateLimiter
    );
    const body = await response.json();

    expect(response.status).toBe(200);
    expect(body).toHaveLength(2);
    expect(
      (body as Array<{ id: number }>).map(({ id }) => id).toSorted()
    ).toEqual([1, 2]);
    expect(limit).toHaveBeenCalledExactlyOnceWith({
      key: "client:anonymous",
    });
  });

  it("charges each expensive operation in a legacy batch", async () => {
    const { assets } = createAssets();
    const { limit, rateLimiter } = createRateLimiter();
    const response = await handleDocsMcpRequest(
      new Request(`${docsOrigin}/docs/mcp`, {
        body: JSON.stringify([
          { id: 1, jsonrpc: "2.0", method: "ping" },
          {
            id: 2,
            jsonrpc: "2.0",
            method: "resources/read",
            params: { uri: `${docsOrigin}/docs/cache/overview.md` },
          },
          {
            id: 3,
            jsonrpc: "2.0",
            method: "tools/call",
            params: {
              arguments: { query: "cache" },
              name: "search_docs",
            },
          },
        ]),
        headers: {
          Accept: "application/json, text/event-stream",
          "Content-Type": "application/json",
          "MCP-Protocol-Version": "2025-03-26",
        },
        method: "POST",
      }),
      assets,
      rateLimiter
    );

    expect(response.status).toBe(200);
    expect(await response.json()).toHaveLength(3);
    expect(limit).toHaveBeenCalledTimes(3);
    expect(limit.mock.calls).toEqual(
      Array.from({ length: 3 }, () => [{ key: "client:anonymous" }])
    );
  });

  it("rejects modern JSON-RPC batches before loading resources", async () => {
    const { assets, fetch } = createAssets();
    const { limit, rateLimiter } = createRateLimiter();
    const response = await handleDocsMcpRequest(
      new Request(`${docsOrigin}/docs/mcp`, {
        body: JSON.stringify([
          { id: 1, jsonrpc: "2.0", method: "ping" },
          { id: 2, jsonrpc: "2.0", method: "ping" },
        ]),
        headers: {
          Accept: "application/json, text/event-stream",
          "Content-Type": "application/json",
          "MCP-Protocol-Version": "2025-11-25",
        },
        method: "POST",
      }),
      assets,
      rateLimiter
    );

    expect(response.status).toBe(400);
    expect(fetch).not.toHaveBeenCalled();
    expect(limit).toHaveBeenCalledExactlyOnceWith({
      key: "client:anonymous",
    });
  });

  it("rate-limits expensive operations before loading the corpus", async () => {
    const { assets, fetch } = createAssets();
    const { limit, rateLimiter } = createRateLimiter();
    limit
      .mockResolvedValueOnce({ success: true })
      .mockResolvedValueOnce({ success: false });
    const consoleWarn = vi
      .spyOn(console, "warn")
      .mockImplementation(() => undefined);
    const response = await handleDocsMcpRequest(
      createRpcRequest(
        "tools/call",
        {
          arguments: { query: "cache" },
          name: "search_docs",
        },
        { clientIp: "203.0.113.8" }
      ),
      assets,
      rateLimiter
    );
    const body = await response.json();

    try {
      expect(response.status).toBe(429);
      expect(response.headers.get("Retry-After")).toBe("60");
      expect(body).toMatchObject({
        error: {
          message: "MCP operation rate limit exceeded. Retry later.",
        },
        id: 1,
      });
      expect(fetch).not.toHaveBeenCalled();
      expect(limit).toHaveBeenCalledTimes(2);
      expect(limit).toHaveBeenNthCalledWith(1, {
        key: "client:203.0.113.8",
      });
      expect(limit).toHaveBeenNthCalledWith(2, {
        key: "client:203.0.113.8",
      });
      expect(consoleWarn).toHaveBeenCalledExactlyOnceWith({
        event: "mcp_rate_limited",
        phase: "operation",
        status: 429,
      });
    } finally {
      consoleWarn.mockRestore();
    }
  });

  it("rate-limits requests before reading their body", async () => {
    const { assets, fetch } = createAssets();
    const { limit, rateLimiter } = createRateLimiter(false);
    const consoleWarn = vi
      .spyOn(console, "warn")
      .mockImplementation(() => undefined);
    const body = new ReadableStream<Uint8Array>({
      pull() {
        throw new Error("body must not be read");
      },
    });
    const request = new Request(`${docsOrigin}/docs/mcp`, {
      body,
      duplex: "half",
      headers: { "CF-Connecting-IP": "203.0.113.9" },
      method: "POST",
    } as RequestInit & { duplex: "half" });

    try {
      const response = await handleDocsMcpRequest(request, assets, rateLimiter);

      expect(response.status).toBe(429);
      expect(response.headers.get("Retry-After")).toBe("60");
      expect(await response.json()).toMatchObject({
        error: { message: "MCP request rate limit exceeded. Retry later." },
        id: null,
      });
      expect(fetch).not.toHaveBeenCalled();
      expect(limit).toHaveBeenCalledExactlyOnceWith({
        key: "client:203.0.113.9",
      });
      expect(consoleWarn).toHaveBeenCalledExactlyOnceWith({
        event: "mcp_rate_limited",
        phase: "request",
        status: 429,
      });
    } finally {
      consoleWarn.mockRestore();
    }
  });

  it("fails closed when request-level metering is unavailable", async () => {
    const { assets, fetch } = createAssets();
    const { limit, rateLimiter } = createRateLimiter();
    limit.mockRejectedValueOnce(new Error("limiter unavailable"));
    const consoleError = vi
      .spyOn(console, "error")
      .mockImplementation(() => undefined);

    try {
      const response = await handleDocsMcpRequest(
        createRpcRequest("ping"),
        assets,
        rateLimiter
      );

      expect(response.status).toBe(503);
      expect(await response.json()).toMatchObject({
        error: { message: "The MCP endpoint is temporarily unavailable." },
        id: null,
      });
      expect(fetch).not.toHaveBeenCalled();
      expect(limit).toHaveBeenCalledOnce();
      expect(consoleError).toHaveBeenCalledExactlyOnceWith({
        error: "limiter unavailable",
        errorName: "Error",
        event: "mcp_rate_limit_failure",
        phase: "request",
      });
    } finally {
      consoleError.mockRestore();
    }
  });

  it("fails closed when expensive-operation metering is unavailable", async () => {
    const { assets, fetch } = createAssets();
    const { limit, rateLimiter } = createRateLimiter();
    limit
      .mockResolvedValueOnce({ success: true })
      .mockRejectedValueOnce(new Error("operation limiter unavailable"));
    const consoleError = vi
      .spyOn(console, "error")
      .mockImplementation(() => undefined);

    try {
      const response = await handleDocsMcpRequest(
        createRpcRequest("resources/read", {
          uri: `${docsOrigin}/docs/cache/overview.md`,
        }),
        assets,
        rateLimiter
      );

      expect(response.status).toBe(503);
      expect(await response.json()).toMatchObject({
        error: { message: "The MCP endpoint is temporarily unavailable." },
        id: 1,
      });
      expect(fetch).not.toHaveBeenCalled();
      expect(limit).toHaveBeenCalledTimes(2);
      expect(consoleError).toHaveBeenCalledExactlyOnceWith({
        error: "operation limiter unavailable",
        errorName: "Error",
        event: "mcp_rate_limit_failure",
        phase: "operation",
      });
    } finally {
      consoleError.mockRestore();
    }
  });

  it("normalizes unexpected request-stream failures", async () => {
    const { assets, fetch } = createAssets();
    const { limit, rateLimiter } = createRateLimiter();
    const consoleError = vi
      .spyOn(console, "error")
      .mockImplementation(() => undefined);
    const body = new ReadableStream<Uint8Array>({
      pull() {
        throw new Error("stream failed");
      },
    });
    const request = new Request(`${docsOrigin}/docs/mcp`, {
      body,
      duplex: "half",
      headers: {
        Accept: "application/json, text/event-stream",
        "Content-Type": "application/json",
      },
      method: "POST",
    } as RequestInit & { duplex: "half" });

    try {
      const response = await handleDocsMcpRequest(request, assets, rateLimiter);

      expect(response.status).toBe(500);
      expect(await response.json()).toMatchObject({
        error: { code: -32603 },
        id: null,
      });
      expect(fetch).not.toHaveBeenCalled();
      expect(limit).toHaveBeenCalledExactlyOnceWith({
        key: "client:anonymous",
      });
    } finally {
      consoleError.mockRestore();
    }
  });

  it("rejects foreign browser origins before loading the corpus", async () => {
    const { assets, fetch } = createAssets();
    const { rateLimiter } = createRateLimiter();
    const response = await handleDocsMcpRequest(
      createRpcRequest("tools/list", undefined, {
        origin: "https://example.com",
      }),
      assets,
      rateLimiter
    );

    expect(response.status).toBe(403);
    expect(fetch).not.toHaveBeenCalled();
  });

  it("rejects untrusted request hosts even without an Origin header", async () => {
    const { assets, fetch } = createAssets();
    const { limit, rateLimiter } = createRateLimiter();
    const request = createRpcRequest("tools/list");
    const response = await handleDocsMcpRequest(
      new Request("http://evil.example/mcp", request),
      assets,
      rateLimiter
    );

    expect(response.status).toBe(403);
    expect(fetch).not.toHaveBeenCalled();
    expect(limit).not.toHaveBeenCalled();
  });

  it("answers same-origin preflight and rejects non-POST protocol requests", async () => {
    const { assets } = createAssets();
    const { rateLimiter } = createRateLimiter();
    const preflight = await handleDocsMcpRequest(
      new Request(`${docsOrigin}/docs/mcp`, {
        headers: { Origin: docsOrigin },
        method: "OPTIONS",
      }),
      assets,
      rateLimiter
    );
    const getResponse = await handleDocsMcpRequest(
      new Request(`${docsOrigin}/docs/mcp`),
      assets,
      rateLimiter
    );

    expect(preflight.status).toBe(204);
    expect(preflight.headers.get("Access-Control-Allow-Origin")).toBe(
      docsOrigin
    );
    expect(preflight.headers.get("Vary")).toBe("Origin");
    expect(getResponse.status).toBe(405);
    expect(getResponse.headers.get("Allow")).toBe("POST, OPTIONS");
  });
});
