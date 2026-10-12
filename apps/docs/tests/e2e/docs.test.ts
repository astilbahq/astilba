import { createHash } from "node:crypto";

import AxeBuilder from "@axe-core/playwright";
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { StreamableHTTPClientTransport } from "@modelcontextprotocol/sdk/client/streamableHttp.js";
import { expect, type Page, type Route, test } from "@playwright/test";

import { AGENT_SETUP_COPY_TEXT } from "../../src/docs/agent-setup";
import { EXPECTED_CORPUS_PAGES } from "../../src/docs/mcp-corpus";
import { GLOBAL_SECURITY_HEADERS } from "../../src/docs/security";
import { ASTILBA_ORIGIN, docsUrl, withDocsBase } from "../../src/docs/urls";

interface WebMcpToolProbe {
  annotations: {
    readOnlyHint: boolean;
    untrustedContentHint: boolean;
  };
  execute: (input?: { offset?: number }) => Promise<string>;
  name: string;
}

declare global {
  interface Window {
    __cspViolations?: Array<{
      blockedUri: string;
      effectiveDirective: string;
    }>;
    __setAstilbaFinePointer: (value: boolean) => void;
    __tooltipExitDuration?: string;
    __webMcpRegistrationMethods?: string[];
    __webMcpTools?: WebMcpToolProbe[];
  }
}

const docsOrigin = ASTILBA_ORIGIN;

const expectSimpleGetCors = (headers: Record<string, string>): void => {
  expect(headers["access-control-allow-origin"]).toBe("*");
  expect(headers["access-control-allow-headers"]).toBeUndefined();
  expect(headers["access-control-allow-methods"]).toBeUndefined();
};

const expectGlobalSecurityHeaders = (headers: Record<string, string>): void => {
  for (const [name, value] of Object.entries(GLOBAL_SECURITY_HEADERS)) {
    expect(headers[name.toLowerCase()]).toBe(value);
  }
};

const getExpectedPageDiscoveryLink = (markdownPath: string): string =>
  `<${markdownPath}>; rel="alternate"; type="text/markdown", <${withDocsBase("/llms.txt")}>; rel="describedby"; type="text/plain", <${withDocsBase("/.well-known/api-catalog")}>; rel="api-catalog"; type="application/linkset+json"`;

const expectNoAxeViolations = async (page: Page): Promise<void> => {
  const results = await new AxeBuilder({ page })
    // Base UI's focus sentinels are intentionally keyboard-focusable and hidden
    // from the accessibility tree; axe otherwise reports the focus-loop
    // implementation itself instead of the interactive surface it protects.
    .exclude("[data-base-ui-focus-guard]")
    .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"])
    .analyze();

  expect(
    results.violations.map(({ id, impact, nodes }) => ({
      id,
      impact,
      targets: nodes.map(({ target }) => target),
    }))
  ).toEqual([]);
};

test("right-clicking the Astilba title reveals the interface showcase", async ({
  page,
}) => {
  await page.addInitScript(() => {
    const query = "(hover: hover) and (pointer: fine)";
    const originalMatchMedia = window.matchMedia.bind(window);
    const finePointer = originalMatchMedia(query);
    let matches = false;

    Object.defineProperty(finePointer, "matches", {
      configurable: true,
      get: () => matches,
    });
    window.matchMedia = (value) =>
      value === query ? finePointer : originalMatchMedia(value);
    window.__setAstilbaFinePointer = (value) => {
      matches = value;
    };
  });
  await page.route("https://ui.astilba.com/", async (route) => {
    await route.fulfill({
      body: "<title>Astilba Interface</title>",
      contentType: "text/html",
      status: 200,
    });
  });
  await page.goto(withDocsBase("/"));

  const title = page.locator("header a.site-title");
  await expect(title).toHaveAttribute("href", withDocsBase("/"));
  await title.click({ button: "right" });
  await expect(page).toHaveURL(withDocsBase("/"));

  await page.evaluate(() => {
    window.__setAstilbaFinePointer(true);
  });
  await title.click({ button: "right" });

  await expect(page).toHaveURL("https://ui.astilba.com/");
});

test("serves the public documentation corpus over MCP", async ({ baseURL }) => {
  if (!baseURL) {
    throw new Error("The MCP test requires Playwright's local Worker URL.");
  }

  const client = new Client({
    name: "astilba-docs-test",
    version: "1.0.0",
  });
  const transport = new StreamableHTTPClientTransport(
    new URL(withDocsBase("/mcp"), baseURL)
  );

  try {
    await client.connect(transport);
    await client.ping();
    const tools = await client.listTools();
    const resources = await client.listResources();
    const search = await client.callTool({
      arguments: { query: "tag invalidation" },
      name: "search_docs",
    });
    const overviewUri = `${docsOrigin}/docs/cache/overview.md`;
    const overview = await client.readResource({ uri: overviewUri });

    expect(tools.tools.map(({ name }) => name)).toEqual([
      "search_docs",
      "read_doc",
    ]);
    expect(
      tools.tools.every(
        ({ annotations }) =>
          annotations?.readOnlyHint === true &&
          annotations.destructiveHint === false &&
          annotations.openWorldHint === false
      )
    ).toBe(true);
    expect(resources.resources).toHaveLength(EXPECTED_CORPUS_PAGES);
    expect(resources.resources.some(({ uri }) => uri === overviewUri)).toBe(
      true
    );
    const searchOutput = search.structuredContent as {
      results: Array<Record<string, unknown>>;
    };
    expect(searchOutput.results[0]).toMatchObject({
      title: "Invalidate cached data",
      uri: `${docsOrigin}/docs/cache/tags-and-invalidation.md`,
    });
    expect(overview.contents[0]).toMatchObject({
      mimeType: "text/markdown",
      uri: overviewUri,
    });
    expect(overview.contents[0]).toHaveProperty(
      "text",
      expect.stringContaining("# Overview")
    );
  } finally {
    await client.close();
  }
});

test("serves the direct sitemap as a static XML asset", async ({ request }) => {
  const sitemapResponse = await request.get("/docs/sitemap.xml", {
    maxRedirects: 0,
  });
  expect(sitemapResponse.status()).toBe(200);
  expect(sitemapResponse.headers()["content-type"]).toContain(
    "application/xml"
  );
  expectGlobalSecurityHeaders(sitemapResponse.headers());
  const sitemap = await sitemapResponse.text();
  expect(sitemap).toContain(
    '<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"'
  );
  expect(sitemap).toContain(`<loc>${docsUrl("/")}</loc>`);
  expect(sitemap).toMatch(
    /<lastmod>\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z<\/lastmod>/
  );

  const sitemapHeadResponse = await request.head("/docs/sitemap.xml", {
    maxRedirects: 0,
  });
  expect(sitemapHeadResponse.status()).toBe(200);
  expect(sitemapHeadResponse.headers()["content-type"]).toContain(
    "application/xml"
  );
  expectGlobalSecurityHeaders(sitemapHeadResponse.headers());
});

