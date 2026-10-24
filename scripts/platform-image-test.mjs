import { execFileSync } from "node:child_process";
import { randomBytes, randomUUID } from "node:crypto";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const host = process.env.PLAT_APP_DOCKER_HOST;
if (!host?.startsWith("unix:///"))
  throw new Error("PLAT_APP_DOCKER_HOST must select a disposable local daemon");
const id = randomUUID(),
  label = `com.astilba.p1-test=${id}`,
  image = `astilba-p1-test:${id}`,
  network = `astilba-p1-${id}`,
  volume = `astilba-p1-${id}`,
  db = `astilba-db-${id}`,
  app = `astilba-app-${id}`,
  password = randomBytes(32).toString("hex");
const docker = (args, options = {}) =>
  execFileSync("docker", ["--host", host, ...args], {
    cwd: root,
    encoding: "utf8",
    stdio: ["pipe", "pipe", "pipe"],
    ...options,
  });
const directory = await mkdtemp(join(tmpdir(), "astilba-image-"));
let madeNetwork = false,
  madeVolume = false;
const containers = [];
const extraVolumes = [];
try {
  console.log("Building application image…");
  docker(
    [
      "build",
      "--build-arg",
      `SOURCE_REVISION=${process.env.SOURCE_REVISION ?? "development"}`,
      "--label",
      label,
      "-t",
      image,
      ".",
    ],
    { stdio: "inherit", timeout: 600000 },
  );
  if (
    docker([
      "image",
      "inspect",
      "--format",
      "{{.Config.User}}",
      image,
    ]).trim() !== "65532:65532"
  )
    throw new Error("Image must run as non-root");
  docker([
    "run",
    "--rm",
    "--entrypoint",
    "/bin/sh",
    image,
    "-c",
    "! command -v node",
  ]);
  docker(["network", "create", "--label", label, network]);
  madeNetwork = true;
  docker(["volume", "create", "--label", label, volume]);
  madeVolume = true;
  const envfile = join(directory, "database.env");
  await writeFile(envfile, `POSTGRES_PASSWORD=${password}\n`, { mode: 0o600 });
  docker([
    "run",
    "-d",
    "--name",
    db,
    "--label",
    label,
    "--network",
    network,
    "--network-alias",
    "database",
    "--env-file",
    envfile,
    "postgres:18.4-alpine3.23@sha256:996d0920e4ff9df1fc19dacb904492f3c1ec0ec1cc338f0ad7123be7731c5f5e",
  ]);
  containers.push(db);
  let ready = false;
  for (let i = 0; i < 100; i++) {
    try {
      docker(["exec", db, "pg_isready", "-h", "127.0.0.1", "-U", "postgres"]);
      ready = true;
      break;
    } catch {
      await new Promise((r) => setTimeout(r, 100));
    }
  }
  if (!ready) throw new Error("Postgres not ready");
  docker([
    "run",
    "--rm",
    "--user",
    "0:0",
    "-v",
    `${volume}:/keys`,
    "--entrypoint",
    "/bin/sh",
    image,
    "-c",
    "astilba keygen --out /keys/keyring.json && chown 65532:65532 /keys/keyring.json",
  ]);
  const runtime = join(directory, "runtime.env");
  await writeFile(
    runtime,
    `ASTILBA_DATABASE_URL=postgres://postgres:${password}@database:5432/postgres?sslmode=disable\nASTILBA_KEYRING=/keys/keyring.json\nASTILBA_ORIGIN=https://example.test\nASTILBA_LISTEN=0.0.0.0:8080\n`,
    { mode: 0o600 },
  );
  const common = [
    "--network",
    network,
    "--env-file",
    runtime,
    "-v",
    `${volume}:/keys:ro`,
  ];
  docker(["run", "--rm", ...common, image, "migrate"]);
  docker(
    [
      "run",
      "--rm",
      "-i",
      ...common,
      image,
      "bootstrap",
      "--email",
      "owner@example.test",
      "--name",
      "Owner",
      "--workspace",
      "Test",
    ],
    { input: "correct horse battery staple" },
  );
  docker([
    "run",
    "-d",
    "--name",
    app,
    "--label",
    label,
    ...common,
    "--read-only",
    "--cap-drop",
    "ALL",
    "--security-opt",
    "no-new-privileges:true",
    "-p",
    "127.0.0.1::8080",
    image,
  ]);
  containers.push(app);
  const address = docker(["port", app, "8080/tcp"]).trim();
  ready = false;
  for (let i = 0; i < 100; i++) {
    try {
      if ((await fetch(`http://${address}/healthz`)).status === 204) {
        ready = true;
        break;
      }
    } catch {}
    await new Promise((r) => setTimeout(r, 100));
  }
  if (!ready) throw new Error("Image server did not become ready");
  const html = await (await fetch(`http://${address}/`)).text();
  const notice = await (
    await fetch(`http://${address}/source/NOTICE.txt`)
  ).text();
  if (
    !html.includes('<div id="root">') ||
    !notice.includes("GNU AGPL version 3")
  )
    throw new Error("Embedded assets or source notices missing");
  const connect = (base) => {
    let cookie = "", csrf = "";
    return async (path, body, expected = 200) => {
      const response = await fetch(`${base}${path}`, {
        method: body === undefined ? "GET" : "POST",
        headers: { Cookie: cookie, Origin: "https://example.test", "X-CSRF-Token": csrf, ...(body === undefined ? {} : { "Content-Type": "application/json" }) },
        body: body === undefined ? undefined : JSON.stringify(body),
      });
      const nextCookie = response.headers.getSetCookie()[0];
      if (nextCookie) cookie = nextCookie.split(";")[0];
      if (response.status !== expected) throw new Error(`Packaged API ${path} returned ${response.status}, expected ${expected}`);
      const value = response.status === 204 ? undefined : await response.json();
      if (value?.csrfToken) csrf = value.csrfToken;
      return value;
    };
  };
  const original = connect(`http://${address}`);
  await original("/api/v1/session");
  const session = await original("/api/v1/auth/login", { email: "owner@example.test", password: "correct horse battery staple" });
  const workspace = session.workspaces[0].id;
  const root = `/api/v1/workspaces/${workspace}`;
  const project = await original(`${root}/projects`, { name: "Recovery" }, 201);
  const environment = await original(`${root}/projects/${project.id}/environments`, { name: "Production" }, 201);
  const secret = await original(`${root}/projects/${project.id}/environments/${environment.id}/secrets`, { name: "RESTORE_TOKEN", value: "packaged-restore-canary" }, 201);
  const server = await original(`${root}/servers`, { name: "Recovery host", role: "deployment" }, 201);
  const credential = randomBytes(32).toString("base64url");
  await original("/agent/v1/enroll", { serverId: server.server.id, token: server.token, credential, machineId: randomUUID(), capabilities: { protocol: 1, version: "fixture", os: "linux", arch: "arm64", docker: true }, preflight: { ready: true, osRelease: "Ubuntu 24.04", dockerVersion: "fixture", port80Busy: false, port443Busy: false, freeBytes: 10000000000, existingContainers: 0, notes: [] } });
  const plan = await original(`${root}/plans`, { serverId: server.server.id, environmentId: environment.id, action: "inspect" }, 201);
  const operation = await original(`${root}/plans/${plan.id}/apply`, { idempotencyKey: randomUUID() }, 201);
  docker(["stop", app]);
  const databaseBackup = docker(["exec", db, "pg_dump", "--username", "postgres", "--format=custom", "--no-owner", "postgres"], { encoding: null });
  const restoredDatabase = `astilba-restored-db-${id}`;
  docker(["run", "-d", "--name", restoredDatabase, "--label", label, "--network", network, "--network-alias", "restored-database", "--env-file", envfile, "postgres:18.4-alpine3.23@sha256:996d0920e4ff9df1fc19dacb904492f3c1ec0ec1cc338f0ad7123be7731c5f5e"]);
  containers.push(restoredDatabase);
  let restoredReady = false;
  for (let attempt = 0; attempt < 100; attempt++) {
    try { docker(["exec", restoredDatabase, "pg_isready", "-h", "127.0.0.1", "-U", "postgres"]); restoredReady = true; break; }
    catch { await new Promise((resolve) => setTimeout(resolve, 100)); }
  }
  if (!restoredReady) throw new Error("Restored PostgreSQL did not become ready");
  docker(["exec", "-i", restoredDatabase, "pg_restore", "--username", "postgres", "--dbname", "postgres", "--no-owner", "--exit-on-error", "--single-transaction"], { input: databaseBackup });
  const restoredKeys = `astilba-restored-keys-${id}`;
  docker(["volume", "create", "--label", label, restoredKeys]); extraVolumes.push(restoredKeys);
  docker(["run", "--rm", "--user", "0:0", "-v", `${volume}:/source:ro`, "-v", `${restoredKeys}:/keys`, "--entrypoint", "/bin/sh", image, "-c", "cp /source/keyring.json /keys/keyring.json && chown 65532:65532 /keys/keyring.json && chmod 600 /keys/keyring.json"]);
  const restoredRuntime = join(directory, "restored.env");
  await writeFile(restoredRuntime, `ASTILBA_DATABASE_URL=postgres://postgres:${password}@restored-database:5432/postgres?sslmode=disable\nASTILBA_KEYRING=/keys/keyring.json\nASTILBA_ORIGIN=https://example.test\nASTILBA_LISTEN=0.0.0.0:8080\n`, { mode: 0o600 });
  const restoredCommon = ["--network", network, "--env-file", restoredRuntime, "-v", `${restoredKeys}:/keys:ro`];
  docker(["run", "--rm", ...restoredCommon, image, "rewrap"]);
  docker(["run", "--rm", ...restoredCommon, image, "prepare-recovery", "--server", server.server.id, "--controller-generation", "1"]);
  const restoredApp = `astilba-restored-app-${id}`;
  docker(["run", "-d", "--name", restoredApp, "--label", label, ...restoredCommon, "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "-p", "127.0.0.1::8080", image]);
  containers.push(restoredApp);
  const restoredAddress = docker(["port", restoredApp, "8080/tcp"]).trim();
  restoredReady = false;
  for (let attempt = 0; attempt < 100; attempt++) {
    try { if ((await fetch(`http://${restoredAddress}/healthz`)).status === 204) { restoredReady = true; break; } }
    catch { /* Wait for the restored process to listen. */ }
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  if (!restoredReady) throw new Error("Restored image did not become ready");
  const restored = connect(`http://${restoredAddress}`);
  await restored("/api/v1/session");
  const restoredSession = await restored("/api/v1/auth/login", { email: "owner@example.test", password: "correct horse battery staple" });
  if (restoredSession.workspaces[0].id !== workspace) throw new Error("Restored workspace identity changed");
  const secrets = await restored(`${root}/projects/${project.id}/environments/${environment.id}/secrets`);
  if (!secrets.some((value) => value.id === secret.id && value.secretId === secret.secretId)) throw new Error("Restored secret metadata missing");
  const operations = await restored(`${root}/operations`);
  if (!operations.some((value) => value.id === operation.id && value.state === "cancelled")) throw new Error("Restored pending operation was replayable");
  const denied = await fetch(`http://${restoredAddress}/agent/v1/heartbeat`, { method: "POST", headers: { "Content-Type": "application/json", Authorization: `Bearer ${server.server.id}.${credential}` }, body: JSON.stringify({ capabilities: { protocol: 1, version: "fixture", os: "linux", arch: "arm64", docker: true }, preflight: { ready: true, osRelease: "Ubuntu 24.04", dockerVersion: "fixture", port80Busy: false, port443Busy: false, freeBytes: 10000000000, existingContainers: 0, notes: [] } }) });
  if (denied.status !== 401) throw new Error("Restored controller accepted old agent credentials");
  await denied.body?.cancel();
  console.log("Image passed: non-root Node-free installation, explicit migrations/bootstrap, source notices, separate database/key restore and stale-work fencing.");
} finally {
  for (const name of containers.reverse()) {
    const actual = docker([
      "inspect",
      "--format",
      '{{ index .Config.Labels "com.astilba.p1-test" }}',
      name,
    ]).trim();
    if (actual === id) docker(["rm", "-f", "-v", name]);
  }
  for (const name of extraVolumes) {
    const actual = docker(["volume", "inspect", "--format", '{{ index .Labels "com.astilba.p1-test" }}', name]).trim();
    if (actual === id) docker(["volume", "rm", name]);
  }
  for (const [kind, name, made] of [
    ["volume", volume, madeVolume],
    ["network", network, madeNetwork],
  ]) {
    if (made) {
      const actual = docker([
        kind,
        "inspect",
        "--format",
        '{{ index .Labels "com.astilba.p1-test" }}',
        name,
      ]).trim();
      if (actual === id) docker([kind, "rm", name]);
    }
  }
  try {
    if (
      docker([
        "image",
        "inspect",
        "--format",
        '{{ index .Config.Labels "com.astilba.p1-test" }}',
        image,
      ]).trim() === id
    )
      docker(["image", "rm", image]);
  } catch {}
  await rm(directory, { recursive: true, force: true });
}
