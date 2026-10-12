import { createHash } from "node:crypto";
import { readFile, readdir } from "node:fs/promises";
import { join, resolve } from "node:path";

import addFormats from "ajv-formats";
import Ajv2020 from "ajv/dist/2020.js";

import {
  API_CATALOG_PATH,
  API_CATALOG_LINK_VALUE,
  createApiCatalog,
  createMcpCatalog,
  createMcpCompatibilityCard,
  createMcpServerCard,
  MCP_CATALOG_PATH,
  MCP_COMPATIBILITY_CARD_PATH,
  MCP_SERVER_CARD_PATH,
} from "../src/docs/agent-discovery.ts";
import { parseDocsCorpus } from "../src/docs/mcp-corpus.ts";
import {
  CONTENT_SECURITY_POLICY_ASSET_PATH,
  GLOBAL_SECURITY_HEADERS,
} from "../src/docs/security.ts";
import { createDocsSitemapLastModified } from "../src/docs/sitemap.ts";
import { DOCS_BASE_PATH, docsUrl, withDocsBase } from "../src/docs/urls.ts";
import {
  createContentSecurityPolicy,
  getInlineScriptHashes,
} from "./security-headers.mjs";

const siteValue = process.env.ASTILBA_DOCS_SITE;

if (!siteValue) {
  throw new Error(
    "[agent-artifacts] ASTILBA_DOCS_SITE is required to verify canonical URLs."
  );
}

const site = new URL(siteValue);
const dist = resolve(process.cwd(), "dist");
const packageJson = JSON.parse(
  await readFile(resolve(process.cwd(), "package.json"), "utf8")
);
const envVersion = packageJson.dependencies["@astilba/env"];
const toArtifactPath = (path) => path.replace(/^\/+/, "");
const docsArtifact = (path) => toArtifactPath(withDocsBase(path));

const collectFiles = async (directory) => {
  const entries = await readdir(directory, { withFileTypes: true });
  const files = await Promise.all(
    entries.map(async (entry) => {
      const path = join(directory, entry.name);
      return entry.isDirectory() ? collectFiles(path) : path;
    })
  );

  return files.flat();
};

const readArtifact = async (relativePath) => {
  try {
    return await readFile(resolve(dist, relativePath), "utf8");
  } catch (error) {
    throw new Error(
      `[agent-artifacts] Missing or unreadable dist/${relativePath}.`,
      { cause: error }
    );
  }
};

const assertIncludes = (artifact, content, expected) => {
  if (!content.includes(expected)) {
    throw new Error(
      `[agent-artifacts] dist/${artifact} does not include ${JSON.stringify(expected)}.`
    );
  }
};

const assertExact = (artifact, label, actual, expected) => {
  if (JSON.stringify(actual) !== JSON.stringify(expected)) {
    throw new Error(
      `[agent-artifacts] dist/${artifact} has unexpected ${label}: expected ${JSON.stringify(expected)}, found ${JSON.stringify(actual)}.`
    );
  }
};

const compareStrings = (left, right) =>
  left < right ? -1 : left > right ? 1 : 0;

const parseHeaderRules = (content) =>
  content
    .trim()
    .split(/\n\s*\n/)
    .map((block) => {
      const [pattern, ...lines] = block.split("\n");
      const headers = new Map();

      for (const line of lines) {
        const separator = line.indexOf(":");

        if (!/^\s+/.test(line) || separator === -1) {
          throw new Error(
            `[agent-artifacts] dist/_headers contains a malformed header line: ${JSON.stringify(line)}.`
          );
        }

        const name = line.slice(0, separator).trim().toLowerCase();
        const value = line.slice(separator + 1).trim();
        const values = headers.get(name) ?? [];
        values.push(value);
        headers.set(name, values);
      }

      return { headers, pattern };
    });

const assertHeaderValues = (rules, pattern, name, expected) => {
  const matches = rules.filter((rule) => rule.pattern === pattern);

  if (matches.length !== 1) {
    throw new Error(
      `[agent-artifacts] dist/_headers must contain exactly one ${JSON.stringify(pattern)} rule, found ${matches.length}.`
    );
  }

  assertExact(
    "_headers",
    `${pattern} ${name} values`,
    matches[0].headers.get(name.toLowerCase()) ?? [],
    expected
  );
};

const getLinkAttribute = (tag, name) =>
  tag.match(new RegExp(`\\b${name}="([^"]+)"`))?.[1];

const getMarkdownLinks = (html) =>
  (html.match(/<link\b[^>]*>/g) ?? []).filter(
    (tag) =>
      getLinkAttribute(tag, "rel") === "alternate" &&
      getLinkAttribute(tag, "type") === "text/markdown"
  );

