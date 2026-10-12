import { defineEnvironment, env } from "@astilba/env";

export default defineEnvironment({
  consumers: { worker: env.server() },
  entries: { enabled: env.public.deployment.boolean() },
  id: "com.astilba.examples.cloudflare-worker",
  targets: {
    workerDeployment: env.process("worker", { enabled: "WORKER_ENABLED" }),
  },
});
