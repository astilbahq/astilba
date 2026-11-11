import { readFileSync } from "node:fs";
import { join } from "node:path";

// The Colima VM that runs the local stack's agent and database. Every command names its
// profile explicitly, and nothing here selects or changes the machine's Docker context.
// `run(command, args, options)` resolves with stdout and rejects on a non-zero exit.
export const profilePattern = /^[a-z0-9][a-z0-9-]{0,30}$/;
// Colima resolves both names to its default profile, which belongs to the developer.
const reserved = new Set(["default", "colima"]);
const vmFlags = [
  "--activate=false",
  // No Mac folders inside the VM, and no published port forwarded onto every Mac
  // interface: the stack opens its own loopback-only tunnel instead.
  "--mount",
  "none",
  "--port-forwarder",
  "none",
  "--cpus",
  "2",
  "--memory",
  "4",
  "--disk",
  "30",
];
// The VM ports the tunnel reaches. The agent's management origin is fixed here, so a
// different `--port` on the Mac doesn't change what the enrolled agent recorded.
export const vmPorts = { postgres: 5432, ingress: 80, management: 9443 };
export const agentOrigin = `https://localhost:${vmPorts.management}`;
export const backupRoot = "/var/lib/astilba-dev-backups";
// Creating a VM downloads its image the first time.
const slow = 900000;
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;

export function checkProfile(profile) {
  // Colima also reads `colima-NAME` as profile NAME, which would bypass the checks below.
  if (
    !profilePattern.test(profile) ||
    reserved.has(profile) ||
    profile.startsWith("colima-")
  )
    throw new Error(
      "Name a disposable Colima profile for the local stack, such as astilba-dev; Colima's default profile is refused",
    );
}

export function devHost({ profile, owner, root, run }) {
  if (!uuid.test(owner)) throw new Error("Invalid local stack owner");
  // Values cross the VM's shell as arguments, so only fixed tokens and validated
  // identifiers go there; anything else travels on stdin.
  const vm = (args, options = {}) =>
    run("colima", ["ssh", "--profile", profile, "--", ...args], options);
  const sudo = (args, options) => vm(["sudo", ...args], options);
  // Scripts may download and install packages, within their own retry limits.
  const script = (path, args) =>
    sudo(["env", "ASTILBA_DISPOSABLE_HOST=1", "bash", "-s", "--", ...args], {
      input: readFileSync(join(root, path)),
      timeout: slow,
    });

  async function status(options) {
    const output = await run("colima", ["list", "--json"], options);
    for (const line of output.split("\n")) {
      if (!line.trim()) continue;
      const entry = JSON.parse(line);
      if (entry.name === profile) return entry.status;
    }
    return "absent";
  }

  return {
    profile,
    status,
    // Creates the profile with the stack's settings; claim() then marks it as this stack's.
    async create() {
      await run("colima", ["start", profile, ...vmFlags], {
        inherit: true,
        timeout: slow,
      });
    },
    async claim() {
      await script("scripts/platform-dev-host.sh", ["claim", owner]);
    },
    // Starts an existing profile as it is configured; its ownership is checked next.
    async start() {
      if ((await status()) !== "Running")
        await run("colima", ["start", profile, "--activate=false"], {
          inherit: true,
          timeout: slow,
        });
    },
    async prepare() {
      await script("scripts/platform-dev-host.sh", ["prepare", owner]);
      const restic = await sudo([
        "sh",
        "-c",
        "test -x /opt/astilba-platform-tools/restic && echo present || true",
      ]);
      if (restic !== "present")
        await script("services/platform/host/backup-tools.sh", []);
    },
    async owned() {
      const marker = await sudo([
        "sh",
        "-c",
        "cat /etc/astilba-dev/owner 2>/dev/null || true",
      ]);
      return marker === owner;
    },
    async stop() {
      await run("colima", ["stop", "--profile", profile], {
        cleanup: true,
        timeout: slow,
      });
    },
    async remove() {
      await run("colima", ["delete", profile, "--data", "--force"], {
        inherit: true,
        timeout: slow,
      });
    },
    async dockerHost() {
      const { docker_socket: socket } = JSON.parse(
        await run("colima", ["status", "--profile", profile, "--json"]),
      );
      if (typeof socket !== "string" || !socket.startsWith("unix:///"))
        throw new Error("Colima did not report the VM's Docker socket");
      return socket;
    },
    // Lima's own SSH configuration for the VM, and the host alias it defines.
    async sshConfig() {
      const config = await run("colima", ["ssh-config", "--profile", profile]);
      const alias = /^Host (\S+)$/m.exec(config)?.[1];
      if (!alias)
        throw new Error("Colima did not report the VM's SSH configuration");
      return { config: `${config}\n`, alias };
    },
    async arch() {
      const machine = await vm(["uname", "-m"]);
      if (machine === "aarch64") return "arm64";
      if (machine === "x86_64") return "amd64";
      throw new Error(`Unsupported VM architecture: ${machine}`);
    },
    // One SSH connection, independent of Lima's own control connection, carrying the
    // database and routes to Mac loopback and the agent's management link into the VM's.
    tunnel({ config, alias, databasePort, ingressPort, proxyPort }) {
      return [
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
        alias,
      ];
    },
    // Writes a file the launcher produced into the VM's staging directory.
    async stage(name, data) {
      if (!/^[a-z0-9.-]+$/.test(name))
        throw new Error("Invalid staged file name");
      await sudo(["install", "-d", "-m", "0700", "/var/lib/astilba-dev"]);
      await sudo(["dd", `of=/var/lib/astilba-dev/${name}`, "status=none"], {
        input: data,
      });
    },
    sudo,
    script,
  };
}
