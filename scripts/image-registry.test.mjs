import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { test } from "node:test";
import { platformManifests, registry } from "./image-registry.mjs";

const digest = (fill) => fill.repeat(64);
const sha256 = (text) =>
  `sha256:${createHash("sha256").update(text).digest("hex")}`;
const response = (status, body = "", headers = {}) =>
  new Response(status === 204 ? null : body, { status, headers });

test("takes the registry's anonymous token and checks every manifest against its digest", async () => {
  const index = JSON.stringify({ manifests: [] });
  const calls = [];
  const client = registry(async (url, init = {}) => {
    calls.push([
      String(url),
      init.method ?? "GET",
      init.headers?.authorization,
    ]);
    const href = String(url);
    if (href.startsWith("https://auth.example.test/token"))
      return response(200, JSON.stringify({ token: "t" }));
    if (!init.headers?.authorization)
      return response(401, "", {
        "www-authenticate":
          'Bearer realm="https://auth.example.test/token",service="registry.example.test",scope="repository:team/app:pull"',
      });
    if (href.endsWith(`/manifests/${sha256(index)}`))
      return response(200, index);
    if (href.endsWith("/manifests/sha256:" + digest("f")))
      return response(200, index);
    if (href.endsWith("/manifests/1.0"))
      return response(200, "", { "docker-content-digest": sha256(index) });
    if (href.endsWith("/tags/list?n=1000"))
      return response(200, JSON.stringify({ tags: ["1.0"] }), {
        link: '</v2/team/app/tags/list?n=1000&last=1.0>; rel="next"',
      });
    if (href.endsWith("/tags/list?n=1000&last=1.0"))
      return response(200, JSON.stringify({ tags: ["1.1"] }));
    return response(404);
  });
  const pin = {
    registry: "registry.example.test",
    repository: "team/app",
    image: "registry.example.test/team/app",
    tag: "1.0",
  };
  assert.deepEqual(await client.manifest(pin, sha256(index)), {
    manifests: [],
  });
  assert.deepEqual(
    calls.slice(0, 3).map(([url, , auth]) => [url.split("?")[0], auth]),
    [
      [
        `https://registry.example.test/v2/team/app/manifests/${sha256(index)}`,
        undefined,
      ],
      ["https://auth.example.test/token", undefined],
      [
        `https://registry.example.test/v2/team/app/manifests/${sha256(index)}`,
        "Bearer t",
      ],
    ],
  );
  await assert.rejects(
    client.manifest(pin, `sha256:${digest("f")}`),
    /returned other bytes/,
  );
  assert.equal(await client.tagDigest(pin), sha256(index));
  assert.deepEqual(await client.tags(pin), ["1.0", "1.1"]);
  const docker = registry(async () =>
    response(401, "", {
      "www-authenticate": 'Bearer realm="http://plain.test/token"',
    }),
  );
  await assert.rejects(
    docker.manifest(pin, sha256(index)),
    /unexpected registry challenge/,
  );
});

test("every registry request carries a time limit", async () => {
  const signals = [];
  const client = registry(async (_url, init) => {
    signals.push(init.signal);
    return response(200, JSON.stringify({ tags: [] }));
  });
  await client.tags({
    registry: "r.test",
    repository: "a/b",
    image: "r.test/a/b",
  });
  assert.ok(signals.every((signal) => signal instanceof AbortSignal));
  assert.equal(signals.length, 1);
});

test("a registry that keeps naming the same next page of tags is cut off", async () => {
  let calls = 0;
  const client = registry(async () => {
    calls++;
    return response(200, JSON.stringify({ tags: ["1.0"] }), {
      link: '</v2/team/app/tags/list?n=1000&last=1.0>; rel="next"',
    });
  });
  const pin = {
    registry: "r.test",
    repository: "team/app",
    image: "r.test/team/app",
  };
  await assert.rejects(client.tags(pin), /lists tags past 2 pages/);
  assert.equal(calls, 2);
});

test("scans the amd64 and arm64 images an index holds, or the one platform pinned", async () => {
  const pin = { image: "x", digest: `sha256:${digest("a")}` };
  const index = {
    manifests: [
      {
        digest: "sha256:amd",
        platform: { os: "linux", architecture: "amd64" },
      },
      {
        digest: "sha256:v7",
        platform: { os: "linux", architecture: "arm", variant: "v7" },
      },
      {
        digest: "sha256:arm",
        platform: { os: "linux", architecture: "arm64", variant: "v8" },
      },
      {
        digest: "sha256:att",
        platform: { os: "unknown", architecture: "unknown" },
      },
    ],
  };
  assert.deepEqual(await platformManifests({}, pin, index), [
    { platform: "linux/amd64", digest: "sha256:amd" },
    { platform: "linux/arm64", digest: "sha256:arm" },
  ]);
  const client = { blob: async () => ({ os: "linux", architecture: "arm64" }) };
  assert.deepEqual(
    await platformManifests(client, pin, { config: { digest: "sha256:c" } }),
    [{ platform: "linux/arm64", digest: pin.digest }],
  );
});
