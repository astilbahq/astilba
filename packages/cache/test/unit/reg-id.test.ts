// regId resolution teeth — the DO-name-derived registry identity (identity = address).
//
// PURE unit teeth in the Node lane over `resolveRegId` (src/adapters/cloudflare/reg-id.ts): the
// SAME precedence + grammar refusal the Coordinator runs at boot, driven directly on {name, env}
// inputs. Three properties, each RUN here (deterministic — no fast-check):
//   • precedence — name > env > "default" (R3): the DO name wins; env is the DEMOTED fallback for
//     an un-named object; "default" is the last resort (zero-config).
// • refuse-on-invalid-name (R2) — a NAME violating the grammar throws a TYPED
//     `InvalidRegistryNameError` naming the grammar + suggesting `REGISTRY_ID`; env/default winners
//     still pass `assertRegId` (a malformed `REGISTRY_ID` also fails loud).
//   • the idFromString/newUniqueId trap — an undefined name (the platform drops it) falls through
//     to env then default, never throws for a *missing* name.

import { describe, expect, test } from "vitest";

import {
  InvalidRegistryNameError,
  resolveRegId,
} from "../../src/adapters/cloudflare/reg-id";

describe("resolveRegId — DO-name-derived registry identity (E19-b)", () => {
  test("precedence R3: the DO name WINS over env and default", () => {
    // Name present + env present ⇒ the name (identity = address), env is NOT an override.
    expect(resolveRegId("shop-eu", "env-registry")).toBe("shop-eu");
    // Name present, env absent ⇒ still the name.
    expect(resolveRegId("shop-eu")).toBe("shop-eu");
  });

  test("precedence R3: env is the DEMOTED fallback for an un-named object", () => {
    // Un-named object (newUniqueId / idFromString ⇒ name undefined) falls to env `REGISTRY_ID`.
    expect(resolveRegId(undefined, "env-registry")).toBe("env-registry");
  });

  test('precedence R3: "default" is the last resort (zero-config)', () => {
    // No name, no env ⇒ "default".
    expect(resolveRegId()).toBe("default");
    // An empty-string env value is treated as unset ⇒ "default" (not an invalid "" regId).
    expect(resolveRegId(undefined, "")).toBe("default");
  });

  test("refuse R2: a NAME violating E19's grammar throws a TYPED InvalidRegistryNameError", () => {
    // Uppercase / delimiter / whitespace / over-length all violate `[a-z0-9._-]{1,64}`.
    for (const bad of [
      "SHOP",
      "has:colon",
      "has space",
      "a".repeat(65),
      "💥",
    ]) {
      expect(() => resolveRegId(bad)).toThrow(InvalidRegistryNameError);
    }
  });

  test("refuse R2: the typed error names the grammar AND suggests REGISTRY_ID", () => {
    // `instanceof` in the IIFE narrows to a typed value (no oxlint-forbidden cast); the expects stay
    // top-level (no conditional-expect). A non-refusing call would return null and fail the guard.
    const caught = ((): InvalidRegistryNameError | null => {
      try {
        resolveRegId("BAD!!");
        return null;
      } catch (error) {
        return error instanceof InvalidRegistryNameError ? error : null;
      }
    })();
    expect(caught).not.toBeNull();
    expect(caught?.registryName).toBe("BAD!!");
    expect(caught?.message).toContain("[a-z0-9._-]");
    expect(caught?.message).toContain("REGISTRY_ID");
  });

  test("R3: an env/default winner is still asserted — a malformed REGISTRY_ID fails loud", () => {
    // The name is undefined, so env is the winner; a malformed env value must still throw (via the
    // shared `assertRegId` gate). It is NOT the name-refuse path, so it throws the generic regId gate
    // error, not the typed name-refuse error.
    expect(() => resolveRegId(undefined, "BAD:env")).toThrow(/E19/u);
  });

  test("the trap: an undefined name never throws for absence — it falls through to env/default", () => {
    // A DO reached via `idFromString` (or minted by `newUniqueId`) has `ctx.id.name === undefined`;
    // that is NOT an invalid name, so it resolves via the fallback chain, never refuses.
    expect(() => resolveRegId()).not.toThrow();
    expect(() => resolveRegId(undefined, "env-registry")).not.toThrow();
  });
});
