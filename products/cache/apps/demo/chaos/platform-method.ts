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
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the preceding guard checks callability; this assertion supplies argument and result types
  return value as PlatformMethod;
}
