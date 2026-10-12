import { defineEnvironment, env } from "@astilba/env";

export default defineEnvironment({
  id: "com.astilba.web",
  entries: {
    docsOrigin: env.public.build.origin({ required: false }),
    siteOrigin: env.public.build.origin({ required: false }),
  },
  consumers: {
    docs: env.server(["docsOrigin"]),
    site: env.server(["siteOrigin"]),
  },
  targets: {
    docsBuild: env.process("docs", {
      docsOrigin: "ASTILBA_DOCS_SITE",
    }),
    siteBuild: env.process("site", {
      siteOrigin: "ASTILBA_SITE",
    }),
  },
});