test("publishes MCP and RFC 9727 discovery metadata", async ({
  page,
  request,
}) => {
  await page.emulateMedia({ reducedMotion: "no-preference" });

  const apiCatalogResponse = await request.get(
    "/docs/.well-known/api-catalog",
    {
      maxRedirects: 0,
    }
  );
  expect(apiCatalogResponse.status()).toBe(200);
  expect(apiCatalogResponse.headers()["content-type"]).toBe(
    'application/linkset+json; profile="https://www.rfc-editor.org/info/rfc9727"'
  );
  expectSimpleGetCors(apiCatalogResponse.headers());
  expect(apiCatalogResponse.headers()["x-content-type-options"]).toBe(
    "nosniff"
  );
  expectGlobalSecurityHeaders(apiCatalogResponse.headers());
  expect(await apiCatalogResponse.json()).toEqual({
    linkset: [
      {
        anchor: `${docsOrigin}/docs/mcp`,
        "service-desc": [
          {
            href: `${docsOrigin}/docs/mcp/server-card`,
            type: "application/mcp-server-card+json",
          },
        ],
        "service-doc": [
          {
            href: `${docsOrigin}/docs/agents/mcp/`,
            type: "text/html",
          },
        ],
      },
    ],
  });

  const apiCatalogHead = await request.head("/docs/.well-known/api-catalog", {
    maxRedirects: 0,
  });
  expect(apiCatalogHead.status()).toBe(200);
  expect(apiCatalogHead.headers().link).toBe(
    '</docs/.well-known/api-catalog>; rel="api-catalog"; type="application/linkset+json"'
  );

  const mcpCatalogResponse = await request.get(
    "/docs/.well-known/mcp/catalog.json"
  );
  expect(mcpCatalogResponse.status()).toBe(200);
  expect(mcpCatalogResponse.headers()["content-type"]).toContain(
    "application/json"
  );
  expectSimpleGetCors(mcpCatalogResponse.headers());
  expect(await mcpCatalogResponse.json()).toEqual({
    entries: [
      {
        identifier: "urn:air:astilba.com:docs",
        type: "application/mcp-server-card+json",
        url: `${docsOrigin}/docs/mcp/server-card`,
      },
    ],
    specVersion: "draft",
  });

  const serverCardResponse = await request.get("/docs/mcp/server-card", {
    headers: { Accept: "application/mcp-server-card+json" },
    maxRedirects: 0,
  });
  expect(serverCardResponse.status()).toBe(200);
  expect(serverCardResponse.headers()["content-type"]).toBe(
    "application/mcp-server-card+json"
  );
  expectSimpleGetCors(serverCardResponse.headers());
  const serverCard = await serverCardResponse.json();
  expect(serverCard).toMatchObject({
    name: "com.astilba/docs",
    remotes: [
      {
        type: "streamable-http",
        url: `${docsOrigin}/docs/mcp`,
      },
    ],
    version: "0.1.0",
  });
  expect(serverCard).not.toHaveProperty("capabilities");
  const serverCardHead = await request.head("/docs/mcp/server-card", {
    maxRedirects: 0,
  });
  expect(serverCardHead.status()).toBe(200);
  expect(serverCardHead.headers()["content-type"]).toBe(
    "application/mcp-server-card+json"
  );

  const compatibilityCardResponse = await request.get(
    "/docs/.well-known/mcp/server-card.json"
  );
  expect(compatibilityCardResponse.status()).toBe(200);
  expectSimpleGetCors(compatibilityCardResponse.headers());
  expect(await compatibilityCardResponse.json()).toMatchObject({
    capabilities: { resources: {}, tools: {} },
    serverInfo: { name: "com.astilba/docs", version: "0.1.0" },
    transport: {
      endpoint: `${docsOrigin}/docs/mcp`,
      type: "streamable-http",
    },
  });

  const unsupportedMcpMethod = await request.get("/docs/mcp");
  expect(unsupportedMcpMethod.status()).toBe(405);
  expectGlobalSecurityHeaders(unsupportedMcpMethod.headers());

  const initializeMcp = await request.post("/docs/mcp", {
    data: {
      id: 1,
      jsonrpc: "2.0",
      method: "initialize",
      params: {
        capabilities: {},
        clientInfo: { name: "edge-header-test", version: "1.0.0" },
        protocolVersion: "2025-11-25",
      },
    },
    headers: {
      Accept: "application/json, text/event-stream",
      "Content-Type": "application/json",
      "MCP-Protocol-Version": "2025-11-25",
    },
  });
  expect(initializeMcp.status()).toBe(200);
  expectGlobalSecurityHeaders(initializeMcp.headers());

  const usageResponse = await request.get("/docs/agents/mcp/", {
    maxRedirects: 0,
  });
  expect(usageResponse.status()).toBe(200);
  expect(await usageResponse.text()).toContain("MCP Server");
  const usageMarkdownResponse = await request.get("/docs/agents/mcp/", {
    headers: { Accept: "text/markdown" },
  });
  expect(usageMarkdownResponse.headers()["content-location"]).toBe(
    "/docs/agents/mcp.md"
  );
  expect(await usageMarkdownResponse.text()).toContain("# MCP Server");

  const llmsGuideResponse = await request.get("/docs/agents/llms-txt/", {
    headers: { Accept: "text/markdown" },
  });
  expect(llmsGuideResponse.status()).toBe(200);
  expect(llmsGuideResponse.headers()["content-location"]).toBe(
    "/docs/agents/llms-txt.md"
  );
  expect(await llmsGuideResponse.text()).toContain("# LLMs.txt");

  await page.goto("/docs/agents/llms-txt/");
  await expect(page.getByRole("heading", { name: "LLMs.txt" })).toBeVisible();
  const agentSetupInstruction = page
    .locator(".expressive-code .frame")
    .filter({ hasText: AGENT_SETUP_COPY_TEXT });
  await expect(agentSetupInstruction).toHaveCount(1);
  await expect(agentSetupInstruction).not.toHaveClass(/\bhas-title\b/);
  await expectNoAxeViolations(page);

  await page.goto("/docs/agents/mcp/");
  await expect(page.getByRole("heading", { name: "MCP Server" })).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Copy Markdown" })
  ).toBeVisible();

  const compactCodeFrames = page.locator(
    ".expressive-code .frame:not(.has-title)"
  );
  const copyCodeButtons = page.getByRole("button", { name: "Copy code" });

  await expect(compactCodeFrames).toHaveCount(2);
  await expect(copyCodeButtons).toHaveCount(2);

  for (let index = 0; index < 2; index += 1) {
    const frame = compactCodeFrames.nth(index);
    const line = frame.locator(".ec-line");
    const button = frame.getByRole("button", { name: "Copy code" });
    const [frameBox, lineBox, buttonBox] = await Promise.all([
      frame.boundingBox(),
      line.boundingBox(),
      button.boundingBox(),
    ]);

    if (!(frameBox && lineBox && buttonBox)) {
      throw new Error(
        "Compact code text and its copy control must be visible."
      );
    }

    expect(
      Math.abs(
        lineBox.y + lineBox.height / 2 - (buttonBox.y + buttonBox.height / 2)
      )
    ).toBeLessThan(1);
    expect(
      frameBox.x + frameBox.width - (buttonBox.x + buttonBox.width)
    ).toBeCloseTo(4, 1);
    await expect(button).not.toHaveAttribute("title");
  }

  const firstCopyCode = copyCodeButtons.nth(0);
  const firstCopyWrapper = compactCodeFrames.nth(0).locator(".copy");
  const idleCopyTooltip = await firstCopyWrapper.evaluate((element) => {
    const tooltip = getComputedStyle(element, "::before");

    return {
      content: tooltip.content,
      duration: tooltip.transitionDuration,
      fontWeight: tooltip.fontWeight,
      opacity: tooltip.opacity,
      transform: tooltip.transform,
    };
  });

  expect(idleCopyTooltip.content).toBe('"Copy code"');
  expect(idleCopyTooltip.duration).toBe("0.05s, 0.05s");
  expect(idleCopyTooltip.fontWeight).toBe("400");
  expect(idleCopyTooltip.opacity).toBe("0");
  expect(idleCopyTooltip.transform).toContain("0.96");

  await firstCopyCode.hover();
  await expect
    .poll(() =>
      firstCopyWrapper.evaluate(
        (element) => getComputedStyle(element, "::before").opacity
      )
    )
    .toBe("1");
  expect(
    await firstCopyWrapper.evaluate(
      (element) => getComputedStyle(element, "::before").transitionDuration
    )
  ).toBe("0.15s");

  await firstCopyCode.click();
  await expect
    .poll(() => page.evaluate(() => navigator.clipboard.readText()))
    .toBe("https://astilba.com/docs/mcp");
  await expectNoAxeViolations(page);
});

