import { execFileSync } from "node:child_process";
import { mkdirSync } from "node:fs";
import { resolve } from "node:path";
// Builds the agent for both architectures, for local use. Releases come only from
// .github/workflows/agent-release.yml; see the release procedure in docs/agent.md. The minimum
// version comes from services/platform/cmd/agent/minimum-version.
const root = resolve(import.meta.dirname, "..");
const version = process.env.AGENT_VERSION ?? "0.0.1-development";
// agentrelease.ValidVersion: a semantic version without build metadata.
const identifier = "(0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)";
const semver = new RegExp(`^(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)(-${identifier}(\\.${identifier})*)?$`);
if (version.length > 80 || !semver.test(version)) throw new Error("AGENT_VERSION must be a semantic version, such as 0.3.0");
// A build outside a Git checkout, such as from the corresponding-source archive, names no commit.
let commit = process.env.AGENT_COMMIT;
if (!commit) {
  try {
    commit = execFileSync("git", ["rev-parse", "HEAD"], { cwd: root, encoding: "utf8", stdio: ["ignore", "pipe", "ignore"] }).trim();
  } catch {
    commit = "0".repeat(40);
  }
}
if (!/^[0-9a-f]{40}$/.test(commit)) throw new Error("AGENT_COMMIT must be a 40-character commit");
const destination = resolve(root, ".artifacts/agent", version);
mkdirSync(destination, { recursive: true });
for (const arch of ["amd64", "arm64"]) {
  execFileSync("go", ["build", "-trimpath", "-ldflags", `-s -w -X main.version=${version} -X main.commit=${commit}`, "-o", resolve(destination, `astilba-agent-linux-${arch}`), "./cmd/agent"], {
    cwd: resolve(root, "services/platform"), stdio: "inherit",
    env: { ...process.env, CGO_ENABLED: "0", GOOS: "linux", GOARCH: arch },
  });
}
console.log(`Agent artifacts: ${destination}`);
