import { env } from "cloudflare:test";
import { describe, expect, it } from "vitest";

import type { StoreMetadata } from "../../src/drivers";
import { cloudflareKV } from "../../src/adapters/cloudflare/kv";

/* References: §4.2, E5, §1. */









describe("cloudflareKV read-cache hint → cacheTtl (§4.2, Ruling 2)", () => {
  it("maps pointer→30, delta/snap→86400, unhinted→none — the cacheTtl the sink selects", async () => {
    const realKv = env.REGISTRY_KV;
    const seen: { readonly key: string; readonly cacheTtl: number | undefined }[] =
      [];
    // Record every read's selected `cacheTtl`, delegating to the real namespace. `new
    // Proxy<KVNamespace>` is typed by the target (no cast). The metadata overload is the one the
    // driver calls (`getWithMetadata<StoreMetadata>`).
    const recordingKv: KVNamespace = new Proxy(realKv, {
      get(target, property, receiver) {
        if (property === "getWithMetadata") {
          return (
            key: string,
            options?: Partial<KVNamespaceGetOptions<undefined>>
          ): Promise<KVNamespaceGetWithMetadataResult<string, StoreMetadata>> => {
            seen.push({ cacheTtl: options?.cacheTtl, key });
            return target.getWithMetadata<StoreMetadata>(key, options);
          };
        }
        return Reflect.get(target, property, receiver);
      },
    });

    const store = cloudflareKV(recordingKv);
    await store.get("read-cache:pointer", "pointer");
    await store.get("read-cache:delta", "delta");
    await store.get("read-cache:snap", "snap");
    await store.get("read-cache:entry"); // unhinted — §1-licensed, keeps the platform default

    const ttlFor = (key: string): number | undefined =>
      seen.find((s) => s.key === key)?.cacheTtl;
    expect(ttlFor("read-cache:pointer")).toBe(30); // the pinned read-cache minimum
    expect(ttlFor("read-cache:delta")).toBe(86_400); // immutable — cache hard
    expect(ttlFor("read-cache:snap")).toBe(86_400);
    expect(ttlFor("read-cache:entry")).toBeUndefined(); // no hint ⇒ no cacheTtl ⇒ platform default
  });
});
