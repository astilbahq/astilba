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
  if (!profilePattern.test(profile) || reserved.has(profile))
    throw new Error(
      "Name a disposable Colima profile for the local stack, such as astilba-dev; Colima's default profile is refused",
    );
}

export function devHost({ profile, owner, root, run }) {
  checkProfile(profile);
  if (!uuid.test(owner)) throw new Error("Invalid local stack owner");
  // Values cross the VM's shell as arguments, so only fixed tokens and validated
  // identifiers go there; anything else travels on stdin.
  const vm = (args, options = {}) =>
    run("colima", ["ssh", "--profile", profile, "--", ...args], options);
  const sudo = (args, options) => vm(["sudo", ...args], options);
  const script = (path, args) =>
    sudo(["env", "ASTILBA_DISPOSABLE_HOST=1", "bash", "-s", "--", ...args], {
      input: readFileSync(join(root, path)),
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
    async create() {
      await run("colima", ["start", profile, ...vmFlags], {
        inherit: true,
        timeout: slow,
      });
      await script("scripts/platform-dev-host.sh", ["claim", owner]);
    },
    async start() {
      // Repeating the flags keeps a profile someone restarted by hand on the stack's settings.
      if ((await status()) !== "Running")
        await run("colima", ["start", profile, ...vmFlags], {
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
