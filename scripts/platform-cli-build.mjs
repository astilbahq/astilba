import { execFileSync } from "node:child_process";
import { mkdirSync } from "node:fs";
import { resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const version = process.env.CLI_VERSION ?? "development";
if (!/^[a-zA-Z0-9][a-zA-Z0-9._-]{0,79}$/.test(version))
  throw new Error("Invalid CLI version");
const destination = resolve(root, ".artifacts/cli", version);
mkdirSync(destination, { recursive: true });
for (const platform of ["linux", "darwin", "windows"]) {
  for (const arch of ["amd64", "arm64"]) {
    const extension = platform === "windows" ? ".exe" : "";
    execFileSync(
      "go",
      [
        "build",
        "-trimpath",
        "-buildvcs=false",
        "-ldflags",
        `-s -w -X main.version=${version}`,
        "-o",
        resolve(destination, `astilba-cli-${platform}-${arch}${extension}`),
        "./cmd/cli",
      ],
      {
        cwd: resolve(root, "services/platform"),
        stdio: "inherit",
        env: { ...process.env, CGO_ENABLED: "0", GOOS: platform, GOARCH: arch },
      },
    );
  }
}
execFileSync(
  process.execPath,
  [resolve(root, "scripts/platform-notices.mjs"), destination],
  { cwd: root, stdio: "inherit", env: process.env },
);
console.log(`CLI artifacts: ${destination}`);
