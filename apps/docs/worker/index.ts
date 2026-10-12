import { API_CATALOG_LINK_VALUE } from "../src/docs/agent-discovery";
import { docsProducts, getDefaultPage, getPageHref } from "../src/docs/catalog";
import {
  CONTENT_SECURITY_POLICY_ASSET_PATH,
  CONTENT_SECURITY_POLICY_HEADER,
  GLOBAL_SECURITY_HEADERS,
} from "../src/docs/security";
import { siteDocsPages } from "../src/docs/site-pages";
import {
  ASTILBA_ORIGIN,
  LEGACY_DOCS_ORIGIN,
  withDocsBase,
} from "../src/docs/urls";
import { DOCS_MCP_PATH, handleDocsMcpRequest } from "./docs-mcp";

const MARKDOWN_MEDIA_TYPE = "text/markdown";
const TOKEN_PATTERN = /^[!#$%&'*+\-.^_`|~0-9A-Za-z]+$/;
const QUALITY_PATTERN = /^(?:0(?:\.\d{0,3})?|1(?:\.0{0,3})?)$/;
const PAGE_MARKDOWN_ENTRIES = [
  ...siteDocsPages.map(
    ({ canonicalPath, markdownPath }) => [canonicalPath, markdownPath] as const
  ),
  ...docsProducts.flatMap((product) =>
    product.versions.flatMap((version) =>
      version.sections.flatMap((section) =>
        section.items.map(
          (page) =>
            [
              getPageHref(version, page),
              withDocsBase(`/${version.basePath}/${page.slug}.md`),
            ] as const
        )
      )
    )
  ),
];
const PAGE_MARKDOWN_PATHS = new Map(PAGE_MARKDOWN_ENTRIES);
const CANONICAL_SITE_PATHS = new Set(
  siteDocsPages.map(({ canonicalPath }) => canonicalPath)
);
const VERSION_ROOT_REDIRECTS = new Map<string, string>(
  docsProducts.flatMap((product) =>
    product.versions.flatMap((version) => {
      const canonicalRoot = withDocsBase(`/${version.basePath}/`);

      if (CANONICAL_SITE_PATHS.has(canonicalRoot)) {
        return [[withDocsBase(`/${version.basePath}`), canonicalRoot] as const];
      }

      const target = getPageHref(version, getDefaultPage(product, version));

      return [
        [withDocsBase(`/${version.basePath}`), target] as const,
        [withDocsBase(`/${version.basePath}/`), target] as const,
      ];
    })
  )
);

if (PAGE_MARKDOWN_PATHS.size !== PAGE_MARKDOWN_ENTRIES.length) {
  throw new Error("Documentation pages must have unique canonical paths.");
}

const addVaryAccept = (response: Response): Response => {
  const headers = new Headers(response.headers);
  const vary = headers.get("Vary");
  const variesOnAccept = vary
    ?.split(",")
    .some((value) => value.trim().toLowerCase() === "accept");

  if (!variesOnAccept) {
    headers.set("Vary", vary ? `${vary}, Accept` : "Accept");
  }

  return new Response(response.body, {
    headers,
    status: response.status,
    statusText: response.statusText,
  });
};

const addDirectMarkdownHeaders = (response: Response): Response => {
  if (!(response.ok || response.status === 304)) {
    return response;
  }

  const headers = new Headers(response.headers);
  headers.set("Content-Type", `${MARKDOWN_MEDIA_TYPE}; charset=utf-8`);
  headers.set("X-Content-Type-Options", "nosniff");

  return new Response(response.body, {
    headers,
    status: response.status,
    statusText: response.statusText,
  });
};

const addGlobalSecurityHeaders = (response: Response): Response => {
  const headers = new Headers(response.headers);

  for (const [name, value] of Object.entries(GLOBAL_SECURITY_HEADERS)) {
    headers.set(name, value);
  }

  return new Response(response.body, {
    headers,
    status: response.status,
    statusText: response.statusText,
  });
};

const getPageDiscoveryLink = (markdownPath: string): string =>
  `<${markdownPath}>; rel="alternate"; type="text/markdown", <${withDocsBase("/llms.txt")}>; rel="describedby"; type="text/plain", ${API_CATALOG_LINK_VALUE}`;

const addCanonicalPageDiscovery = (
  response: Response,
  markdownPath: string
): Response => {
  const contentType = response.headers.get("Content-Type");
  const isHtml =
    response.ok && contentType?.toLowerCase().startsWith("text/html");

  if (!(isHtml || response.status === 304)) {
    return response;
  }

  const headers = new Headers(response.headers);
  headers.set("Link", getPageDiscoveryLink(markdownPath));

  return new Response(response.body, {
    headers,
    status: response.status,
    statusText: response.statusText,
  });
};

interface MediaRange {
  parameters: ReadonlyMap<string, string>;
  quality: number;
  subtype: string;
  type: string;
}

interface Representation {
  parameters: ReadonlyMap<string, string>;
  subtype: string;
  type: string;
}

const HTML_REPRESENTATION = {
  parameters: new Map([["charset", "utf-8"]]),
  subtype: "html",
  type: "text",
} satisfies Representation;

const MARKDOWN_REPRESENTATION = {
  parameters: new Map([["charset", "utf-8"]]),
  subtype: "markdown",
  type: "text",
} satisfies Representation;

const splitOutsideQuotes = (
  value: string,
  delimiter: "," | ";"
): string[] | undefined => {
  const parts: string[] = [];
  let escaped = false;
  let quoted = false;
  let start = 0;

  for (let index = 0; index < value.length; index += 1) {
    const character = value[index];

    if (escaped) {
      escaped = false;
      continue;
    }

    if (quoted && character === "\\") {
      escaped = true;
      continue;
    }

    if (character === '"') {
      quoted = !quoted;
      continue;
    }

    if (!quoted && character === delimiter) {
      parts.push(value.slice(start, index));
      start = index + 1;
    }
  }

  if (quoted || escaped) {
    return undefined;
  }

  parts.push(value.slice(start));
  return parts;
};

const parseParameterValue = (value: string): string | undefined => {
  const trimmedValue = value.trim();

  if (!trimmedValue.startsWith('"')) {
    return TOKEN_PATTERN.test(trimmedValue)
      ? trimmedValue.toLowerCase()
      : undefined;
  }

  if (trimmedValue.length < 2 || !trimmedValue.endsWith('"')) {
    return undefined;
  }

  let parsedValue = "";
  let escaped = false;

  for (const character of trimmedValue.slice(1, -1)) {
    if (escaped) {
      parsedValue += character;
      escaped = false;
    } else if (character === "\\") {
      escaped = true;
    } else if (character === '"') {
      return undefined;
    } else {
      parsedValue += character;
    }
  }

  return escaped ? undefined : parsedValue.toLowerCase();
};

const parseMediaRange = (value: string): MediaRange | undefined => {
  const parts = splitOutsideQuotes(value, ";");
  if (!parts) {
    return undefined;
  }

  const mediaTypeParts = parts[0]?.trim().toLowerCase().split("/");
  if (!mediaTypeParts || mediaTypeParts.length !== 2) {
    return undefined;
  }

  const [type, subtype] = mediaTypeParts;
  if (
    !(type && subtype) ||
    !TOKEN_PATTERN.test(type) ||
    !TOKEN_PATTERN.test(subtype) ||
    (type === "*" && subtype !== "*")
  ) {
    return undefined;
  }

  const parameters = new Map<string, string>();
  let quality = 1;
  let sawQuality = false;

  for (const rawParameter of parts.slice(1)) {
    const separator = rawParameter.indexOf("=");
    const name = rawParameter
      .slice(0, separator < 0 ? undefined : separator)
      .trim()
      .toLowerCase();

    if (!TOKEN_PATTERN.test(name)) {
      return undefined;
    }

    if (name === "q") {
      const rawQuality = rawParameter.slice(separator + 1).trim();
      if (sawQuality || separator < 0 || !QUALITY_PATTERN.test(rawQuality)) {
        return undefined;
      }

      quality = Number(rawQuality);
      sawQuality = true;
      continue;
    }

    const parameterValue = parseParameterValue(
      rawParameter.slice(separator + 1)
    );
    if (separator < 0 || parameterValue === undefined || parameters.has(name)) {
      return undefined;
    }

    parameters.set(name, parameterValue);
  }

  return { parameters, quality, subtype, type };
};

const parseMediaRanges = (accept: string): MediaRange[] => {
  const rangeValues = splitOutsideQuotes(accept, ",");
  if (!rangeValues) {
    return [];
  }

  return rangeValues.flatMap((rangeValue) => {
    const range = parseMediaRange(rangeValue);

    return range ? [range] : [];
  });
};

const matchesRepresentation = (
  range: MediaRange,
  representation: Representation
): boolean => {
  const matchesType =
    (range.type === "*" || range.type === representation.type) &&
    (range.subtype === "*" || range.subtype === representation.subtype);

  return (
    matchesType &&
    [...range.parameters].every(
      ([name, value]) => representation.parameters.get(name) === value
    )
  );
};

const getRepresentationQuality = (
  ranges: MediaRange[],
  representation: Representation
): number => {
  let bestParameterSpecificity = -1;
  let bestQuality = 0;
  let bestTypeSpecificity = -1;

  for (const range of ranges) {
    if (!matchesRepresentation(range, representation)) {
      continue;
    }

    const typeSpecificity =
      range.type === "*" ? 0 : range.subtype === "*" ? 1 : 2;
    const parameterSpecificity = range.parameters.size;
    const isMoreSpecific =
      typeSpecificity > bestTypeSpecificity ||
      (typeSpecificity === bestTypeSpecificity &&
        parameterSpecificity > bestParameterSpecificity);

    if (isMoreSpecific) {
      bestParameterSpecificity = parameterSpecificity;
      bestQuality = range.quality;
      bestTypeSpecificity = typeSpecificity;
    } else if (
      typeSpecificity === bestTypeSpecificity &&
      parameterSpecificity === bestParameterSpecificity
    ) {
      bestQuality = Math.max(bestQuality, range.quality);
    }
  }

  return bestQuality;
};

// Wildcards can affect quality, but only an explicit text/markdown range opts
// into Markdown so generic Accept headers keep the canonical HTML response.
export const acceptsMarkdown = (accept: string | null): boolean => {
  if (!accept) {
    return false;
  }

  const ranges = parseMediaRanges(accept);
  const hasExplicitMarkdownRange = ranges.some(
    (range) =>
      range.type === MARKDOWN_REPRESENTATION.type &&
      range.subtype === MARKDOWN_REPRESENTATION.subtype &&
      matchesRepresentation(range, MARKDOWN_REPRESENTATION)
  );
  const markdownQuality = getRepresentationQuality(
    ranges,
    MARKDOWN_REPRESENTATION
  );

  return (
    hasExplicitMarkdownRange &&
    markdownQuality > 0 &&
    markdownQuality >= getRepresentationQuality(ranges, HTML_REPRESENTATION)
  );
};

export const getMarkdownPath = (pathname: string): string | undefined => {
  return PAGE_MARKDOWN_PATHS.get(pathname);
};

const getAsset = (
  assets: Fetcher,
  request: Request,
  pathname?: string
): Promise<Response> => {
  if (!pathname) {
    return assets.fetch(request);
  }

  const url = new URL(request.url);
  url.pathname = pathname;

  return assets.fetch(
    new Request(url, {
      headers: request.headers,
      method: request.method,
      redirect: request.redirect,
    })
  );
};

const getContentSecurityPolicy = async (
  assets: Fetcher,
  request: Request
): Promise<string> => {
  const policyUrl = new URL(request.url);
  policyUrl.hash = "";
  policyUrl.pathname = CONTENT_SECURITY_POLICY_ASSET_PATH;
  policyUrl.search = "";
  const policyResponse = await assets.fetch(
    new Request(policyUrl, {
      headers: { Accept: "text/plain" },
      method: "GET",
      redirect: "manual",
    })
  );

  if (!policyResponse.ok) {
    await policyResponse.body?.cancel();
    throw new Error(
      `The generated Content-Security-Policy asset returned HTTP ${policyResponse.status}.`
    );
  }

  const policy = (await policyResponse.text()).trim();

  if (!policy || policy.length > 2000 || /[\r\n]/.test(policy)) {
    throw new Error("The generated Content-Security-Policy asset is invalid.");
  }

  return policy;
};

const addContentSecurityPolicy = async (
  response: Response,
  request: Request,
  assets: Fetcher
): Promise<Response> => {
  const contentType = response.headers.get("Content-Type")?.toLowerCase();
  const isHtml = contentType?.startsWith("text/html");

  if (!isHtml) {
    return response;
  }

  const headers = new Headers(response.headers);
  headers.set(
    CONTENT_SECURITY_POLICY_HEADER,
    await getContentSecurityPolicy(assets, request)
  );

  return new Response(response.body, {
    headers,
    status: response.status,
    statusText: response.statusText,
  });
};

const routeRequest = async (
  request: Request,
  assets: Fetcher,
  mcpRateLimiter?: RateLimit
): Promise<Response> => {
  const url = new URL(request.url);

  if (url.pathname === DOCS_MCP_PATH) {
    if (!mcpRateLimiter) {
      return Response.json(
        {
          error: {
            code: -32603,
            message: "The MCP endpoint is temporarily unavailable.",
          },
          id: null,
          jsonrpc: "2.0",
        },
        {
          headers: {
            "Cache-Control": "no-store",
            "Content-Type": "application/json; charset=utf-8",
          },
          status: 503,
        }
      );
    }

    return handleDocsMcpRequest(request, assets, mcpRateLimiter);
  }

  if (request.method !== "GET" && request.method !== "HEAD") {
    return assets.fetch(request);
  }

  const versionRootTarget = VERSION_ROOT_REDIRECTS.get(url.pathname);

  if (versionRootTarget) {
    return new Response(null, {
      headers: { Location: `${versionRootTarget}${url.search}` },
      status: 307,
    });
  }

  if (url.pathname.endsWith(".md")) {
    return addDirectMarkdownHeaders(await getAsset(assets, request));
  }

  const markdownPath = getMarkdownPath(url.pathname);

  if (markdownPath && acceptsMarkdown(request.headers.get("Accept"))) {
    const markdownResponse = await getAsset(assets, request, markdownPath);
    const contentType = markdownResponse.headers.get("Content-Type");

    const isNotModified = markdownResponse.status === 304;
    const isMarkdown =
      markdownResponse.ok &&
      contentType?.toLowerCase().startsWith(MARKDOWN_MEDIA_TYPE);

    if (isNotModified || isMarkdown) {
      const headers = new Headers(markdownResponse.headers);
      headers.set("Content-Type", `${MARKDOWN_MEDIA_TYPE}; charset=utf-8`);
      headers.set("Content-Location", markdownPath);
      headers.set("Link", getPageDiscoveryLink(markdownPath));

      return addVaryAccept(
        new Response(markdownResponse.body, {
          headers,
          status: markdownResponse.status,
          statusText: markdownResponse.statusText,
        })
      );
    }

    await markdownResponse.body?.cancel();
  }

  const response = await getAsset(assets, request);

  return addVaryAccept(
    markdownPath ? addCanonicalPageDiscovery(response, markdownPath) : response
  );
};

export const handleRequest = async (
  request: Request,
  assets: Fetcher,
  mcpRateLimiter?: RateLimit
): Promise<Response> => {
  const requestUrl = new URL(request.url);

  if (requestUrl.origin === LEGACY_DOCS_ORIGIN) {
    const location = new URL(withDocsBase(requestUrl.pathname), ASTILBA_ORIGIN);
    location.search = requestUrl.search;

    return addGlobalSecurityHeaders(
      new Response(null, {
        headers: { Location: location.href },
        status: 308,
      })
    );
  }

  const response = await routeRequest(request, assets, mcpRateLimiter);
  let securedResponse: Response;

  try {
    securedResponse = await addContentSecurityPolicy(response, request, assets);
  } catch (error) {
    console.error(
      JSON.stringify({
        error: error instanceof Error ? error.message : String(error),
        event: "docs.content_security_policy_unavailable",
      })
    );
    await response.body?.cancel();
    securedResponse = new Response(
      "Documentation is temporarily unavailable.",
      {
        headers: {
          "Cache-Control": "no-store",
          "Content-Type": "text/plain; charset=utf-8",
          "X-Content-Type-Options": "nosniff",
        },
        status: 503,
      }
    );
  }

  return addGlobalSecurityHeaders(securedResponse);
};

export default {
  fetch(request, env) {
    return handleRequest(request, env.ASSETS, env.MCP_RATE_LIMITER);
  },
} satisfies ExportedHandler<Env>;
