import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import { checkProfile } from "./platform-dev-host.mjs";
import { allowed } from "./platform-dev-proxy.mjs";

const read = (path) =>
  readFileSync(new URL(`../${path}`, import.meta.url), "utf8");

test("the local VM installs the Compose package the guided bootstrap pins", () => {
  const bootstrap = read("services/platform/host/bootstrap.sh.tmpl");
  const local = read("scripts/platform-dev-host.sh");
  // The bootstrap lists containerd, the Docker CLI, Docker and then Compose for each architecture.
  for (const [machine, arch] of [
    ["x86_64", "amd64"],
    ["aarch64", "arm64"],
  ]) {
    const pins = new RegExp(
      `${machine}\\)\\n\\s+arch=${arch}\\n\\s+runtime_shas=\\(([a-f0-9 ]+)\\)`,
    ).exec(bootstrap);
    assert.ok(pins, `bootstrap pins for ${arch}`);
    const compose = pins[1].split(" ")[3];
    assert.match(
      local,
      new RegExp(`${machine}\\) arch=${arch} compose_sha=${compose} `),
    );
  }
  assert.match(
    bootstrap,
    /docker-compose-plugin_5\.6\.1-1~ubuntu\.24\.04~noble_\$arch\.deb/,
  );
  assert.match(
    local,
    /docker-compose-plugin_5\.6\.1-1~ubuntu\.24\.04~noble_\$arch\.deb/,
  );
});

test("the VM's management link carries only agent paths", () => {
  const server = "0b5c3d9e-8f43-4b1e-9d55-2f7a9a1c6e01";
  for (const path of [
    "/agent/v1/poll",
    `/agent/v1/troubleshooting/${server}/stream`,
    `/agent-artifacts/${"a".repeat(64)}/manifest.json`,
    "/agent-public-key.pem",
  ])
    assert.ok(allowed(path), path);
  for (const path of [
    "/api/v1/session",
    "/agent/v1/../../api/v1/session",
    "/agent/v1/%2e%2e/api/v1/session",
    "/agent/v1/.%2e/api",
    "/agent//v1/poll/../../api",
    "/agent",
    "/agent-public-key.pem/x",
    "/healthz",
  ])
    assert.ok(!allowed(path), path);
});

test("the stack refuses Colima's default profile and unsafe names", () => {
  assert.doesNotThrow(() => checkProfile("astilba-dev"));
  for (const name of ["default", "colima", "", "Astilba", "a b", "-x", "a;b"])
    assert.throws(() => checkProfile(name), name);
});
