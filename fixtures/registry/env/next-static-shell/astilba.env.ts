import { defineEnvironment, env } from "@astilba/env";

export default defineEnvironment({
  consumers: {
    bootstrap: env.server(["browserOrigin", "label"]),
    browser: env.browser(["appName", "label"]),
    server: env.server(),
  },
  entries: {
    appName: env.public.build.string(),
    browserOrigin: env.public.deployment.origin({ required: false }),
    label: env.public.deployment.string(),
    serviceToken: env.private.deployment.secret(),
  },
  id: "com.astilba.examples.next-static-shell",
  targets: {
    browserBuild: env.process("browser", { appName: "NEXT_APP_NAME" }),
    browserDeployment: env.process("browser", { label: "NEXT_LABEL" }),
    bootstrapDeployment: env.process("bootstrap", {
      browserOrigin: "NEXT_CANONICAL_ORIGIN",
      label: "NEXT_LABEL",
    }),
    serverDeployment: env.process("server", {
      browserOrigin: "NEXT_CANONICAL_ORIGIN",
      label: "NEXT_LABEL",
      serviceToken: "NEXT_SERVICE_TOKEN",
    }),
  },
});
