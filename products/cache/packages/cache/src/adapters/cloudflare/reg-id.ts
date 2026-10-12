/* References: E19-b, E19. */

// PLATFORM: Cloudflare runtime bindings are confined to this adapter.

import { assertRegId, DEFAULT_REG_ID, isRegId } from "../../registry";

/**
 * Refused (E19-b, R2): a Coordinator addressed by a Durable Object name that violates E19's regId
 * grammar (`[a-z0-9._-]`, 1–64). Typed so the boot failure is unambiguous and machine-checkable —
 * the fix is a conforming name or an explicit `REGISTRY_ID`. Thrown at first boot only (before any
 * data), so it strands nothing: validate-and-refuse, not journal-wins. `Error.name` stays the class
 * name; `registryName` carries the offending Durable Object name.
 */
export class InvalidRegistryNameError extends Error {
  readonly registryName: string;

  constructor(registryName: string) {
    super(
      `astilba/cache coordinator: Durable Object name ${JSON.stringify(registryName)} is not a ` +
        `valid registry id — it must match [a-z0-9._-], length 1-64; use a conforming name or ` +
        `set REGISTRY_ID (E19, E19-b)`
    );
    this.name = "InvalidRegistryNameError";
    this.registryName = registryName;
  }
}

/**
 * Resolve a Coordinator's regId from its platform identity (E19-b, R3). `name` is `ctx.id.name`
 * (undefined for an un-named / `idFromString`-reached object); `envRegId` is `env.REGISTRY_ID`.
 * Precedence: name ?? env ?? `"default"`. A NAME that violates E19's grammar REFUSES with a typed
 * `InvalidRegistryNameError` (R2). An env/default winner is still asserted through the existing
 * `assertRegId` (R3 — every winner passes the same charset gate), so a malformed `REGISTRY_ID` also
 * fails loud. An empty-string env value is treated as unset (falls through to `"default"`).
 */
export function resolveRegId(name?: string, envRegId?: string): string {
  if (name !== undefined) {
    // The name is the address (identity = address). Refuse loudly rather than hash or lowercase —
    // hashing forfeits legible keys/telemetry and silent lowercasing mints a collision the
    // developer never asked for.
    if (!isRegId(name)) {
      throw new InvalidRegistryNameError(name);
    }
    return name;
  }
  // Un-named object: env `REGISTRY_ID` is the DEMOTED fallback, then `"default"` (zero-config).
  const fallback =
    envRegId !== undefined && envRegId !== "" ? envRegId : DEFAULT_REG_ID;
  assertRegId(fallback);
  return fallback;
}