const assertLink = (html, rel, href) => {
  const linkTags = html.match(/<link\b[^>]*>/g) ?? [];
  const found = linkTags.some(
    (tag) =>
      getLinkAttribute(tag, "rel") === rel &&
      getLinkAttribute(tag, "href") === href
  );

  if (!found) {
    throw new Error(
      `[agent-artifacts] HTML is missing a rel=${JSON.stringify(rel)} link to ${href}.`
    );
  }
};

const linkHeaderHasRelation = (value, relation) => {
  const relationPattern = /(?:^|;)\s*rel\s*=\s*(?:"([^"]*)"|([^;,\s]+))/gi;

  for (const match of value.matchAll(relationPattern)) {
    const relations = (match[1] ?? match[2] ?? "")
      .trim()
      .split(/\s+/)
      .map((item) => item.toLowerCase());

    if (relations.includes(relation.toLowerCase())) {
      return true;
    }
  }

  return false;
};

const getFrontmatterString = (
  markdown,
  field,
  artifact,
  { required = false } = {}
) => {
  const frontmatter = markdown.match(
    /^---\r?\n([\s\S]*?)\r?\n---(?:\r?\n|$)/
  )?.[1];

  if (!frontmatter) {
    throw new Error(
      `[agent-artifacts] dist/${artifact} must begin with generated frontmatter.`
    );
  }

  const matches = [
    ...frontmatter.matchAll(
      new RegExp(`^${field}:\\s*("(?:[^"\\\\]|\\\\.)*")\\s*$`, "gm")
    ),
  ];

  if (matches.length === 0 && !required) {
    return undefined;
  }

  if (matches.length !== 1) {
    throw new Error(
      `[agent-artifacts] dist/${artifact} must define ${field} ${required ? "exactly once" : "at most once"} as a JSON string.`
    );
  }

  return JSON.parse(matches[0][1]);
};

const requiredArtifacts = [
  docsArtifact("/.well-known/api-catalog"),
  docsArtifact("/.well-known/agent-skills/astilba-cache-docs/SKILL.md"),
  docsArtifact("/.well-known/agent-skills/astilba-env-docs/SKILL.md"),
  docsArtifact("/.well-known/agent-skills/index.json"),
  docsArtifact("/.well-known/mcp/catalog.json"),
  docsArtifact("/.well-known/mcp/server-card.json"),
  "_headers",
  docsArtifact("/agent-setup/prompt.md"),
  CONTENT_SECURITY_POLICY_ASSET_PATH.slice(1),
  docsArtifact("/_llms-txt/astilba-cache.txt"),
  docsArtifact("/_llms-txt/astilba-env.txt"),
  docsArtifact("/_mcp/docs.json"),
  docsArtifact("/agents/llms-txt.md"),
  docsArtifact("/agents/llms-txt/index.html"),
  docsArtifact("/agents/mcp.md"),
  docsArtifact("/agents/mcp/index.html"),
  docsArtifact("/cache.md"),
  docsArtifact("/cache/index.html"),
  docsArtifact("/cache/overview.md"),
  docsArtifact("/cache/overview/index.html"),
  docsArtifact("/env.md"),
  docsArtifact("/env/index.html"),
  docsArtifact("/env/overview.md"),
  docsArtifact("/env/overview/index.html"),
  docsArtifact("/index.md"),
  docsArtifact("/index.html"),
  docsArtifact("/llms-full.txt"),
  docsArtifact("/llms-small.txt"),
  docsArtifact("/llms.txt"),
  docsArtifact("/mcp/server-card"),
  docsArtifact("/pagefind/pagefind.js"),
  docsArtifact("/robots.txt"),
  docsArtifact("/sitemap-0.xml"),
  docsArtifact("/sitemap-index.xml"),
  docsArtifact("/sitemap.xml"),
];

const artifacts = new Map();

for (const artifact of requiredArtifacts) {
  artifacts.set(artifact, await readArtifact(artifact));
}

