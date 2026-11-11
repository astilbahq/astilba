import { randomBytes, randomUUID } from "node:crypto";
import {
  chmodSync,
  existsSync,
  mkdirSync,
  readFileSync,
  renameSync,
  rmSync,
  watch,
  writeFileSync,
} from "node:fs";
import { join, resolve } from "node:path";
import { parseArgs } from "node:util";
import { devAgent } from "./platform-dev-agent.mjs";
import {
  agentOrigin,
  checkProfile,
  devHost,
  vmPorts,
} from "./platform-dev-host.mjs";
import {
  anyPort,
  buildChanged,
  freePort,
  supervisor,
  tcpReady,
} from "./platform-dev-process.mjs";
import { agentProxy } from "./platform-dev-proxy.mjs";
import { routeDomain, seed } from "./platform-dev-seed.mjs";

const { values } = parseArgs({
  allowNegative: true,
  options: {
    port: { type: "string", default: "54182" },
    vm: { type: "string" },
    reset: { type: "boolean", default: false },
    wipe: { type: "boolean", default: false },
    watch: { type: "boolean", default: true },
    help: { type: "boolean", default: false },
  },
});
if (values.help) {
  console.log(`pnpm dev [--port 54182] [--vm PROFILE] [--reset | --wipe] [--no-watch]
Starts management and the dashboard locally. The API uses port - 1.
--vm PROFILE also runs the database and an enrolled agent in that disposable Colima
  profile, creating it if needed; deployed routes answer on port - 2. Without it,
  PostgreSQL runs on PLAT_APP_DOCKER_HOST and there is no agent.
--reset deletes this checkout's local data (and the VM it created) before starting.
--wipe deletes it and exits.
Go changes rebuild and restart management and the agent unless --no-watch.
Requires Node 24.18.1, the pinned pnpm and Go 1.27.4. See docs/local-development.md.`);
  process.exit(0);
}
const { packageManager } = JSON.parse(
  readFileSync(new URL("../package.json", import.meta.url), "utf8"),
);
if (
  process.versions.node !== "24.18.1" ||
  !process.env.npm_execpath ||
  !process.env.npm_config_user_agent?.startsWith(
    `${packageManager.replace("@", "/")} `,
  )
)
  throw new Error(`Run pnpm dev with Node 24.18.1 and ${packageManager}`);
const port = Number(values.port);
if (!Number.isInteger(port) || port < 1027 || port > 65535)
  throw new Error("--port must be an integer from 1027 to 65535");
const stack = values.vm !== undefined;
if (stack) checkProfile(values.vm);
if (values.reset && values.wipe) throw new Error("Choose --reset or --wipe");
let host = stack ? undefined : process.env.PLAT_APP_DOCKER_HOST;
if (!stack && !host?.startsWith("unix:///"))
  throw new Error(
    "Set PLAT_APP_DOCKER_HOST to a disposable local Docker Unix socket, or use --vm PROFILE",
  );
const ingressPort = port - 2;

const root = resolve(import.meta.dirname, "..");
const platform = join(root, "services/platform");
// The demo and the full stack keep separate data, so switching between them loses neither.
const directory = join(root, stack ? ".artifacts/dev-stack" : ".artifacts/dev");
const lock = join(root, ".artifacts/dev.lock");
const stateFile = join(directory, "state.json");
const keyring = join(directory, "keyring.json");
const binary = join(directory, "astilba");
const image =
  "postgres:18.4-alpine3.23@sha256:996d0920e4ff9df1fc19dacb904492f3c1ec0ec1cc338f0ad7123be7731c5f5e";
const label = "com.astilba.dev-owner";
const email = "owner@example.test";
const password = "correct horse battery staple";
let state;
let vm;
let agent;
let locked = false;
let cleanupError;
const processes = supervisor({ root, lock });
const { run, serve, waitFor, httpReady, stop } = processes;
for (const signal of ["SIGINT", "SIGTERM"]) process.on(signal, () => stop());

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
const buildServer = () => buildChanged(run, platform, binary, ["./cmd/server"]);

