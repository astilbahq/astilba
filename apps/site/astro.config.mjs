// @ts-check
import react from "@astrojs/react";
import { defineConfig } from "astro/config";

import { check as checkSiteBuild } from "../docs/.astilba/env/siteBuild.server.ts";
import { resolveCanonicalOrigin } from "../docs/src/env/origin.ts";

const siteBuild = checkSiteBuild(process.env);
const site = resolveCanonicalOrigin("ASTILBA_SITE", siteBuild, "siteOrigin");

/** @type {import("astro").AstroIntegration} */
const requireSite = {
  name: "astilba-require-site",
  hooks: {
    "astro:config:setup"({ command }) {
      if (command === "build" && !site) {
        throw new Error(
          "ASTILBA_SITE is required for production builds so canonical URLs cannot be generated with the wrong origin."
        );
      }
    },
  },
};

export default defineConfig({
  site,
  integrations: [requireSite, react()],
  build: {
    inlineStylesheets: "never",
  },
  trailingSlash: "always",
});
