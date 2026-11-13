import {
  chmodSync,
  existsSync,
  mkdirSync,
  readFileSync,
  rmSync,
  statSync,
  writeFileSync,
} from "node:fs";
import { join } from "node:path";
import { buildChanged, sha256 } from "./platform-dev-process.mjs";

// Development releases have no floor and claim no real commit.
const developmentFloor = "0.0.0";
const developmentCommit = "0".repeat(40);

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
      // A release names a semantic version, a minimum version and its source commit. Builds
      // record no Git checkout, so an edited checkout still signs, and the version depends on
      // the source alone.
      const identity = (version) =>
        `-s -w -X main.version=${version} -X main.minimumVersion=${developmentFloor} -X main.commit=${developmentCommit}`;
      await go(
        development,
        ["-buildvcs=false", "-ldflags", identity("0.0.1-development"), "./cmd/agent"],
        env,
      );
      const version = `0.0.1-dev-${sha256(development).slice(0, 12)}`;
      if (state.agentVersion === version && existsSync(binary)) return false;
      await go(
        binary,
        ["-buildvcs=false", "-ldflags", identity(version), "./cmd/agent"],
        env,
      );
      await go(tool, ["./cmd/agent"]);
      if (!existsSync(releaseKey))
        await run(tool, ["release-keygen", "--key", releaseKey]);
      // Management reads the catalog at startup, and it holds only the current build.
      rmSync(catalog, { recursive: true, force: true });
      // The release workflow writes this manifest from the agent's own describe output; a
      // cross-compiled development agent can't run here, so it is written from the build.
      const manifest = join(agentDirectory, "manifest.json");
      writeFileSync(
        manifest,
        JSON.stringify({
          version,
          minimumVersion: developmentFloor,
          commit: developmentCommit,
          protocol: 1,
          arch,
          size: statSync(binary).size,
          sha256: sha256(binary),
        }),
        { mode: 0o600 },
      );
      await run(tool, [
        "release-sign",
        "--key",
        releaseKey,
        "--manifest",
        manifest,
        "--development",
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
