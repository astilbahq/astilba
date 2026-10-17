// @astilba/cache adapter — Coordinator regId resolution (phase 2).
//
// PLATFORM: lives under `src/adapters/**` (the sanctioned platform boundary) but is
// PURE — no ambient time/randomness, no `cloudflare:*` import — so the Node unit lane drives the
// SAME precedence + refusal the Durable Object runs. The Coordinator reads `ctx.id.name` and
// `env.REGISTRY_ID` (platform reads, at the `// PLATFORM:`-commented seeding call site) and hands
// the raw strings HERE; this module owns only the precedence order and the grammar refusal.
//
// IDENTITY = ADDRESS. A Coordinator's regId derives from its OWN Durable Object name — the
// same string a client uses to address it (`idFromName(name)`). Precedence (R3):
//   regId = ctx.id.name ?? env.REGISTRY_ID ?? "default"
// Env is the DEMOTED fallback for an un-named object (`newUniqueId` ⇒ `ctx.id.name === undefined`),
// NOT an override — an override would resurrect the one-registry-per-deployment defect identity = address removes.
// A NAME that violates the grammar REFUSES loud (R2, validate-and-refuse): at first boot no data
// exists, so there is nothing to strand — a malformed name is a developer error, not a migration.

import { assertRegId, DEFAULT_REG_ID, isRegId } from "../../registry";

/**
 * Refused: a Coordinator addressed by a Durable Object name that violates the regId
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
 * Resolve a Coordinator's regId from its platform identity. `name` is `ctx.id.name`
 * (undefined for an un-named / `idFromString`-reached object); `envRegId` is `env.REGISTRY_ID`.
 * Precedence: name ?? env ?? `"default"`. A NAME that violates the grammar REFUSES with a typed
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