test("serves agent-readable Markdown and keeps copy states independent", async ({
  page,
  request,
}) => {
  const homeMarkdownResponse = await request.get("/docs/", {
    headers: { Accept: "text/markdown" },
  });
  expect(homeMarkdownResponse.ok()).toBe(true);
  expect(homeMarkdownResponse.headers()["content-type"]).toContain(
    "text/markdown"
  );
  expect(homeMarkdownResponse.headers().vary).toContain("Accept");
  const homeMarkdown = await homeMarkdownResponse.text();
  expect(homeMarkdown).toContain("# Overview");
  expect(homeMarkdown).not.toContain("## Sponsors");
  expect(homeMarkdown).not.toContain("## How to read these docs");

  const rootMarkdownResponse = await request.get("/docs/index.md");
  expect(rootMarkdownResponse.status()).toBe(200);
  expect(rootMarkdownResponse.headers()["content-type"]).toBe(
    "text/markdown; charset=utf-8"
  );
  expect(rootMarkdownResponse.headers()["x-content-type-options"]).toBe(
    "nosniff"
  );
  expect(rootMarkdownResponse.headers().etag).toBeTruthy();

  const agentSetupResponse = await request.get("/docs/agent-setup/prompt.md");
  expect(agentSetupResponse.status()).toBe(200);
  expect(agentSetupResponse.headers()["content-type"]).toBe(
    "text/markdown; charset=utf-8"
  );
  expect(agentSetupResponse.headers()["access-control-allow-origin"]).toBe("*");
  expect(agentSetupResponse.headers()["x-content-type-options"]).toBe(
    "nosniff"
  );
  expect(agentSetupResponse.headers()["x-robots-tag"]).toBe("noindex");
  expect(await agentSetupResponse.text()).toContain(
    "# Set up Astilba documentation"
  );

  for (const { markdownPath, pagePath } of [
    { markdownPath: "/docs/index.md", pagePath: "/docs/" },
    { markdownPath: "/docs/env.md", pagePath: "/docs/env/" },
    {
      markdownPath: "/docs/env/overview.md",
      pagePath: "/docs/env/overview/",
    },
    {
      markdownPath: "/docs/env/cloudflare-workers.md",
      pagePath: "/docs/env/cloudflare-workers/",
    },
    { markdownPath: "/docs/cache.md", pagePath: "/docs/cache/" },
    {
      markdownPath: "/docs/cache/overview.md",
      pagePath: "/docs/cache/overview/",
    },
  ]) {
    const expectedLink = getExpectedPageDiscoveryLink(markdownPath);
    const pageResponse = await request.get(pagePath, {
      headers: { Accept: "text/html" },
    });
    expect(pageResponse.status()).toBe(200);
    expect(pageResponse.headers().link).toBe(expectedLink);
    expect(pageResponse.headers().vary).toContain("Accept");
    const pageEtag = pageResponse.headers().etag;
    expect(pageEtag).toBeTruthy();
    if (!pageEtag) {
      throw new Error(`${pagePath} must include an ETag.`);
    }

    const pageHeadResponse = await request.head(pagePath, {
      headers: { Accept: "text/html" },
    });
    expect(pageHeadResponse.status()).toBe(200);
    expect(pageHeadResponse.headers().link).toBe(expectedLink);

    const pageRevalidationResponse = await request.get(pagePath, {
      headers: {
        Accept: "text/html",
        "If-None-Match": pageEtag,
      },
    });
    expect(pageRevalidationResponse.status()).toBe(304);
    expect(pageRevalidationResponse.headers().link).toBe(expectedLink);
  }

  const negotiatedMarkdownResponse = await request.get(
    "/docs/cache/overview/",
    {
      headers: { Accept: "text/markdown" },
    }
  );
  expect(negotiatedMarkdownResponse.ok()).toBe(true);
  expect(negotiatedMarkdownResponse.headers()["content-type"]).toContain(
    "text/markdown"
  );
  expect(negotiatedMarkdownResponse.headers()["content-location"]).toBe(
    "/docs/cache/overview.md"
  );
  expect(negotiatedMarkdownResponse.headers()["content-signal"]).toBe(
    "ai-train=yes, search=yes, ai-input=yes"
  );
  expect(negotiatedMarkdownResponse.headers().link).toContain(
    'rel="describedby"'
  );
  expect(negotiatedMarkdownResponse.headers().vary).toContain("Accept");
  expect(await negotiatedMarkdownResponse.text()).toContain(
    "For React applications, “server-side” means"
  );

  const markdownHeadResponse = await request.head("/docs/cache/overview/", {
    headers: { Accept: "text/markdown" },
  });
  expect(markdownHeadResponse.ok()).toBe(true);
  expect(markdownHeadResponse.headers()["content-type"]).toContain(
    "text/markdown"
  );

  const canonicalRedirect = await request.get("/docs/cache/overview", {
    headers: { Accept: "text/markdown" },
    maxRedirects: 0,
  });
  expect(canonicalRedirect.status()).toBe(307);
  expect(canonicalRedirect.headers().location).toBe("/docs/cache/overview/");

  const productRootRedirect = await request.get("/docs/cache", {
    maxRedirects: 0,
  });
  expect(productRootRedirect.status()).toBe(307);
  expect(productRootRedirect.headers().location).toBe("/docs/cache/");

  const productHomeResponse = await request.get("/docs/cache/", {
    maxRedirects: 0,
  });
  expect(productHomeResponse.status()).toBe(200);
  expect(productHomeResponse.headers()["content-type"]).toContain("text/html");

  const htmlResponse = await request.get("/docs/cache/overview/", {
    headers: { Accept: "text/html, text/markdown;q=0" },
  });
  expect(htmlResponse.headers()["content-type"]).toContain("text/html");
  expect(htmlResponse.headers().vary).toContain("Accept");
  expectGlobalSecurityHeaders(htmlResponse.headers());

  const preferredHtmlResponse = await request.get("/docs/cache/overview/", {
    headers: { Accept: "text/html;q=1, text/markdown;q=0.1" },
  });
  expect(preferredHtmlResponse.headers()["content-type"]).toContain(
    "text/html"
  );

  const markdownResponse = await request.get("/docs/cache/overview.md");
  expect(markdownResponse.ok()).toBe(true);
  expect(markdownResponse.headers()["content-type"]).toBe(
    "text/markdown; charset=utf-8"
  );
  expect(markdownResponse.headers()["x-content-type-options"]).toBe("nosniff");
  expectGlobalSecurityHeaders(markdownResponse.headers());
  const markdownEtag = markdownResponse.headers().etag;
  expect(markdownEtag).toBeTruthy();
  if (!markdownEtag) {
    throw new Error("The direct Markdown response must include an ETag.");
  }
  const markdown = await markdownResponse.text();
  expect(markdown).toContain("# Overview");
  expect(markdown).toContain("For React applications, “server-side” means");

  const markdownRevalidationResponse = await request.get(
    "/docs/cache/overview.md",
    {
      headers: { "If-None-Match": markdownEtag },
    }
  );
  expect(markdownRevalidationResponse.status()).toBe(304);
  expect(markdownRevalidationResponse.headers()["content-type"]).toBe(
    "text/markdown; charset=utf-8"
  );
  expect(markdownRevalidationResponse.headers()["x-content-type-options"]).toBe(
    "nosniff"
  );

  const missingMarkdownResponse = await request.get("/docs/missing/", {
    headers: { Accept: "text/markdown" },
  });
  expect(missingMarkdownResponse.status()).toBe(404);
  expect(missingMarkdownResponse.headers()["content-type"]).toContain(
    "text/html"
  );
  expect(missingMarkdownResponse.headers().link).toBeUndefined();

  const missingNestedPageResponse = await request.get("/docs/cache/missing/", {
    headers: { Accept: "text/html" },
  });
  expect(missingNestedPageResponse.status()).toBe(404);
  expect(missingNestedPageResponse.headers()["content-type"]).toContain(
    "text/html"
  );
  expect(missingNestedPageResponse.headers().link).toBeUndefined();

  const missingPageHeadResponse = await request.head("/docs/cache/missing/", {
    headers: { Accept: "text/html" },
  });
  expect(missingPageHeadResponse.status()).toBe(404);
  expect(missingPageHeadResponse.headers().link).toBeUndefined();

  const missingDirectResponse = await request.get("/docs/missing.md");
  const missingEtag = missingDirectResponse.headers().etag;
  expect(missingDirectResponse.status()).toBe(404);
  expect(missingDirectResponse.headers()["content-type"]).toContain(
    "text/html"
  );
  expect(missingEtag).toBeTruthy();

  const missingNestedResponse = await request.get("/docs/cache/missing.md");
  expect(missingNestedResponse.status()).toBe(404);
  expect(missingNestedResponse.headers()["content-type"]).toContain(
    "text/html"
  );

  const missingRevalidationResponse = await request.get("/docs/missing/", {
    headers: {
      Accept: "text/markdown",
      "If-None-Match": missingEtag,
    },
  });
  expect(missingRevalidationResponse.status()).toBe(304);
  expect(missingRevalidationResponse.headers()["content-type"]).toContain(
    "text/html"
  );
  expect(
    missingRevalidationResponse.headers()["content-location"]
  ).toBeUndefined();
  expect(missingRevalidationResponse.headers().link).toBeUndefined();

  const skillsIndexResponse = await request.get(
    "/docs/.well-known/agent-skills/index.json"
  );
  expect(skillsIndexResponse.ok()).toBe(true);
  expect(skillsIndexResponse.headers()["content-type"]).toContain(
    "application/json"
  );
  expect(skillsIndexResponse.headers()["access-control-allow-origin"]).toBe(
    "*"
  );
  const skillsIndex = await skillsIndexResponse.json();
  const skillEntry = skillsIndex.skills[0];
  expect(skillEntry.name).toBe("astilba-cache-docs");
  expect(
    skillsIndex.skills.some(
      ({ name }: { name: string }) => name === "astilba-env-docs"
    )
  ).toBe(true);

  const skillResponse = await request.get(skillEntry.url);
  expect(skillResponse.ok()).toBe(true);
  expect(skillResponse.headers()["content-type"]).toContain("text/markdown");
  const skillContent = await skillResponse.body();
  expect(skillEntry.digest).toBe(
    `sha256:${createHash("sha256").update(skillContent).digest("hex")}`
  );

  await page.clock.install({ time: new Date("2026-01-01T00:00:00Z") });
  await page.goto("/docs/cache/overview/");
  await page.clock.pauseAt(new Date("2026-01-01T00:05:00Z"));
  const pageTitleBox = await page
    .getByRole("heading", { level: 1, name: "Overview" })
    .boundingBox();
  const pageActions = page.getByRole("group", { name: "Page actions" });
  const pageActionsBox = await pageActions.boundingBox();

  if (!(pageTitleBox && pageActionsBox)) {
    throw new Error(
      "Page title and actions must have measurable layout boxes."
    );
  }

  expect(pageActionsBox.x).toBeCloseTo(pageTitleBox.x, 1);
  const copyMarkdown = page.getByRole("button", {
    name: "Copy Markdown",
  });
  const morePageActions = page.getByRole("button", {
    name: "More page actions",
  });
  const splitControl = page.locator("[data-page-actions-split]");

  await expect(copyMarkdown).toBeEnabled();
  await expect(morePageActions).toHaveAttribute("aria-haspopup", "menu");
  await page.evaluate(() => {
    document.documentElement.dataset.theme = "light";
  });
  await copyMarkdown.evaluate((button) => {
    button.setAttribute("disabled", "");
  });
  await morePageActions.evaluate((button) => {
    button.setAttribute("disabled", "");
  });
  await expect(copyMarkdown).toHaveCSS("color", "rgb(108, 108, 108)");
  await expect(morePageActions).toHaveCSS("color", "rgb(108, 108, 108)");
  await expect(copyMarkdown).toHaveCSS("border-top-width", "0px");
  await expect(morePageActions).toHaveCSS(
    "border-inline-start-color",
    "rgba(0, 0, 0, 0.12)"
  );
  await expect(morePageActions).toHaveCSS("border-inline-start-width", "1px");
  expect(
    await splitControl.evaluate((element) => {
      const styles = getComputedStyle(element, "::after");

      return {
        borderColor: styles.borderColor,
        borderWidth: styles.borderWidth,
      };
    })
  ).toEqual({
    borderColor: "rgba(0, 0, 0, 0.12)",
    borderWidth: "1px",
  });
  await copyMarkdown.evaluate((button) => {
    button.removeAttribute("disabled");
  });
  await morePageActions.evaluate((button) => {
    button.removeAttribute("disabled");
  });
  const copyMarkdownWidth = await copyMarkdown.evaluate(
    (button) => button.getBoundingClientRect().width
  );
  const copyMarkdownIdleLabel = copyMarkdown.locator(
    '[data-copy-label="idle"]'
  );
  const copyMarkdownCopiedLabel = copyMarkdown.locator(
    '[data-copy-label="copied"]'
  );
  await expect(copyMarkdownIdleLabel).toHaveCSS("opacity", "1");
  await expect(copyMarkdownCopiedLabel).toHaveCSS("opacity", "0");
  await page.route(
    "**/cache/overview.md",
    async (route) => {
      await new Promise((resolve) => {
        setTimeout(resolve, 200);
      });
      await route.continue();
    },
    { times: 1 }
  );
  await copyMarkdown.click();
  await expect(copyMarkdown).toHaveAttribute("aria-busy", "true");
  await expect(copyMarkdown).toHaveAttribute("aria-disabled", "true");
  await expect(copyMarkdown).toBeEnabled();
  await expect(
    pageActions.getByRole("status").filter({ hasText: "Page Markdown copied." })
  ).toHaveText("Page Markdown copied.");
  await expect(copyMarkdown).toHaveAttribute("data-copy-state", "copied");
  await expect(copyMarkdownIdleLabel).toHaveCSS("opacity", "0");
  await expect(copyMarkdownCopiedLabel).toHaveCSS("opacity", "1");
  await expect(copyMarkdownCopiedLabel).toHaveText("Copied!");
  await expect
    .poll(() =>
      copyMarkdown.evaluate((button) => button.getBoundingClientRect().width)
    )
    .toBe(copyMarkdownWidth);
  expect(await page.evaluate(() => navigator.clipboard.readText())).toContain(
    "# Overview"
  );

  let repeatedCopyRequestCount = 0;
  const repeatedCopyRoute = async (route: Route): Promise<void> => {
    repeatedCopyRequestCount += 1;
    await route.abort("failed");
  };
  await page.route("**/cache/overview.md", repeatedCopyRoute);

  try {
    await page.clock.runFor(1200);
    await copyMarkdown.click();
    await copyMarkdown.click();
    await copyMarkdown.click();
    await expect(copyMarkdown).toHaveAttribute("data-copy-state", "copied");
    await page.clock.runFor(1999);
    await expect(copyMarkdown).toHaveAttribute("data-copy-state", "copied");
    await page.clock.runFor(1);
    await expect(copyMarkdown).toHaveAttribute("data-copy-state", "idle");
    expect(repeatedCopyRequestCount).toBe(0);
  } finally {
    await page.unroute("**/cache/overview.md", repeatedCopyRoute);
    await page.clock.resume();
  }

  await morePageActions.focus();
  await morePageActions.press("Enter");
  await expect(page.getByRole("menu")).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(page.getByRole("menu")).toBeHidden();
  await expect(morePageActions).toBeFocused();

  await morePageActions.click();
  const pageActionsMenu = page.getByRole("menu");
  const copyMarkdownLink = page.getByRole("menuitem", {
    name: "Copy Markdown link",
  });
  const sidebarSubItem = page.locator("[data-docs-nav-link]").first();

  await expect(pageActionsMenu).toHaveCSS("opacity", "1");
  await expect(pageActionsMenu).toHaveAttribute(
    "data-input-modality",
    "pointer"
  );
  await copyMarkdownLink.hover();
  await expect(copyMarkdownLink).toHaveCSS("outline-style", "none");
  const [menuItemMetrics, sidebarItemMetrics] = await Promise.all(
    [copyMarkdownLink, sidebarSubItem].map((item) =>
      item.evaluate((element) => {
        const styles = getComputedStyle(element);

        return {
          blockSize: styles.blockSize,
          fontSize: styles.fontSize,
          lineHeight: styles.lineHeight,
          paddingBlockEnd: styles.paddingBlockEnd,
          paddingBlockStart: styles.paddingBlockStart,
          paddingInlineEnd: styles.paddingInlineEnd,
          paddingInlineStart: styles.paddingInlineStart,
        };
      })
    )
  );

  expect(menuItemMetrics).toEqual({
    ...sidebarItemMetrics,
    paddingInlineEnd: "12px",
    paddingInlineStart: "12px",
  });
  await expect(copyMarkdownLink.locator("svg").first()).toHaveCSS(
    "inline-size",
    "14px"
  );
  await expect(
    page.getByRole("menuitem", { name: "GitHub" }).locator("svg").last()
  ).toHaveCSS("inline-size", "12px");

  const copyMarkdownLinkWidth = await copyMarkdownLink.evaluate(
    (item) => item.getBoundingClientRect().width
  );
  const copyMarkdownLinkIdleLabel = copyMarkdownLink.locator(
    '[data-copy-label="idle"]'
  );
  const copyMarkdownLinkCopiedLabel = copyMarkdownLink.locator(
    '[data-copy-label="copied"]'
  );
  await expect(copyMarkdownLinkIdleLabel).toHaveCSS("opacity", "1");
  await expect(copyMarkdownLinkCopiedLabel).toHaveCSS("opacity", "0");
  await copyMarkdownLink.click();
  await expect(
    pageActions.getByRole("status").filter({ hasText: "Markdown link copied." })
  ).toHaveText("Markdown link copied.");
  await expect(pageActionsMenu).toBeVisible();
  await expect(copyMarkdownLink).toHaveAttribute("data-copy-state", "copied");
  await expect(copyMarkdownLink.locator('[data-copy-icon="copied"]')).toHaveCSS(
    "opacity",
    "1"
  );
  await expect(copyMarkdownLinkIdleLabel).toHaveCSS("opacity", "0");
  await expect(copyMarkdownLinkCopiedLabel).toHaveCSS("opacity", "1");
  await expect(copyMarkdownLinkCopiedLabel).toHaveText("Copied!");
  await expect
    .poll(() =>
      copyMarkdownLink.evaluate((item) => item.getBoundingClientRect().width)
    )
    .toBe(copyMarkdownLinkWidth);
  await expect(copyMarkdown).toHaveAttribute("data-copy-state", "idle");
  await expect
    .poll(() => page.evaluate(() => navigator.clipboard.readText()))
    .toMatch(/\/cache\/overview\.md$/);

  await page.evaluate(() => {
    const clipboard = navigator.clipboard;
    const writeText = clipboard.writeText.bind(clipboard);
    const state = { settled: 0, started: 0 };

    Object.defineProperty(window, "__linkCopyWrites", {
      configurable: true,
      value: state,
    });
    Object.defineProperty(clipboard, "writeText", {
      configurable: true,
      value: async (value: string) => {
        state.started += 1;
        const delay = state.started === 1 ? 50 : 600;
        await new Promise((resolve) => {
          window.setTimeout(resolve, delay);
        });
        await writeText(value);
        state.settled += 1;
      },
    });
  });

  await copyMarkdownLink.click();
  await copyMarkdownLink.click();
  await page.waitForFunction(
    () =>
      (
        window as typeof window & {
          __linkCopyWrites?: { settled: number };
        }
      ).__linkCopyWrites?.settled === 2
  );
  await expect(copyMarkdownLink).toHaveAttribute("data-copy-state", "copied");
  await page.waitForTimeout(1600);
  await expect(copyMarkdownLink).toHaveAttribute("data-copy-state", "copied");
  await expect(copyMarkdownLink).toHaveAttribute("data-copy-state", "idle", {
    timeout: 1000,
  });
});

