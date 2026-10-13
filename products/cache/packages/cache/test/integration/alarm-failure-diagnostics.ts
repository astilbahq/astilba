import { listDurableObjectIds, runInDurableObject } from "cloudflare:test";
import { env } from "cloudflare:workers";
import { beforeEach, onTestFailed } from "vitest";

// Failure-only, read-only evidence. Do not retry a test or infer success from a drained alarm.
// Cursor advancement distinguishes a consumed flush from an alarm that never ran; the old
// manual-trigger boolean alone cannot distinguish those cases. Never log raw tags or KV values.
beforeEach(() => {
  onTestFailed(async () => {
    let ids: DurableObjectId[];
    try {
      ids = await listDurableObjectIds(env.COORDINATOR);
    } catch {
      console.error("[alarm-failure] object enumeration unavailable");
      return;
    }
    for (const id of ids) {
      const stub = env.COORDINATOR.get(id);
      try {
        const evidence = await runInDurableObject(stub, async (_instance, state) => {
          const journal = await state.storage.get<readonly { op?: string }[]>("__journal");
          return {
            alarm: await state.storage.getAlarm(),
            cursor: (await state.storage.get<number>("kvFlushedEpoch")) ?? 0,
            mutations: journal?.filter(
              (entry) => entry.op === "expire" || entry.op === "delete"
            ).length ?? 0,
          };
        });
        console.error(
          "[alarm-failure]",
          JSON.stringify({ objectId: id.toString(), ...evidence })
        );
      } catch {
        // Diagnostics must not replace or suppress the original test failure.
        console.error("[alarm-failure] durable state unavailable");
      }
    }
  });
});
