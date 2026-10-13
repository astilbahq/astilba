import { runDurableObjectAlarm, runInDurableObject } from "cloudflare:test";
import { env } from "cloudflare:workers";
import { mirrorKeys } from "@astilba/cache/registry";
import { expect, it } from "vitest";
import { fireAlarm } from "./registry-harness";

it("an automatically delivered flush leaves no alarm for a later manual trigger", async () => {
  const regId = "alarm-automatic-delivery";
  const stub = env.COORDINATOR.get(env.COORDINATOR.idFromName(regId));
  const ack = await stub.expire(["alarm:tag"]);
  expect(ack.epoch).toBe(1);

  // Deliberately let the platform deliver the scheduled alarm. Poll durable completion,
  // not the presence of an alarm slot; the integration lane alone admits wall-time tolerance.
  // Coordinator.#armFlushAlarm chooses the next 1100ms window boundary, so the 5s
  // observation budget covers the scheduled delay without changing the host's deadline.
  await expect.poll(
    async () => await runInDurableObject(stub, async (_instance, state) =>
      await state.storage.get<number>("kvFlushedEpoch")
    ),
    { timeout: 5000, interval: 20 }
  ).toBe(ack.epoch);

  expect(await runInDurableObject(stub, async (_instance, state) =>
    await state.storage.getAlarm()
  )).toBeNull();
  expect(await runDurableObjectAlarm(stub)).toBe(false);
  await expect(fireAlarm(stub)).rejects.toThrow(
    "[fireAlarm] no alarm became runnable within 50 turns"
  );
  expect(await env.REGISTRY_KV.get(mirrorKeys(regId).deltaKey(1))).not.toBeNull();
});

it("a never-scheduled alarm has no durable flush cursor or mirror batch", async () => {
  const regId = "alarm-never-scheduled";
  const stub = env.COORDINATOR.get(env.COORDINATOR.idFromName(regId));
  await stub.check(["alarm:tag"]);
  expect(await runDurableObjectAlarm(stub)).toBe(false);
  await expect(fireAlarm(stub)).rejects.toThrow(
    "[fireAlarm] no alarm became runnable within 50 turns"
  );
  expect(await runInDurableObject(stub, async (_instance, state) =>
    await state.storage.get<number>("kvFlushedEpoch")
  )).toBeUndefined();
  expect(await env.REGISTRY_KV.get(mirrorKeys(regId).deltaKey(1))).toBeNull();
});