test("copies heading links without navigating", async ({ page }) => {
  await page.emulateMedia({ reducedMotion: "no-preference" });
  await page.setViewportSize({ height: 900, width: 1440 });
  await page.goto("/docs/cache/overview/");
  await page.evaluate(() => {
    document.documentElement.dataset.theme = "light";
  });

  const heading = page.getByRole("heading", {
    level: 2,
    name: "Decide whether Cache fits",
  });
  const anchor = page.locator(
    '.sl-anchor-link[href="#decide-whether-cache-fits"]'
  );
  const tooltip = page.getByRole("tooltip");
  const idleLabel = tooltip.locator('[data-tooltip-label="idle"]');
  const activeLabel = tooltip.locator('[data-tooltip-label="active"]');

  await expect(anchor).toBeVisible();
  await expect(anchor).toHaveAttribute(
    "aria-label",
    "Copy link to Decide whether Cache fits"
  );
  await expect(anchor).toHaveCSS("color", "rgb(108, 108, 108)");
  await expect(anchor).toHaveCSS("transition-property", "color");
  await expect(anchor.locator("svg")).toHaveClass(/\blucide-link\b/);
  await expect(anchor.locator("svg")).toHaveCSS("inline-size", "18px");

  const [headingBox, anchorBox] = await Promise.all([
    heading.boundingBox(),
    anchor.boundingBox(),
  ]);

  if (!(headingBox && anchorBox)) {
    throw new Error("The heading and its copy link must be measurable.");
  }

  expect(anchorBox.x + anchorBox.width).toBeLessThan(headingBox.x);

  await anchor.hover();
  await expect(anchor).toHaveCSS("color", "rgb(13, 13, 13)");
  await expect(tooltip).toBeVisible();
  await expect(tooltip).toHaveAttribute("aria-label", "Copy link");
  await expect(tooltip).toHaveAttribute("data-open", "");
  await expect(tooltip).toHaveCSS("font-weight", "500");
  await expect(tooltip).toHaveCSS("transition-duration", "0.15s");
  await expect(tooltip).toHaveCSS("transform", "matrix(1, 0, 0, 1, 0, 0)");
  await expect(idleLabel).toHaveCSS("opacity", "1");
  await expect(activeLabel).toHaveCSS("opacity", "0");

  const idleTooltipWidth = await tooltip.evaluate(
    (element) => element.getBoundingClientRect().width
  );
  const tooltipBox = await tooltip.boundingBox();

  if (!tooltipBox) {
    throw new Error("The heading tooltip must be measurable.");
  }

  expect(
    Math.abs(
      tooltipBox.x + tooltipBox.width / 2 - (anchorBox.x + anchorBox.width / 2)
    )
  ).toBeLessThan(1);

  const pageUrl = page.url();
  await anchor.click();
  await expect(page).toHaveURL(pageUrl);
  await expect(anchor).toHaveAttribute("data-tooltip-state", "active");
  await expect(anchor).toHaveAttribute(
    "aria-label",
    "Copied link to Decide whether Cache fits"
  );
  await expect(tooltip).toHaveAttribute("aria-label", "Copied!");
  await expect(idleLabel).toHaveCSS("opacity", "0");
  await expect(activeLabel).toHaveCSS("opacity", "1");
  await expect
    .poll(() =>
      tooltip.evaluate((element) => element.getBoundingClientRect().width)
    )
    .toBe(idleTooltipWidth);
  await expect
    .poll(() => page.evaluate(() => navigator.clipboard.readText()))
    .toBe(`${pageUrl}#decide-whether-cache-fits`);
  await expect(
    page
      .getByRole("status")
      .filter({ hasText: "Link to Decide whether Cache fits copied." })
  ).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(tooltip).toHaveCount(0);
  await expect(anchor).toHaveAttribute("data-tooltip-state", "idle");
  await page.getByRole("heading", { level: 1, name: "Overview" }).hover();
  await anchor.hover();
  await expect(tooltip).toHaveAttribute("aria-label", "Copy link");
  await page.evaluate(() => {
    window.__tooltipExitDuration = undefined;
    const observer = new MutationObserver(() => {
      const endingTooltip = document.querySelector<HTMLElement>(
        '[role="tooltip"][data-ending-style]'
      );

      if (endingTooltip) {
        window.__tooltipExitDuration =
          getComputedStyle(endingTooltip).transitionDuration;
        observer.disconnect();
      }
    });

    observer.observe(document.body, {
      attributeFilter: ["data-ending-style"],
      attributes: true,
      subtree: true,
    });
  });
  await page.getByRole("heading", { level: 1, name: "Overview" }).hover();
  await expect
    .poll(() => page.evaluate(() => window.__tooltipExitDuration))
    .toBe("0.05s");
  await expect(tooltip).toHaveCount(0);

  await page.evaluate(() => {
    Object.defineProperty(navigator.clipboard, "writeText", {
      configurable: true,
      value: async () => {
        throw new Error("Clipboard write rejected.");
      },
    });
  });
  await anchor.click();
  await expect(page).toHaveURL(`${pageUrl}#decide-whether-cache-fits`);
  await expectNoAxeViolations(page);
});

test("searches the production Pagefind index", async ({ page }) => {
  await page.goto("/docs/cache/overview/");

  const searchButton = page.locator("[data-sidebar-search-trigger]");
  await expect(searchButton).toBeEnabled();
  await searchButton.click();

  const dialog = page.locator("site-search dialog");
  await expect(dialog).toBeVisible();
  await dialog
    .getByRole("textbox", { name: "Search" })
    .fill("Runtime architecture");
  await expect(
    dialog.getByRole("link", { name: /Runtime architecture/i }).first()
  ).toBeVisible();
  await dialog
    .getByRole("textbox", { name: "Search" })
    .fill("Check name inventory drift");
  await expect(
    dialog.getByRole("link", { name: /Check name inventory drift/i }).first()
  ).toBeVisible();
  await dialog.getByRole("textbox", { name: "Search" }).fill("MCP Server");
  await expect(
    dialog.getByRole("link", { name: /MCP Server/i }).first()
  ).toBeVisible();
});

test("enforces CSP without blocking the interactive shell", async ({
  page,
}) => {
  await page.addInitScript(() => {
    window.__cspViolations = [];
    document.addEventListener("securitypolicyviolation", (event) => {
      window.__cspViolations?.push({
        blockedUri: event.blockedURI,
        effectiveDirective: event.effectiveDirective,
      });
    });
  });

  const response = await page.goto("/docs/cache/overview/");
  const policy = response?.headers()["content-security-policy"] ?? "";

  expect(policy).toContain("default-src 'none'");
  expect(policy).toContain("script-src 'self' 'wasm-unsafe-eval'");
  expect(policy).not.toContain("'unsafe-eval'");
  expect(policy).not.toMatch(
    /(?:^|;)\s*script-src(?:-elem)?\b[^;]*'unsafe-inline'/
  );
  expect(policy).toContain("script-src-attr 'none'");
  expect(policy).toContain("frame-ancestors 'none'");

  await page.getByRole("button", { name: "Switch to dark theme" }).click();
  await page.locator("[data-sidebar-search-trigger]").click();
  const search = page.getByRole("dialog", { name: "Search" });
  await search
    .getByRole("textbox", { name: "Search" })
    .fill("Runtime architecture");
  await expect(
    search.getByRole("link", { name: /Runtime architecture/i }).first()
  ).toBeVisible();

  expect(await page.evaluate(() => window.__cspViolations)).toEqual([]);
});

