import { describe, expect, it } from "vitest";

type InstalledModeVerifier = (options: {
  expectedMode: number;
  installedMode: number;
  manager: "bun" | "npm" | "pnpm";
  path: string;
  platform: NodeJS.Platform;
}) => boolean;

type RunFailureFormatter = (
  command: string,
  arguments_: readonly string[],
  result: {
    error?: NodeJS.ErrnoException;
    signal: NodeJS.Signals | null;
    status: number | null;
    stderr?: string | Buffer;
    stdout?: string | Buffer;
  }
) => string;

type PackageManagerInvocationResolver = (
  manager: "bun" | "npm" | "pnpm",
  arguments_: readonly string[],
  options?: { execPath?: string; platform?: NodeJS.Platform }
) => { arguments_: readonly string[]; command: string };

type CommandRunner = (
  command: string,
  arguments_: readonly string[],
  cwd: string,
  env?: NodeJS.ProcessEnv
) => string;

const isInstalledModeVerifier = (
  value: unknown
): value is InstalledModeVerifier => typeof value === "function";

const isRunFailureFormatter = (value: unknown): value is RunFailureFormatter =>
  typeof value === "function";

const isRunSummaryFormatter = (value: unknown): value is RunFailureFormatter =>
  typeof value === "function";

const isPackageManagerInvocationResolver = (
  value: unknown
): value is PackageManagerInvocationResolver => typeof value === "function";

const isCommandRunner = (value: unknown): value is CommandRunner =>
  typeof value === "function";

const matrixArtifactModule: unknown =
  // @ts-expect-error -- The verifier is a checked JSDoc module without declarations.
  await import("../scripts/matrix-artifact.mjs");
if (
  typeof matrixArtifactModule !== "object" ||
  matrixArtifactModule === null ||
  !("isAcceptedInstalledMode" in matrixArtifactModule) ||
  !isInstalledModeVerifier(matrixArtifactModule.isAcceptedInstalledMode) ||
  !("formatRunFailure" in matrixArtifactModule) ||
  !isRunFailureFormatter(matrixArtifactModule.formatRunFailure) ||
  !("formatRunSummary" in matrixArtifactModule) ||
  !isRunSummaryFormatter(matrixArtifactModule.formatRunSummary) ||
  !("resolvePackageManagerInvocation" in matrixArtifactModule) ||
  !isPackageManagerInvocationResolver(
    matrixArtifactModule.resolvePackageManagerInvocation
  ) ||
  !("run" in matrixArtifactModule) ||
  !isCommandRunner(matrixArtifactModule.run)
) {
  throw new TypeError("Matrix artifact exports are unavailable.");
}
const {
  formatRunFailure,
  formatRunSummary,
  isAcceptedInstalledMode,
  resolvePackageManagerInvocation,
  run,
} = matrixArtifactModule;

describe("installed archive mode verification", () => {
  it("accepts Bun's manager-owned CLI mode normalization only", () => {
    const executable = {
      expectedMode: 0o755,
      installedMode: 0o777,
      path: "dist/cli/astilba-env.js",
      platform: "darwin",
    } as const;

    expect(isAcceptedInstalledMode({ ...executable, manager: "bun" })).toBe(
      true
    );
    expect(isAcceptedInstalledMode({ ...executable, manager: "npm" })).toBe(
      false
    );
    expect(isAcceptedInstalledMode({ ...executable, manager: "pnpm" })).toBe(
      false
    );
    expect(
      isAcceptedInstalledMode({
        ...executable,
        manager: "bun",
        path: "dist/index.js",
      })
    ).toBe(false);
    expect(
      isAcceptedInstalledMode({
        ...executable,
        expectedMode: 0o644,
        manager: "bun",
      })
    ).toBe(false);
  });

  it("keeps exact installed modes valid for every manager", () => {
    for (const manager of ["bun", "npm", "pnpm"] as const) {
      expect(
        isAcceptedInstalledMode({
          expectedMode: 0o644,
          installedMode: 0o644,
          manager,
          path: "dist/index.js",
          platform: "linux",
        })
      ).toBe(true);
    }
  });
});

