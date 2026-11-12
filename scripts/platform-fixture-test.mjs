import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { once } from "node:events";
import {
  chmod,
  copyFile,
  mkdir,
  mkdtemp,
  readFile,
  readdir,
  rm,
  writeFile,
} from "node:fs/promises";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { delimiter, join } from "node:path";
import { test } from "node:test";
import { promisify } from "node:util";
import { dashboardPorts, reservePortBlock } from "./test-ports.mjs";

const execute = promisify(execFile);
// The block stays this process's while it runs, so other suites cannot take it.
const base = await reservePortBlock();
const ports = dashboardPorts(base);

async function fixture(t) {
  const root = await mkdtemp(join(tmpdir(), "astilba-fixture-startup-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  for (const directory of ["scripts", "bin", "tmp", `.artifacts/p8/browser/${base}`])
    await mkdir(join(root, directory), { recursive: true });
  for (const script of ["platform-fixture.mjs", "docker-forward.mjs", "test-ports.mjs"])
    await copyFile(
      new URL(script, import.meta.url),
      join(root, "scripts", script),
    );
  const marker = join(root, `.artifacts/p8/browser/${base}/hosted-fixture.json`);
  const probe = join(root, "probe.json");
  const docker = join(root, "bin/docker");
  // The runtime boundary fails deliberately after probing the real proxy socket.
  await writeFile(
    docker,
    `#!/usr/bin/env node
const net = require("node:net");
const fs = require("node:fs");
const socket = net.connect(Number(process.env.ASTILBA_FIXTURE_PROBE_PORT), "127.0.0.1");
const finish = (bound) => {
  socket.destroy();
  fs.writeFileSync(process.env.ASTILBA_FIXTURE_PROBE, JSON.stringify({ bound }));
  process.exit(42);
};
socket.once("connect", () => finish(true));
socket.once("error", () => finish(false));
socket.setTimeout(3000, () => finish(false));
`,
  );
  await chmod(docker, 0o755);
  const run = async () => {
    await assert.rejects(
      execute(process.execPath, [join(root, "scripts/platform-fixture.mjs")], {
        env: {
          ...process.env,
          PATH: `${join(root, "bin")}${delimiter}${process.env.PATH}`,
          TMPDIR: join(root, "tmp"),
          PLAT_APP_DOCKER_HOST: `unix://${join(root, "unused.sock")}`,
          ASTILBA_FIXTURE_PROBE: probe,
          ASTILBA_FIXTURE_PROBE_PORT: String(ports.hosted),
          PLAT_BROWSER_PORT_BASE: String(base),
        },
        timeout: 15000,
      }),
      (error) => error.code === 1,
    );
  };
  return { root, marker, probe, run };
}

for (const port of [ports.gitProvider, ports.hosted]) {
  test(`startup conflict on ${port} preserves another fixture's metadata and cleans owned files`, async (t) => {
    const existing = createServer();
    existing.listen(port, "127.0.0.1");
    await once(existing, "listening");
    t.after(() => new Promise((resolve) => existing.close(resolve)));
    const f = await fixture(t);
    await writeFile(f.marker, "another fixture owns this file");
    await f.run();
    assert.equal(
      await readFile(f.marker, "utf8"),
      "another fixture owns this file",
    );
    assert.deepEqual(await readdir(join(f.root, "tmp")), []);
    await assert.rejects(readFile(f.probe), { code: "ENOENT" });
    assert.equal(existing.listening, true);
  });
}

test("fixture holds the HTTPS proxy port before starting the external runtime", async (t) => {
  const f = await fixture(t);
  await f.run();
  assert.deepEqual(JSON.parse(await readFile(f.probe, "utf8")), {
    bound: true,
  });
  assert.deepEqual(await readdir(join(f.root, "tmp")), []);
  const released = createServer();
  released.listen(ports.hosted, "127.0.0.1");
  await once(released, "listening");
  await new Promise((resolve) => released.close(resolve));
});
