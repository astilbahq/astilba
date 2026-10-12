import { describe, expect, test } from "vitest";

import { guardedRequest } from "./request";

// Harness self-test — the guarded request must RECORD reads (a test witness) and
// must NOT throw or demote on its own; the kernel's dev-mode Proxy is what demotes
// a public-scoped entry that touches it (INV-4). A fake that threw would make the
// invariant red for the wrong reason.

describe(guardedRequest, () => {
  test("records property reads without throwing", () => {
    const guard = guardedRequest({ tenant: "acme", userId: "u1" });
    expect(guard.reads).toStrictEqual([]);
    expect(guard.request.userId).toBe("u1");
    expect(guard.request.tenant).toBe("acme");
    expect(guard.reads).toContain("userId");
    expect(guard.reads).toContain("tenant");
  });
});
