import { mkdir, readFile, readdir, writeFile } from "node:fs/promises";
import { isAbsolute, join, relative, resolve, sep } from "node:path";

import { parseDocsCorpus } from "../src/docs/mcp-corpus.ts";
import { withDocsBase } from "../src/docs/urls.ts";

const maxCorpusBytes = 1_000_000;
const maxPageBytes = 128_000;
const dist = resolve(process.cwd(), "dist");
const outputDirectory = resolve(dist, withDocsBase("/_mcp").slice(1));
const outputPath = resolve(outputDirectory, "docs.json");
const siteValue = process.env.ASTILBA_DOCS_SITE;

if (!siteValue) {
  throw new Error(
    "[mcp-corpus] ASTILBA_DOCS_SITE is required to generate canonical resource URIs."
  );
}

const site = new URL(siteValue);
const decoder = new TextDecoder("utf-8", { fatal: true });
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

const getLinkAttribute = (tag, name) =>
  tag.match(new RegExp(`\\b${name}="([^"]+)"`))?.[1];

const getMarkdownAlternate = (html, artifact, pageUrl) => {
  const matches = (html.match(/<link\b[^>]*>/g) ?? []).filter(
    (tag) =>
      getLinkAttribute(tag, "rel") === "alternate" &&
      getLinkAttribute(tag, "type") === "text/markdown"
  );

  if (matches.length === 0) {
    return undefined;
  }

  if (matches.length !== 1) {
    throw new Error(
      `[mcp-corpus] ${artifact} must advertise exactly one Markdown alternate, found ${matches.length}.`
    );
  }

  const href = getLinkAttribute(matches[0], "href");

  if (!href) {
    throw new Error(
      `[mcp-corpus] ${artifact} has a Markdown alternate without an href.`
    );
  }

  return new URL(href, pageUrl);
};

const getFrontmatter = (markdown, artifact) => {
  const frontmatter = markdown.match(
    /^---\r?\n([\s\S]*?)\r?\n---(?:\r?\n|$)/
  )?.[1];

  if (!frontmatter) {
    throw new Error(
      `[mcp-corpus] ${artifact} must begin with generated frontmatter.`
    );
  }

  return frontmatter;
};

const getFrontmatterString = (
  frontmatter,
  field,
  artifact,
  { required = false } = {}
) => {
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
      `[mcp-corpus] ${artifact} must define ${field} ${required ? "exactly once" : "at most once"} as a JSON string.`
    );
  }

  return JSON.parse(matches[0][1]);
};

const files = await collectFiles(dist);
const pages = [];
const canonicalUrls = new Set();
const markdownPaths = new Set();
const resourceUris = new Set();

