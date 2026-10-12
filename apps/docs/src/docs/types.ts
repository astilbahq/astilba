export const docsIcons = [
  "approve-check-circle",
  "cloudflare-workers",
  "code-branch",
  "database",
  "file-digit",
  "link",
  "model-context-protocol",
  "open-book",
  "padlock",
  "react-router",
  "rocket",
  "server",
] as const;

export type DocsIcon = (typeof docsIcons)[number];

type DocsBadgeVariant =
  | "caution"
  | "danger"
  | "default"
  | "note"
  | "success"
  | "tip";

export interface DocsBadge {
  text: string;
  variant: DocsBadgeVariant;
}

export interface DocsPage {
  badge?: DocsBadge;
  icon: DocsIcon;
  key: string;
  label: string;
  slug: string;
  sourcePath: string;
}

interface DocsSection {
  items: DocsPage[];
  label: string;
}

type DocsVersionLifecycle = "archived" | "latest" | "maintained" | "unreleased";

export interface DocsVersion {
  basePath: string;
  id: string;
  label: string;
  lifecycle: DocsVersionLifecycle;
  sections: DocsSection[];
}

export interface DocsProduct {
  availabilityNote: string;
  defaultPage: string;
  defaultVersion: string;
  description: string;
  homePath: string;
  icon: DocsIcon;
  id: string;
  label: string;
  repositoryUrl: string;
  status?: DocsBadge;
  versions: DocsVersion[];
}

export interface DocsContext {
  page: DocsPage;
  product: DocsProduct;
  version: DocsVersion;
}

export interface DocsProductContext {
  product: DocsProduct;
  version: DocsVersion;
}
