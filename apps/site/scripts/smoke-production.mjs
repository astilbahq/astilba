const ORIGIN = "https://astilba.com";
const USER_AGENT = "astilba-site-production-smoke/1.0";

const readInteger = (name, fallback, maximum) => {
  const rawValue = process.env[name];

  if (rawValue === undefined) {
    return fallback;
  }

  const value = Number(rawValue);
  if (!Number.isSafeInteger(value) || value < 1 || value > maximum) {
    throw new Error(`${name} must be an integer from 1 to ${maximum}.`);
  }

  return value;
};

const attempts = readInteger("ASTILBA_SITE_SMOKE_ATTEMPTS", 6, 10);
const requestTimeoutMs = readInteger(
  "ASTILBA_SITE_SMOKE_REQUEST_TIMEOUT_MS",
  10_000,
  30_000
);
const retryDelayMs = readInteger(
  "ASTILBA_SITE_SMOKE_RETRY_DELAY_MS",
  2_000,
  30_000
);
const origin = new URL(process.env.ASTILBA_SITE_SMOKE_ORIGIN ?? ORIGIN);
const isLoopback = new Set(["127.0.0.1", "[::1]", "localhost"]).has(
  origin.hostname
);

if (
  origin.username ||
  origin.password ||
  origin.pathname !== "/" ||
  origin.search ||
  origin.hash ||
  (origin.protocol !== "https:" && !(origin.protocol === "http:" && isLoopback))
) {
  throw new Error(
    "ASTILBA_SITE_SMOKE_ORIGIN must be an HTTPS origin, or an HTTP loopback origin."
  );
}

const sleep = (durationMs) =>
  new Promise((resolve) => {
    setTimeout(resolve, durationMs);
  });

const request = (path) =>
  fetch(new URL(path, origin), {
    headers: { "User-Agent": USER_AGENT },
    redirect: "manual",
    signal: AbortSignal.timeout(requestTimeoutMs),
  });

const requireStatus = (response, status, label) => {
  if (response.status !== status) {
    throw new Error(
      `${label} returned HTTP ${response.status}; expected ${status}.`
    );
  }
};

const requireHeaderIncludes = (response, name, expected, label) => {
  const actual = response.headers.get(name) ?? "";
  if (!actual.toLowerCase().includes(expected.toLowerCase())) {
    throw new Error(`${label} returned unexpected ${name}: ${actual}`);
  }
};

const requireBodyIncludes = (body, expected, label) => {
  if (!body.includes(expected)) {
    throw new Error(`${label} did not include ${expected}.`);
  }
};

const run = async () => {
  const homeResponse = await request("/");
  requireStatus(homeResponse, 200, "Homepage");
  requireHeaderIncludes(
    homeResponse,
    "Content-Security-Policy",
    "default-src 'none'",
    "Homepage"
  );
  requireHeaderIncludes(
    homeResponse,
    "Content-Signal",
    "search=yes",
    "Homepage"
  );
  const home = await homeResponse.text();
  requireBodyIncludes(
    home,
    'href="https://astilba.com/" rel="canonical"',
    "Homepage"
  );
  requireBodyIncludes(home, "@astilba/env", "Homepage");
  requireBodyIncludes(home, "0.3.0 is a public alpha", "Homepage");
  requireBodyIncludes(home, "Cache remains a development preview", "Homepage");

  const cacheResponse = await request("/cache/");
  requireStatus(cacheResponse, 200, "Cache page");
  requireBodyIncludes(
    await cacheResponse.text(),
    "No npm package",
    "Cache page"
  );

  const envResponse = await request("/env/");
  requireStatus(envResponse, 200, "Env page");
  const env = await envResponse.text();
  requireBodyIncludes(env, "0.3.0 · public alpha · local-first", "Env page");
  requireBodyIncludes(env, "github.com/astilbahq/env", "Env page");

  const llmsResponse = await request("/llms.txt");
  requireStatus(llmsResponse, 200, "LLMs.txt");
  requireHeaderIncludes(llmsResponse, "Content-Type", "text/plain", "LLMs.txt");
  const llms = await llmsResponse.text();
  requireBodyIncludes(llms, "[Env](https://astilba.com/env/)", "LLMs.txt");
  requireBodyIncludes(llms, "[Cache](https://astilba.com/cache/)", "LLMs.txt");

  const skillsResponse = await request("/.well-known/agent-skills/index.json");
  requireStatus(skillsResponse, 200, "Agent Skills discovery");
  requireHeaderIncludes(
    skillsResponse,
    "Content-Type",
    "application/json",
    "Agent Skills discovery"
  );
  const discovery = await skillsResponse.json();
  const skillNames = discovery.skills?.map(({ name }) => name);
  if (
    !Array.isArray(skillNames) ||
    skillNames.length !== 2 ||
    !skillNames.includes("astilba-env-docs") ||
    !skillNames.includes("astilba-cache-docs")
  ) {
    throw new Error(
      "Agent Skills discovery must publish the Env and Cache skills."
    );
  }

  const docsRedirect = await request("/docs");
  requireStatus(docsRedirect, 308, "Docs canonical redirect");
  const expectedLocation = new URL("/docs/", origin).href;
  if (docsRedirect.headers.get("Location") !== expectedLocation) {
    throw new Error(
      `Docs canonical redirect returned unexpected Location: ${docsRedirect.headers.get("Location")}`
    );
  }

  const catalogResponse = await request("/.well-known/api-catalog");
  requireStatus(catalogResponse, 200, "API catalog");
  requireHeaderIncludes(
    catalogResponse,
    "Content-Type",
    "application/linkset+json",
    "API catalog"
  );
  requireBodyIncludes(
    await catalogResponse.text(),
    "https://astilba.com/docs/mcp",
    "API catalog"
  );

  const sitemapResponse = await request("/sitemap.xml");
  requireStatus(sitemapResponse, 200, "Sitemap index");
  requireBodyIncludes(
    await sitemapResponse.text(),
    "https://astilba.com/docs/sitemap.xml",
    "Sitemap index"
  );

  const siteSitemapResponse = await request("/sitemap-site.xml");
  requireStatus(siteSitemapResponse, 200, "Site sitemap");
  const siteSitemap = await siteSitemapResponse.text();
  requireBodyIncludes(
    siteSitemap,
    "<loc>https://astilba.com/env/</loc>",
    "Site sitemap"
  );
};

let lastError;
for (let attempt = 1; attempt <= attempts; attempt += 1) {
  try {
    await run();
    process.stdout.write("Astilba site production smoke passed.\n");
    lastError = undefined;
    break;
  } catch (error) {
    lastError = error;
    if (attempt < attempts) {
      await sleep(retryDelayMs);
      continue;
    }

    break;
  }
}

if (lastError instanceof Error) {
  throw lastError;
}

if (lastError !== undefined) {
  throw new Error("Astilba site production smoke failed.", {
    cause: lastError,
  });
}
