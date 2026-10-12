import { world } from "@astilba/harness";

const digits = "0".repeat(1_000_000);
const retentions: number[] = [];

for (const input of [`${digits}x`, `1.${digits}x`, `${digits}1s`]) {
  const rig = world({
    isolates: 1,
    defaults: {
      // @ts-expect-error Malformed runtime input is intentional in this regression fixture.
      heartbeatInterval: input,
      // @ts-expect-error Malformed runtime input is intentional in this regression fixture.
      maxEntryRetention: input,
      // @ts-expect-error Malformed runtime input is intentional in this regression fixture.
      maxSyncLag: input,
    },
  });
  rig.registry.registerRetention = async (milliseconds) => {
    retentions.push(milliseconds);
  };
  await rig.isolate(0).cache.getOrSet({
    factory: async () => "value",
    key: "duration-input",
    ttl: "1s",
  });
}

process.stdout.write(JSON.stringify(retentions));
