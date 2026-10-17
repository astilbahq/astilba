// Forwarding a platform object's method, honestly.
//
// Both of this demo's wrappers are Proxies over host objects, and both have to hand a method back to
// a caller or invoke it against the real target. That is three lines of plumbing with one sharp
// edge, and the edge is worth a file of its own because both wrappers would otherwise cut themselves
// on it independently.
//
// THE EDGE: `Reflect.get` hands back `unknown`, which is right. Narrowing it with
// `typeof member === "function"` gives TypeScript's bare `Function` — and `Function`'s `apply`, and
// `Reflect.apply` over it, are both typed to return `any`. So the obvious spelling,
// `return await Reflect.apply(member, target, args)`, silently returns `any` into the app: the one
// type this repo does not allow, arriving through the door marked "just forwarding".
// It is not a theoretical leak either — `any` at a Proxy boundary means every downstream misuse of a
// platform result typechecks perfectly.
//
// The fix is to say what a platform method actually is, once, and route both wrappers through it.

/** A method on a platform object: callable, unknown receiver, unknown arguments, unknown result.
 * `unknown` on the way out is the honest answer — the wrapper does not know what a given binding's
 * method returns, and its job is to hand the value back untouched rather than to describe it. */
export type PlatformMethod = (
  this: unknown,
  ...args: readonly unknown[]
) => unknown;

/**
 * View a value already proven callable as a `PlatformMethod`.
 *
 * The assertion is the narrowest one available and it is doing real work rather than papering over a
 * doubt: the caller has established `typeof value === "function"`, so the value IS callable, and all
 * this states is the argument and return types that `Function` declines to carry. The alternative
 * spellings all end in `any` — which is the thing being avoided — so this is the honest floor rather
 * than a shortcut past it.
 */
export function asPlatformMethod(value: unknown): PlatformMethod {
  if (typeof value !== "function") {
    throw new TypeError(
      "chaos: asPlatformMethod called with a value that is not callable"
    );
  }
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the guard above PROVES callability; this assertion adds only the arg/return types that TS's bare `Function` refuses to carry. Every alternative (`Reflect.apply` over `Function`, `Function.prototype.apply`) is typed `any`, which ground rule 6 forbids outright — so this narrow assertion is what keeps `any` out of the wrappers rather than a way around a check.
  return value as PlatformMethod;
}
