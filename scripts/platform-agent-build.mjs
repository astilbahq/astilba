import { execFileSync } from "node:child_process";
import { mkdirSync, readFileSync } from "node:fs";
import { resolve } from "node:path";
// Builds the agent for both architectures as .github/workflows/agent-release.yml does, for local
// use. Releases come only from that workflow; see the release procedure in docs/agent.md.
const root = resolve(import.meta.dirname, "..");
const version = process.env.AGENT_VERSION ?? "development";
if (!/^[a-zA-Z0-9][a-zA-Z0-9._-]{0,79}$/.test(version)) throw new Error("Invalid agent version");
const minimum = readFileSync(resolve(root, "services/platform/cmd/agent/minimum-version"), "utf8").trim();
const commit = execFileSync("git", ["rev-parse", "HEAD"], { cwd: root, encoding: "utf8" }).trim();
const destination = resolve(root, ".artifacts/agent", version);
mkdirSync(destination, { recursive: true });
for (const arch of ["amd64", "arm64"]) {
  execFileSync("go", ["build", "-trimpath", "-ldflags", `-s -w -X main.version=${version} -X main.minimumVersion=${minimum} -X main.commit=${commit}`, "-o", resolve(destination, `astilba-agent-linux-${arch}`), "./cmd/agent"], {
    cwd: resolve(root, "services/platform"), stdio: "inherit",
    env: { ...process.env, CGO_ENABLED: "0", GOOS: "linux", GOARCH: arch },
  });
}
console.log(`Agent artifacts: ${destination}`);
