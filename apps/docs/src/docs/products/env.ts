import type { DocsProduct } from "../types.ts";

const envSections = [
  {
    label: "Get started",
    items: [
      {
        key: "overview",
        label: "Overview",
        slug: "overview",
        icon: "open-book",
        sourcePath: "src/content/docs/env/overview.md",
      },
      {
        key: "quickstart",
        label: "Configure a Node application",
        slug: "quickstart",
        icon: "rocket",
        sourcePath: "src/content/docs/env/quickstart.md",
      },
    ],
  },
  {
    label: "Runtimes",
    items: [
      {
        key: "nodejs",
        label: "Node.js",
        slug: "nodejs",
        icon: "server",
        sourcePath: "src/content/docs/env/nodejs.md",
      },
      {
        key: "browser-runtime",
        label: "Browser",
        slug: "browser-runtime",
        icon: "open-book",
        sourcePath: "src/content/docs/env/browser-runtime.md",
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
        sourcePath: "src/content/docs/env/cloudflare-workers.md",
      },
    ],
  },
  {
    label: "Frameworks",
    items: [
      {
        key: "vite",
        label: "Vite",
        slug: "vite",
        icon: "code-branch",
        sourcePath: "src/content/docs/env/vite.md",
      },
      {
        key: "nextjs",
        label: "Next.js",
        slug: "nextjs",
        icon: "code-branch",
        sourcePath: "src/content/docs/env/nextjs.md",
      },
    ],
  },
  {
    label: "Guides",
    items: [
      {
        key: "browser-delivery",
        label: "Deliver browser configuration",
        slug: "browser-delivery",
        icon: "server",
        sourcePath: "src/content/docs/env/browser-delivery.md",
      },
      {
        key: "inventory-and-drift",
        label: "Check name inventory drift",
        slug: "inventory-and-drift",
        icon: "approve-check-circle",
        sourcePath: "src/content/docs/env/inventory-and-drift.md",
      },
      {
        key: "migrate-from-next-dynamic-env",
        label: "Migrate from next-dynamic-env",
        slug: "migrate-from-next-dynamic-env",
        icon: "code-branch",
        sourcePath: "src/content/docs/env/migrate-from-next-dynamic-env.md",
      },
    ],
  },
  {
    label: "Concepts",
    items: [
      {
        key: "lifecycles-and-projections",
        label: "Lifecycles and projections",
        slug: "lifecycles-and-projections",
        icon: "code-branch",
        sourcePath: "src/content/docs/env/lifecycles-and-projections.md",
      },
      {
        key: "validation-and-standard-schema",
        label: "Validation and Standard Schema",
        slug: "validation-and-standard-schema",
        icon: "approve-check-circle",
        sourcePath: "src/content/docs/env/validation-and-standard-schema.md",
      },
    ],
  },
  {
    label: "Reference",
    items: [
      {
        key: "declaration-reference",
        label: "Declaration reference",
        slug: "declaration-reference",
        icon: "file-digit",
        sourcePath: "src/content/docs/env/declaration-reference.md",
      },
      {
        key: "cli-reference",
        label: "CLI reference",
        slug: "cli-reference",
        icon: "open-book",
        sourcePath: "src/content/docs/env/cli-reference.md",
      },
      {
        key: "release-and-support",
        label: "Release and support",
        slug: "release-and-support",
        icon: "approve-check-circle",
        sourcePath: "src/content/docs/env/release-and-support.md",
      },
    ],
  },
] satisfies DocsProduct["versions"][number]["sections"];

export const envProduct = {
  id: "env",
  label: "Env",
  icon: "file-digit",
  description:
    "A local-first configuration contract compiler with explicit lifecycles and physically separated browser and server projections.",
  availabilityNote:
    "Released as @astilba/env 0.3.0 for public-alpha evaluation. Expect deliberate breaking changes before a stable release.",
  homePath: "/env/",
  repositoryUrl: "https://github.com/astilbahq/env",
  status: {
    text: "Public alpha",
    variant: "caution",
  },
  defaultPage: "overview",
  defaultVersion: "0.3",
  versions: [
    {
      id: "0.3",
      label: "0.3",
      lifecycle: "latest",
      basePath: "env",
      sections: envSections,
    },
  ],
} satisfies DocsProduct;
