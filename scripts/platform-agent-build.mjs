import { execFileSync } from "node:child_process";
import { mkdirSync } from "node:fs";
import { resolve } from "node:path";
const root = resolve(import.meta.dirname, "..");
const version = process.env.AGENT_VERSION ?? "development";
if (!/^[a-zA-Z0-9][a-zA-Z0-9._-]{0,79}$/.test(version)) throw new Error("Invalid agent version");
const destination = resolve(root, ".artifacts/agent", version);
mkdirSync(destination, { recursive: true });
for (const arch of ["amd64", "arm64"]) {
  execFileSync("go", ["build", "-trimpath", "-ldflags", `-s -w -X main.version=${version}`, "-o", resolve(destination, `astilba-agent-linux-${arch}`), "./cmd/agent"], {
    cwd: resolve(root, "services/platform"), stdio: "inherit",
    env: { ...process.env, CGO_ENABLED: "0", GOOS: "linux", GOARCH: arch },
  });
}
console.log(`Agent artifacts: ${destination}`);
