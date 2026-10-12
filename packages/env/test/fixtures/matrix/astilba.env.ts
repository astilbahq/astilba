import { defineEnvironment, env } from "@astilba/env";

export default defineEnvironment({
  consumers: {
    server: env.server(),
    web: env.browser(["apiOrigin", "clientMode", "requestLabel"]),
  },
  entries: {
    apiOrigin: env.public.deployment.origin(),
    clientMode: env.public.build.enum(["standard", "compact"]),
    internalValue: env.private.deployment.secret(),
    requestLabel: env.public.request.string({ required: false }),
  },
  id: "com.example.matrix",
  targets: {
    serverDeployment: env.process("server", {
      apiOrigin: "API_ORIGIN",
      internalValue: "INTERNAL_VALUE",
    }),
    serverRequest: env.process("server", { requestLabel: "REQUEST_LABEL" }),
    webBuild: env.process("web", { clientMode: "CLIENT_MODE" }),
    webDeployment: env.process("web", { apiOrigin: "API_ORIGIN" }),
    webRequest: env.process("web", { requestLabel: "REQUEST_LABEL" }),
  },
});
