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
  id: "com.astilba.examples.vite",
  targets: {
    browserBuild: env.process("browser", { appName: "VITE_APP_NAME" }),
    browserDeployment: env.process("browser", { label: "VITE_LABEL" }),
    bootstrapDeployment: env.process("bootstrap", {
      browserOrigin: "VITE_PUBLIC_ORIGIN",
      label: "VITE_LABEL",
    }),
    serverDeployment: env.process("server", {
      browserOrigin: "VITE_PUBLIC_ORIGIN",
      label: "VITE_LABEL",
      serviceToken: "VITE_SERVICE_TOKEN",
    }),
  },
});
