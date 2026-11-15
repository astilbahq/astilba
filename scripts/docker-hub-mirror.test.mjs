import assert from "node:assert/strict";
import {
  chmodSync,
  existsSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { mock, test } from "node:test";
import { fromDockerHub, requireDockerHubMirror } from "./docker-hub-mirror.mjs";

mock.method(process.stderr, "write", () => true);

const digest = `@sha256:${"a".repeat(64)}`;

test("images are read from Docker Hub as Docker decides it", () => {
  for (const image of [
    `busybox:1.37.0${digest}`,
    `moby/buildkit:v0.34.1${digest}`,
    `docker.io/library/alpine:3.23${digest}`,
    `index.docker.io/gitea/gitea:28.2.0${digest}`,
  ])
    assert.equal(fromDockerHub(image), true, image);
  for (const image of [
    `mcr.microsoft.com/playwright:v1.64.1-noble${digest}`,
    `ghcr.io/usekaneo/kaneo:2.49.0${digest}`,
    `localhost/fixture${digest}`,
    `registry.local:5000/app${digest}`,
  ])
    assert.equal(fromDockerHub(image), false, image);
});

// A fake Docker CLI that answers `info` with the given mirrors and records its arguments.
function fakeDocker(t, mirrors) {
  const directory = mkdtempSync(join(tmpdir(), "astilba-mirror-"));
  t.after(() => rmSync(directory, { recursive: true, force: true }));
  const path = join(directory, "docker");
  const calls = join(directory, "calls");
  writeFileSync(
    path,
    `#!/bin/sh\nprintf '%s\\n' "$*" >> "${calls}"\nprintf '%s\\n' '${mirrors}'\n`,
  );
  chmodSync(path, 0o755);
  return {
    path,
    calls: () =>
      existsSync(calls) ? readFileSync(calls, "utf8").trim().split("\n") : [],
  };
}

test("a Docker Hub pull needs the daemon to list the mirror", async (t) => {
  const docker = fakeDocker(t, '["https://mirror.gcr.io/"]');
  await requireDockerHubMirror(
    [`busybox:1.37.0${digest}`],
    [docker.path, "--host", "unix:///x"],
  );
  assert.deepEqual(docker.calls(), [
    "--host unix:///x info --format {{json .RegistryConfig.Mirrors}}",
  ]);
});

for (const [name, mirrors] of [
  ["no mirror", "null"],
  ["an empty list", "[]"],
  ["another mirror", '["https://mirror.example/"]'],
])
  test(`a Docker Hub pull is refused with ${name}`, async (t) => {
    const docker = fakeDocker(t, mirrors);
    await assert.rejects(
      requireDockerHubMirror(
        [`ghcr.io/a/b${digest}`, `postgres:18.4${digest}`],
        [docker.path],
      ),
      (error) =>
        error.message.includes(`postgres:18.4${digest}`) &&
        !error.message.includes("ghcr.io") &&
        error.message.includes(".github/actions/docker-hub-mirror"),
    );
  });

test("other registries need no mirror and no daemon question", async (t) => {
  const docker = fakeDocker(t, "[]");
  await requireDockerHubMirror(
    [`mcr.microsoft.com/playwright:v1.64.1-noble${digest}`],
    [docker.path],
  );
  assert.deepEqual(docker.calls(), []);
});
