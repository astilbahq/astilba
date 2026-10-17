import { runDurableObjectAlarm, runInDurableObject } from "cloudflare:test";
import { env } from "cloudflare:workers";
import { mirrorKeys } from "@astilba/cache/registry";
import { expect, it, vi } from "vitest";
import { fireAlarm } from "./registry-harness";

it("the manual clock reaches the adapter without faking timers or alarm completion", async () => {
  const regId = "manual-clock-delivery";
  const stub = env.COORDINATOR.get(env.COORDINATOR.idFromName(regId));
  const logicalNow = Date.parse("2100-01-01T00:00:00.000Z");
  expect(Date.now()).toBe(logicalNow);
  expect(await runInDurableObject(stub, () => Date.now())).toBe(logicalNow);

  const ack = await stub.expire(["manual:tag"]);
  expect(ack.epoch).toBe(1);
  const before = await runInDurableObject(stub, async (_instance, state) => ({
    alarm: await state.storage.getAlarm(),
    cursor: await state.storage.get<number>("kvFlushedEpoch"),
  }));
  expect(before.alarm).toBeGreaterThan(logicalNow);
  expect(before.alarm).toBeGreaterThan(vi.getRealSystemTime());
  expect(before.cursor).toBeUndefined();
  expect(await env.REGISTRY_KV.get(mirrorKeys(regId).deltaKey(1))).toBeNull();

  // A native timer remains runnable without advancing the fake Date. The real
  // scheduler crosses a complete flush window, yet cannot steal this future alarm.
  await new Promise<void>((resolve) => setTimeout(resolve, 1200));
  expect(Date.now()).toBe(logicalNow);
  expect(await runInDurableObject(stub, async (_instance, state) =>
    await state.storage.getAlarm()
  )).toBe(before.alarm);
  expect(await runDurableObjectAlarm(stub)).toBe(true);
  expect(await runInDurableObject(stub, async (_instance, state) =>
    await state.storage.get<number>("kvFlushedEpoch")
  )).toBe(ack.epoch);
  expect(await env.REGISTRY_KV.get(mirrorKeys(regId).deltaKey(1))).not.toBeNull();
  expect(await runDurableObjectAlarm(stub)).toBe(false);
});

it("the manual clock cannot manufacture a missing alarm or a successful flush", async () => {
  const regId = "manual-clock-unscheduled";
  const stub = env.COORDINATOR.get(env.COORDINATOR.idFromName(regId));
  await stub.check(["manual:tag"]);
  expect(await runDurableObjectAlarm(stub)).toBe(false);
  await expect(fireAlarm(stub)).rejects.toThrow(
    "[fireAlarm] no alarm became runnable within 50 turns"
  );
  expect(await runInDurableObject(stub, async (_instance, state) =>
    await state.storage.get<number>("kvFlushedEpoch")
  )).toBeUndefined();
  expect(await env.REGISTRY_KV.get(mirrorKeys(regId).deltaKey(1))).toBeNull();
});