let management;
let env;
function startManagement() {
  management = serve(binary, ["serve"], env);
}
async function restartManagement() {
  await processes.replace(management);
  await run(binary, ["migrate"], { env, inherit: true });
  startManagement();
  await waitFor(
    () => httpReady(`http://127.0.0.1:${port - 1}/healthz`, 204),
    "API",
  );
}

// Rebuilds after Go changes and restarts only what a changed binary belongs to. A failed
// build keeps the running processes and waits for the next change.
function watchSources(arch) {
  let timer;
  let running = false;
  let again = false;
  const rebuild = async () => {
    if (running) {
      again = true;
      return;
    }
    running = true;
    try {
      do {
        again = false;
        console.log("\nGo sources changed; rebuilding…");
        const server = await buildServer();
        const built = stack && (await agent.build(arch));
        if (built) {
          await agent.install(vm, true);
          console.log(`Agent ${state.agentVersion} installed in the VM.`);
        }
        // The release catalog is read at startup, so a new agent restarts management too.
        if (server || built) {
          await restartManagement();
          console.log("Management restarted.");
        }
        if (!server && !built) console.log("No binary changed.");
      } while (again && !processes.stopping);
    } catch (error) {
      if (!processes.stopping)
        console.error(
          `Rebuild failed; still running the previous build.\n${error.message}`,
        );
    } finally {
      running = false;
    }
  };
  const watcher = watch(platform, { recursive: true }, (_event, file) => {
    if (!file || file.endsWith("_test.go")) return;
    if (!/(\.(go|sql|sh|tmpl|service)|go\.(mod|sum))$/.test(file)) return;
    clearTimeout(timer);
    timer = setTimeout(() => void rebuild(), 500);
  });
  processes.signal.addEventListener("abort", () => {
    clearTimeout(timer);
    watcher.close();
  });
}

