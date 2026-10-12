import type { DocsProduct } from "../types.ts";

const cacheSections = [
  {
    label: "Get started",
    items: [
      {
        key: "overview",
        label: "Overview",
        slug: "overview",
        icon: "open-book",
        sourcePath: "src/content/docs/cache/overview.md",
      },
      {
        key: "api-status",
        label: "Implementation status",
        slug: "api-status",
        icon: "approve-check-circle",
        sourcePath: "src/content/docs/cache/api-status.md",
      },
      {
        key: "quickstart",
        label: "Source walkthrough",
        slug: "quickstart",
        icon: "rocket",
        sourcePath: "src/content/docs/cache/quickstart.md",
      },
      {
        key: "core-concepts",
        label: "Cache fundamentals",
        slug: "core-concepts",
        icon: "open-book",
        sourcePath: "src/content/docs/cache/core-concepts.md",
      },
    ],
  },
  {
    label: "Platforms",
    items: [
      {
        key: "cloudflare-workers",
        label: "Cloudflare Workers",
        slug: "cloudflare-workers",
        icon: "cloudflare-workers",
        sourcePath: "src/content/docs/cache/cloudflare-workers.md",
      },
    ],
  },
  {
    label: "Frameworks",
    items: [
      {
        key: "react-and-server-apps",
        label: "React Router",
        slug: "react-and-server-apps",
        icon: "react-router",
        sourcePath: "src/content/docs/cache/react-and-server-apps.md",
      },
    ],
  },
  {
    label: "Guides",
    items: [
      {
        key: "reading-and-filling",
        label: "Read and cache values",
        slug: "reading-and-filling",
        icon: "database",
        sourcePath: "src/content/docs/cache/reading-and-filling.md",
      },
      {
        key: "tags-and-invalidation",
        label: "Invalidate cached data",
        slug: "tags-and-invalidation",
        icon: "link",
        sourcePath: "src/content/docs/cache/tags-and-invalidation.md",
      },
      {
        key: "scopes-and-privacy",
        label: "Control cache sharing",
        slug: "scopes-and-privacy",
        icon: "padlock",
        sourcePath: "src/content/docs/cache/scopes-and-privacy.md",
      },
      {
        key: "response-caching",
        label: "Cache HTTP responses",
        slug: "response-caching",
        icon: "server",
        sourcePath: "src/content/docs/cache/response-caching.md",
      },
      {
        key: "consistency-and-resilience",
        label: "Consistency and resilience",
        slug: "consistency-and-resilience",
        icon: "approve-check-circle",
        sourcePath: "src/content/docs/cache/consistency-and-resilience.md",
      },
      {
        key: "observability",
        label: "Inspect cache behavior",
        slug: "observability",
        icon: "approve-check-circle",
        sourcePath: "src/content/docs/cache/observability.md",
      },
    ],
  },
  {
    label: "Concepts",
    items: [
      {
        key: "how-it-works",
        label: "How Cache works",
        slug: "how-it-works",
        icon: "code-branch",
        sourcePath: "src/content/docs/cache/how-it-works.md",
      },
      {
        key: "architecture",
        label: "Runtime architecture",
        slug: "architecture",
        icon: "code-branch",
        sourcePath: "src/content/docs/cache/architecture.md",
      },
    ],
  },
  {
    label: "Reference",
    items: [
      {
        key: "api-reference",
        label: "API reference",
        slug: "api-reference",
        icon: "open-book",
        sourcePath: "src/content/docs/cache/api-reference.md",
      },
      {
        key: "drivers-and-status",
        label: "Driver implementations",
        slug: "drivers-and-status",
        icon: "server",
        sourcePath: "src/content/docs/cache/drivers-and-status.md",
      },
    ],
  },
] satisfies DocsProduct["versions"][number]["sections"];

export const cacheProduct = {
  id: "cache",
  label: "Cache",
  icon: "database",
  description:
    "A portable server-side TypeScript cache with explicit invalidation, resilience, and privacy boundaries.",
  availabilityNote:
    "Unreleased source snapshot. There is no supported npm release, public checkout, or production installation path yet.",
  homePath: "/cache/",
  repositoryUrl: "https://github.com/astilbahq/docs",
  status: {
    text: "Preview",
    variant: "caution",
  },
  defaultPage: "overview",
  defaultVersion: "unreleased",
  versions: [
    {
      id: "unreleased",
      label: "Unreleased",
      lifecycle: "unreleased",
      basePath: "cache",
      sections: cacheSections,
    },
  ],
} satisfies DocsProduct;
