import type { RequestContext } from "@astilba/cache";

// A guarded adapter request object. Property reads are RECORDED (never thrown)
// so a test can witness that a public-scoped factory touched a guarded accessor.
// The kernel's dev-mode Proxy is what demotes such an entry off the shared tier
// — so the failure must come from the kernel (NotImplementedError), not
// from this fake. The fake must never throw and never demote on its own.

export interface GuardedRequest {
  readonly request: RequestContext;
  /** The property names read through the request so far (a test witness). */
  readonly reads: readonly string[];
}

export function guardedRequest(
  fields: Record<string, unknown>
): GuardedRequest {
  const reads: string[] = [];
  const request: RequestContext = new Proxy(fields, {
    get(target, prop, receiver): unknown {
      if (typeof prop === "string") {
        reads.push(prop);
      }
      return Reflect.get(target, prop, receiver) as unknown;
    },
  });
  return { request, reads };
}