describe("npm command invocation", () => {
  it("keeps npm on PATH outside Windows", () => {
    expect(
      resolvePackageManagerInvocation("npm", ["--version"], {
        execPath: "/opt/node/bin/node",
        platform: "linux",
      })
    ).toStrictEqual({ arguments_: ["--version"], command: "npm" });
  });

  it("executes npm's adjacent CLI through the pinned Windows Node runtime", () => {
    const execPath =
      "C:\\hostedtoolcache\\windows\\node\\24.18.1\\x64\\node.exe";
    expect(
      resolvePackageManagerInvocation("npm", ["--version"], {
        execPath,
        platform: "win32",
      })
    ).toStrictEqual({
      arguments_: [
        "C:\\hostedtoolcache\\windows\\node\\24.18.1\\x64\\node_modules\\npm\\bin\\npm-cli.js",
        "--version",
      ],
      command: execPath,
    });
  });

  it("keeps install arguments when invoking npm through Windows Node", () => {
    const execPath =
      "C:\\hostedtoolcache\\windows\\node\\26.5.1\\x64\\node.exe";
    expect(
      resolvePackageManagerInvocation(
        "npm",
        ["install", "--ignore-scripts", "--package-lock=false"],
        { execPath, platform: "win32" }
      )
    ).toStrictEqual({
      arguments_: [
        "C:\\hostedtoolcache\\windows\\node\\26.5.1\\x64\\node_modules\\npm\\bin\\npm-cli.js",
        "install",
        "--ignore-scripts",
        "--package-lock=false",
      ],
      command: execPath,
    });
  });

  it("keeps pnpm and Bun direct on Windows", () => {
    for (const manager of ["bun", "pnpm"] as const) {
      expect(
        resolvePackageManagerInvocation(manager, ["--version"], {
          execPath: "C:\\hostedtoolcache\\windows\\node\\26.5.1\\x64\\node.exe",
          platform: "win32",
        })
      ).toStrictEqual({ arguments_: ["--version"], command: manager });
    }
  });
});

describe("command failure formatting", () => {
  it("reports both command output streams without serializing its environment", () => {
    expect(
      formatRunFailure("npm", ["install"], {
        signal: null,
        status: 1,
        stderr: "install failed",
        stdout: "resolving dependencies",
      })
    ).toBe(
      "npm install failed (status 1).\nstdout:\nresolving dependencies\nstderr:\ninstall failed"
    );
  });

  it("reports both Buffer output streams when a command ends by signal", () => {
    expect(
      formatRunFailure("npm", ["install"], {
        signal: "SIGTERM",
        status: null,
        stderr: Buffer.from("install terminated"),
        stdout: Buffer.from("resolving dependencies"),
      })
    ).toBe(
      "npm install failed (signal SIGTERM).\nstdout:\nresolving dependencies\nstderr:\ninstall terminated"
    );
  });

  it("reports spawn failures with stable error details and unavailable streams", () => {
    const error = Object.assign(new Error("spawn npm ENOENT"), {
      code: "ENOENT",
    });
    expect(
      formatRunFailure("npm", ["install"], {
        error,
        signal: null,
        status: null,
      })
    ).toBe(
      "npm install failed (error ENOENT: spawn npm ENOENT).\nstdout:\n<unavailable>\nstderr:\n<unavailable>"
    );
  });

  it("summarizes secret-bearing Next output without emitting it", () => {
    const stdout = "secret-token";
    const stderr = "private-value";
    const summary = formatRunSummary("Next", ["build"], {
      signal: null,
      status: 1,
      stderr,
      stdout,
    });
    expect(summary).toBe(
      "Next build failed (status 1; stdout 12 bytes; stderr 13 bytes)."
    );
    expect(summary).not.toContain(stdout);
    expect(summary).not.toContain(stderr);
  });

  it("turns a real spawn ENOENT into the primary command diagnostic", () => {
    let thrown: unknown;
    try {
      run("astilba-env-command-that-does-not-exist", [], process.cwd());
    } catch (error) {
      thrown = error;
    }
    if (!(thrown instanceof Error)) {
      throw new TypeError("Expected a command failure.");
    }
    expect(thrown.message).toContain("error ENOENT:");
    expect(thrown.message).toContain("stdout:\n<unavailable>");
    expect(thrown.message).toContain("stderr:\n<unavailable>");
    expect(thrown.message).not.toContain("TypeError");
  });
});
