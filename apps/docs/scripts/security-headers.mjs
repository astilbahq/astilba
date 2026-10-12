import { createHash } from "node:crypto";

import { parse } from "parse5";

const sha256SourcePattern = /^'sha256-[A-Za-z0-9+/]{43}='$/;

const visitNodes = (node, visitor) => {
  visitor(node);

  for (const child of node.childNodes ?? []) {
    visitNodes(child, visitor);
  }

  if (node.content) {
    visitNodes(node.content, visitor);
  }
};

export const getInlineScriptHashes = (htmlDocuments) => {
  const hashes = new Set();

  for (const html of htmlDocuments) {
    const document = parse(html);

    visitNodes(document, (node) => {
      if (
        node.nodeName !== "script" ||
        node.attrs?.some(({ name }) => name === "src")
      ) {
        return;
      }

      const source = (node.childNodes ?? [])
        .map((child) => child.value ?? "")
        .join("");

      if (source.length === 0) {
        return;
      }

      hashes.add(
        `'sha256-${createHash("sha256").update(source, "utf8").digest("base64")}'`
      );
    });
  }

  return [...hashes].sort();
};

export const createContentSecurityPolicy = (inlineScriptHashes) => {
  const hashes = [...new Set(inlineScriptHashes)].sort();

  if (hashes.length === 0) {
    throw new Error(
      "[security-headers] At least one inline script hash is required."
    );
  }

  if (!hashes.every((hash) => sha256SourcePattern.test(hash))) {
    throw new Error(
      "[security-headers] Inline script hashes must be quoted SHA-256 sources."
    );
  }

  return [
    "default-src 'none'",
    "base-uri 'none'",
    "connect-src 'self'",
    "font-src 'self' data:",
    "form-action 'none'",
    "frame-ancestors 'none'",
    "frame-src 'none'",
    "img-src 'self' data:",
    "manifest-src 'none'",
    "media-src 'none'",
    "object-src 'none'",
    `script-src 'self' 'wasm-unsafe-eval' ${hashes.join(" ")}`,
    "script-src-attr 'none'",
    "style-src 'self' 'unsafe-inline'",
    "worker-src 'self'",
  ].join("; ");
};
