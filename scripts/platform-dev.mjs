import { spawn } from "node:child_process";
import { randomBytes, randomUUID } from "node:crypto";
import {
  appendFileSync,
  chmodSync,
  existsSync,
  mkdirSync,
  readFileSync,
  renameSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { createServer } from "node:net";
import { basename, join, resolve } from "node:path";
import { setTimeout as delay } from "node:timers/promises";
import { parseArgs } from "node:util";

const { values } = parseArgs({
  options: {
    port: { type: "string", default: "54182" },
    reset: { type: "boolean", default: false },
    help: { type: "boolean", default: false },
  },
});
if (values.help) {
  console.log(
    "pnpm dev [--port 54182] [--reset]\nStarts a local demo. API uses port - 1; PostgreSQL gets a free loopback port.\n--reset deletes this checkout's demo database before starting.\nRequires PLAT_APP_DOCKER_HOST, Node 24.18.1, pnpm 11.17.0 and Go 1.27.3.",
  );
  process.exit(0);
}
if (
  process.versions.node !== "24.18.1" ||
  !process.env.npm_execpath ||
  !process.env.npm_config_user_agent?.startsWith("pnpm/11.17.0 ")
)
  throw new Error("Run pnpm dev with Node 24.18.1 and pnpm 11.17.0");
const port = Number(values.port);
if (!Number.isInteger(port) || port < 1025 || port > 65535)
  throw new Error("--port must be an integer from 1025 to 65535");
const host = process.env.PLAT_APP_DOCKER_HOST;
if (!host?.startsWith("unix:///"))
  throw new Error(
    "Set PLAT_APP_DOCKER_HOST to a disposable local Docker Unix socket",
  );

const root = resolve(import.meta.dirname, "..");
const directory = join(root, ".artifacts/dev");
const lock = join(root, ".artifacts/dev.lock");
const stateFile = join(directory, "state.json");
const keyring = join(directory, "keyring.json");
const binary = join(directory, "astilba");
const image =
  "postgres:18.4-alpine3.23@sha256:996d0920e4ff9df1fc19dacb904492f3c1ec0ec1cc338f0ad7123be7731c5f5e";
const label = "com.astilba.dev-owner";
const abort = new AbortController();
const active = new Set();
const services = new Set();
let state;
let locked = false;
let stopping = false;
let failure;
let stopped;
const stopRequested = new Promise((resolve) => {
  stopped = resolve;
});

function signalGroup(child, signal) {
  if (!child.pid) return;
  try {
    process.kill(-child.pid, signal);
  } catch (error) {
    if (error.code !== "ESRCH") throw error;
  }
}
function stop(error) {
  if (stopping) return;
  stopping = true;
  failure = error;
  abort.abort();
  for (const child of new Set([...active, ...services]))
    signalGroup(child, "SIGTERM");
  stopped();
}
for (const signal of ["SIGINT", "SIGTERM"]) process.on(signal, () => stop());

function start(
  command,
  args,
  {
    env = process.env,
    cwd = root,
    input,
    inherit = false,
    cleanup = false,
  } = {},
) {
  if (stopping && !cleanup) throw new Error("Startup cancelled");
  const child = spawn(command, args, {
    cwd,
    env,
    detached: true,
    stdio: [
      input === undefined ? "ignore" : "pipe",
      inherit ? "inherit" : "pipe",
      inherit ? "inherit" : "pipe",
    ],
  });
  active.add(child);
  // Keep child groups discoverable if SIGKILL prevents our finally block.
  if (child.pid)
    appendFileSync(
      join(lock, "processes"),
      `${child.pid} ${basename(command)}\n`,
    );
  let output = "";
  let error;
  child.stdout?.setEncoding("utf8").on("data", (chunk) => {
    output += chunk;
  });
  child.stderr?.resume();
  child.on("error", (cause) => {
    error = cause;
  });
  child.stdin?.on("error", () => {});
  child.stdin?.end(input);
  child.done = new Promise((resolve) =>
    child.once("close", (code) => {
      active.delete(child);
      resolve({ code, error, output });
    }),
  );
  return child;
}
async function run(command, args, options) {
  const child = start(command, args, options);
  const timeout = setTimeout(() => signalGroup(child, "SIGKILL"), 180000);
  try {
    const result = await child.done;
    if (result.error || result.code !== 0)
      throw new Error(
        `${basename(command)} failed${result.error?.code ? ` (${result.error.code})` : ""}; check prerequisites and the selected Docker daemon`,
      );
    return result.output.trim();
  } finally {
    clearTimeout(timeout);
  }
}
const docker = (args, cleanup = false) =>
  run("docker", ["--host", host, ...args], { cleanup });
function saveState() {
  writeFileSync(`${stateFile}.tmp`, JSON.stringify(state), { mode: 0o600 });
  renameSync(`${stateFile}.tmp`, stateFile);
}
async function container(cleanup = false) {
  const id = await docker(
    ["ps", "-aq", "--filter", `name=^/astilba-dev-${state.owner}$`],
    cleanup,
  );
  if (!id) return;
  const owner = await docker(
    ["inspect", "--format", `{{ index .Config.Labels "${label}" }}`, id],
    cleanup,
  );
  if (owner !== state.owner)
    throw new Error("Demo container ownership mismatch; refusing to alter it");
  return id;
}
async function freePort(value) {
  const server = createServer();
  await new Promise((resolve, reject) => {
    server.once("error", () =>
      reject(
        new Error(
          `Port ${value} is in use; stop that preview or choose --port`,
        ),
      ),
    );
    server.listen(value, "127.0.0.1", () => server.close(resolve));
  });
}
async function waitFor(check, description) {
  for (let i = 0; i < 150; i++) {
    abort.signal.throwIfAborted();
    if (await check()) return;
    await delay(200, undefined, { signal: abort.signal });
  }
  throw new Error(`${description} did not become ready`);
}
async function httpReady(url, status) {
  try {
    const response = await fetch(url, {
      signal: AbortSignal.any([abort.signal, AbortSignal.timeout(1000)]),
    });
    await response.body?.cancel();
    return response.status === status;
  } catch {
    return false;
  }
}
function serve(command, args, env) {
  const child = start(command, args, { env, inherit: true });
  services.add(child);
  void child.done.then(() => {
    if (!stopping) stop(new Error(`${basename(command)} exited unexpectedly`));
  });
}

try {
  mkdirSync(join(root, ".artifacts"), { recursive: true });
  try {
    mkdirSync(lock, { mode: 0o700 });
  } catch (error) {
    if (error.code === "EEXIST")
      throw new Error(
        "A demo launcher holds .artifacts/dev.lock. See docs/frontend-development.md for shutdown recovery.",
      );
    throw error;
  }
  locked = true;
  writeFileSync(
    join(lock, "launcher"),
    `${process.pid}\nUI ${port}\nAPI ${port - 1}\n`,
    { mode: 0o600 },
  );
  writeFileSync(join(lock, "processes"), "", { mode: 0o600 });
  await freePort(port);
  await freePort(port - 1);
  mkdirSync(directory, { recursive: true, mode: 0o700 });
  chmodSync(directory, 0o700);
  if (existsSync(stateFile)) {
    let saved;
    try {
      saved = JSON.parse(readFileSync(stateFile, "utf8"));
    } catch {
      throw new Error(
        "Cannot read .artifacts/dev/state.json; restore the local state before continuing",
      );
    }
    if (saved.version !== 1 || saved.root !== root || saved.host !== host)
      throw new Error(
        "Demo state belongs to another checkout or Docker daemon; use its original checkout and PLAT_APP_DOCKER_HOST",
      );
    state = saved;
  }
  if (values.reset && state) {
    const id = await container();
    if (id) await docker(["rm", "--force", "--volumes", id]);
    rmSync(directory, { recursive: true });
    mkdirSync(directory, { mode: 0o700 });
    state = undefined;
  }
  if (!state) {
    state = {
      version: 1,
      root,
      host,
      owner: randomUUID(),
      password: randomBytes(32).toString("hex"),
      initialized: false,
    };
    saveState();
  }
  let id = await container();
  if (state.initialized && (!id || !existsSync(keyring)))
    throw new Error(
      "Demo data or keyring is missing; restore it or explicitly start over with --reset",
    );
  if (id && !existsSync(keyring))
    throw new Error(
      "Demo container exists without its keyring; restore it or use --reset",
    );
  console.log("Building the local API…");
  await run("go", ["build", "-o", binary, "./cmd/server"], {
    cwd: join(root, "services/platform"),
    inherit: true,
  });
  // Local preview must not inherit production database, mail, billing or release settings.
  const env = Object.fromEntries(
    Object.entries(process.env).filter(([key]) => !key.startsWith("ASTILBA_")),
  );
  Object.assign(env, {
    ASTILBA_KEYRING: keyring,
    ASTILBA_DEVELOPMENT: "true",
    ASTILBA_HOSTED: "false",
    ASTILBA_LISTEN: `127.0.0.1:${port - 1}`,
    ASTILBA_ORIGIN: `http://127.0.0.1:${port}`,
  });
  if (!existsSync(keyring))
    await run(binary, ["keygen", "--out", keyring], { env });
  if (!id) {
    const envfile = join(directory, "postgres.env");
    writeFileSync(envfile, `POSTGRES_PASSWORD=${state.password}\n`, {
      mode: 0o600,
    });
    await docker([
      "run",
      "-d",
      "--name",
      `astilba-dev-${state.owner}`,
      "--label",
      `${label}=${state.owner}`,
      "-p",
      "127.0.0.1::5432",
      "--env-file",
      envfile,
      image,
    ]);
    id = await container();
  } else await docker(["start", id]);
  await waitFor(async () => {
    try {
      await docker([
        "exec",
        id,
        "pg_isready",
        "-h",
        "127.0.0.1",
        "-U",
        "postgres",
      ]);
      return true;
    } catch {
      return false;
    }
  }, "PostgreSQL");
  const address = await docker(["port", id, "5432/tcp"]);
  if (!/^127\.0\.0\.1:\d+$/.test(address))
    throw new Error("Demo PostgreSQL must publish one loopback port");
  env.ASTILBA_DATABASE_URL = `postgres://postgres:${state.password}@${address}/postgres?sslmode=disable`;
  await run(binary, ["migrate"], { env, inherit: true });
  const count = await docker([
    "exec",
    id,
    "psql",
    "-U",
    "postgres",
    "-Atc",
    "SELECT count(*) FROM accounts",
  ]);
  if (count === "0")
    await run(
      binary,
      [
        "bootstrap",
        "--email",
        "owner@example.test",
        "--name",
        "Owner",
        "--workspace",
        "UI Sandbox",
      ],
      { env, input: "correct horse battery staple" },
    );
  state.initialized = true;
  saveState();
  serve(binary, ["serve"], env);
  await waitFor(
    () => httpReady(`http://127.0.0.1:${port - 1}/healthz`, 204),
    "API",
  );
  serve(
    process.execPath,
    [process.env.npm_execpath, "--filter", "@astilba/dashboard", "dev"],
    {
      ...env,
      ASTILBA_DEV_UI_PORT: String(port),
      ASTILBA_DEV_API_PORT: String(port - 1),
    },
  );
  await waitFor(
    () => httpReady(`http://127.0.0.1:${port}/api/v1/session`, 200),
    "Frontend",
  );
  console.log(
    `\nDemo ready: http://127.0.0.1:${port}/login\nSign in: owner@example.test / correct horse battery staple\nCtrl-C stops the preview and retains its database. Use --reset to start over.\n`,
  );
  await stopRequested;
} catch (error) {
  if (!stopping) stop(error);
} finally {
  if (locked) {
    stop();
    const children = new Set([...active, ...services]);
    const kill = setTimeout(() => {
      for (const child of children) signalGroup(child, "SIGKILL");
    }, 5000);
    await Promise.all([...children].map((child) => child.done));
    clearTimeout(kill);
    try {
      if (state) {
        const id = await container(true);
        if (id) await docker(["stop", "--time", "5", id], true);
      }
    } catch (error) {
      failure ??= error;
    }
    rmSync(lock, { recursive: true });
  }
}
if (failure) {
  console.error(failure.message);
  process.exitCode = 1;
}
