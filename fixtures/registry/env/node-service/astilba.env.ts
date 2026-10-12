import { defineEnvironment, env } from "@astilba/env";

export default defineEnvironment({
  consumers: { service: env.server() },
  entries: {
    apiOrigin: env.public.deployment.origin(),
    serviceName: env.public.deployment.text(),
  },
  id: "com.astilba.examples.node-service",
  targets: {
    serviceDeployment: env.process("service", {
      apiOrigin: "SERVICE_API_ORIGIN",
      serviceName: "SERVICE_NAME",
    }),
  },
});