let proxy;
try {
  mkdirSync(join(root, ".artifacts"), { recursive: true });
  try {
    mkdirSync(lock, { mode: 0o700 });
  } catch (error) {
    if (error.code === "EEXIST")
      throw new Error(
        "A local launcher holds .artifacts/dev.lock. See docs/local-development.md for shutdown recovery.",
      );
    throw error;
  }
  locked = true;
  writeFileSync(
    join(lock, "launcher"),
    `${process.pid}\nUI ${port}\nAPI ${port - 1}\n${stack ? `VM ${values.vm}\nRoutes ${ingressPort}\n` : ""}`,
    { mode: 0o600 },
  );
  writeFileSync(join(lock, "processes"), "", { mode: 0o600 });
  await freePort(port);
  await freePort(port - 1);
  if (stack) await freePort(ingressPort);
  mkdirSync(directory, { recursive: true, mode: 0o700 });
  chmodSync(directory, 0o700);
  if (existsSync(stateFile)) {
    let saved;
    try {
      saved = JSON.parse(readFileSync(stateFile, "utf8"));
    } catch {
      throw new Error(
        `Cannot read ${stateFile.slice(root.length + 1)}; restore the local state before continuing`,
      );
    }
    if (saved.version !== 1 || saved.root !== root)
      throw new Error(
        "Local state belongs to another checkout; use its original checkout",
      );
    if (!stack && saved.host !== host)
      throw new Error(
        "Demo state belongs to another Docker daemon; use its original PLAT_APP_DOCKER_HOST",
      );
    if (stack && saved.profile !== values.vm)
      throw new Error(
        `This checkout's stack runs in Colima profile ${saved.profile}; pass --vm ${saved.profile}, or --wipe it first`,
      );
    state = saved;
  }
  if ((values.reset || values.wipe) && state) {
    if (stack) {
      vm = devHost({ profile: state.profile, owner: state.owner, root, run });
      const status = await vm.status();
      if (status !== "absent") {
        // Only a VM this stack claimed is deleted, after reading its marker.
        if (!state.vm)
          throw new Error(
            `Colima profile ${state.profile} was not created by this stack; delete it yourself if it is disposable`,
          );
        await vm.start();
        if (!(await vm.owned()))
          throw new Error(
            `Colima profile ${state.profile} belongs to another stack; refusing to delete it`,
          );
        console.log(`Deleting Colima profile ${state.profile}…`);
        await vm.remove();
      }
    } else {
      const id = await container();
      if (id) await docker(["rm", "--force", "--volumes", id]);
    }
    rmSync(directory, { recursive: true });
    mkdirSync(directory, { mode: 0o700 });
    state = undefined;
  }
  if (values.wipe) console.log("Local data deleted.");
  else {
    if (!state) {
      state = {
        version: 1,
        root,
        ...(stack ? { profile: values.vm } : { host }),
        owner: randomUUID(),
        password: randomBytes(32).toString("hex"),
        initialized: false,
      };
      saveState();
    }
    let arch;
    if (stack) {
      vm = devHost({ profile: state.profile, owner: state.owner, root, run });
      const status = await vm.status();
      if (status === "absent") {
        if (state.initialized)
          throw new Error(
            "The local stack's VM is missing; start over with --reset",
          );
        console.log(
          `Creating Colima profile ${state.profile}; the first start takes a few minutes…`,
        );
        await vm.create();
        state.vm = true;
        saveState();
      } else {
        if (!state.vm)
          throw new Error(
            `Colima profile ${state.profile} already exists and was not created by this stack; choose another --vm profile`,
          );
        console.log(`Starting Colima profile ${state.profile}…`);
        await vm.start();
      }
      if (!(await vm.owned()))
        throw new Error(
          `Colima profile ${state.profile} belongs to another stack; refusing to change it`,
        );
      await vm.prepare();
      host = await vm.dockerHost();
      arch = await vm.arch();
    }
    let id = await container();
    if (state.initialized && (!id || !existsSync(keyring)))
      throw new Error(
        "Local data or keyring is missing; restore it or explicitly start over with --reset",
      );
    if (id && !existsSync(keyring))
      throw new Error(
        "Database container exists without its keyring; restore it or use --reset",
      );
    console.log("Building management…");
    await buildServer();
    // Local runs must not inherit production database, mail, billing or release settings.
    env = Object.fromEntries(
      Object.entries(process.env).filter(
        ([key]) => !key.startsWith("ASTILBA_"),
      ),
    );
    Object.assign(env, {
      ASTILBA_KEYRING: keyring,
      ASTILBA_DEVELOPMENT: "true",
      ASTILBA_HOSTED: "false",
      ASTILBA_SMTP_MODE: "console",
      ASTILBA_SMTP_FROM: "Astilba <accounts@localhost>",
      ASTILBA_LISTEN: `127.0.0.1:${port - 1}`,
      ASTILBA_ORIGIN: `http://127.0.0.1:${port}`,
    });
    if (stack) {
      console.log("Building the agent…");
      agent = devAgent({ directory, platform, run, state, saveState });
      await agent.build(arch);
      Object.assign(env, {
        ASTILBA_AGENT_RELEASES: agent.catalog,
        ASTILBA_AGENT_RELEASE_KEY: agent.releasePublicKey,
        ASTILBA_TROUBLESHOOTING: "single-instance",
      });
    }
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
        // In the VM, a fixed loopback port that only the stack's tunnel reaches.
        stack ? `127.0.0.1:${vmPorts.postgres}:5432` : "127.0.0.1::5432",
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
    let address = await docker(["port", id, "5432/tcp"]);
    if (!/^127\.0\.0\.1:\d+$/.test(address))
      throw new Error("Local PostgreSQL must publish one loopback port");
    let proxyPort;
    if (stack) {
      if (address !== `127.0.0.1:${vmPorts.postgres}`)
        throw new Error("The stack's PostgreSQL is not on its VM port");
      // One SSH connection carries the database and routes to Mac loopback, and the agent's
      // management link into the VM's loopback. Nothing listens on another interface.
      const ssh = await vm.sshConfig();
      const config = join(directory, "ssh.config");
      writeFileSync(config, ssh.config, { mode: 0o600 });
      const databasePort = await anyPort();
      proxyPort = await anyPort();
      serve(
        "ssh",
        [
          "-F",
          config,
          "-N",
          "-o",
          "ControlMaster=no",
          "-o",
          "ControlPath=none",
          "-o",
          "ExitOnForwardFailure=yes",
          "-o",
          "ServerAliveInterval=10",
          "-o",
          "ServerAliveCountMax=3",
          "-o",
          "LogLevel=ERROR",
          "-L",
          `127.0.0.1:${databasePort}:127.0.0.1:${vmPorts.postgres}`,
          "-L",
          `127.0.0.1:${ingressPort}:127.0.0.1:${vmPorts.ingress}`,
          "-R",
          `127.0.0.1:${vmPorts.management}:127.0.0.1:${proxyPort}`,
          ssh.alias,
        ],
        process.env,
        { last: true },
      );
      await waitFor(() => tcpReady(databasePort), "VM tunnel");
      address = `127.0.0.1:${databasePort}`;
    }
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
          email,
          "--name",
          "Owner",
          "--workspace",
          stack ? "Local stack" : "UI Sandbox",
        ],
        { env, input: password },
      );
    state.initialized = true;
    saveState();
    startManagement();
    await waitFor(
      () => httpReady(`http://127.0.0.1:${port - 1}/healthz`, 204),
      "API",
    );
    if (stack) {
      await agent.ensureCertificate();
      proxy = agentProxy({
        key: readFileSync(agent.certificateKey),
        cert: readFileSync(agent.certificate),
        apiPort: port - 1,
      });
      await new Promise((resolve, reject) => {
        proxy.once("error", reject);
        proxy.listen(proxyPort, "127.0.0.1", resolve);
      });
    }
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
    if (stack) {
      console.log("Seeding the workspace and enrolling the agent…");
      const seeded = await seed({
        api: `http://127.0.0.1:${port - 1}`,
        origin: `http://127.0.0.1:${port}`,
        email,
        password,
        signal: processes.signal,
      });
      await agent.install(vm, false);
      if (!state.enrolled) {
        const enrollment = await seeded.enrollment(state.server);
        state.server = enrollment.server.id;
        saveState();
        // The code travels on stdin, never in arguments or a file.
        await vm.sudo(
          [
            "astilba-agent",
            "enroll",
            "--management",
            agentOrigin,
            "--server",
            state.server,
            "--release-key",
            "/etc/astilba/release-public.pem",
          ],
          { input: enrollment.token },
        );
        // Terminal sessions need the host owner's consent, given with the agent stopped.
        await vm.sudo(["astilba-agent", "troubleshooting-enable"]);
        state.enrolled = true;
        saveState();
      }
      await vm.sudo(["systemctl", "enable", "astilba-agent"]);
      await vm.sudo(["systemctl", "restart", "astilba-agent"]);
      await seeded.awaitServer(state.server);
    }
    if (values.watch) watchSources(arch);
    console.log(
      `\nReady: http://127.0.0.1:${port}/login\nSign in: ${email} / ${password}\nLocal email is printed here instead of being sent.${
        stack
          ? `\nThe agent in Colima profile ${state.profile} is online. Deployed routes answer at http://${routeDomain}:${ingressPort}/`
          : ""
      }\n${values.watch ? "Go changes rebuild and restart what they affect; dashboard changes reload in place.\n" : ""}Ctrl-C stops everything and keeps its data. --reset starts over; --wipe deletes it.\n`,
    );
    await processes.stopRequested;
  }
} catch (error) {
  stop(error);
} finally {
  if (locked) {
    proxy?.close();
    proxy?.closeAllConnections?.();
    await processes.shutdown();
    try {
      if (state && host && !values.wipe) {
        const id = await container(true);
        if (id) await docker(["stop", "--time", "5", id], true);
      }
      // The VM keeps its data and workloads, which start again with it.
      if (
        stack &&
        state?.vm &&
        !values.wipe &&
        (await vm?.status({ cleanup: true })) === "Running"
      ) {
        console.log(`Stopping Colima profile ${state.profile}…`);
        await vm.stop();
      }
    } catch (error) {
      cleanupError = error;
    }
    rmSync(lock, { recursive: true });
  }
}
const failure = processes.failure ?? cleanupError;
if (failure) {
  console.error(failure.message);
  process.exitCode = 1;
}