test("registers a read-only WebMCP tool when the API is available", async ({
  page,
}) => {
  const markdownRequests: string[] = [];
  page.on("request", (request) => {
    const pathname = new URL(request.url()).pathname;
    if (pathname.endsWith(".md")) {
      markdownRequests.push(pathname);
    }
  });

  await page.addInitScript(() => {
    window.__webMcpRegistrationMethods = [];
    window.__webMcpTools = [];
    Object.defineProperty(Document.prototype, "modelContext", {
      configurable: true,
      get() {
        return {
          registerTool: async (tool: WebMcpToolProbe) => {
            window.__webMcpRegistrationMethods?.push("document.registerTool");
            window.__webMcpTools?.push(tool);
          },
        };
      },
    });
    Object.defineProperty(window.navigator, "modelContext", {
      configurable: true,
      value: {
        provideContext: ({ tools }: { tools: WebMcpToolProbe[] }) => {
          window.__webMcpRegistrationMethods?.push("navigator.provideContext");
          window.__webMcpTools?.push(...tools);
        },
        registerTool: (tool: WebMcpToolProbe) => {
          window.__webMcpRegistrationMethods?.push("navigator.registerTool");
          window.__webMcpTools?.push(tool);
        },
      },
    });
  });

  await page.goto("/docs/cache/overview/");
  await expect
    .poll(() => page.evaluate(() => window.__webMcpTools?.length ?? 0))
    .toBe(1);
  expect(await page.evaluate(() => window.__webMcpRegistrationMethods)).toEqual(
    ["document.registerTool"]
  );

  const tool = await page.evaluate(() => {
    const registered = window.__webMcpTools?.[0];

    return registered
      ? { annotations: registered.annotations, name: registered.name }
      : undefined;
  });
  expect(tool).toEqual({
    annotations: { readOnlyHint: true, untrustedContentHint: false },
    name: "read_current_page_markdown",
  });

  const markdownChunks = await page.evaluate(async () => {
    const registered = window.__webMcpTools?.[0];
    const chunks: string[] = [];
    let expectedTotal: number | undefined;
    let offset: number | undefined = 0;

    while (offset !== undefined) {
      const chunk: string | undefined = await registered?.execute({
        offset,
      });
      if (!chunk) {
        break;
      }

      chunks.push(chunk);
      const range: RegExpMatchArray | null = chunk.match(
        /^Markdown characters (\d+)–(\d+) of (\d+)\.\n\n/
      );
      if (!range) {
        throw new Error("Markdown chunk metadata is missing.");
      }

      const start: number = Number(range[1]);
      const end: number = Number(range[2]);
      const total: number = Number(range[3]);
      if (
        ![start, end, total].every(Number.isSafeInteger) ||
        start !== offset ||
        end < start ||
        end > total ||
        (expectedTotal !== undefined && total !== expectedTotal)
      ) {
        throw new Error("Markdown chunk metadata is inconsistent.");
      }
      expectedTotal = total;

      const nextOffset: string | undefined = chunk.match(
        /\n\nNext offset: (\d+)\.$/
      )?.[1];
      if (nextOffset !== undefined) {
        const parsedOffset: number = Number(nextOffset);
        if (
          !Number.isSafeInteger(parsedOffset) ||
          parsedOffset !== end ||
          parsedOffset <= start
        ) {
          throw new Error("Markdown chunk traversal did not make progress.");
        }
        offset = parsedOffset;
      } else if (/\n\nEnd of page\.$/.test(chunk) && end === total) {
        offset = undefined;
      } else {
        throw new Error("Markdown chunk continuation is missing.");
      }
    }

    return chunks;
  });
  expect(markdownChunks.length).toBeGreaterThan(0);
  expect(markdownChunks.every((chunk) => chunk.length <= 1500)).toBe(true);
  expect(markdownChunks.join("\n")).toContain("# Overview");
  expect(markdownChunks.join("\n")).toContain(
    "For React applications, “server-side” means"
  );
  expect(markdownChunks.at(-1)).toMatch(/End of page\.$/);
  expect(markdownRequests).toEqual(["/docs/cache/overview.md"]);

  const nextPageChunk = await page.evaluate(async () => {
    const markdownLink = document.querySelector<HTMLLinkElement>(
      'link[rel="alternate"][type="text/markdown"]'
    );
    markdownLink?.setAttribute("href", "/docs/cache/quickstart.md");

    return window.__webMcpTools?.[0]?.execute({ offset: 0 });
  });
  expect(nextPageChunk).toContain("# Source walkthrough");
  expect(markdownRequests).toEqual([
    "/docs/cache/overview.md",
    "/docs/cache/quickstart.md",
  ]);

  const cachedOverviewChunk = await page.evaluate(async () => {
    const markdownLink = document.querySelector<HTMLLinkElement>(
      'link[rel="alternate"][type="text/markdown"]'
    );
    markdownLink?.setAttribute("href", "/docs/cache/overview.md");

    return window.__webMcpTools?.[0]?.execute({ offset: 0 });
  });
  expect(cachedOverviewChunk).toContain("# Overview");
  expect(markdownRequests).toEqual([
    "/docs/cache/overview.md",
    "/docs/cache/quickstart.md",
  ]);
});

test("falls back to legacy navigator.registerTool for WebMCP", async ({
  page,
}) => {
  await page.addInitScript(() => {
    window.__webMcpRegistrationMethods = [];
    window.__webMcpTools = [];
    Object.defineProperty(Document.prototype, "modelContext", {
      configurable: true,
      get: () => undefined,
    });
    Object.defineProperty(window.navigator, "modelContext", {
      configurable: true,
      value: {
        provideContext: ({ tools }: { tools: WebMcpToolProbe[] }) => {
          window.__webMcpRegistrationMethods?.push("navigator.provideContext");
          window.__webMcpTools?.push(...tools);
        },
        registerTool: (tool: WebMcpToolProbe) => {
          window.__webMcpRegistrationMethods?.push("navigator.registerTool");
          window.__webMcpTools?.push(tool);
        },
      },
    });
  });

  await page.goto("/docs/cache/overview/");
  await expect
    .poll(() => page.evaluate(() => window.__webMcpTools?.length ?? 0))
    .toBe(1);

  expect(await page.evaluate(() => window.__webMcpRegistrationMethods)).toEqual(
    ["navigator.registerTool"]
  );
  expect(
    await page.evaluate(() => window.__webMcpTools?.[0]?.annotations)
  ).toEqual({ readOnlyHint: true, untrustedContentHint: false });
});

test("falls back to legacy navigator.provideContext for WebMCP", async ({
  page,
}) => {
  await page.addInitScript(() => {
    window.__webMcpRegistrationMethods = [];
    window.__webMcpTools = [];
    Object.defineProperty(Document.prototype, "modelContext", {
      configurable: true,
      get: () => undefined,
    });
    Object.defineProperty(window.navigator, "modelContext", {
      configurable: true,
      value: {
        provideContext: ({ tools }: { tools: WebMcpToolProbe[] }) => {
          window.__webMcpRegistrationMethods?.push("navigator.provideContext");
          window.__webMcpTools?.push(...tools);
        },
      },
    });
  });

  await page.goto("/docs/cache/overview/");
  await expect
    .poll(() => page.evaluate(() => window.__webMcpTools?.length ?? 0))
    .toBe(1);

  expect(await page.evaluate(() => window.__webMcpRegistrationMethods)).toEqual(
    ["navigator.provideContext"]
  );
  expect(
    await page.evaluate(() => window.__webMcpTools?.[0]?.annotations)
  ).toEqual({ readOnlyHint: true, untrustedContentHint: false });
});

test("keeps desktop sidebar disclosures accessible and persistent", async ({
  page,
}) => {
  await page.goto("/docs/cache/overview/");
  const sidebarRoot = page.locator("[data-docs-sidebar-root]");
  await expect(sidebarRoot).not.toHaveAttribute("data-restoring", "");

  const getStarted = page.getByRole("button", {
    name: "Get started",
    exact: true,
  });
  const controlledPanelId = await getStarted.getAttribute("aria-controls");
  expect(controlledPanelId).toBeTruthy();

  const getStartedPanel = page.locator(`#${controlledPanelId}`);
  await expect(getStartedPanel).toHaveCount(1);
  await expect(getStartedPanel).toHaveClass(/\bastilba-collapsible-panel\b/);
  const reducedMotionDurations = await getStartedPanel.evaluate((element) =>
    getComputedStyle(element)
      .transitionDuration.split(",")
      .map((duration) => Number(duration.trim().replace(/s$/, "")))
  );
  expect(reducedMotionDurations.length).toBeGreaterThan(0);
  expect(reducedMotionDurations.every((duration) => duration <= 0.001)).toBe(
    true
  );

  await getStarted.focus();
  await expect(getStarted).toBeFocused();
  await expect(getStarted).toHaveCSS("outline-style", "solid");
  await getStarted.press("Enter");
  await expect(getStarted).toHaveAttribute("aria-expanded", "false");
  await expect(getStarted).toBeFocused();
  await expect(
    getStartedPanel.getByRole("link", { name: "Overview", exact: true })
  ).toBeHidden();

  await getStarted.press("Space");
  await expect(getStarted).toHaveAttribute("aria-expanded", "true");
  await expect(getStarted).toBeFocused();
  await expect(
    getStartedPanel.getByRole("link", { name: "Overview", exact: true })
  ).toBeVisible();

  await getStarted.click();
  await expect(getStarted).toHaveAttribute("aria-expanded", "false");

  await page
    .locator("#starlight__sidebar")
    .getByRole("link", { name: "Invalidate cached data" })
    .click();
  await expect(page).toHaveURL(/\/cache\/tags-and-invalidation\/$/);
  await expect(
    page.getByRole("button", { name: "Get started", exact: true })
  ).toHaveAttribute("aria-expanded", "false");

  await page.emulateMedia({ reducedMotion: "no-preference" });
  const currentGuidesPanel = page.locator(
    '[data-docs-sidebar-group="docs-group-3"] [data-docs-sidebar-panel]'
  );
  const normalMotionDurations = await currentGuidesPanel.evaluate((element) =>
    getComputedStyle(element)
      .transitionDuration.split(",")
      .map((duration) => Number(duration.trim().replace(/s$/, "")))
  );
  expect(normalMotionDurations.some((duration) => duration >= 0.2)).toBe(true);

  await page.addInitScript(() => {
    const initialPanelTransitions: string[] = [];
    Object.defineProperty(window, "__restoringPanelTransitions", {
      configurable: true,
      value: initialPanelTransitions,
    });
    document.addEventListener(
      "transitionrun",
      (event) => {
        const target = event.target;

        if (
          target instanceof HTMLElement &&
          target.matches("[data-docs-sidebar-panel]")
        ) {
          initialPanelTransitions.push(event.propertyName);
        }
      },
      true
    );
  });
  await page.evaluate(() => {
    const storedState = JSON.parse(
      sessionStorage.getItem("astilba-docs-sidebar-state-v1") ?? "null"
    ) as { hash?: string } | null;

    if (!storedState?.hash) {
      throw new Error("Expected the sidebar to persist its navigation hash.");
    }

    sessionStorage.setItem(
      "astilba-docs-sidebar-state-v1",
      JSON.stringify({
        hash: storedState.hash,
        open: [],
        scroll: 0,
      })
    );
  });
  await page.reload();
  await expect(sidebarRoot).not.toHaveAttribute("data-restoring", "");
  await page.evaluate(
    () =>
      new Promise<void>((resolve) => {
        window.requestAnimationFrame(() => {
          window.requestAnimationFrame(() => resolve());
        });
      })
  );
  await expect(
    page.getByRole("button", { name: "Get started", exact: true })
  ).toHaveAttribute("aria-expanded", "false");
  await expect(
    page.getByRole("button", { name: "Guides", exact: true })
  ).toHaveAttribute("aria-expanded", "true");
  await expect(
    page
      .locator("#starlight__sidebar")
      .getByRole("link", { name: "Invalidate cached data" })
  ).toHaveAttribute("aria-current", "page");
  expect(
    await page.evaluate(
      () =>
        (
          window as typeof window & {
            __restoringPanelTransitions?: string[];
          }
        ).__restoringPanelTransitions ?? []
    )
  ).toEqual([]);
});

