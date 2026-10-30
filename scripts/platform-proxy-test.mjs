import assert from "node:assert/strict";
import { registerHooks } from "node:module";
import { test } from "node:test";
import dashboard from "../apps/platform/worker.ts";

// Only the Cloudflare host class is substituted. The adapter handles real
// Request/Response objects and its own readiness and forwarding path.
const hooks = registerHooks({
  resolve(specifier, context, nextResolve) {
    if (specifier === "cloudflare:workers") {
      return {
        url: "data:text/javascript,export class DurableObject { constructor(ctx, env) { this.ctx = ctx; this.env = env; } }",
        shortCircuit: true,
      };
    }
    return nextResolve(specifier, context);
  },
});
const { Management } = await import("../apps/control-plane-worker/src/index.ts");
hooks.deregister();

for (const adapter of ["dashboard", "container"]) {
  test(`${adapter} forwards only the edge client address`, async (t) => {
    const forwarded = [];
    const fetchUpstream = async (request) => {
      if (new URL(typeof request === "string" ? request : request.url).pathname !== "/healthz") forwarded.push(request);
      return new Response(null, { status: 204 });
    };
    t.mock.method(globalThis, "fetch", fetchUpstream);
    const container = {
      running: true,
      setInactivityTimeout: async () => {},
      getTcpPort: () => ({ fetch: fetchUpstream }),
    };
    const management = new Management({ container }, {});
    const env = { API_ORIGIN: "https://api.example.test" };
    for (const clientIP of ["198.51.100.20", "2001:db8::20", undefined]) {
      const headers = new Headers({
        "X-Forwarded-For": "203.0.113.99",
        Forwarded: "for=203.0.113.99",
        "X-Real-IP": "203.0.113.99",
      });
      if (clientIP) headers.set("CF-Connecting-IP", clientIP);
      const request = new Request("https://panel.example.test/api/v1/session", { headers });
      const response = adapter === "dashboard"
        ? await dashboard.fetch(request, env)
        : await management.fetch(request);
      assert.equal(response.status, 204);
      const result = forwarded.at(-1);
      assert.equal(result.headers.get("X-Forwarded-For"), clientIP ?? null);
      assert.equal(result.headers.get("Forwarded"), null);
    }
    assert.equal(forwarded.length, 3);
  });
}

test("dashboard forwards the existing network preparation download", async (t) => {
  t.mock.method(globalThis, "fetch", async (request) => {
    assert.equal(
      request.url,
      "https://api.example.test/onboarding/network-prepare.sh",
    );
    return new Response("#!/bin/sh\n", {
      headers: {
        "Content-Type": "text/x-shellscript",
        "Cache-Control": "no-store",
      },
    });
  });
  const response = await dashboard.fetch(
    new Request("https://panel.example.test/onboarding/network-prepare.sh"),
    { API_ORIGIN: "https://api.example.test" },
  );
  assert.equal(response.headers.get("Content-Type"), "text/x-shellscript");
  assert.equal(await response.text(), "#!/bin/sh\n");
});

test("dashboard replaces a conditional HTML HEAD response with fresh nonce metadata", async () => {
  const requests = [];
  const response = await dashboard.fetch(
    new Request("https://panel.example.test/", {
      method: "HEAD",
      headers: { "If-None-Match": '"old-shell"', Range: "bytes=0-10" },
    }),
    {
      ASSETS: {
        fetch: async (request) => {
          requests.push(request);
          return requests.length === 1
            ? new Response(null, { status: 304 })
            : new Response(null, {
                headers: {
                  "Content-Type": "text/html",
                  ETag: '"old-shell"',
                  "Content-Length": "10",
                },
              });
        },
      },
    },
  );
  assert.equal(requests.length, 2);
  assert.equal(requests[1].headers.get("If-None-Match"), null);
  assert.equal(requests[1].headers.get("Range"), null);
  assert.equal(response.status, 200);
  assert.equal(response.headers.get("Cache-Control"), "no-store");
  assert.equal(response.headers.get("ETag"), null);
  assert.equal(response.headers.get("Content-Length"), null);
  assert.match(response.headers.get("Content-Security-Policy"), /'nonce-[^']+'/);
  assert.equal(response.body, null);
});
