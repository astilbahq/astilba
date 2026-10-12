import { access, mkdir, readFile, readdir, writeFile } from "node:fs/promises";
import { dirname, join, relative, resolve } from "node:path";

import { CONTENT_SECURITY_POLICY_ASSET_PATH } from "../src/docs/security.ts";
import { DOCS_BASE_PATH } from "../src/docs/urls.ts";
import {
  createContentSecurityPolicy,
  getInlineScriptHashes,
} from "./security-headers.mjs";

const dist = resolve(process.cwd(), "dist");
const headersPath = resolve(dist, "_headers");
const docsDist = resolve(dist, DOCS_BASE_PATH.slice(1));
const headersTemplatePath = resolve(process.cwd(), "public/_headers");
const contentSecurityPolicyAssetPath = resolve(
  dist,
  CONTENT_SECURITY_POLICY_ASSET_PATH.slice(1)
);
const maxHeaderRules = 100;
const siteValue = process.env.ASTILBA_DOCS_SITE;

if (!siteValue) {
  throw new Error(
    "[agent-headers] ASTILBA_DOCS_SITE is required to generate canonical response links."
  );
}

const site = new URL(siteValue);

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

const fileExists = async (path) => {
  try {
    await access(path);
    return true;
  } catch (error) {
    if (error?.code === "ENOENT") {
      return false;
    }

    throw error;
  }
};

const staticHeaders = (await readFile(headersTemplatePath, "utf8")).trimEnd();
const files = await collectFiles(docsDist);
const htmlDocuments = await Promise.all(
  files
    .filter((file) => file.endsWith(".html"))
    .map((file) => readFile(file, "utf8"))
);
const inlineScriptHashes = getInlineScriptHashes(htmlDocuments);
const contentSecurityPolicy = createContentSecurityPolicy(inlineScriptHashes);
const contentSecurityPolicyHeader = `  Content-Security-Policy: ${contentSecurityPolicy}`;
const maxHeaderLineLength = 2000;

if (contentSecurityPolicyHeader.length > maxHeaderLineLength) {
  throw new Error(
    `[agent-headers] Generated Content-Security-Policy exceeds Cloudflare's ${maxHeaderLineLength}-character header-line limit.`
  );
}
let markdownPageCount = 0;

for (const file of files) {
  if (!file.endsWith("index.html")) {
    continue;
  }

  const html = await readFile(file, "utf8");
  const markdownLinks = (html.match(/<link\b[^>]*>/g) ?? []).filter(
    (tag) =>
      tag.includes('rel="alternate"') && tag.includes('type="text/markdown"')
  );

  if (markdownLinks.length === 0) {
    continue;
  }

  if (markdownLinks.length !== 1) {
    throw new Error(
      `[agent-headers] ${relative(dist, file)} must advertise exactly one Markdown alternate, found ${markdownLinks.length}.`
    );
  }

  const href = markdownLinks[0].match(/\bhref="([^"]+)"/)?.[1];

  if (!href) {
    throw new Error(
      `[agent-headers] ${relative(dist, file)} has a Markdown alternate without an href.`
    );
  }

  const markdownUrl = new URL(href, site);

  if (
    markdownUrl.origin !== site.origin ||
    markdownUrl.search ||
    markdownUrl.hash
  ) {
    throw new Error(
      `[agent-headers] ${relative(dist, file)} has a non-canonical Markdown alternate: ${href}.`
    );
  }

  const markdownPath = decodeURIComponent(markdownUrl.pathname.slice(1));

  if (
    !markdownPath.endsWith(".md") ||
    !(await fileExists(resolve(dist, markdownPath)))
  ) {
    throw new Error(
      `[agent-headers] ${relative(dist, file)} points to missing dist/${markdownPath}.`
    );
  }

  markdownPageCount += 1;
}

if (markdownPageCount === 0) {
  throw new Error(
    "[agent-headers] No HTML pages with Markdown alternates were found."
  );
}

const staticRuleCount = staticHeaders.split(/\n\s*\n/).length;

if (staticRuleCount > maxHeaderRules) {
  throw new Error(
    `[agent-headers] ${staticRuleCount} static header rules exceed Cloudflare's ${maxHeaderRules}-rule limit.`
  );
}

if (/^\s+Content-Security-Policy:/im.test(staticHeaders)) {
  throw new Error(
    "[agent-headers] The source headers must not contain a static Content-Security-Policy; the build generates its inline-script hashes."
  );
}

const rootRulePrefix = `${DOCS_BASE_PATH}/*\n`;

if (!staticHeaders.startsWith(rootRulePrefix)) {
  throw new Error(
    `[agent-headers] dist/_headers must begin with the global ${DOCS_BASE_PATH}/* rule.`
  );
}

const generatedHeaders = staticHeaders.replace(
  rootRulePrefix,
  `${rootRulePrefix}${contentSecurityPolicyHeader}\n`
);

await mkdir(dirname(contentSecurityPolicyAssetPath), { recursive: true });
await Promise.all([
  writeFile(headersPath, `${generatedHeaders}\n`, "utf8"),
  writeFile(
    contentSecurityPolicyAssetPath,
    `${contentSecurityPolicy}\n`,
    "utf8"
  ),
]);

console.log(
  `[agent-headers] Validated ${markdownPageCount} Worker-managed Markdown alternates and ${inlineScriptHashes.length} CSP script hashes (${staticRuleCount}/${maxHeaderRules} static header rules).`
);