test("targets each public product source from product pages", async ({
  page,
}) => {
  await page.goto("/docs/");
  await expect(
    page.getByRole("link", { name: "Astilba on GitHub", exact: true })
  ).toHaveAttribute("href", "https://github.com/astilbahq");

  await page.goto("/docs/cache/overview/");
  await expect(
    page.getByRole("link", {
      name: "Astilba Cache on GitHub",
      exact: true,
    })
  ).toHaveAttribute("href", "https://github.com/astilbahq/docs");
  await expect(
    page.locator('a[href*="github.com/astilbahq/cache"]')
  ).toHaveCount(0);

  await page.goto("/docs/cache/");
  await expect(
    page.getByRole("link", {
      name: "Astilba Cache on GitHub",
      exact: true,
    })
  ).toHaveAttribute("href", "https://github.com/astilbahq/docs");
  await expect(
    page.locator('a[href*="github.com/astilbahq/cache"]')
  ).toHaveCount(0);

  await page.goto("/docs/env/overview/");
  await expect(
    page.getByRole("link", {
      name: "Astilba Env on GitHub",
      exact: true,
    })
  ).toHaveAttribute("href", "https://github.com/astilbahq/env");
});

test("uses product-aware document titles", async ({ page }) => {
  await page.goto("/docs/cache/quickstart/");
  await expect(page).toHaveTitle("Source walkthrough | Astilba Cache");
  await expect(page.locator('meta[property="og:title"]')).toHaveAttribute(
    "content",
    "Source walkthrough | Astilba Cache"
  );

  await page.goto("/docs/");
  await expect(page).toHaveTitle("Astilba");

  await page.goto("/docs/cache/");
  await expect(page).toHaveTitle("Cache | Astilba");
  await expect(page.locator('meta[property="og:title"]')).toHaveAttribute(
    "content",
    "Cache | Astilba"
  );

  await page.goto("/docs/env/migrate-from-next-dynamic-env/");
  await expect(page).toHaveTitle("Migrate from next-dynamic-env | Astilba Env");
  await expectNoAxeViolations(page);

  await page.goto("/docs/env/");
  await expect(page).toHaveTitle("Env | Astilba");
  await expect(page.locator('meta[property="og:title"]')).toHaveAttribute(
    "content",
    "Env | Astilba"
  );

  await page.goto("/docs/agents/mcp/");
  await expect(page).toHaveTitle("MCP Server | Astilba");
});

test("nests level-three headings beneath their page-outline parent", async ({
  page,
}) => {
  await page.goto("/docs/env/declaration-reference/");

  const outline = page.locator("starlight-toc");
  const parent = outline.getByRole("link", {
    name: "Built-in codecs",
    exact: true,
  });
  const child = outline.getByRole("link", {
    name: "Boolean options",
    exact: true,
  });

  await expect(outline).toHaveAttribute("data-min-h", "2");
  await expect(outline).toHaveAttribute("data-max-h", "3");
  await expect(parent).toBeVisible();
  await expect(child).toBeVisible();

  expect(
    await child.evaluate(
      (link) =>
        link.closest("ul")?.parentElement?.querySelector(":scope > a")
          ?.textContent
    )
  ).toBe("Built-in codecs");

  const [parentLabelBounds, childLabelBounds] = await Promise.all(
    [parent, child].map((link) => link.locator("span").boundingBox())
  );

  if (!(parentLabelBounds && childLabelBounds)) {
    throw new Error("Expected both page-outline labels to be visible.");
  }

  expect(childLabelBounds.x).toBeGreaterThan(parentLabelBounds.x);
});

test("omits terminal window headers without removing useful code titles", async ({
  page,
}) => {
  await page.goto("/docs/env/quickstart/");

  const terminalFrames = page.locator(".expressive-code .frame.is-terminal");
  const terminalFrameCount = await terminalFrames.count();
  expect(terminalFrameCount).toBeGreaterThan(0);
  await expect(
    terminalFrames.locator(":scope > figcaption.header")
  ).toHaveCount(0);

  for (let index = 0; index < terminalFrameCount; index += 1) {
    const frame = terminalFrames.nth(index);

    expect(await frame.locator(".ec-line").count()).toBeGreaterThan(0);
    await expect(
      frame.getByRole("button", { name: "Copy code" })
    ).toBeVisible();
  }

  const firstTerminalFrame = terminalFrames.first();
  const firstTerminalLine = firstTerminalFrame.locator(".ec-line");
  const firstTerminalCopy = firstTerminalFrame.getByRole("button", {
    name: "Copy code",
  });
  const [frameBox, lineBox, copyBox] = await Promise.all([
    firstTerminalFrame.boundingBox(),
    firstTerminalLine.boundingBox(),
    firstTerminalCopy.boundingBox(),
  ]);

  if (!(frameBox && lineBox && copyBox)) {
    throw new Error("Terminal code and its copy control must be visible.");
  }

  expect(
    Math.abs(lineBox.y + lineBox.height / 2 - (copyBox.y + copyBox.height / 2))
  ).toBeLessThan(1);
  const rightInset = frameBox.x + frameBox.width - (copyBox.x + copyBox.width);
  expect(Math.abs(rightInset - 4)).toBeLessThan(1);
  await expectNoAxeViolations(page);

  await page.goto("/docs/cache/quickstart/");

  const titledCodeFrame = page
    .locator(".expressive-code .frame.has-title:not(.is-terminal)")
    .first();
  await expect(
    titledCodeFrame.locator(":scope > figcaption.header")
  ).toBeVisible();
  await expect(titledCodeFrame.locator(".title")).toHaveText("cache.ts");
  await expectNoAxeViolations(page);
});

test("presents products and copies the agent setup prompt from the homepage", async ({
  page,
}) => {
  await page.goto("/docs/");

  const pageTitle = page.getByRole("heading", { level: 1, name: "Overview" });
  await expect(pageTitle).toBeVisible();
  await expect(pageTitle).toHaveCSS("font-size", "56px");
  await expect(pageTitle).toHaveCSS("font-weight", "500");
  const sidebar = page.locator("#starlight__sidebar");
  await expect(sidebar).toBeVisible();
  await expect(
    sidebar.getByRole("button", { name: /Choose product/ })
  ).toHaveCount(0);
  await expect(
    sidebar.getByRole("link", {
      name: "Env documentation",
      exact: true,
    })
  ).toHaveAttribute("href", "/docs/env/");
  await expect(
    sidebar.getByRole("link", {
      name: "Cache documentation",
      exact: true,
    })
  ).toHaveAttribute("href", "/docs/cache/");
  await expect(
    sidebar.getByRole("button", {
      name: "AI for Agents",
      exact: true,
    })
  ).toHaveAttribute("aria-expanded", "true");
  await expect(
    sidebar.getByRole("link", { name: "LLMs.txt", exact: true })
  ).toHaveAttribute("href", "/docs/agents/llms-txt/");
  await expect(
    sidebar.getByRole("link", { name: "MCP Server", exact: true })
  ).toHaveAttribute("href", "/docs/agents/mcp/");
  const primaryAction = page.getByRole("link", {
    name: "Read the docs",
    exact: true,
  });
  await expect(primaryAction).toHaveAttribute("href", "/docs/env/");
  await expect(primaryAction).toHaveCSS("height", "40px");
  await expect(primaryAction).toHaveCSS("padding-inline", "14px");
  const primarySelection = await primaryAction.evaluate((element) => {
    const selection = getComputedStyle(element, "::selection");

    return {
      backgroundColor: selection.backgroundColor,
      color: selection.color,
    };
  });
  expect(primarySelection).toEqual({
    backgroundColor: "rgb(255, 255, 255)",
    color: "rgb(13, 13, 13)",
  });
  await primaryAction.hover();
  await expect(primaryAction).toHaveCSS("background-color", "rgb(42, 42, 42)");
  await expect(primaryAction).toHaveCSS("color", "rgb(255, 255, 255)");

  const envProduct = page.getByRole("article").filter({
    has: page.getByRole("heading", { level: 3, name: "Env" }),
  });
  await expect(envProduct).toContainText("Public alpha");
  await expect(envProduct).toContainText("@astilba/env 0.3.0");
  await expect(envProduct.getByRole("link", { name: "Env" })).toHaveAttribute(
    "href",
    "/docs/env/"
  );

  const cacheProduct = page.getByRole("article").filter({
    has: page.getByRole("heading", { level: 3, name: "Cache" }),
  });
  await expect(cacheProduct).toContainText("Preview");
  await expect(cacheProduct).toContainText("There is no supported npm release");
  await expect(
    cacheProduct.getByRole("link", { name: "Cache" })
  ).toHaveAttribute("href", "/docs/cache/");
  await expect(
    page.getByRole("heading", { level: 2, name: "How to read these docs" })
  ).toHaveCount(0);
  await expect(
    page.getByRole("heading", { level: 2, name: "Sponsors" })
  ).toHaveCount(0);
  await expect(
    page.getByRole("link", { name: "Sponsor Astilba on GitHub." })
  ).toHaveCount(0);

  const copy = page.getByRole("button", {
    name: "Copy agent setup",
    exact: true,
  });
  await expect(copy).toHaveCSS("cursor", "copy");
  await expect(copy).toHaveCSS("height", "40px");
  await expect(copy).toHaveCSS("padding-inline", "14px");
  await expect(copy).toHaveCSS("border-color", "rgba(0, 0, 0, 0.12)");
  await expect(copy.locator("[data-agent-setup-content]")).toHaveCSS(
    "gap",
    "12px"
  );
  const chatGptIcon = page.locator('[data-agent-brand-icon="chatgpt"]');
  const claudeIcon = page.locator('[data-agent-brand-icon="claude"]');
  const cursorIcon = page.locator('[data-agent-brand-icon="cursor"]');
  const copyLabel = copy.locator('[data-agent-setup-label="copy"]');
  const copiedLabel = copy.locator('[data-agent-setup-label="copied"]');
  const errorLabel = copy.locator('[data-agent-setup-label="error"]');
  await copy.hover();
  await expect(chatGptIcon).toHaveCSS("animation-delay", "0s");
  await expect(claudeIcon).toHaveCSS("animation-delay", "0.08s");
  await expect(cursorIcon).toHaveCSS("animation-delay", "0.16s");
  await expect(chatGptIcon).toHaveCSS("animation-duration", "0.54s");
  await expect(chatGptIcon).not.toHaveCSS("animation-name", "none");
  await expect(copyLabel).toBeVisible();
  await expect(copiedLabel).toBeHidden();
  const initialButtonWidth = await copy.evaluate(
    (element) => element.getBoundingClientRect().width
  );
  await copy.click();
  await expect(copy).toHaveAttribute("data-copy-state", "copied");
  await expect(copyLabel).toBeHidden();
  await expect(copiedLabel).toBeVisible();
  await expect
    .poll(() =>
      copy.evaluate((element) => element.getBoundingClientRect().width)
    )
    .toBe(initialButtonWidth);
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(
    AGENT_SETUP_COPY_TEXT
  );
  await expect(
    page.getByRole("status").filter({ hasText: "Agent setup prompt copied." })
  ).toHaveText("Agent setup prompt copied.");
  await expect(copy).toHaveAttribute("data-copy-state", "idle", {
    timeout: 3000,
  });
  await page.evaluate(() => {
    Object.defineProperty(navigator.clipboard, "writeText", {
      configurable: true,
      value: async () => {
        throw new Error("Clipboard write rejected.");
      },
    });
  });
  await copy.click();
  await expect(copy).toHaveAttribute("data-copy-state", "error");
  await expect(copyLabel).toBeHidden();
  await expect(copiedLabel).toBeHidden();
  await expect(errorLabel).toBeVisible();
  await expect
    .poll(() =>
      copy.evaluate((element) => element.getBoundingClientRect().width)
    )
    .toBe(initialButtonWidth);
  await expect(
    page
      .getByRole("status")
      .filter({ hasText: "Agent setup prompt could not be copied." })
  ).toHaveText("Agent setup prompt could not be copied.");
  await expectNoAxeViolations(page);
});

