import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdtemp, rm, symlink } from "node:fs/promises";
import os from "node:os";
import nodePath from "node:path";
import { describe, it } from "node:test";
import { fileURLToPath } from "node:url";

import { rollbackVersion } from "./rollback-version.mjs";

const baseline = "11111111-1111-1111-1111-111111111111";
const candidate = "22222222-2222-2222-2222-222222222222";
const before = "33333333-3333-3333-3333-333333333333";
const after = "44444444-4444-4444-4444-444444444444";
const input = {
  account: "a".repeat(32),
  expectedDeployment: before,
  token: "fixture-not-a-credential",
  version: baseline,
  worker: "astilba-docs",
};
const deployment = (id, version) => ({
  id,
  versions: [{ percentage: 100, version_id: version }],
});
const fixture = (overrides = {}) => {
  const calls = [];
  let active = deployment(before, candidate);
  const request = (url, options) => {
    calls.push({ options, url });
    assert.equal(options.redirect, "error");
    assert.ok(options.signal instanceof AbortSignal);
    assert.equal(
      options.headers.Authorization,
      "Bearer fixture-not-a-credential"
    );
    const path = new URL(url);
    assert.equal(path.origin, "https://api.cloudflare.com");
    assert.equal(path.search, "");
    const prefix = `/client/v4/accounts/${input.account}/workers/scripts/`;
    assert.ok(path.pathname.startsWith(prefix));
    if (options.method === "POST") {
      assert.match(path.pathname, /\/deployments$/u);
      assert.deepEqual(JSON.parse(options.body), {
        annotations: { "workers/message": "Astilba cutover rollback" },
        strategy: "percentage",
        versions: [{ percentage: 100, version_id: baseline }],
      });
      if (overrides.writeError) {
        return Response.json(
          { errors: [{ code: 10_220 }], success: false },
          { status: 400 }
        );
      }
      if (overrides.lostResponse) {
        active = deployment(after, baseline);
        throw new Error(
          "fixture connection lost after provider accepted write"
        );
      }
      active = deployment(after, baseline);
      return Response.json({ result: { id: after }, success: true });
    }
    assert.equal(options.method, "GET");
    if (path.pathname.endsWith(`/versions/${baseline}`)) {
      return Response.json({
        result: { id: overrides.wrongVersion ? candidate : baseline },
        success: true,
      });
    }
    assert.match(path.pathname, /\/deployments$/u);
    return Response.json({
      result: {
        deployments: [
          overrides.active ??
            (overrides.readbackMismatch && calls.length === 4
              ? deployment(before, candidate)
              : active),
        ],
      },
      success: true,
    });
  };
  return { calls, request };
};

describe("minimal provider-version rollback", () => {
  it("blocks the real CLI with the checked-in inactive policy", () => {
    const result = spawnSync(
      process.execPath,
      ["tooling/deployment/rollback-version.mjs"],
      {
        cwd: new URL("../..", import.meta.url),
        encoding: "utf-8",
        env: {},
      }
    );
    assert.equal(result.status, 1);
    assert.match(result.stderr, /Deployment preparation is inactive/u);
  });

  it("can be imported without running the CLI or requiring activation", () => {
    const module = new URL("rollback-version.mjs", import.meta.url).href;
    const result = spawnSync(
      process.execPath,
      [
        "--input-type=module",
        "--eval",
        `await import(${JSON.stringify(module)})`,
      ],
      { encoding: "utf-8", env: {} }
    );
    assert.equal(result.status, 0, result.stderr);
    assert.equal(result.stdout, "");
    assert.equal(result.stderr, "");
  });

  it("also blocks direct CLI execution through a symlink", async () => {
    const directory = await mkdtemp(
      nodePath.join(os.tmpdir(), "astilba-rollback-cli-")
    );
    const entry = nodePath.join(directory, "rollback.mjs");
    try {
      await symlink(
        fileURLToPath(new URL("rollback-version.mjs", import.meta.url)),
        entry
      );
      const result = spawnSync(process.execPath, [entry], {
        encoding: "utf-8",
        env: {},
      });
      assert.equal(result.status, 1);
      assert.match(result.stderr, /Deployment preparation is inactive/u);
    } finally {
      await rm(directory, { force: true, recursive: true });
    }
  });

  it("restores exactly one specified version at 100% and reads it back", async () => {
    await Promise.all(
      ["astilba-site", "astilba-docs", "astilba-ui-showcase"].map(
        async (worker) => {
          const provider = fixture();
          assert.equal(
            await rollbackVersion({ ...input, worker }, provider.request),
            after
          );
          assert.deepEqual(
            provider.calls.map(({ options }) => options.method),
            ["GET", "GET", "POST", "GET"]
          );
          assert.ok(
            provider.calls.every(({ url }) =>
              new URL(url).pathname.includes(`/scripts/${worker}/`)
            )
          );
        }
      )
    );
  });

  it("rejects unknown workers, malformed identifiers and absent credentials before requests", async () => {
    await Promise.all(
      [
        { worker: "other" },
        { worker: "astilba-docs/../other" },
        { account: "invalid" },
        { version: "previous" },
        { version: `${baseline}?force=true` },
        { expectedDeployment: "latest" },
        { token: "" },
      ].map(async (change) => {
        const provider = fixture();
        await assert.rejects(
          rollbackVersion({ ...input, ...change }, provider.request)
        );
        assert.equal(provider.calls.length, 0);
      })
    );
  });

  it("stops before writes on baseline mismatch, stale deployment or split traffic", async () => {
    await Promise.all(
      [
        { wrongVersion: true },
        { active: deployment(after, candidate) },
        {
          active: {
            id: before,
            versions: [
              { percentage: 50, version_id: baseline },
              { percentage: 50, version_id: candidate },
            ],
          },
        },
      ].map(async (overrides) => {
        const provider = fixture(overrides);
        await assert.rejects(rollbackVersion(input, provider.request));
        assert.equal(
          provider.calls.filter(({ options }) => options.method === "POST")
            .length,
          0
        );
      })
    );
  });

  it("does not force or retry a rejected or indeterminate write", async () => {
    await Promise.all(
      [{ writeError: true }, { lostResponse: true }].map(async (overrides) => {
        const provider = fixture(overrides);
        await assert.rejects(rollbackVersion(input, provider.request));
        assert.equal(provider.calls.length, 3);
        assert.equal(
          provider.calls.filter(({ options }) => options.method === "POST")
            .length,
          1
        );
      })
    );
  });

  it("does not declare success on a mismatched readback", async () => {
    const provider = fixture({ readbackMismatch: true });
    await assert.rejects(
      rollbackVersion(input, provider.request),
      /readback differs/u
    );
    assert.equal(provider.calls.length, 4);
  });
});
