import { check } from "../.astilba/env/workerDeployment.server.ts";

export interface Env {
  readonly WORKER_ENABLED: string;
}

type Worker = Readonly<{
  fetch(request: Request, bindings: Env): Response;
}>;

export default {
  fetch(_request: Request, bindings: Env): Response {
    const checked = check(bindings);
    if (!checked.ok) {
      return Response.json(
        { error: checked.diagnostics[0]?.code },
        { status: 500 }
      );
    }
    const enabled = checked.value.enabled;
    return Response.json({ configured: enabled });
  },
} satisfies Worker;
