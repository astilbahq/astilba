import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import { backupRoot, checkProfile } from "./platform-dev-host.mjs";
import { allowed } from "./platform-dev-proxy.mjs";
import { stackVM } from "./platform-dev-vm.mjs";

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
  // Colima reads `colima-NAME` as profile NAME, so it would reach someone else's VM.
  for (const name of [
    "default",
    "colima",
    "colima-astilba-dev",
    "",
    "Astilba",
    "a b",
    "-x",
    "a;b",
  ])
    assert.throws(() => checkProfile(name), name);
});

test("the agent's allowed backup root is the one the seed's destination uses", () => {
  const local = read("scripts/platform-dev-host.sh");
  assert.match(local, new RegExp(`^backups=${backupRoot}$`, "m"));
});

// A Colima profile as the stack sees it. `marker` is the owner the VM records, or a
// function that throws when the VM cannot be reached.
function fakeVM({ status = "Running", marker = "ours", create } = {}) {
  const calls = [];
  const vm = {
    profile: "astilba-dev",
    calls,
    status: async () => status,
    marker: async () => (typeof marker === "function" ? marker() : marker),
    start: async () => {
      calls.push("start");
      status = "Running";
    },
    create: async () => {
      calls.push("create");
      if (create) {
        status = create.status;
        throw new Error("create failed");
      }
      status = "Running";
      marker = "none";
    },
    claim: async () => {
      calls.push("claim");
      marker = "ours";
    },
    remove: async () => {
      calls.push("remove");
      status = "absent";
    },
    stop: async () => {
      calls.push("stop");
      status = "Stopped";
    },
  };
  return vm;
}
const stack = (vm, state) => {
  const saves = [];
  return {
    saves,
    lifecycle: stackVM({
      vm,
      state,
      saveState: () => saves.push({ ...state }),
    }),
  };
};
const quietly = async (work) => {
  const log = console.log;
  console.log = () => {};
  try {
    return await work();
  } finally {
    console.log = log;
  }
};

test("wipe deletes only this stack's VM, or an unmarked one it was creating", async () => {
  for (const [vmState, marker, deleted] of [
    [true, "ours", true],
    [true, "other", false],
    [true, "none", false],
    ["creating", "none", true],
    ["creating", "other", false],
  ]) {
    const vm = fakeVM({ marker });
    const { lifecycle } = stack(vm, { vm: vmState });
    assert.equal(await quietly(() => lifecycle.remove()), deleted);
    assert.equal(vm.calls.includes("remove"), deleted, `${vmState} ${marker}`);
  }
});

test("an interrupted creation is redone only while no other stack has claimed it", async () => {
  const claimed = fakeVM({ marker: "other" });
  await assert.rejects(
    quietly(() => stack(claimed, { vm: "creating" }).lifecycle.bring()),
    /claimed by another stack/,
  );
  assert.deepEqual(claimed.calls, ["start"]);

  const unmarked = fakeVM({ marker: "none" });
  const state = { vm: "creating" };
  await quietly(() => stack(unmarked, state).lifecycle.bring());
  assert.deepEqual(unmarked.calls, ["start", "remove", "create", "claim"]);
  assert.equal(state.vm, true);
});

test("a creation that leaves no profile forgets it was creating one", async () => {
  for (const state of [{}, { vm: "creating" }]) {
    const vm = fakeVM({
      status: state.vm ? "Running" : "absent",
      marker: "none",
      create: { status: "absent" },
    });
    await assert.rejects(quietly(() => stack(vm, state).lifecycle.bring()));
    assert.equal(state.vm, undefined);
  }
});

test("the way out stops only a VM this run checked, started or created for itself", async () => {
  const startedOnly = fakeVM({ status: "Stopped", marker: "other" });
  const wiping = stack(startedOnly, { vm: true }).lifecycle;
  assert.equal(await quietly(() => wiping.remove()), false);
  await quietly(() => wiping.release());
  assert.deepEqual(startedOnly.calls, ["start", "stop"]);

  const untouched = fakeVM({ marker: "other" });
  await quietly(() => stack(untouched, { vm: true }).lifecycle.release());
  assert.deepEqual(untouched.calls, []);

  // Another stack won the race to claim the profile this run was creating.
  const lost = fakeVM({ status: "absent" });
  lost.claim = async () => {
    throw new Error("already claimed");
  };
  const racing = stack(lost, {}).lifecycle;
  await assert.rejects(quietly(() => racing.bring()));
  lost.marker = async () => "other";
  await quietly(() => racing.release());
  assert.deepEqual(lost.calls, ["create"]);

  // Interrupted before the VM answers: nothing shows it is another stack's.
  const early = fakeVM({ status: "absent" });
  early.claim = async () => {
    throw new Error("interrupted");
  };
  const interrupted = stack(early, {}).lifecycle;
  await assert.rejects(quietly(() => interrupted.bring()));
  early.marker = async () => {
    throw new Error("ssh not up");
  };
  await quietly(() => interrupted.release());
  assert.deepEqual(early.calls, ["create", "stop"]);
});

