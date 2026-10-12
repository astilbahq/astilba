export const ASTILBA_ORIGIN = "https://astilba.com";
export const DOCS_BASE_PATH = "/docs";
export const LEGACY_DOCS_ORIGIN = "https://docs.astilba.com";

const ensureAbsolutePath = (path: string): string =>
  path.startsWith("/") ? path : `/${path}`;

/** Prefix a documentation-relative path with the canonical `/docs` mount. */
export const withDocsBase = (path = "/"): string => {
  const absolutePath = ensureAbsolutePath(path);

  if (
    absolutePath === DOCS_BASE_PATH ||
    absolutePath.startsWith(`${DOCS_BASE_PATH}/`)
  ) {
    return absolutePath;
  }

  return absolutePath === "/"
    ? `${DOCS_BASE_PATH}/`
    : `${DOCS_BASE_PATH}${absolutePath}`;
};

export const docsUrl = (path = "/"): string =>
  new URL(withDocsBase(path), ASTILBA_ORIGIN).href;

/** Remove the canonical mount from a documentation path for catalog checks. */
export const withoutDocsBase = (path: string): string => {
  const absolutePath = ensureAbsolutePath(path);

  if (absolutePath === DOCS_BASE_PATH) {
    return "/";
  }

  return absolutePath.startsWith(`${DOCS_BASE_PATH}/`)
    ? absolutePath.slice(DOCS_BASE_PATH.length)
    : absolutePath;
};