for (const file of files) {
  if (!file.endsWith(`${sep}index.html`)) {
    continue;
  }

  const htmlArtifact = relative(dist, file).split(sep).join("/");
  const pageUrl = new URL(".", new URL(htmlArtifact, `${site.origin}/`));
  const html = decoder.decode(await readFile(file));
  const markdownUrl = getMarkdownAlternate(html, htmlArtifact, pageUrl);

  if (!markdownUrl) {
    continue;
  }

  if (
    markdownUrl.origin !== site.origin ||
    markdownUrl.search ||
    markdownUrl.hash ||
    !markdownUrl.pathname.endsWith(".md")
  ) {
    throw new Error(
      `[mcp-corpus] ${htmlArtifact} advertises a non-canonical Markdown resource: ${markdownUrl.href}.`
    );
  }

  const markdownPath = decodeURIComponent(markdownUrl.pathname);
  const markdownArtifact = markdownPath.replace(/^\/+/, "");
  const markdownFile = resolve(dist, markdownArtifact);
  const relativeMarkdownFile = relative(dist, markdownFile);

  if (
    relativeMarkdownFile.startsWith(`..${sep}`) ||
    relativeMarkdownFile === ".." ||
    isAbsolute(relativeMarkdownFile)
  ) {
    throw new Error(
      `[mcp-corpus] ${htmlArtifact} advertises a Markdown path outside dist: ${markdownPath}.`
    );
  }

  const bytes = await readFile(markdownFile);

  if (bytes.byteLength > maxPageBytes) {
    throw new Error(
      `[mcp-corpus] dist/${markdownArtifact} is ${bytes.byteLength} bytes; the per-page limit is ${maxPageBytes}.`
    );
  }

  const content = decoder.decode(bytes);
  const frontmatter = getFrontmatter(content, markdownArtifact);
  const canonicalUrl = getFrontmatterString(
    frontmatter,
    "canonical",
    markdownArtifact,
    { required: true }
  );
  const canonical = new URL(canonicalUrl);

  if (
    canonical.origin !== site.origin ||
    canonical.search ||
    canonical.hash ||
    !canonical.pathname.endsWith("/")
  ) {
    throw new Error(
      `[mcp-corpus] dist/${markdownArtifact} has a non-canonical page URL: ${canonical.href}.`
    );
  }

  if (
    canonicalUrls.has(canonical.href) ||
    markdownPaths.has(markdownPath) ||
    resourceUris.has(markdownUrl.href)
  ) {
    throw new Error(
      `[mcp-corpus] Duplicate documentation resource: ${markdownUrl.href}.`
    );
  }

  const product = getFrontmatterString(
    frontmatter,
    "product",
    markdownArtifact
  );
  const productId = getFrontmatterString(
    frontmatter,
    "productId",
    markdownArtifact
  );
  const docsVersion = getFrontmatterString(
    frontmatter,
    "docsVersion",
    markdownArtifact
  );
  const docsVersionId = getFrontmatterString(
    frontmatter,
    "docsVersionId",
    markdownArtifact
  );
  const lifecycle = getFrontmatterString(
    frontmatter,
    "lifecycle",
    markdownArtifact
  );
  const productMetadata = [product, productId];
  const versionMetadata = [docsVersion, docsVersionId, lifecycle];
  const productMetadataCount = productMetadata.filter(
    (value) => value !== undefined
  ).length;
  const versionMetadataCount = versionMetadata.filter(
    (value) => value !== undefined
  ).length;

  if (
    (productMetadataCount !== 0 &&
      productMetadataCount !== productMetadata.length) ||
    (versionMetadataCount !== 0 &&
      versionMetadataCount !== versionMetadata.length) ||
    (versionMetadataCount !== 0 &&
      productMetadataCount !== productMetadata.length)
  ) {
    throw new Error(
      `[mcp-corpus] dist/${markdownArtifact} must provide no product metadata, a complete product pair, or complete product/version metadata.`
    );
  }

  canonicalUrls.add(canonical.href);
  markdownPaths.add(markdownPath);
  resourceUris.add(markdownUrl.href);
  pages.push({
    canonicalUrl: canonical.href,
    content,
    description: getFrontmatterString(
      frontmatter,
      "description",
      markdownArtifact,
      { required: true }
    ),
    docsVersion,
    docsVersionId,
    lifecycle,
    markdownPath,
    product,
    productId,
    title: getFrontmatterString(frontmatter, "title", markdownArtifact, {
      required: true,
    }),
    uri: markdownUrl.href,
  });
}

if (pages.length === 0) {
  throw new Error(
    "[mcp-corpus] No public documentation pages with Markdown alternates were found."
  );
}

const corpus = parseDocsCorpus({ pages, schemaVersion: 1 });
const output = `${JSON.stringify(corpus)}\n`;
const outputBytes = new TextEncoder().encode(output).byteLength;

if (outputBytes > maxCorpusBytes) {
  throw new Error(
    `[mcp-corpus] Generated corpus is ${outputBytes} bytes; the limit is ${maxCorpusBytes}.`
  );
}

await mkdir(outputDirectory, { recursive: true });
await writeFile(outputPath, output, "utf8");

console.log(
  `[mcp-corpus] Generated ${pages.length} public resources (${outputBytes} bytes).`
);
