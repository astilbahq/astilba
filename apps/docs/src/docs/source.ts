import { assertDocsContentPath } from "./content-history.ts";

const DOCS_CONTENT_ROOT = "src/content/docs/";
const DOCS_SOURCE_BASE = new URL(
  "https://github.com/astilbahq/astilba/blob/main/apps/docs/"
);

export const getDocsSourcePath = (
  filePath: string | undefined,
  entryId: string
): string => {
  const normalizedFilePath = filePath?.replaceAll("\\", "/") ?? "";
  const sourceMarkerIndex = normalizedFilePath.indexOf(DOCS_CONTENT_ROOT);
  const sourceExtension =
    normalizedFilePath.match(/\.(?:md|mdx)$/i)?.[0].toLowerCase() ?? ".md";

  const path =
    sourceMarkerIndex >= 0
      ? normalizedFilePath.slice(sourceMarkerIndex)
      : `${DOCS_CONTENT_ROOT}${entryId}${sourceExtension}`;
  assertDocsContentPath(path);
  return path;
};

export const getDocsSourceUrl = (
  filePath: string | undefined,
  entryId: string
): string =>
  new URL(
    getDocsSourcePath(filePath, entryId)
      .split("/")
      .map(encodeURIComponent)
      .join("/"),
    DOCS_SOURCE_BASE
  ).href;