test("the stack keeps the custody it was created with", async () => {
  const { checkCustody } = await import("./platform-dev-custody.mjs");
  checkCustody({ custody: "file", stack: false });
  checkCustody({ custody: "openbao", stack: true });
  // An older stack, or one that never started, may choose.
  checkCustody({ custody: "file", stack: true, saved: { initialized: true } });
  checkCustody({
    custody: "openbao",
    stack: true,
    saved: { initialized: false, custody: "file" },
  });
  for (const [args, message] of [
    [{ custody: "vault", stack: true }, /--custody must be/],
    [{ custody: "openbao", stack: false }, /add --vm/],
    [
      { custody: "openbao", stack: true, saved: { initialized: true } },
      /--custody=file; pass that, or start over with --reset/,
    ],
    [
      {
        custody: "file",
        stack: true,
        saved: { initialized: true, custody: "openbao" },
      },
      /--custody=openbao/,
    ],
  ])
    assert.throws(() => checkCustody(args), message);
});

test("the stack's OpenBao listens only on the VM's loopback, with an audit device", async () => {
  const { openBaoRun, openBaoImage } = await import(
    "./platform-dev-custody.mjs"
  );
  const args = openBaoRun({
    name: "astilba-dev-x-openbao",
    label: "com.astilba.dev-owner",
    owner: "x",
    port: 8200,
  });
  assert.equal(args[args.indexOf("-p") + 1], "127.0.0.1:8200:8200");
  assert.ok(args.includes(openBaoImage));
  assert.match(openBaoImage, /^openbao\/openbao:[0-9.]+@sha256:[0-9a-f]{64}$/);
  const config = args.find((a) => a.startsWith("ASTILBA_OPENBAO_CONFIG="));
  assert.match(config, /audit "file"/);
  assert.match(config, /storage "file"/);
  assert.doesNotMatch(config, /-dev/);
  // The integration tests run the same release.
  assert.ok(
    read("services/platform/internal/integration/openbao_test.go").includes(
      openBaoImage,
    ),
  );
});

test("the stack's OpenBao is initialised once and unsealed on every start", async () => {
  const { mkdtempSync, readFileSync: read, statSync } = await import("node:fs");
  const { tmpdir } = await import("node:os");
  const { join } = await import("node:path");
  const { openBao } = await import("./platform-dev-custody.mjs");
  const directory = mkdtempSync(join(tmpdir(), "astilba-openbao-"));
  let status = { initialized: false, sealed: true };
  const calls = [];
  const fetch = async (url, { method, body }) => {
    const path = new URL(url).pathname;
    calls.push(`${method} ${path}`);
    let answer = {};
    if (path === "/v1/sys/seal-status") answer = status;
    if (path === "/v1/sys/init") {
      status = { initialized: true, sealed: true };
      answer = { keys_base64: ["unseal-key"], root_token: "root-token" };
    }
    if (path === "/v1/sys/unseal") {
      assert.equal(JSON.parse(body).key, "unseal-key");
      status = { initialized: true, sealed: false };
      answer = status;
    }
    return new Response(JSON.stringify(answer), { status: 200 });
  };
  const vault = openBao({ directory, fetch });
  await vault.unseal("http://127.0.0.1:1");
  assert.equal(read(vault.tokenFile, "utf8"), "root-token\n");
  assert.equal(statSync(vault.tokenFile).mode & 0o777, 0o600);
  assert.equal(statSync(join(directory, "openbao.json")).mode & 0o777, 0o600);
  status = { initialized: true, sealed: true };
  await vault.unseal("http://127.0.0.1:1");
  assert.deepEqual(calls, [
    "GET /v1/sys/seal-status",
    "PUT /v1/sys/init",
    "PUT /v1/sys/unseal",
    "GET /v1/sys/seal-status",
    "PUT /v1/sys/unseal",
  ]);
  // A server that lost its storage is never initialised again over the keys it held.
  status = { initialized: false, sealed: true };
  await assert.rejects(vault.unseal("http://127.0.0.1:1"), /lost its storage/);
});
