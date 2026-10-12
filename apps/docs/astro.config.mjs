// @ts-check
import { copyFile, readFile } from "node:fs/promises";

import react from "@astrojs/react";
import sitemap from "@astrojs/sitemap";
import starlight from "@astrojs/starlight";
import { defineConfig } from "astro/config";
import starlightLinksValidator from "starlight-links-validator";
import starlightLlmsTxt from "starlight-llms-txt";

import { check as checkDocsBuild } from "./.astilba/env/docsBuild.server.ts";
import { docsProducts, docsSidebar } from "./src/docs/catalog.ts";
import { removeTerminalWindowHeaders } from "./src/docs/expressive-code.ts";
import { siteDocsPages } from "./src/docs/site-pages.ts";
import {
  addDocsSitemapLastModified,
  createDocsSitemapLastModified,
} from "./src/docs/sitemap.ts";
import { DOCS_BASE_PATH, docsUrl } from "./src/docs/urls.ts";
import { resolveCanonicalOrigin } from "./src/env/origin.ts";

// starlight-llms-txt needs the deployed origin to generate canonical links.
const docsBuild = checkDocsBuild(process.env);
const site = resolveCanonicalOrigin(
  "ASTILBA_DOCS_SITE",
  docsBuild,
  "docsOrigin"
);
const sitemapLastModified = site ? createDocsSitemapLastModified() : new Map();

/** @type {import("astro").AstroIntegration} */
const requireDocsSite = {
  name: "astilba-require-docs-site",
  hooks: {
    "astro:config:setup"({ command }) {
      if (command === "build" && !site) {
        throw new Error(
          "ASTILBA_DOCS_SITE is required for production builds so canonical and agent-readable URLs cannot be generated with the wrong origin."
        );
      }
    },
  },
};

/** @type {import("astro").AstroIntegration} */
const publishSingleSitemap = {
  name: "astilba-publish-single-sitemap",
  hooks: {
    async "astro:build:done"({ dir }) {
      if (!site) {
        return;
      }

      const sitemapIndex = await readFile(
        new URL("sitemap-index.xml", dir),
        "utf8"
      );
      const childLocations = [
        ...sitemapIndex.matchAll(/<loc>([^<]+)<\/loc>/g),
      ].map((match) => match[1]);
      const expectedChild = docsUrl("/sitemap-0.xml");

      if (childLocations.length !== 1 || childLocations[0] !== expectedChild) {
        throw new Error(
          "The direct sitemap requires exactly one generated sitemap child."
        );
      }

      await copyFile(
        new URL("sitemap-0.xml", dir),
        new URL("sitemap.xml", dir)
      );
    },
  },
};

const docsPageOrder = docsProducts.flatMap((product) =>
  product.versions.flatMap((version) =>
    version.sections.flatMap((section) =>
      section.items.map((page) => `${version.basePath}/${page.slug}`)
    )
  )
);
const sitePageOrder = siteDocsPages.map(({ id }) => id);

export default defineConfig({
  base: DOCS_BASE_PATH,
  outDir: "./dist/docs",
  site,
  integrations: [
    requireDocsSite,
    react(),
    sitemap({
      serialize(item) {
        return addDocsSitemapLastModified(item, sitemapLastModified);
      },
    }),
    starlight({
      title: "Astilba",
      description:
        "Documentation for Astilba infrastructure tools and libraries.",
      favicon: "/favicon.svg",
      head: site
        ? [
            {
              tag: "link",
              attrs: {
                href: docsUrl("/sitemap.xml"),
                rel: "sitemap",
              },
            },
          ]
        : [],
      logo: {
        dark: "./src/assets/astilba-logomark-light.svg",
        light: "./src/assets/astilba-logomark-dark.svg",
      },
      social: [
        {
          icon: "github",
          label: "Astilba on GitHub",
          href: "https://github.com/astilbahq",
        },
      ],
      customCss: [
        "@fontsource-variable/inter",
        "@fontsource-variable/geist",
        "@fontsource-variable/jetbrains-mono",
        "./src/styles/panda.css",
        "@astilba/ui/styles.css",
        "./src/styles/starlight.css",
      ],
      expressiveCode: {
        plugins: [removeTerminalWindowHeaders],
      },
      components: {
        Banner: "./src/components/VersionBanner.astro",
        Header: "./src/components/Header.astro",
        Head: "./src/components/Head.astro",
        MarkdownContent: "./src/components/MarkdownContent.astro",
        MobileMenuToggle: "./src/components/MobileMenuToggle.astro",
        Pagination: "./src/components/Pagination.astro",
        PageTitle: "./src/components/PageTitle.astro",
        Sidebar: "./src/components/Sidebar.astro",
      },
      lastUpdated: false,
      tableOfContents: {
        minHeadingLevel: 2,
        maxHeadingLevel: 3,
      },
      plugins: [
        starlightLinksValidator(),
        ...(site
          ? [
              starlightLlmsTxt({
                details:
                  "Astilba Env 0.3.0 is a public alpha; Astilba Cache remains an unreleased preview. Check each product's release or implementation-status page before making availability claims.",
                optionalLinks: [
                  {
                    label: "Astilba documentation MCP",
                    url: docsUrl("/mcp"),
                    description:
                      "Read-only Streamable HTTP endpoint for searching and reading the published documentation corpus.",
                  },
                ],
                customSets: [
                  {
                    label: "Astilba Env",
                    description:
                      "Public-alpha documentation for the local-first @astilba/env configuration contract compiler.",
                    paths: ["env", "env/**"],
                  },
                  {
                    label: "Astilba Cache",
                    description:
                      "Unreleased preview documentation for Astilba's TypeScript caching library. The package is not installable yet.",
                    paths: ["cache", "cache/**"],
                  },
                ],
                promote: [...sitePageOrder, ...docsPageOrder],
              }),
            ]
          : []),
      ],
      routeMiddleware: "./src/docs/route-middleware.ts",
      sidebar: docsSidebar,
    }),
    publishSingleSitemap,
  ],
});
