const DOCS_CONTENT_ROOT = "src/content/docs/";
const DOCS_SOURCE_BASE = new URL(
  "https://github.com/astilbahq/docs/blob/main/"
);

export const getDocsSourcePath = (
  filePath: string | undefined,
  entryId: string
): string => {
  const normalizedFilePath = filePath?.replaceAll("\\", "/") ?? "";
  const sourceMarkerIndex = normalizedFilePath.indexOf(DOCS_CONTENT_ROOT);
  const sourceExtension =
    normalizedFilePath.match(/\.(?:md|mdx)$/i)?.[0].toLowerCase() ?? ".md";

  return sourceMarkerIndex >= 0
    ? normalizedFilePath.slice(sourceMarkerIndex)
    : `${DOCS_CONTENT_ROOT}${entryId}${sourceExtension}`;
};

export const getDocsSourceUrl = (
  filePath: string | undefined,
  entryId: string
): string =>
  new URL(getDocsSourcePath(filePath, entryId), DOCS_SOURCE_BASE).href;
