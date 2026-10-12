import { docsProducts } from "./catalog.ts";
import { siteDocsPages } from "./site-pages.ts";
import { ASTILBA_ORIGIN, withDocsBase } from "./urls.ts";

export const MAX_CORPUS_CHARS = 1_000_000;
export const MAX_DOCUMENT_CHARS = 128_000;

export interface DocsCorpusPage {
  canonicalUrl: string;
  content: string;
  description: string;
  docsVersion?: string;
  docsVersionId?: string;
  lifecycle?: string;
  markdownPath: string;
  product?: string;
  productId?: string;
  title: string;
  uri: string;
}

export interface DocsCorpus {
  pages: readonly DocsCorpusPage[];
  schemaVersion: 1;
}

interface CatalogMetadata {
  docsVersion?: string;
  docsVersionId?: string;
  lifecycle?: string;
  product: string;
  productId: string;
}

const compareStrings = (left: string, right: string): number =>
  left < right ? -1 : left > right ? 1 : 0;

const catalogMetadataByPath = new Map<string, CatalogMetadata>();
const siteMarkdownPaths = new Set(
  siteDocsPages.map(({ markdownPath }) => markdownPath)
);

for (const product of docsProducts) {
  for (const version of product.versions) {
    for (const section of version.sections) {
      for (const page of section.items) {
        catalogMetadataByPath.set(
          withDocsBase(`/${version.basePath}/${page.slug}.md`),
          {
            docsVersion: version.label,
            docsVersionId: version.id,
            lifecycle: version.lifecycle,
            product: product.label,
            productId: product.id,
          }
        );
      }
    }
  }
}

for (const markdownPath of siteMarkdownPaths) {
  if (catalogMetadataByPath.has(markdownPath)) {
    throw new Error(
      `[docs-mcp] Site and product documentation both claim ${markdownPath}.`
    );
  }
}

for (const page of siteDocsPages) {
  if (!page.productId) {
    continue;
  }

  const product = docsProducts.find(({ id }) => id === page.productId);

  if (!product) {
    throw new Error(
      `[docs-mcp] Unknown documentation product for ${page.markdownPath}.`
    );
  }

  catalogMetadataByPath.set(page.markdownPath, {
    product: product.label,
    productId: product.id,
  });
}

export const EXPECTED_CORPUS_PAGES = new Set([
  ...catalogMetadataByPath.keys(),
  ...siteMarkdownPaths,
]).size;

const isRecord = (value: unknown): value is Record<string, unknown> =>
  typeof value === "object" && value !== null && !Array.isArray(value);

const requireString = (
  value: unknown,
  field: string,
  { maxLength = 2048 }: { maxLength?: number } = {}
): string => {
  if (
    typeof value !== "string" ||
    value.length === 0 ||
    value.length > maxLength
  ) {
    throw new Error(
      `[docs-mcp] Corpus field ${field} must be a non-empty string of at most ${maxLength} characters.`
    );
  }

  return value;
};

const optionalString = (value: unknown, field: string): string | undefined =>
  value === undefined
    ? undefined
    : requireString(value, field, { maxLength: 128 });

const parsePublicUrl = (
  value: unknown,
  field: string,
  expectedPath?: string
): string => {
  const rawUrl = requireString(value, field);
  const url = new URL(rawUrl);

  if (
    url.origin !== ASTILBA_ORIGIN ||
    url.username ||
    url.password ||
    url.search ||
    url.hash ||
    (expectedPath !== undefined && url.pathname !== expectedPath)
  ) {
    throw new Error(`[docs-mcp] Corpus field ${field} is not canonical.`);
  }

  return url.href;
};

