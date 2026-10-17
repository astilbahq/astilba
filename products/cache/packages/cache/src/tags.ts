import { InvalidTagError, notImplemented } from "./errors";

// Tags.
// - charset [a-z0-9:_\-\.|%] (extended with %), <=256 B
// - <=126 user tags (+2 reserved: __k, __ns); user tags beginning `__` reject
//   - compound() escapes `%` first (%25), then `:` (%3a) and `|` (%7c), lowercase
// hex; arity is explicit (empty vector != ['']) — injective

/** The two reserved tag namespaces: the per-key digest and the
 * per-namespace watermark. A user tag beginning with this prefix is rejected
 * at write — only the kernel itself may stamp `__k:<h>` / `__ns:<name>`. */
const RESERVED_TAG_PREFIX = "__";

/** Whether `tag` falls in the reserved `__`-prefixed namespace. */
export function isReservedTag(tag: string): boolean {
  return tag.startsWith(RESERVED_TAG_PREFIX);
}

/**
 * Validate one caller-supplied tag at the write boundary. Throws a
 * real `InvalidTagError` — never `NotImplementedError` — the instant a
 * `__`-prefixed tag reaches `getOrSet`/`getOrSetEntry`/a purge selector, so a
 * forged reserved tag can never be smuggled in as a "user" tag.
 *
 * Division of labor: the CHARSET/length/non-empty authority is the builders — `t`
 * validates-and-rejects, `compound` escapes — so a `Tag` they produce is already
 * well-formed. This write-time check is deliberately the NARROW `__`-prefix backstop,
 * not a re-validation: it catches only the one class the type system cannot — a
 * `__`-prefixed string force-branded to `Tag` (a raw `as Tag` cast, a harness `mkTag`,
 * any hand-forged brand) that never passed through a builder. It intentionally does NOT
 * re-check charset — narrow, not weak.
 */
export function assertUserTag(tag: string): void {
  if (isReservedTag(tag)) {
    throw new InvalidTagError(
      tag,
      `user tags may not begin with the reserved "${RESERVED_TAG_PREFIX}" prefix (§2.3, MI13)`
    );
  }
}

/**
 * A validated cache tag. Produced ONLY by `t`, `compound`, or `globalTag`; a raw
 * string is never assignable to `Tag`. This nominal brand is how the library kills
 * stringly-typed tags (the failure class it exists to prevent).
 */
export type Tag = string & { readonly "astilba.tag": true };

/** A positional argument to `compound()` / an interpolation in `t`. */
export type TagPart = string | number;

/** The full tag charset: the design's `[a-z0-9:_\-.|]` extended with
 * `%` (`compound()`'s escape byte). One or more of these, nothing else. `-` is placed
 * last so it is a literal, not a range. */
const TAG_CHARSET = /^[a-z0-9:_.|%-]+$/u;

/** Maximum tag size — a UTF-8 BYTE budget, not code units. */
const MAX_TAG_BYTES = 256;

/** Maximum number of USER tags per entry — the two reserved tags (`__k`, `__ns`)
 * are stamped by the kernel and do NOT count against this ceiling. Enforced at the write
 * boundary over the committed set, whichever tag source(s) that set unions. */
export const MAX_USER_TAGS = 126;

/** UTF-8 byte length of `s` — the tag budget is bytes, not string length. */
function byteLength(s: string): number {
  return new TextEncoder().encode(s).length;
}

/**
 * Template helper for a single tag, e.g. ``t`book:${id}` ``. The tag TRUST
 * BOUNDARY: the interpolations (`values`) are UNTRUSTED user data, so `t` assembles the
 * template, then VALIDATES-and-REJECTS the final string against the full grammar
 * (non-empty, charset, ≤256 UTF-8 bytes, not `__`-prefixed) — throwing `InvalidTagError`
 * on any violation. It never escapes-by-guess: escaping is `compound()`'s separate
 * contract, and silently rewriting an interpolation is exactly how tag injection slips
 * through. On success returns the branded `Tag`.
 */
export function t(
  strings: TemplateStringsArray,
  ...values: readonly TagPart[]
): Tag {
  let assembled = "";
  for (const [i, literal] of strings.entries()) {
    assembled += literal;
    if (i < values.length) {
      assembled += String(values[i]);
    }
  }

  if (assembled.length === 0) {
    throw new InvalidTagError(assembled, "a tag must be non-empty (§2.3)");
  }
  // The narrow `__`-prefix backstop — the same rejection a user tag hits at write.
  assertUserTag(assembled);
  if (!TAG_CHARSET.test(assembled)) {
    throw new InvalidTagError(
      assembled,
      "a tag character is outside the [a-z0-9:_-.|%] grammar (§2.3, E10)"
    );
  }
  if (byteLength(assembled) > MAX_TAG_BYTES) {
    throw new InvalidTagError(
      assembled,
      `a tag may not exceed ${MAX_TAG_BYTES} UTF-8 bytes (§2.3)`
    );
  }
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- branding a fully-validated tag: a single narrowing assertion (string → branded string), not an `as unknown as` cast
  return assembled as Tag;
}

/**
 * Escapes one `compound()` argument: `%` first (`%25`), then `:`
 * (`%3a`) and `|` (`%7c`), lowercase hex. Escaping `%` first guarantees the
 * mapping is injective — an escape-lookalike in the input (a literal `%3a`)
 * is itself escaped to `%253a`, so it can never be produced by escaping a raw
 * `:` (which yields `%3a`, with no leading `%25`).
 */
function escapePart(part: TagPart): string {
  return String(part)
    .replaceAll("%", "%25")
    .replaceAll(":", "%3a")
    .replaceAll("|", "%7c");
}

/**
 * Injective compound tag over an argument vector, e.g. `compound('book', id,
 * 'locale', locale)`. Escaping; empty vector distinct from
 * `['']`.
 *
 * Every part is escaped so an escaped part can never itself
 * contain an unescaped `|`; joining escaped parts on `|` is therefore an
 * injective join. An explicit leading arity count (itself `|`-terminated)
 * makes the count unambiguous — it is always a decimal-digit run followed by
 * the first `|` — which in turn makes vectors of differing length (including
 * the empty vector vs. `['']`) distinct regardless of what the escaped parts
 * contain.
 */
export function compound(...parts: readonly TagPart[]): Tag {
  const encoded = `${parts.length}|${parts.map(escapePart).join("|")}`;
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- branding a validated compound-encoded string: a single narrowing assertion (string → branded string), not an `as unknown as` cast
  return encoded as Tag;
}

/**
 * A globally-scoped tag — its own scope-prefix escape hatch. Deferred to v0.4:
 * its escape-FORM is a wire-compatibility decision reserved for the scope-qualification
 * design, and ships with that consumer rather than standalone here.
 */
export function globalTag(name: string): Tag {
  return notImplemented("globalTag (deferred to v0.4)", name);
}
