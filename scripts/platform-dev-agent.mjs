import {
  chmodSync,
  existsSync,
  mkdirSync,
  readFileSync,
  rmSync,
} from "node:fs";
import { join } from "node:path";
import { buildChanged, sha256 } from "./platform-dev-process.mjs";

// The local stack's agent: its build, the release catalog management serves for it,
// the certificate it trusts for management, and its installation in the VM.
export function devAgent({ directory, platform, run, state, saveState }) {
  const agentDirectory = join(directory, "agent");
  const development = join(agentDirectory, "astilba-agent.development");
  const binary = join(agentDirectory, "astilba-agent");
  // A native build of the same command signs releases with the stack's own key.
  const tool = join(agentDirectory, "astilba-agent-tool");
  const releaseKey = join(directory, "release.pem");
  const catalog = join(directory, "releases");
  const certificate = join(directory, "management.pem");
  const certificateKey = join(directory, "management.key");
  const go = (target, args, env) =>
    buildChanged(run, platform, target, args, env);

  return {
    catalog,
    releasePublicKey: `${releaseKey}.pub`,
    certificate,
    certificateKey,
    // Says whether the build changed. The version is named after the build, so the
    // Servers page shows which one an agent runs.
    async build(arch) {
      mkdirSync(agentDirectory, { recursive: true, mode: 0o700 });
      const env = {
        ...process.env,
        CGO_ENABLED: "0",
        GOOS: "linux",
        GOARCH: arch,
      };
      await go(
        development,
        ["-ldflags", "-s -w -X main.version=development", "./cmd/agent"],
        env,
      );
      const version = `dev-${sha256(development).slice(0, 12)}`;
      if (state.agentVersion === version && existsSync(binary)) return false;
      await go(
        binary,
        ["-ldflags", `-s -w -X main.version=${version}`, "./cmd/agent"],
        env,
      );
      await go(tool, ["./cmd/agent"]);
      if (!existsSync(releaseKey))
        await run(tool, ["release-keygen", "--key", releaseKey]);
      // Management reads the catalog at startup, and it holds only the current build.
      rmSync(catalog, { recursive: true, force: true });
      await run(tool, [
        "release-sign",
        "--key",
        releaseKey,
        "--version",
        version,
        "--arch",
        arch,
        "--artifact",
        binary,
        "--out",
        catalog,
      ]);
      state.agentVersion = version;
      saveState();
      return true;
    },
    // Only the VM trusts this certificate, for the agent's HTTPS link to management.
    async ensureCertificate() {
      if (existsSync(certificate) && existsSync(certificateKey)) {
        try {
          await run("openssl", [
            "x509",
            "-checkend",
            "604800",
            "-noout",
            "-in",
            certificate,
          ]);
          return;
        } catch {
          // Renew a certificate that expires within a week.
        }
      }
      await run("openssl", [
        "req",
        "-x509",
        "-newkey",
        "rsa:2048",
        "-nodes",
        "-keyout",
        certificateKey,
        "-out",
        certificate,
        "-days",
        "365",
        "-subj",
        "/CN=localhost",
        "-addext",
        "subjectAltName=DNS:localhost",
        "-addext",
        "basicConstraints=critical,CA:FALSE",
      ]);
      chmodSync(certificateKey, 0o600);
    },
    // Installs the current build with the product's installer and unit; the VM script
    // replaces only an executable that changed.
    async install(vm, restart) {
      await vm.stage("astilba-agent", readFileSync(binary));
      await vm.stage("release-public.pem", readFileSync(`${releaseKey}.pub`));
      await vm.stage(
        "astilba-agent.service",
        readFileSync(join(platform, "host/astilba-agent.service")),
      );
      await vm.stage(
        "install.sh",
        readFileSync(join(platform, "host/install.sh")),
      );
      await vm.stage("management.pem", readFileSync(certificate));
      await vm.script("scripts/platform-dev-host.sh", ["agent", state.owner]);
      if (restart) await vm.sudo(["systemctl", "restart", "astilba-agent"]);
    },
  };
}
