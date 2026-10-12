import { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { WebStandardStreamableHTTPServerTransport } from "@modelcontextprotocol/sdk/server/webStandardStreamableHttp.js";
import { DEFAULT_NEGOTIATED_PROTOCOL_VERSION } from "@modelcontextprotocol/sdk/types.js";
import { z } from "zod/v4";

import {
  MCP_ENDPOINT_PATH,
  MCP_SERVER_INFO,
  MCP_SERVER_INSTRUCTIONS,
} from "../src/docs/agent-discovery.ts";
import {
  type DocsCorpus,
  type DocsCorpusPage,
  MAX_CORPUS_CHARS,
  MAX_DOCUMENT_CHARS,
  parseDocsCorpus,
} from "../src/docs/mcp-corpus.ts";
import {
  ASTILBA_ORIGIN,
  LEGACY_DOCS_ORIGIN,
  withDocsBase,
} from "../src/docs/urls.ts";

export const DOCS_MCP_PATH = MCP_ENDPOINT_PATH;

const DOCS_HOSTNAMES = new Set(
  [ASTILBA_ORIGIN, LEGACY_DOCS_ORIGIN].map((origin) => new URL(origin).hostname)
);
const CORPUS_PATH = withDocsBase("/_mcp/docs.json");
const STRUCTURED_TOOL_RESULTS_VERSION = "2025-06-18";
const MAX_LEGACY_BATCH_MESSAGES = 16;
const MAX_REQUEST_BYTES = 256_000;
const DEFAULT_READ_LIMIT = 16_000;
const MAX_READ_LIMIT = 32_000;
const DEFAULT_SEARCH_LIMIT = 5;
const MAX_SEARCH_LIMIT = 10;
const SNIPPET_LENGTH = 240;
const textDecoder = new TextDecoder("utf-8", { fatal: true });
const compareStrings = (left: string, right: string): number =>
  left < right ? -1 : left > right ? 1 : 0;

interface PreparedPage {
  descriptionText: string;
  page: DocsCorpusPage;
  plainText: string;
  searchText: string;
  titleText: string;
}

interface SearchDocsInput {
  limit?: number;
  productId?: string;
  query: string;
  versionId?: string;
}

interface ReadDocInput {
  limit?: number;
  offset?: number;
  uri: string;
}

class McpRequestError extends Error {
  readonly code: number;
  readonly status: number;

  constructor(status: number, code: number, message: string) {
    super(message);
    this.name = "McpRequestError";
    this.code = code;
    this.status = status;
  }
}

const isRecord = (value: unknown): value is Record<string, unknown> =>
  typeof value === "object" && value !== null && !Array.isArray(value);

const logMcpError = (
  event: string,
  error: unknown,
  details: Record<string, string> = {}
): void => {
  console.error({
    error: error instanceof Error ? error.message : String(error),
    errorName: error instanceof Error ? error.name : "UnknownError",
    event,
    ...details,
  });
};

const corpusCache = new WeakMap<object, Promise<DocsCorpus>>();

const readCorpus = async (
  assets: Fetcher,
  requestUrl: string
): Promise<DocsCorpus> => {
  const corpusUrl = new URL(CORPUS_PATH, requestUrl);
  const response = await assets.fetch(
    new Request(corpusUrl, {
      headers: { Accept: "application/json" },
    })
  );

  if (!response.ok) {
    await response.body?.cancel();
    throw new Error(
      `[docs-mcp] Generated corpus returned HTTP ${response.status}.`
    );
  }

  const serialized = await response.text();

  if (serialized.length > MAX_CORPUS_CHARS) {
    throw new Error("[docs-mcp] Generated corpus exceeds its runtime limit.");
  }

  return parseDocsCorpus(JSON.parse(serialized));
};

const loadCorpus = async (
  assets: Fetcher,
  requestUrl: string
): Promise<DocsCorpus> => {
  const cacheKey = assets as object;
  const cached = corpusCache.get(cacheKey);

  if (cached) {
    return cached;
  }

  const pending = readCorpus(assets, requestUrl);
  corpusCache.set(cacheKey, pending);

  try {
    return await pending;
  } catch (error) {
    if (corpusCache.get(cacheKey) === pending) {
      corpusCache.delete(cacheKey);
    }

    throw error;
  }
};

const normalizeSearchText = (value: string): string =>
  value.normalize("NFKC").toLocaleLowerCase("en");

const markdownToText = (content: string): string =>
  content
    .replace(/^---\r?\n[\s\S]*?\r?\n---(?:\r?\n|$)/, "")
    .replace(/<[^>]+>/g, " ")
    .replace(/!?(?:\[([^\]]*)\])\([^)]*\)/g, "$1")
    .replace(/[`*_~>#|{}[\]():]/g, " ")
    .replace(/\s+/g, " ")
    .trim();

const preparedCorpusCache = new WeakMap<DocsCorpus, readonly PreparedPage[]>();

const prepareCorpus = (corpus: DocsCorpus): readonly PreparedPage[] => {
  const cached = preparedCorpusCache.get(corpus);

  if (cached) {
    return cached;
  }

  const prepared = Object.freeze(
    corpus.pages.map((page) => {
      const plainText = markdownToText(page.content);

      return Object.freeze({
        descriptionText: normalizeSearchText(page.description),
        page,
        plainText,
        searchText: normalizeSearchText(plainText),
        titleText: normalizeSearchText(page.title),
      });
    })
  );
  preparedCorpusCache.set(corpus, prepared);
  return prepared;
};

const countOccurrences = (value: string, search: string): number => {
  let count = 0;
  let offset = 0;

  while (count < 8) {
    const index = value.indexOf(search, offset);

    if (index === -1) {
      break;
    }

    count += 1;
    offset = index + search.length;
  }

  return count;
};

const getSearchTerms = (query: string): string[] => [
  ...new Set(normalizeSearchText(query).match(/[\p{L}\p{N}_@.$/-]+/gu) ?? []),
];

const createSnippet = (text: string, terms: readonly string[]): string => {
  const normalized = normalizeSearchText(text);
  const matchingIndexes = terms
    .map((term) => normalized.indexOf(term))
    .filter((index) => index >= 0);
  const firstMatch = Math.min(...matchingIndexes);
  const idealStart = Number.isFinite(firstMatch)
    ? Math.max(
        0,
        Math.min(firstMatch, text.length) - Math.floor(SNIPPET_LENGTH / 3)
      )
    : 0;
  let start = idealStart;

  if (start > 0) {
    const nextSpace = text.indexOf(" ", start);
    start = nextSpace === -1 ? start : nextSpace + 1;
  }

  let end = Math.min(text.length, start + SNIPPET_LENGTH);

  if (end < text.length) {
    const previousSpace = text.lastIndexOf(" ", end);
    end = previousSpace > start ? previousSpace : end;
  }

  return `${start > 0 ? "…" : ""}${text.slice(start, end).trim()}${
    end < text.length ? "…" : ""
  }`;
};

export const searchDocs = (corpus: DocsCorpus, input: SearchDocsInput) => {
  const query = input.query.trim();
  const terms = getSearchTerms(query);
  const limit = input.limit ?? DEFAULT_SEARCH_LIMIT;

  if (terms.length === 0) {
    return [];
  }

  const phrase = normalizeSearchText(query);
  return prepareCorpus(corpus)
    .flatMap((prepared) => {
      const { page } = prepared;
      const matchesAllTerms = terms.every(
        (term) =>
          prepared.titleText.includes(term) ||
          prepared.descriptionText.includes(term) ||
          prepared.searchText.includes(term)
      );

      if (
        (input.productId && page.productId !== input.productId) ||
        (input.versionId && page.docsVersionId !== input.versionId) ||
        !matchesAllTerms
      ) {
        return [];
      }

      let score = 0;

      if (prepared.titleText.includes(phrase)) {
        score += 40;
      }
      if (prepared.descriptionText.includes(phrase)) {
        score += 20;
      }
      if (prepared.searchText.includes(phrase)) {
        score += 10;
      }

      for (const term of terms) {
        score += countOccurrences(prepared.titleText, term) * 12;
        score += countOccurrences(prepared.descriptionText, term) * 6;
        score += countOccurrences(prepared.searchText, term);
      }

      return [
        {
          canonicalUrl: page.canonicalUrl,
          description: page.description,
          product: page.product ?? null,
          productId: page.productId ?? null,
          score,
          snippet: createSnippet(prepared.plainText, terms),
          title: page.title,
          uri: page.uri,
          version: page.docsVersion ?? null,
          versionId: page.docsVersionId ?? null,
        },
      ];
    })
    .sort(
      (left, right) =>
        right.score - left.score ||
        compareStrings(left.title, right.title) ||
        compareStrings(left.uri, right.uri)
    )
    .slice(0, Math.min(limit, MAX_SEARCH_LIMIT));
};

const findPage = (
  corpus: DocsCorpus,
  reference: string
): DocsCorpusPage | undefined => {
  let url: URL;

  try {
    url = new URL(reference, `${ASTILBA_ORIGIN}/`);
  } catch {
    return undefined;
  }

  if (
    url.origin !== ASTILBA_ORIGIN ||
    url.username ||
    url.password ||
    url.search ||
    url.hash
  ) {
    return undefined;
  }

  return corpus.pages.find(
    (page) =>
      page.uri === url.href ||
      page.canonicalUrl === url.href ||
      page.markdownPath === url.pathname
  );
};

const avoidSplitSurrogate = (
  content: string,
  offset: number,
  end: number
): number => {
  const before = content.charCodeAt(end - 1);
  const after = content.charCodeAt(end);

  const splitsPair =
    end > offset &&
    before >= 0xd800 &&
    before <= 0xdbff &&
    after >= 0xdc00 &&
    after <= 0xdfff;

  if (!splitsPair) {
    return end;
  }

  return end - 1 === offset ? end + 1 : end - 1;
};

export const readDoc = (corpus: DocsCorpus, input: ReadDocInput) => {
  const page = findPage(corpus, input.uri);

  if (!page) {
    return undefined;
  }

  const offset = input.offset ?? 0;
  const previousCharacter = page.content.charCodeAt(offset - 1);
  const currentCharacter = page.content.charCodeAt(offset);
  const startsInsideSurrogatePair =
    offset > 0 &&
    previousCharacter >= 0xd800 &&
    previousCharacter <= 0xdbff &&
    currentCharacter >= 0xdc00 &&
    currentCharacter <= 0xdfff;

  if (
    !Number.isInteger(offset) ||
    offset < 0 ||
    offset > page.content.length ||
    startsInsideSurrogatePair
  ) {
    return undefined;
  }

  const limit = Math.min(input.limit ?? DEFAULT_READ_LIMIT, MAX_READ_LIMIT);
  const rawEnd = Math.min(page.content.length, offset + limit);
  const end = avoidSplitSurrogate(page.content, offset, rawEnd);
  const content = page.content.slice(offset, end);

  return {
    canonicalUrl: page.canonicalUrl,
    content,
    nextOffset: end < page.content.length ? end : null,
    offset,
    returnedChars: content.length,
    title: page.title,
    totalChars: page.content.length,
    uri: page.uri,
  };
};

const toolAnnotations = {
  destructiveHint: false,
  idempotentHint: true,
  openWorldHint: false,
  readOnlyHint: true,
} as const;

const createDocsMcpServer = (
  corpus: DocsCorpus,
  protocolVersion = DEFAULT_NEGOTIATED_PROTOCOL_VERSION
): McpServer => {
  const supportsStructuredToolResults =
    /^\d{4}-\d{2}-\d{2}$/.test(protocolVersion) &&
    protocolVersion >= STRUCTURED_TOOL_RESULTS_VERSION;
  const server = new McpServer(MCP_SERVER_INFO, {
    instructions: MCP_SERVER_INSTRUCTIONS,
  });

  for (const page of corpus.pages) {
    server.registerResource(
      `docs:${page.productId ?? "astilba"}:${
        page.docsVersionId ?? "site"
      }:${page.markdownPath}`,
      page.uri,
      {
        annotations: {
          audience: ["assistant", "user"],
          priority: page.markdownPath === "/index.md" ? 1 : 0.8,
        },
        description: page.description,
        mimeType: "text/markdown",
        title: page.title,
      },
      async () => ({
        contents: [
          {
            mimeType: "text/markdown",
            text: page.content,
            uri: page.uri,
          },
        ],
      })
    );
  }

  server.registerTool(
    "search_docs",
    {
      annotations: toolAnnotations,
      description:
        "Search the published Astilba documentation. Returns ranked page summaries and links to exact Markdown resources.",
      inputSchema: {
        limit: z
          .number()
          .int()
          .min(1)
          .max(MAX_SEARCH_LIMIT)
          .default(DEFAULT_SEARCH_LIMIT)
          .describe("Maximum number of results."),
        productId: z
          .string()
          .regex(/^[a-z0-9]+(?:-[a-z0-9]+)*$/)
          .max(64)
          .optional()
          .describe("Optional product ID, such as cache."),
        query: z
          .string()
          .trim()
          .min(1)
          .max(256)
          .describe("Words or API names to find."),
        versionId: z
          .string()
          .regex(/^[a-z0-9]+(?:[.-][a-z0-9]+)*$/)
          .max(64)
          .optional()
          .describe("Optional product-specific documentation version ID."),
      },
      ...(supportsStructuredToolResults
        ? {
            outputSchema: {
              query: z.string(),
              results: z.array(
                z.object({
                  canonicalUrl: z.url(),
                  description: z.string(),
                  product: z.string().nullable(),
                  productId: z.string().nullable(),
                  score: z.number(),
                  snippet: z.string(),
                  title: z.string(),
                  uri: z.url(),
                  version: z.string().nullable(),
                  versionId: z.string().nullable(),
                })
              ),
            },
          }
        : {}),
      title: "Search documentation",
    },
    async ({ limit, productId, query, versionId }) => {
      const results = searchDocs(corpus, {
        limit,
        productId,
        query,
        versionId,
      });
      const output = { query, results };

      return {
        content: [
          {
            text: JSON.stringify(output),
            type: "text",
          },
          ...(supportsStructuredToolResults
            ? results.map((result) => ({
                description: result.description,
                mimeType: "text/markdown" as const,
                name: result.uri,
                title: result.title,
                type: "resource_link" as const,
                uri: result.uri,
              }))
            : []),
        ],
        ...(supportsStructuredToolResults ? { structuredContent: output } : {}),
      };
    }
  );

  server.registerTool(
    "read_doc",
    {
      annotations: toolAnnotations,
      description:
        "Read a bounded chunk of one published documentation page. Only URIs and paths from this server's resource list are accepted.",
      inputSchema: {
        limit: z
          .number()
          .int()
          .min(1)
          .max(MAX_READ_LIMIT)
          .default(DEFAULT_READ_LIMIT)
          .describe("Maximum UTF-16 characters to return."),
        offset: z
          .number()
          .int()
          .min(0)
          .max(MAX_DOCUMENT_CHARS)
          .default(0)
          .describe("UTF-16 character offset at which to start."),
        uri: z
          .string()
          .trim()
          .min(1)
          .max(2048)
          .describe(
            "A resource URI or canonical docs path returned by this server."
          ),
      },
      ...(supportsStructuredToolResults
        ? {
            outputSchema: {
              canonicalUrl: z.url(),
              nextOffset: z.number().int().nullable(),
              offset: z.number().int(),
              returnedChars: z.number().int(),
              title: z.string(),
              totalChars: z.number().int(),
              uri: z.url(),
            },
          }
        : {}),
      title: "Read documentation",
    },
    async ({ limit, offset, uri }) => {
      const result = readDoc(corpus, { limit, offset, uri });

      if (!result) {
        return {
          content: [
            {
              text: "Unknown documentation URI or invalid offset. Use search_docs or resources/list first.",
              type: "text",
            },
          ],
          isError: true,
        };
      }

      const { content, ...metadata } = result;

      return {
        content: [
          {
            text: JSON.stringify(metadata),
            type: "text",
          },
          {
            text: content,
            type: "text",
          },
          ...(supportsStructuredToolResults
            ? [
                {
                  mimeType: "text/markdown" as const,
                  name: result.uri,
                  title: result.title,
                  type: "resource_link" as const,
                  uri: result.uri,
                },
              ]
            : []),
        ],
        ...(supportsStructuredToolResults
          ? { structuredContent: metadata }
          : {}),
      };
    }
  );

  return server;
};

const LOOPBACK_HOSTNAMES = new Set(["127.0.0.1", "[::1]", "localhost"]);

const getAllowedOrigin = (request: Request): string | undefined => {
  let requestUrl: URL;

  try {
    requestUrl = new URL(request.url);
  } catch {
    throw new McpRequestError(403, -32000, "Request host is not allowed.");
  }

  const isDocsHost =
    DOCS_HOSTNAMES.has(requestUrl.hostname) &&
    (requestUrl.protocol === "https:" || requestUrl.protocol === "http:");
  const isLoopbackDevelopmentOrigin =
    requestUrl.protocol === "http:" &&
    LOOPBACK_HOSTNAMES.has(requestUrl.hostname);

  if (!(isDocsHost || isLoopbackDevelopmentOrigin)) {
    throw new McpRequestError(403, -32000, "Request host is not allowed.");
  }

  const origin = request.headers.get("Origin");

  if (!origin) {
    return undefined;
  }

  let parsedOrigin: URL;

  try {
    parsedOrigin = new URL(origin);
  } catch {
    throw new McpRequestError(403, -32000, "Invalid Origin header.");
  }

  if (
    origin !== parsedOrigin.origin ||
    parsedOrigin.origin !== requestUrl.origin
  ) {
    throw new McpRequestError(403, -32000, "Origin is not allowed.");
  }

  return parsedOrigin.origin;
};

const getProtocolVersion = (request: Request, parsedBody: unknown): string => {
  const messages = Array.isArray(parsedBody) ? parsedBody : [parsedBody];
  const initializeRequest = messages.find(
    (message) => isRecord(message) && message.method === "initialize"
  );

  if (
    isRecord(initializeRequest) &&
    isRecord(initializeRequest.params) &&
    typeof initializeRequest.params.protocolVersion === "string"
  ) {
    return initializeRequest.params.protocolVersion;
  }

  return (
    request.headers.get("MCP-Protocol-Version") ??
    DEFAULT_NEGOTIATED_PROTOCOL_VERSION
  );
};

const validateBatch = (parsedBody: unknown, protocolVersion: string): void => {
  if (!Array.isArray(parsedBody)) {
    return;
  }

  if (
    parsedBody.length === 0 ||
    parsedBody.length > MAX_LEGACY_BATCH_MESSAGES
  ) {
    throw new McpRequestError(400, -32600, "Invalid JSON-RPC batch.");
  }

  if (
    /^\d{4}-\d{2}-\d{2}$/.test(protocolVersion) &&
    protocolVersion >= STRUCTURED_TOOL_RESULTS_VERSION
  ) {
    throw new McpRequestError(
      400,
      -32600,
      "JSON-RPC batching is not supported by this protocol version."
    );
  }
};

const EXPENSIVE_METHODS = new Set(["resources/read", "tools/call"]);

const countExpensiveOperations = (parsedBody: unknown): number => {
  const messages = Array.isArray(parsedBody) ? parsedBody : [parsedBody];

  return messages.filter(
    (message) =>
      isRecord(message) &&
      typeof message.method === "string" &&
      EXPENSIVE_METHODS.has(message.method)
  ).length;
};

const getRateLimitKey = (request: Request): string => {
  const clientAddress = request.headers.get("CF-Connecting-IP")?.trim();

  // This unauthenticated endpoint has no account or API-token identity.
  // Cloudflare's source address is the strongest stable signal available, so
  // clients behind shared egress intentionally share capacity. The fallback is
  // primarily for local tests or requests that did not traverse the edge.
  return clientAddress && clientAddress.length <= 64
    ? `client:${clientAddress}`
    : "client:anonymous";
};

const consumeRateLimitUnits = async (
  rateLimiter: RateLimit,
  request: Request,
  units: number,
  message: string
): Promise<void> => {
  const key = getRateLimitKey(request);

  for (let index = 0; index < units; index += 1) {
    const outcome = await rateLimiter.limit({ key });

    if (!outcome.success) {
      throw new McpRequestError(429, -32000, message);
    }
  }
};

type RateLimitPhase = "operation" | "request";

const rateLimitErrorResponse = (
  error: unknown,
  phase: RateLimitPhase,
  allowedOrigin?: string,
  id: number | string | null = null
): Response => {
  if (error instanceof McpRequestError) {
    console.warn({
      event: "mcp_rate_limited",
      phase,
      status: error.status,
    });
    const response = errorResponse(
      error.status,
      error.code,
      error.message,
      allowedOrigin,
      id
    );
    response.headers.set("Retry-After", "60");
    return response;
  }

  logMcpError("mcp_rate_limit_failure", error, { phase });
  return errorResponse(
    503,
    -32603,
    "The MCP endpoint is temporarily unavailable.",
    allowedOrigin,
    id
  );
};

const withProtocolHeaders = (
  response: Response,
  allowedOrigin?: string
): Response => {
  const headers = new Headers(response.headers);
  headers.set("Cache-Control", "no-store");
  headers.set("Content-Signal", "ai-train=yes, search=yes, ai-input=yes");
  headers.set("X-Content-Type-Options", "nosniff");
  headers.set(
    "Access-Control-Expose-Headers",
    "MCP-Protocol-Version, MCP-Session-Id"
  );

  if (allowedOrigin) {
    headers.set("Access-Control-Allow-Origin", allowedOrigin);
    const vary = headers.get("Vary");
    const values = vary?.split(",").map((value) => value.trim()) ?? [];

    if (!values.some((value) => value.toLowerCase() === "origin")) {
      headers.set("Vary", vary ? `${vary}, Origin` : "Origin");
    }
  }

  return new Response(response.body, {
    headers,
    status: response.status,
    statusText: response.statusText,
  });
};

const errorResponse = (
  status: number,
  code: number,
  message: string,
  allowedOrigin?: string,
  id: number | string | null = null
): Response =>
  withProtocolHeaders(
    Response.json(
      {
        error: { code, message },
        id,
        jsonrpc: "2.0",
      },
      { status }
    ),
    allowedOrigin
  );

const getResponseId = (parsedBody: unknown): number | string | null => {
  if (!isRecord(parsedBody)) {
    return null;
  }

  const { id } = parsedBody;
  return typeof id === "number" || typeof id === "string" ? id : null;
};

const readRequestJson = async (request: Request): Promise<unknown> => {
  const contentLength = Number(request.headers.get("Content-Length"));

  if (Number.isFinite(contentLength) && contentLength > MAX_REQUEST_BYTES) {
    throw new McpRequestError(413, -32000, "Request body is too large.");
  }

  if (!request.body) {
    throw new McpRequestError(400, -32700, "Request body is required.");
  }

  const reader = request.body.getReader();
  const chunks: Uint8Array[] = [];
  let byteLength = 0;

  while (true) {
    const { done, value } = await reader.read();

    if (done) {
      break;
    }

    byteLength += value.byteLength;

    if (byteLength > MAX_REQUEST_BYTES) {
      await reader.cancel();
      throw new McpRequestError(413, -32000, "Request body is too large.");
    }

    chunks.push(value);
  }

  const bytes = new Uint8Array(byteLength);
  let offset = 0;

  for (const chunk of chunks) {
    bytes.set(chunk, offset);
    offset += chunk.byteLength;
  }

  try {
    return JSON.parse(textDecoder.decode(bytes));
  } catch {
    throw new McpRequestError(400, -32700, "Request body is not valid JSON.");
  }
};

export const handleDocsMcpRequest = async (
  request: Request,
  assets: Fetcher,
  rateLimiter: RateLimit
): Promise<Response> => {
  let allowedOrigin: string | undefined;

  try {
    allowedOrigin = getAllowedOrigin(request);
  } catch (error) {
    if (error instanceof McpRequestError) {
      return errorResponse(error.status, error.code, error.message);
    }

    logMcpError("mcp_origin_validation_failure", error);
    return errorResponse(500, -32603, "Internal MCP server error.");
  }

  if (request.method === "OPTIONS") {
    const headers = new Headers({
      "Access-Control-Allow-Headers":
        "Accept, Content-Type, MCP-Protocol-Version, MCP-Session-Id",
      "Access-Control-Allow-Methods": "POST, OPTIONS",
      "Access-Control-Max-Age": "86400",
    });

    return withProtocolHeaders(
      new Response(null, { headers, status: 204 }),
      allowedOrigin
    );
  }

  if (request.method !== "POST") {
    const response = errorResponse(
      405,
      -32000,
      "Method not allowed. This stateless server accepts POST requests.",
      allowedOrigin
    );
    response.headers.set("Allow", "POST, OPTIONS");
    return response;
  }

  try {
    await consumeRateLimitUnits(
      rateLimiter,
      request,
      1,
      "MCP request rate limit exceeded. Retry later."
    );
  } catch (error) {
    return rateLimitErrorResponse(error, "request", allowedOrigin);
  }

  let parsedBody: unknown;

  try {
    parsedBody = await readRequestJson(request);
  } catch (error) {
    if (error instanceof McpRequestError) {
      return errorResponse(
        error.status,
        error.code,
        error.message,
        allowedOrigin
      );
    }

    logMcpError("mcp_request_body_failure", error);
    return errorResponse(
      500,
      -32603,
      "Internal MCP server error.",
      allowedOrigin
    );
  }

  const protocolVersion = getProtocolVersion(request, parsedBody);
  const responseId = getResponseId(parsedBody);

  try {
    validateBatch(parsedBody, protocolVersion);
  } catch (error) {
    if (error instanceof McpRequestError) {
      return errorResponse(
        error.status,
        error.code,
        error.message,
        allowedOrigin
      );
    }

    logMcpError("mcp_payload_validation_failure", error);
    return errorResponse(
      500,
      -32603,
      "Internal MCP server error.",
      allowedOrigin
    );
  }

  try {
    await consumeRateLimitUnits(
      rateLimiter,
      request,
      countExpensiveOperations(parsedBody),
      "MCP operation rate limit exceeded. Retry later."
    );
  } catch (error) {
    return rateLimitErrorResponse(
      error,
      "operation",
      allowedOrigin,
      responseId
    );
  }

  let corpus: DocsCorpus;

  try {
    corpus = await loadCorpus(assets, request.url);
  } catch (error) {
    logMcpError("mcp_corpus_load_failure", error);
    return errorResponse(
      503,
      -32603,
      "Documentation resources are temporarily unavailable.",
      allowedOrigin,
      responseId
    );
  }

  const server = createDocsMcpServer(corpus, protocolVersion);
  const transport = new WebStandardStreamableHTTPServerTransport({
    enableJsonResponse: true,
    sessionIdGenerator: undefined,
  });

  try {
    await server.connect(transport);
    const response = await transport.handleRequest(request, { parsedBody });
    return withProtocolHeaders(response, allowedOrigin);
  } catch (error) {
    logMcpError("mcp_request_failure", error);
    return errorResponse(
      500,
      -32603,
      "Internal MCP server error.",
      allowedOrigin,
      responseId
    );
  } finally {
    try {
      await server.close();
    } catch (closeError) {
      logMcpError("mcp_server_close_failure", closeError);
    }
  }
};