test("presents Cache as a distinct product home", async ({ page }) => {
  await page.goto("/docs/cache/");

  const title = page.getByRole("heading", { level: 1, name: "Cache" });
  await expect(title).toBeVisible();
  await expect(title).toHaveCSS("font-size", "56px");
  await expect(title).toHaveCSS("font-weight", "500");
  await expect(title.locator("img")).toHaveCount(0);
  await expect(
    page.getByText(
      "A portable server-side TypeScript cache with explicit invalidation, resilience, and privacy boundaries."
    )
  ).toBeVisible();

  await expect(
    page.getByRole("link", { name: "Read the docs", exact: true })
  ).toHaveAttribute("href", "/docs/cache/overview/");
  await expect(
    page.getByRole("button", { name: "Copy agent setup", exact: true })
  ).toBeVisible();
  await expect(
    page.getByText("There is no supported npm release")
  ).toBeVisible();
  await expect(page.getByRole("group", { name: "Page actions" })).toHaveCount(
    0
  );

  const sidebar = page.locator("#starlight__sidebar");
  await expect(
    sidebar.getByRole("button", {
      name: "Choose product. Current product: Cache",
      exact: true,
    })
  ).toBeVisible();
  const productTrigger = sidebar.getByRole("button", {
    name: "Choose product. Current product: Cache",
    exact: true,
  });
  await expect
    .poll(() =>
      productTrigger.evaluate(
        (element) => !element.closest("astro-island")?.hasAttribute("ssr")
      )
    )
    .toBe(true);
  await productTrigger.focus();
  await productTrigger.press("Enter");
  const productMenu = page.getByRole("menu");
  const alternateProduct = productMenu.getByRole("menuitem", {
    name: /Env/,
  });
  const cacheProduct = productMenu.getByRole("menuitem", {
    name: /Cache/,
  });
  await expect(productMenu).toBeVisible();
  await expect(productMenu).toHaveAttribute("data-input-modality", "keyboard");
  await expect(cacheProduct).toHaveAttribute("aria-current", "true");
  await page.keyboard.press("ArrowDown");
  const keyboardHighlightedProduct = productMenu.locator("[data-highlighted]");
  await expect(keyboardHighlightedProduct).toHaveCount(1);
  await expect(keyboardHighlightedProduct).toHaveCSS("outline-style", "solid");
  await expect(keyboardHighlightedProduct).toHaveCSS("outline-width", "2px");
  await alternateProduct.hover();
  await expect(productMenu).toHaveAttribute("data-input-modality", "pointer");
  await expect(alternateProduct).toHaveCSS("outline-style", "none");
  await page.keyboard.press("Escape");
  await expect(productMenu).toBeHidden();
  await expect(productTrigger).toBeFocused();
  await productTrigger.click();
  await alternateProduct.click();
  await expect(page).toHaveURL(/\/docs\/env\/$/);
  await page.goBack();
  await expect(
    sidebar.getByRole("link", { name: "Overview", exact: true })
  ).toHaveAttribute("href", "/docs/cache/overview/");
  await expect(
    page.getByRole("banner").getByRole("link", { name: "Cache", exact: true })
  ).toHaveAttribute("aria-current", "page");
  await expectNoAxeViolations(page);
});

test("presents Env as a distinct public-alpha product home", async ({
  page,
}) => {
  await page.goto("/docs/env/");

  const title = page.getByRole("heading", { level: 1, name: "Env" });
  await expect(title).toBeVisible();
  await expect(title).toHaveCSS("font-size", "56px");
  await expect(
    page.getByText(
      "Compile one portable configuration declaration into separated, typed browser and server interfaces."
    )
  ).toBeVisible();
  await expect(
    page.getByRole("link", { name: "Read the docs", exact: true })
  ).toHaveAttribute("href", "/docs/env/overview/");
  await expect(
    page.getByText(
      "Released as @astilba/env 0.3.0 for public-alpha evaluation. Expect deliberate breaking changes before a stable release."
    )
  ).toBeVisible();
  await expect(
    page.getByText("Public alpha", { exact: true }).first()
  ).toBeVisible();

  const sidebar = page.locator("#starlight__sidebar");
  await expect(
    sidebar.getByRole("button", {
      name: "Choose product. Current product: Env",
      exact: true,
    })
  ).toBeVisible();
  await expect(
    sidebar.getByRole("link", { name: "Overview", exact: true })
  ).toHaveAttribute("href", "/docs/env/overview/");
  await expect(
    page.getByRole("banner").getByRole("link", { name: "Env", exact: true })
  ).toHaveAttribute("aria-current", "page");
  await expectNoAxeViolations(page);
});

test("presents the Env runtime, platform, and framework split", async ({
  page,
}) => {
  await page.goto("/docs/env/cloudflare-workers/");

  await expect(page).toHaveTitle("Cloudflare Workers | Astilba Env");
  await expect(
    page.getByRole("heading", { level: 1, name: "Cloudflare Workers" })
  ).toBeVisible();
  await expect(
    page.getByRole("heading", {
      level: 2,
      name: "Pass string bindings without coercion",
    })
  ).toBeVisible();
  await expect(
    page.getByRole("link", { name: "Wrangler configuration reference" })
  ).toHaveAttribute(
    "href",
    "https://developers.cloudflare.com/workers/wrangler/configuration/"
  );

  const sidebar = page.locator("#starlight__sidebar");
  await expect(
    sidebar.getByRole("link", { name: "Node.js", exact: true })
  ).toHaveAttribute("href", "/docs/env/nodejs/");
  await expect(
    sidebar.getByRole("link", { name: "Browser", exact: true })
  ).toHaveAttribute("href", "/docs/env/browser-runtime/");
  await expect(
    sidebar.getByRole("link", { name: "Cloudflare Workers", exact: true })
  ).toHaveAttribute("aria-current", "page");
  await expect(
    sidebar.getByRole("link", { name: "Vite", exact: true })
  ).toHaveAttribute("href", "/docs/env/vite/");
  await expect(
    sidebar.getByRole("link", { name: "Next.js", exact: true })
  ).toHaveAttribute("href", "/docs/env/nextjs/");
  await expectNoAxeViolations(page);

  await page.goto("/docs/env/overview/");
  await expect(
    page.getByRole("heading", {
      level: 2,
      name: "Choose the runtime path",
    })
  ).toBeVisible();

  for (const [name, href] of [
    [
      "Node server",
      "https://github.com/astilbahq/env/tree/main/examples/node-service",
    ],
    [
      "Cloudflare Worker",
      "https://github.com/astilbahq/env/tree/main/examples/cloudflare-worker",
    ],
    [
      "Next static shell",
      "https://github.com/astilbahq/env/tree/main/examples/next-static-shell",
    ],
    [
      "Vite browser shell",
      "https://github.com/astilbahq/env/tree/main/examples/vite",
    ],
  ]) {
    await expect(page.getByRole("link", { name, exact: true })).toHaveAttribute(
      "href",
      href
    );
  }
  await expectNoAxeViolations(page);

  await page.goto("/docs/env/nextjs/");
  await expect(
    page.getByRole("heading", {
      level: 3,
      name: "Keep the static shell static",
    })
  ).toBeVisible();
  await expect(
    page.getByRole("heading", {
      level: 2,
      name: "Option 1: load through a same-origin JSON route",
    })
  ).toBeVisible();
  await expect(
    page.getByRole("heading", {
      level: 2,
      name: "Option 2: transport a serialized envelope through Next.js",
    })
  ).toBeVisible();
  await expect(page.locator("main")).toContainText(
    "make only the application-owned /api/env route force-dynamic"
  );
  await expect(page.locator("main")).toContainText(
    "parseBrowserBootstrap validates the serialized JSON without a fetch"
  );
  await expect(page.locator("main")).toContainText(
    "This page does not claim support for cacheComponents: true"
  );
  await expect(
    page.getByRole("heading", {
      level: 2,
      name: "Keep server and browser modules separate",
    })
  ).toBeVisible();
  await expect(
    page.getByRole("heading", {
      level: 3,
      name: "Share one browser readiness promise",
    })
  ).toBeVisible();
  await expect(
    page.getByRole("heading", {
      level: 3,
      name: "Diagnose a browser import leak",
    })
  ).toBeVisible();
  await expect(page.locator("main")).toContainText(
    "Do not re-export both through a public-env barrel"
  );
  await expect(page.locator("main")).toContainText(
    "It does not repair a server module that is reachable from browser code"
  );
  await expect(page.locator("main")).toContainText(
    "Do not alias the runtime to an empty browser module"
  );
  await expectNoAxeViolations(page);
});