for (const [path, expected] of [
  [API_CATALOG_PATH, createApiCatalog()],
  [MCP_CATALOG_PATH, createMcpCatalog()],
  [MCP_COMPATIBILITY_CARD_PATH, createMcpCompatibilityCard()],
  [MCP_SERVER_CARD_PATH, createMcpServerCard()],
]) {
  const artifact = path.replace(/^\//, "");
  assertExact(
    artifact,
    "generated discovery document",
    JSON.parse(artifacts.get(artifact)),
    expected
  );
}

const serverCardSchemaSource = await readFile(
  resolve(
    process.cwd(),
    "vendor/modelcontextprotocol/server-card/3b2d974/schema.json"
  ),
  "utf8"
);
const serverCardSchemaDigest = createHash("sha256")
  .update(serverCardSchemaSource)
  .digest("hex");

if (
  serverCardSchemaDigest !==
  "2c772b51edb367f154771d84ddbae87ddba00a624422c8e46f218a9ac03bf042"
) {
  throw new Error(
    "[agent-artifacts] The vendored experimental Server Card schema differs from the reviewed 3b2d974 snapshot."
  );
}

const serverCardSchemaBundle = JSON.parse(serverCardSchemaSource);
const ajv = new Ajv2020({ allErrors: true, strict: true });
addFormats(ajv);
const validateServerCard = ajv.compile({
  $defs: serverCardSchemaBundle.$defs,
  $ref: "#/$defs/ServerCard",
  $schema: serverCardSchemaBundle.$schema,
});
const serverCard = JSON.parse(
  artifacts.get(MCP_SERVER_CARD_PATH.replace(/^\//, ""))
);

if (!validateServerCard(serverCard)) {
  throw new Error(
    `[agent-artifacts] dist${MCP_SERVER_CARD_PATH} does not match the pinned experimental Server Card schema: ${ajv.errorsText(validateServerCard.errors)}.`
  );
}

const contentSignal = "ai-train=yes, search=yes, ai-input=yes";
const staticHeaders = artifacts.get("_headers");
const headerRules = parseHeaderRules(staticHeaders);
const htmlDocuments = await Promise.all(
  (await collectFiles(dist))
    .filter((file) => file.endsWith(".html"))
    .map((file) => readFile(file, "utf8"))
);
const expectedContentSecurityPolicy = createContentSecurityPolicy(
  getInlineScriptHashes(htmlDocuments)
);
const contentSecurityPolicyArtifact =
  CONTENT_SECURITY_POLICY_ASSET_PATH.slice(1);
const markdownHeaderPatterns = headerRules
  .filter((rule) =>
    (rule.headers.get("content-type") ?? []).some((value) =>
      value.toLowerCase().startsWith("text/markdown")
    )
  )
  .map((rule) => rule.pattern)
  .sort();

assertExact(
  "_headers",
  "static Markdown response rule patterns",
  markdownHeaderPatterns,
  []
);

assertHeaderValues(headerRules, `${DOCS_BASE_PATH}/*`, "Content-Signal", [
  contentSignal,
]);

for (const [name, value] of Object.entries(GLOBAL_SECURITY_HEADERS)) {
  assertHeaderValues(headerRules, `${DOCS_BASE_PATH}/*`, name, [value]);
}

assertHeaderValues(
  headerRules,
  `${DOCS_BASE_PATH}/*`,
  "Content-Security-Policy",
  [expectedContentSecurityPolicy]
);
assertExact(
  contentSecurityPolicyArtifact,
  "generated Worker Content-Security-Policy",
  artifacts.get(contentSecurityPolicyArtifact),
  `${expectedContentSecurityPolicy}\n`
);
assertHeaderValues(headerRules, withDocsBase("/_astro/*"), "Cache-Control", [
  "public, max-age=31536000, immutable",
]);
assertHeaderValues(
  headerRules,
  withDocsBase("/agent-setup/prompt.md"),
  "Access-Control-Allow-Origin",
  ["*"]
);
assertHeaderValues(
  headerRules,
  withDocsBase("/agent-setup/prompt.md"),
  "Cache-Control",
  ["public, max-age=3600"]
);
assertHeaderValues(
  headerRules,
  withDocsBase("/agent-setup/prompt.md"),
  "X-Content-Type-Options",
  ["nosniff"]
);
assertHeaderValues(
  headerRules,
  withDocsBase("/agent-setup/prompt.md"),
  "X-Robots-Tag",
  ["noindex"]
);
assertHeaderValues(
  headerRules,
  withDocsBase("/.well-known/agent-skills/*"),
  "Access-Control-Allow-Origin",
  ["*"]
);
assertHeaderValues(
  headerRules,
  withDocsBase("/.well-known/agent-skills/*"),
  "Cache-Control",
  ["public, max-age=3600"]
);
assertHeaderValues(
  headerRules,
  withDocsBase("/.well-known/agent-skills/index.json"),
  "Content-Type",
  ["application/json; charset=utf-8"]
);
assertHeaderValues(
  headerRules,
  API_CATALOG_PATH,
  "Access-Control-Allow-Origin",
  ["*"]
);
assertHeaderValues(headerRules, API_CATALOG_PATH, "Content-Type", [
  'application/linkset+json; profile="https://www.rfc-editor.org/info/rfc9727"',
]);
assertHeaderValues(headerRules, API_CATALOG_PATH, "Link", [
  API_CATALOG_LINK_VALUE,
]);
assertHeaderValues(
  headerRules,
  withDocsBase("/.well-known/mcp/*"),
  "Access-Control-Allow-Origin",
  ["*"]
);
assertHeaderValues(
  headerRules,
  withDocsBase("/.well-known/mcp/*"),
  "Cache-Control",
  ["public, max-age=3600"]
);
assertHeaderValues(headerRules, MCP_CATALOG_PATH, "Content-Type", [
  "application/json; charset=utf-8",
]);
assertHeaderValues(headerRules, MCP_COMPATIBILITY_CARD_PATH, "Content-Type", [
  "application/json; charset=utf-8",
]);
assertHeaderValues(
  headerRules,
  MCP_SERVER_CARD_PATH,
  "Access-Control-Allow-Origin",
  ["*"]
);
assertHeaderValues(headerRules, MCP_SERVER_CARD_PATH, "Content-Type", [
  "application/mcp-server-card+json",
]);
for (const discoveryPattern of [
  API_CATALOG_PATH,
  withDocsBase("/.well-known/mcp/*"),
  MCP_SERVER_CARD_PATH,
]) {
  assertHeaderValues(
    headerRules,
    discoveryPattern,
    "Access-Control-Allow-Headers",
    []
  );
  assertHeaderValues(
    headerRules,
    discoveryPattern,
    "Access-Control-Allow-Methods",
    []
  );
  assertHeaderValues(headerRules, discoveryPattern, "Cache-Control", [
    "public, max-age=3600",
  ]);
  assertHeaderValues(headerRules, discoveryPattern, "X-Content-Type-Options", [
    "nosniff",
  ]);
}
assertHeaderValues(
  headerRules,
  withDocsBase("/_mcp/docs.json"),
  "Cache-Control",
  ["public, max-age=3600"]
);
assertHeaderValues(
  headerRules,
  withDocsBase("/_mcp/docs.json"),
  "Content-Type",
  ["application/json; charset=utf-8"]
);
assertHeaderValues(
  headerRules,
  withDocsBase("/_mcp/docs.json"),
  "X-Content-Type-Options",
  ["nosniff"]
);

const contentSignals = headerRules.flatMap(
  (rule) => rule.headers.get("content-signal") ?? []
);
assertExact("_headers", "Content-Signal values", contentSignals, [
  contentSignal,
]);

const cacheSkillArtifact = docsArtifact(
  "/.well-known/agent-skills/astilba-cache-docs/SKILL.md"
);
const envSkillArtifact = docsArtifact(
  "/.well-known/agent-skills/astilba-env-docs/SKILL.md"
);
const cacheSkill = artifacts.get(cacheSkillArtifact);
const envSkill = artifacts.get(envSkillArtifact);
const cacheSkillDigest = `sha256:${createHash("sha256")
  .update(cacheSkill)
  .digest("hex")}`;
const envSkillDigest = `sha256:${createHash("sha256")
  .update(envSkill)
  .digest("hex")}`;
const skillsIndex = JSON.parse(
  artifacts.get(docsArtifact("/.well-known/agent-skills/index.json"))
);
assertExact(
  ".well-known/agent-skills/index.json",
  "discovery index",
  skillsIndex,
  {
    $schema: "https://schemas.agentskills.io/discovery/0.2.0/schema.json",
    skills: [
      {
        name: "astilba-cache-docs",
        type: "skill-md",
        description:
          "Consult Astilba's public Cache documentation to explain or evaluate the unreleased TypeScript cache preview without inventing installation or production support.",
        url: `/${cacheSkillArtifact}`,
        digest: cacheSkillDigest,
      },
      {
        name: "astilba-env-docs",
        type: "skill-md",
        description:
          "Consult Astilba's public Env documentation to integrate the 0.3 configuration contract compiler without weakening browser, server, platform, inventory, or lifecycle boundaries.",
        url: `/${envSkillArtifact}`,
        digest: envSkillDigest,
      },
    ],
  }
);
assertIncludes(cacheSkillArtifact, cacheSkill, "# Astilba Cache documentation");
assertIncludes(cacheSkillArtifact, cacheSkill, docsUrl("/cache/api-status.md"));
assertIncludes(cacheSkillArtifact, cacheSkill, docsUrl("/mcp"));
assertIncludes(envSkillArtifact, envSkill, "# Astilba Env documentation");
assertIncludes(
  envSkillArtifact,
  envSkill,
  `\`@astilba/env\` ${envVersion} as a public alpha`
);
assertIncludes(
  envSkillArtifact,
  envSkill,
  docsUrl("/env/release-and-support.md")
);
assertIncludes(
  envSkillArtifact,
  envSkill,
  docsUrl("/env/inventory-and-drift.md")
);
assertIncludes(
  envSkillArtifact,
  envSkill,
  "Treat inventory as value-free name evidence only"
);
assertIncludes(envSkillArtifact, envSkill, docsUrl("/mcp"));

const pageUrl = docsUrl("/cache/overview/");
const markdownUrl = docsUrl("/cache/overview.md");
const cacheHomeUrl = docsUrl("/cache/");
const cacheHomeMarkdownUrl = docsUrl("/cache.md");
const envPageUrl = docsUrl("/env/overview/");
const envMarkdownUrl = docsUrl("/env/overview.md");
const envHomeUrl = docsUrl("/env/");
const envHomeMarkdownUrl = docsUrl("/env.md");
const homeUrl = docsUrl("/");
const homeMarkdownUrl = docsUrl("/index.md");
const llmsUrl = docsUrl("/llms.txt");
const cacheSetUrl = docsUrl("/_llms-txt/astilba-cache.txt");
const envSetUrl = docsUrl("/_llms-txt/astilba-env.txt");
const mcpUrl = docsUrl("/mcp");
const apiCatalogUrl = new URL(API_CATALOG_PATH, site).href;
const mcpCatalogUrl = new URL(MCP_CATALOG_PATH, site).href;
const mcpServerCardUrl = new URL(MCP_SERVER_CARD_PATH, site).href;
const sitemapUrl = docsUrl("/sitemap.xml");
const agentSetupInstruction = `Fetch ${docsUrl("/agent-setup/prompt.md")} and follow its instructions.`;
const sitemapArtifact = docsArtifact("/sitemap.xml");
const sitemap = artifacts.get(sitemapArtifact);
const expectedLastModified = createDocsSitemapLastModified();
const sitemapEntries = [...sitemap.matchAll(/<url>([\s\S]*?)<\/url>/g)].map(
  (match) => {
    const entry = match[1];
    const locations = [...entry.matchAll(/<loc>([^<]+)<\/loc>/g)];
    const lastModified = [...entry.matchAll(/<lastmod>([^<]+)<\/lastmod>/g)];

    if (locations.length !== 1 || lastModified.length !== 1) {
      throw new Error(
        "[agent-artifacts] Every sitemap entry must contain exactly one loc and one lastmod."
      );
    }

    return {
      lastmod: lastModified[0][1],
      page: new URL(locations[0][1]),
    };
  }
);
const sitemapPages = sitemapEntries.map(({ page }) => page);
const sitemapLocations = new Set(sitemapPages.map(({ href }) => href));
const pagePatterns = [];
const mcpResources = [];

if (sitemapLocations.size !== sitemapEntries.length) {
  throw new Error("[agent-artifacts] Sitemap page locations must be unique.");
}

for (const { lastmod, page: sitemapPage } of sitemapEntries) {
  if (sitemapPage.origin !== site.origin) {
    throw new Error(
      `[agent-artifacts] Sitemap page ${sitemapPage.href} is outside ${site.origin}.`
    );
  }

  const expectedPageLastModified = expectedLastModified.get(
    sitemapPage.pathname
  );

  if (lastmod !== expectedPageLastModified) {
    throw new Error(
      `[agent-artifacts] Sitemap page ${sitemapPage.href} has unexpected lastmod ${JSON.stringify(lastmod)}; expected ${JSON.stringify(expectedPageLastModified)}.`
    );
  }

  const pagePattern = sitemapPage.pathname;
  const pageDirectory = decodeURIComponent(pagePattern).replace(
    /^\/+|\/+$/g,
    ""
  );
  const htmlArtifact = pageDirectory
    ? `${pageDirectory}/index.html`
    : "index.html";
  const pageHtml = await readArtifact(htmlArtifact);
  const markdownLinks = getMarkdownLinks(pageHtml);

  if (markdownLinks.length !== 1) {
    throw new Error(
      `[agent-artifacts] dist/${htmlArtifact} must advertise exactly one Markdown alternate, found ${markdownLinks.length}.`
    );
  }

  const alternateHref = getLinkAttribute(markdownLinks[0], "href");

  if (!alternateHref) {
    throw new Error(
      `[agent-artifacts] dist/${htmlArtifact} has a Markdown alternate without an href.`
    );
  }

  const alternateUrl = new URL(alternateHref, site);

  if (
    alternateUrl.origin !== site.origin ||
    alternateUrl.search ||
    alternateUrl.hash
  ) {
    throw new Error(
      `[agent-artifacts] dist/${htmlArtifact} has a non-canonical Markdown alternate: ${alternateHref}.`
    );
  }

  const markdownArtifact = decodeURIComponent(
    alternateUrl.pathname.replace(/^\//, "")
  );
  const markdownContent = await readArtifact(markdownArtifact);
  const canonicalUrl = getFrontmatterString(
    markdownContent,
    "canonical",
    markdownArtifact,
    { required: true }
  );

  if (canonicalUrl !== sitemapPage.href) {
    throw new Error(
      `[agent-artifacts] dist/${markdownArtifact} canonical URL does not match the sitemap: ${JSON.stringify(canonicalUrl)}.`
    );
  }

  assertLink(pageHtml, "describedby", llmsUrl);
  assertLink(pageHtml, "api-catalog", apiCatalogUrl);
  assertLink(pageHtml, "sitemap", sitemapUrl);
  pagePatterns.push(pagePattern);
  mcpResources.push({
    canonicalUrl,
    content: markdownContent,
    description: getFrontmatterString(
      markdownContent,
      "description",
      markdownArtifact,
      { required: true }
    ),
    docsVersion: getFrontmatterString(
      markdownContent,
      "docsVersion",
      markdownArtifact
    ),
    docsVersionId: getFrontmatterString(
      markdownContent,
      "docsVersionId",
      markdownArtifact
    ),
    lifecycle: getFrontmatterString(
      markdownContent,
      "lifecycle",
      markdownArtifact
    ),
    markdownPath: alternateUrl.pathname,
    product: getFrontmatterString(markdownContent, "product", markdownArtifact),
    productId: getFrontmatterString(
      markdownContent,
      "productId",
      markdownArtifact
    ),
    title: getFrontmatterString(markdownContent, "title", markdownArtifact, {
      required: true,
    }),
    uri: alternateUrl.href,
  });
}

if (pagePatterns.length === 0) {
  throw new Error(
    "[agent-artifacts] The sitemap does not contain any documentation pages."
  );
}

if (sitemapEntries.length !== expectedLastModified.size) {
  throw new Error(
    `[agent-artifacts] The sitemap contains ${sitemapEntries.length} pages, but ${expectedLastModified.size} public sources are registered.`
  );
}

const staticMarkdownAlternatePatterns = headerRules
  .filter((rule) =>
    (rule.headers.get("link") ?? []).some((value) =>
      linkHeaderHasRelation(value, "alternate")
    )
  )
  .map((rule) => rule.pattern)
  .sort();
assertExact(
  "_headers",
  "static Markdown alternate Link rule patterns",
  staticMarkdownAlternatePatterns,
  []
);

const mcpCorpusArtifact = docsArtifact("/_mcp/docs.json");
const mcpCorpus = parseDocsCorpus(JSON.parse(artifacts.get(mcpCorpusArtifact)));

const sortMcpResources = (left, right) => {
  if (left.markdownPath === withDocsBase("/index.md")) {
    return -1;
  }

  if (right.markdownPath === withDocsBase("/index.md")) {
    return 1;
  }

  return compareStrings(left.markdownPath, right.markdownPath);
};
const expectedMcpResources = mcpResources.toSorted(sortMcpResources);
assertExact(
  mcpCorpusArtifact,
  "public resource corpus",
  mcpCorpus.pages,
  expectedMcpResources
);

const llmsArtifact = docsArtifact("/llms.txt");
const llmsIndex = artifacts.get(llmsArtifact);
assertIncludes(llmsArtifact, llmsIndex, cacheSetUrl);
assertIncludes(llmsArtifact, llmsIndex, envSetUrl);
assertIncludes(
  llmsArtifact,
  llmsIndex,
  "Astilba Cache remains an unreleased preview"
);
assertIncludes(llmsArtifact, llmsIndex, `Env ${envVersion} is a public alpha`);
assertIncludes(llmsArtifact, llmsIndex, mcpUrl);
const llmsFullArtifact = docsArtifact("/llms-full.txt");
assertIncludes(
  llmsFullArtifact,
  artifacts.get(llmsFullArtifact),
  "# MCP Server"
);
assertIncludes(llmsFullArtifact, artifacts.get(llmsFullArtifact), mcpUrl);

const cacheSet = artifacts.get(docsArtifact("/_llms-txt/astilba-cache.txt"));
const firstCacheHeading = cacheSet.match(/^# .+$/m)?.[0];

if (firstCacheHeading !== "# Cache") {
  throw new Error(
    `[agent-artifacts] The Cache document set must begin with Cache, found ${JSON.stringify(firstCacheHeading)}.`
  );
}

const envSet = artifacts.get(docsArtifact("/_llms-txt/astilba-env.txt"));
const firstEnvHeading = envSet.match(/^# .+$/m)?.[0];
assertIncludes(
  docsArtifact("/_llms-txt/astilba-env.txt"),
  envSet,
  "/docs/env/inventory-and-drift/"
);

if (firstEnvHeading !== "# Env") {
  throw new Error(
    `[agent-artifacts] The Env document set must begin with Env, found ${JSON.stringify(firstEnvHeading)}.`
  );
}

const html = artifacts.get(docsArtifact("/cache/overview/index.html"));
assertLink(html, "alternate", markdownUrl);
assertLink(html, "describedby", llmsUrl);

const envHtml = artifacts.get(docsArtifact("/env/overview/index.html"));
assertLink(envHtml, "alternate", envMarkdownUrl);
assertLink(envHtml, "describedby", llmsUrl);

const envHomeHtml = artifacts.get(docsArtifact("/env/index.html"));
assertLink(envHomeHtml, "alternate", envHomeMarkdownUrl);
assertLink(envHomeHtml, "describedby", llmsUrl);

const homeHtml = artifacts.get(docsArtifact("/index.html"));
assertLink(homeHtml, "alternate", homeMarkdownUrl);
assertLink(homeHtml, "describedby", llmsUrl);
assertLink(homeHtml, "api-catalog", apiCatalogUrl);

const cacheHomeArtifact = docsArtifact("/cache/index.html");
const cacheHomeHtml = artifacts.get(cacheHomeArtifact);
assertLink(cacheHomeHtml, "alternate", cacheHomeMarkdownUrl);
assertLink(cacheHomeHtml, "describedby", llmsUrl);

const mcpUsageHtml = artifacts.get(docsArtifact("/agents/mcp/index.html"));
assertLink(mcpUsageHtml, "alternate", docsUrl("/agents/mcp.md"));
assertLink(mcpUsageHtml, "api-catalog", apiCatalogUrl);
const mcpUsageArtifact = docsArtifact("/agents/mcp.md");
const mcpUsageMarkdown = artifacts.get(mcpUsageArtifact);
assertIncludes(mcpUsageArtifact, mcpUsageMarkdown, "# MCP Server");
assertIncludes(mcpUsageArtifact, mcpUsageMarkdown, mcpUrl);
assertIncludes(mcpUsageArtifact, mcpUsageMarkdown, mcpCatalogUrl);
assertIncludes(mcpUsageArtifact, mcpUsageMarkdown, mcpServerCardUrl);

const llmsUsageArtifact = docsArtifact("/agents/llms-txt.md");
const llmsUsageHtml = artifacts.get(
  docsArtifact("/agents/llms-txt/index.html")
);
assertLink(llmsUsageHtml, "alternate", docsUrl("/agents/llms-txt.md"));
const llmsUsageMarkdown = artifacts.get(llmsUsageArtifact);
assertIncludes(llmsUsageArtifact, llmsUsageMarkdown, "# LLMs.txt");
assertIncludes(llmsUsageArtifact, llmsUsageMarkdown, llmsUrl);
assertIncludes(llmsUsageArtifact, llmsUsageMarkdown, mcpUrl);
assertIncludes(
  llmsUsageArtifact,
  llmsUsageMarkdown,
  docsUrl("/cache/overview.md")
);
assertIncludes(llmsUsageArtifact, llmsUsageMarkdown, agentSetupInstruction);

assertIncludes(mcpUsageArtifact, mcpUsageMarkdown, "## Try it");
assertIncludes(mcpUsageArtifact, mcpUsageMarkdown, "## Troubleshooting");
assertIncludes(mcpUsageArtifact, mcpUsageMarkdown, agentSetupInstruction);

const homeArtifact = docsArtifact("/index.md");
const homeMarkdown = artifacts.get(homeArtifact);
assertIncludes(
  homeArtifact,
  homeMarkdown,
  `canonical: ${JSON.stringify(homeUrl)}`
);
assertIncludes(homeArtifact, homeMarkdown, "# Overview");

const cacheHomeMarkdownArtifact = docsArtifact("/cache.md");
const cacheHomeMarkdown = artifacts.get(cacheHomeMarkdownArtifact);
assertIncludes(
  cacheHomeMarkdownArtifact,
  cacheHomeMarkdown,
  `canonical: ${JSON.stringify(cacheHomeUrl)}`
);
assertIncludes(cacheHomeMarkdownArtifact, cacheHomeMarkdown, "# Cache");
assertIncludes(
  cacheHomeMarkdownArtifact,
  cacheHomeMarkdown,
  withDocsBase("/cache/overview/")
);

const envHomeMarkdownArtifact = docsArtifact("/env.md");
const envHomeMarkdown = artifacts.get(envHomeMarkdownArtifact);
assertIncludes(
  envHomeMarkdownArtifact,
  envHomeMarkdown,
  `canonical: ${JSON.stringify(envHomeUrl)}`
);
assertIncludes(envHomeMarkdownArtifact, envHomeMarkdown, "# Env");
assertIncludes(
  envHomeMarkdownArtifact,
  envHomeMarkdown,
  withDocsBase("/env/overview/")
);

const agentSetupArtifact = docsArtifact("/agent-setup/prompt.md");
const agentSetupPrompt = artifacts.get(agentSetupArtifact);
assertIncludes(
  agentSetupArtifact,
  agentSetupPrompt,
  "# Set up Astilba documentation"
);
assertIncludes(agentSetupArtifact, agentSetupPrompt, mcpUrl);
assertIncludes(
  agentSetupArtifact,
  agentSetupPrompt,
  "astilba-cache-docs/SKILL.md"
);
assertIncludes(
  agentSetupArtifact,
  agentSetupPrompt,
  "astilba-env-docs/SKILL.md"
);
assertIncludes(
  agentSetupArtifact,
  agentSetupPrompt,
  "unreleased source preview"
);
assertIncludes(
  agentSetupArtifact,
  agentSetupPrompt,
  `@astilba/env\` ${envVersion}`
);
assertIncludes(agentSetupArtifact, agentSetupPrompt, "Env release and support");
assertIncludes(agentSetupArtifact, agentSetupPrompt, "public alpha");
assertIncludes(agentSetupArtifact, agentSetupPrompt, "inline-script transport");
assertIncludes(agentSetupArtifact, agentSetupPrompt, "@astilba/env/next");

const overviewArtifact = docsArtifact("/cache/overview.md");
const markdown = artifacts.get(overviewArtifact);
assertIncludes(
  overviewArtifact,
  markdown,
  `canonical: ${JSON.stringify(pageUrl)}`
);
assertIncludes(
  overviewArtifact,
  markdown,
  "For React applications, “server-side” means"
);

for (const field of [
  "title",
  "description",
  "product",
  "productId",
  "docsVersion",
  "docsVersionId",
  "lifecycle",
  "source",
]) {
  assertIncludes(overviewArtifact, markdown, `${field}: `);
}

assertIncludes(
  overviewArtifact,
  markdown,
  'source: "https://github.com/astilbahq/docs/blob/main/src/content/docs/cache/overview.md"'
);

const envOverviewArtifact = docsArtifact("/env/overview.md");
const envOverviewMarkdown = artifacts.get(envOverviewArtifact);
assertIncludes(
  envOverviewArtifact,
  envOverviewMarkdown,
  `canonical: ${JSON.stringify(envPageUrl)}`
);
assertIncludes(envOverviewArtifact, envOverviewMarkdown, "@astilba/env");
assertIncludes(
  envOverviewArtifact,
  envOverviewMarkdown,
  'source: "https://github.com/astilbahq/docs/blob/main/src/content/docs/env/overview.md"'
);

for (const field of [
  "title",
  "description",
  "product",
  "productId",
  "docsVersion",
  "docsVersionId",
  "lifecycle",
  "source",
]) {
  assertIncludes(envOverviewArtifact, envOverviewMarkdown, `${field}: `);
}

const robotsArtifact = docsArtifact("/robots.txt");
const robots = artifacts.get(robotsArtifact);
assertExact(robotsArtifact, "directives", robots.trimEnd().split("\n"), [
  "User-agent: *",
  `Content-Signal: ${contentSignal}`,
  `Allow: ${withDocsBase("/")}`,
  "",
  `Sitemap: ${sitemapUrl}`,
]);

const sitemapIndexArtifact = docsArtifact("/sitemap-index.xml");
const sitemapIndex = artifacts.get(sitemapIndexArtifact);
assertIncludes(sitemapIndexArtifact, sitemapIndex, docsUrl("/sitemap-0.xml"));
assertExact(
  sitemapArtifact,
  "single-file sitemap content",
  sitemap,
  artifacts.get(docsArtifact("/sitemap-0.xml"))
);
assertIncludes(sitemapArtifact, sitemap, pageUrl);
assertIncludes(sitemapArtifact, sitemap, envPageUrl);

console.log(
  `[agent-artifacts] Verified ${requiredArtifacts.length} production artifacts for ${site.origin}.`
);