const assertCatalogMetadata = (page: DocsCorpusPage): void => {
  const expected = catalogMetadataByPath.get(page.markdownPath);
  const actual = {
    docsVersion: page.docsVersion,
    docsVersionId: page.docsVersionId,
    lifecycle: page.lifecycle,
    product: page.product,
    productId: page.productId,
  };

  if (!expected) {
    const hasMetadata = Object.values(actual).some(
      (value) => value !== undefined
    );

    if (!siteMarkdownPaths.has(page.markdownPath) || hasMetadata) {
      throw new Error(
        `[docs-mcp] ${page.markdownPath} is not present in the public documentation catalog.`
      );
    }

    return;
  }

  for (const [field, value] of Object.entries(actual)) {
    if (value !== expected[field as keyof CatalogMetadata]) {
      throw new Error(
        `[docs-mcp] ${page.markdownPath} has metadata that differs from the public documentation catalog.`
      );
    }
  }
};

export const parseDocsCorpus = (value: unknown): DocsCorpus => {
  if (
    !isRecord(value) ||
    value.schemaVersion !== 1 ||
    !Array.isArray(value.pages) ||
    value.pages.length !== EXPECTED_CORPUS_PAGES
  ) {
    throw new Error("[docs-mcp] Invalid generated corpus envelope.");
  }

  const canonicalUrls = new Set<string>();
  const markdownPaths = new Set<string>();
  const resourceUris = new Set<string>();
  const pages = value.pages.map((rawPage, index) => {
    if (!isRecord(rawPage)) {
      throw new Error(`[docs-mcp] Corpus page ${index} is not an object.`);
    }

    const markdownPath = requireString(
      rawPage.markdownPath,
      `pages[${index}].markdownPath`
    );

    if (
      !markdownPath.startsWith("/") ||
      !markdownPath.endsWith(".md") ||
      markdownPath.includes("..") ||
      markdownPath.includes("\\")
    ) {
      throw new Error(
        `[docs-mcp] Corpus page ${index} has an invalid Markdown path.`
      );
    }

    const content = requireString(rawPage.content, `pages[${index}].content`, {
      maxLength: MAX_DOCUMENT_CHARS,
    });
    const page = {
      canonicalUrl: parsePublicUrl(
        rawPage.canonicalUrl,
        `pages[${index}].canonicalUrl`
      ),
      content,
      description: requireString(
        rawPage.description,
        `pages[${index}].description`,
        { maxLength: 1024 }
      ),
      docsVersion: optionalString(
        rawPage.docsVersion,
        `pages[${index}].docsVersion`
      ),
      docsVersionId: optionalString(
        rawPage.docsVersionId,
        `pages[${index}].docsVersionId`
      ),
      lifecycle: optionalString(rawPage.lifecycle, `pages[${index}].lifecycle`),
      markdownPath,
      product: optionalString(rawPage.product, `pages[${index}].product`),
      productId: optionalString(rawPage.productId, `pages[${index}].productId`),
      title: requireString(rawPage.title, `pages[${index}].title`, {
        maxLength: 256,
      }),
      uri: parsePublicUrl(rawPage.uri, `pages[${index}].uri`, markdownPath),
    } satisfies DocsCorpusPage;

    if (
      canonicalUrls.has(page.canonicalUrl) ||
      markdownPaths.has(page.markdownPath) ||
      resourceUris.has(page.uri)
    ) {
      throw new Error(
        `[docs-mcp] Duplicate generated resource: ${page.markdownPath}.`
      );
    }

    assertCatalogMetadata(page);
    canonicalUrls.add(page.canonicalUrl);
    markdownPaths.add(page.markdownPath);
    resourceUris.add(page.uri);
    return Object.freeze(page);
  });

  const indexMarkdownPath = withDocsBase("/index.md");
  pages.sort((left, right) => {
    if (left.markdownPath === indexMarkdownPath) {
      return -1;
    }

    if (right.markdownPath === indexMarkdownPath) {
      return 1;
    }

    return compareStrings(left.markdownPath, right.markdownPath);
  });

  return Object.freeze({
    pages: Object.freeze(pages),
    schemaVersion: 1 as const,
  });
};
