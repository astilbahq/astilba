import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { existsSync, readFileSync } from "node:fs";
import { createServer } from "node:http";
import { test } from "node:test";
import { installPnpm, pnpmPin } from "./install-pnpm.mjs";

const tarball = Buffer.from("pnpm tarball");
const integrity = createHash("sha512").update(tarball).digest("base64");
const lockfile = (value = integrity) => `packages:

  pnpm@11.17.0:
    resolution: {integrity: sha512-${value}}
    engines: {node: '>=22.13'}
    hasBin: true

snapshots:

  pnpm@11.17.0: {}
`;
const manifest = { packageManager: "pnpm@11.17.0" };

test("reads the version from packageManager and the hash from the lockfile", () => {
  assert.deepEqual(pnpmPin({ manifest, lockfile: lockfile() }), {
    version: "11.17.0",
    hash: createHash("sha512").update(tarball).digest("hex"),
  });
});

test("refuses a pin it could not check against", () => {
  for (const [input, expected] of [
    [{ manifest: {}, lockfile: lockfile() }, /packageManager must be/],
    [
      { manifest: { packageManager: "pnpm@11" }, lockfile: lockfile() },
      /packageManager must be/,
    ],
    [
      { manifest: { packageManager: "pnpm@11.18.0" }, lockfile: lockfile() },
      /no sha512 integrity for pnpm@11\.18\.0/,
    ],
    [{ manifest, lockfile: lockfile("c2hvcnQ=") }, /malformed sha512/],
  ])
    assert.throws(() => pnpmPin(input), expected);
});

async function registry(t, respond) {
  const requests = [];
  const server = createServer((request, response) => {
    requests.push(request.url);
    respond(response, requests.length);
  });
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  t.after(() => {
    server.closeAllConnections();
    server.close();
  });
  return { url: `http://127.0.0.1:${server.address().port}`, requests };
}
const fakeNpm = () => {
  const calls = [];
  return {
    calls,
    run: (command, args) => {
      const path = args.at(-1);
      calls.push({ command, args, bytes: readFileSync(path), path });
    },
  };
};

test("installs the checked bytes offline, without scripts, after a dropped request", async (t) => {
  t.mock.method(console, "warn", () => {});
  const { url, requests } = await registry(t, (response, request) =>
    request === 1 ? response.socket.destroy() : response.end(tarball),
  );
  const npm = fakeNpm();
  await installPnpm({
    manifest,
    lockfile: lockfile(),
    registry: url,
    run: npm.run,
    sleep: async () => {},
  });
  assert.deepEqual(requests, Array(2).fill("/pnpm/-/pnpm-11.17.0.tgz"));
  const [{ command, args, bytes, path }] = npm.calls;
  assert.equal(command, "npm");
  assert.deepEqual(bytes, tarball);
  for (const flag of ["--global", "--offline", "--ignore-scripts"])
    assert.ok(args.includes(flag), flag);
  assert.equal(existsSync(path), false);
});

test("a changed tarball fails once, names the URL and installs nothing", async (t) => {
  const { url, requests } = await registry(t, (response) =>
    response.end("replaced tarball"),
  );
  const npm = fakeNpm();
  await assert.rejects(
    installPnpm({
      manifest,
      lockfile: lockfile(),
      registry: url,
      run: npm.run,
    }),
    (error) => {
      assert.match(error.message, /pinned hash/);
      assert.ok(error.message.includes(url), error.message);
      return true;
    },
  );
  assert.equal(requests.length, 1);
  assert.deepEqual(npm.calls, []);
});