test("keeps document pagination balanced and uses the ghost treatment", async ({
  page,
}) => {
  await page.addInitScript(() => {
    localStorage.setItem("starlight-theme", "light");
  });
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto("/docs/cache/overview/");

  const pagination = page.locator(".pagination-links");
  const next = pagination.locator('a[rel="next"]');
  const paginationBox = await pagination.boundingBox();
  const nextBox = await next.boundingBox();

  if (!(paginationBox && nextBox)) {
    throw new Error("Pagination must have measurable layout boxes.");
  }

  expect(nextBox.width).toBeCloseTo((paginationBox.width - 12) / 2, 0);
  expect(nextBox.x + nextBox.width).toBeCloseTo(
    paginationBox.x + paginationBox.width,
    0
  );
  await expect(next).toHaveCSS("border-top-width", "0px");
  await expect(next).toHaveCSS("background-color", "rgba(0, 0, 0, 0)");
  await expect(next).toHaveCSS("gap", "12px");
  await expect(next).toHaveCSS("text-transform", "none");
  await expect(next.locator(":scope > span")).toHaveCSS("text-align", "start");
  await expect(next.locator(".link-title")).toHaveCSS("font-weight", "400");
  await expect(next.locator(".link-title")).toHaveCSS(
    "-webkit-line-clamp",
    "1"
  );
  await expect(next.locator("svg")).toHaveCSS("width", "18px");
  await expect(next.locator("svg")).toHaveCSS("height", "10px");
  const nextArrowBox = await next.locator("svg").boundingBox();
  const nextTitleBox = await next.locator(".link-title").boundingBox();

  if (!(nextArrowBox && nextTitleBox)) {
    throw new Error("Pagination arrow and title must be measurable.");
  }

  expect(nextArrowBox.y + nextArrowBox.height / 2).toBeCloseTo(
    nextTitleBox.y + nextTitleBox.height / 2,
    0
  );
  await next.hover();
  await expect(next).toHaveCSS("background-color", "rgba(0, 0, 0, 0.02)");

  await page.goto("/docs/cache/api-status/");
  const previous = page.locator('.pagination-links a[rel="prev"]');
  const previousPaginationBox = await page
    .locator(".pagination-links")
    .boundingBox();
  const previousBox = await previous.boundingBox();

  if (!(previousPaginationBox && previousBox)) {
    throw new Error("Previous pagination must have a measurable layout box.");
  }

  expect(previousBox.width).toBeCloseTo(
    (previousPaginationBox.width - 12) / 2,
    0
  );
  expect(previousBox.x).toBeCloseTo(previousPaginationBox.x, 0);
  await expect(previous.locator(":scope > span")).toHaveCSS(
    "text-align",
    "end"
  );

  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/docs/cache/overview/");
  const mobilePaginationBox = await page
    .locator(".pagination-links")
    .boundingBox();
  const mobileNextBox = await page
    .locator('.pagination-links a[rel="next"]')
    .boundingBox();

  if (!(mobilePaginationBox && mobileNextBox)) {
    throw new Error("Mobile pagination must have measurable layout boxes.");
  }

  expect(mobileNextBox.width).toBeCloseTo(mobilePaginationBox.width, 0);

  await page.goto("/docs/cache/quickstart/");
  const mobilePreviousBox = await page
    .locator('.pagination-links a[rel="prev"]')
    .boundingBox();
  const mobileFollowingBox = await page
    .locator('.pagination-links a[rel="next"]')
    .boundingBox();

  if (!(mobilePreviousBox && mobileFollowingBox)) {
    throw new Error("Mobile pagination links must have measurable boxes.");
  }

  expect(mobilePreviousBox.y).toBeLessThan(mobileFollowingBox.y);
});

test("keeps sidebar controls in place while only navigation scrolls", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1280, height: 480 });
  await page.goto("/docs/cache/overview/");

  const sidebar = page.locator("#starlight__sidebar");
  const searchTrigger = page.locator("[data-sidebar-search-trigger]");
  const context = page.getByRole("group", {
    name: "Documentation context",
  });
  const navigation = page.locator("[data-docs-sidebar-scroll]");

  await expect(searchTrigger).toBeVisible();
  await expect(searchTrigger).toHaveCSS("position", "static");
  await expect(sidebar).toHaveCSS("overflow-y", "hidden");
  await expect(navigation).toHaveCSS("overflow-y", "scroll");
  await expect(navigation).toHaveCSS("mask-image", /linear-gradient/);
  await expect
    .poll(() =>
      navigation.evaluate(
        (element) => element.scrollHeight > element.clientHeight
      )
    )
    .toBe(true);
  await expect(navigation).toHaveAttribute("data-overflow-y-end", "");
  await expect(navigation).not.toHaveAttribute("data-overflow-y-start");

  const scrollArea = page.locator("[data-docs-sidebar-scroll-root]");
  const scrollThumb = scrollArea.locator("[data-docs-sidebar-scroll-thumb]");
  const scrollBar = scrollArea.locator("[data-docs-sidebar-scrollbar]");
  await expect(scrollThumb).toHaveCount(1);
  await expect(scrollBar).toHaveCSS("opacity", "0");
  await expect(scrollBar).toHaveCSS("pointer-events", "none");
  await navigation.hover();
  await expect(scrollBar).toHaveCSS("opacity", "1");
  await expect(scrollBar).toHaveCSS("pointer-events", "auto");
  await page.locator("main").hover();
  await expect(scrollBar).toHaveCSS("opacity", "0");
  await expect(scrollBar).toHaveCSS("pointer-events", "none");
  await page.emulateMedia({ forcedColors: "active" });
  await expect(navigation).toHaveCSS("mask-image", "none");
  await expect(scrollBar).toHaveCSS("opacity", "1");
  await expect(scrollBar).toHaveCSS("pointer-events", "auto");
  await expect(scrollThumb).toHaveCSS("forced-color-adjust", "none");
  await expect(scrollThumb).not.toHaveCSS(
    "background-color",
    "rgba(0, 0, 0, 0)"
  );
  await page.emulateMedia({ forcedColors: "none" });
  await expect(navigation).toHaveCSS("mask-image", /linear-gradient/);
  await expect(scrollBar).toHaveCSS("opacity", "0");
  await expect(scrollBar).toHaveCSS("pointer-events", "none");

  await navigation.evaluate((element) => {
    element.dispatchEvent(
      new WheelEvent("wheel", { bubbles: true, deltaY: 48 })
    );
    element.scrollTop += 48;
  });
  await expect(scrollBar).toHaveAttribute("data-scrolling", "");
  await expect(scrollBar).toHaveCSS("opacity", "1");
  await expect(scrollBar).toHaveCSS("pointer-events", "auto");
  await expect(scrollBar).not.toHaveAttribute("data-scrolling", "", {
    timeout: 1500,
  });
  await expect(scrollBar).toHaveCSS("opacity", "0");
  await expect(scrollBar).toHaveCSS("pointer-events", "none");

  const searchBefore = await searchTrigger.boundingBox();
  const contextBefore = await context.boundingBox();
  await navigation.evaluate((element) => {
    element.scrollTop = 120;
  });
  await expect
    .poll(() => navigation.evaluate((element) => element.scrollTop))
    .toBeGreaterThan(0);
  await expect(navigation).toHaveAttribute("data-overflow-y-start", "");
  const searchAfter = await searchTrigger.boundingBox();
  const contextAfter = await context.boundingBox();

  if (!(searchBefore && contextBefore && searchAfter && contextAfter)) {
    throw new Error("Sidebar controls must have measurable layout boxes.");
  }

  expect(searchAfter.y).toBeCloseTo(searchBefore.y, 1);
  expect(contextAfter.y).toBeCloseTo(contextBefore.y, 1);

  await navigation.evaluate((element) => {
    element.scrollTop = element.scrollHeight;
  });
  await expect(navigation).not.toHaveAttribute("data-overflow-y-end");
  await expect(navigation).toHaveAttribute("data-overflow-y-start", "");

  await navigation.focus();
  await expect(navigation).toHaveCSS("outline-style", "none");
  await expect(scrollArea).toHaveCSS("outline-style", "solid");
  await expect(scrollBar).toHaveCSS("opacity", "1");
  await expect(scrollBar).toHaveCSS("pointer-events", "auto");
  await page.keyboard.press("Tab");
  await expect(
    navigation.getByRole("button", { name: "Get started", exact: true })
  ).toBeFocused();
  await expect(scrollBar).toHaveCSS("opacity", "1");

  await searchTrigger.click();
  await expect(page.getByRole("dialog", { name: "Search" })).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(searchTrigger).toBeFocused();
});

test("keeps native header search available without a sidebar", async ({
  page,
}) => {
  const response = await page.goto("/docs/missing/");
  expect(response?.status()).toBe(404);

  const searchTrigger = page.locator("site-search > button[data-open-modal]");
  await expect(searchTrigger).toBeVisible();
  await searchTrigger.click();
  await expect(page.getByRole("dialog", { name: "Search" })).toBeVisible();
});

test("enables the shared mobile header search proxy", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/docs/cache/quickstart/");

  const searchTrigger = page.locator("[data-mobile-search-trigger]");
  await expect(searchTrigger).toBeEnabled();
  await expect(searchTrigger).not.toHaveAttribute("data-disabled");
  await expect(searchTrigger).toHaveCSS("opacity", "1");
  await searchTrigger.click();
  await expect(page.getByRole("dialog", { name: "Search" })).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(searchTrigger).toBeFocused();
});

test("switches themes and opens mobile navigation", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/docs/cache/quickstart/");

  const root = page.locator("html");
  const initialTheme = await root.getAttribute("data-theme");
  const nextTheme = initialTheme === "light" ? "dark" : "light";
  const themeToggle = page.locator("[data-theme-toggle-button]");
  await themeToggle.click();
  await expect(root).toHaveAttribute("data-theme", nextTheme);
  await expect
    .poll(() => page.evaluate(() => localStorage.getItem("starlight-theme")))
    .toBe(nextTheme);
  await page.reload();
  await expect(root).toHaveAttribute("data-theme", nextTheme);

  const menuButton = page.locator('button[aria-controls="starlight__sidebar"]');
  const menuBox = await menuButton.boundingBox();
  expect(menuBox?.x ?? 0).toBeGreaterThan(300);
  await menuButton.click();
  await expect(menuButton).toHaveAttribute("aria-expanded", "true");
  await expect(page.locator("#starlight__sidebar")).toBeVisible();
  await expect(
    page.getByRole("link", { name: "Invalidate cached data" })
  ).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(menuButton).toHaveAttribute("aria-expanded", "false");
  await expect(menuButton).toBeFocused();
});

test("has no automatically detectable accessibility violations", async ({
  page,
}) => {
  await page.addInitScript(() => {
    localStorage.setItem("starlight-theme", "light");
  });
  await page.goto("/docs/cache/overview/");
  await expect(page.getByRole("heading", { name: "Overview" })).toBeVisible();
  await expectNoAxeViolations(page);

  await page.getByRole("button", { name: "Switch to dark theme" }).click();
  await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
  await expect(page.locator("body")).toHaveCSS(
    "background-color",
    "rgb(18, 18, 18)"
  );
  await expectNoAxeViolations(page);

  await page.getByRole("button", { name: "More page actions" }).click();
  const menu = page.getByRole("menu");
  await expect(menu).toBeVisible();
  await expect(menu).toHaveCSS("opacity", "1");
  await expectNoAxeViolations(page);
  await page.keyboard.press("Escape");

  const searchButton = page.locator("[data-sidebar-search-trigger]");
  await searchButton.click();
  const searchDialog = page.getByRole("dialog", { name: "Search" });
  await expect(searchDialog).toBeVisible();
  await expect(searchDialog).toHaveCSS("opacity", "1");
  await expectNoAxeViolations(page);
  await page.keyboard.press("Escape");

  await page.setViewportSize({ width: 390, height: 844 });
  await page.reload();
  const menuButton = page.locator('button[aria-controls="starlight__sidebar"]');
  await menuButton.click();
  await expect(menuButton).toHaveAttribute("aria-expanded", "true");
  await expectNoAxeViolations(page);
});
