// The binary tripwire's own tooth. `scripts/assert-no-nul-bytes.mjs` is a CI gate, and a gate with
// nothing pointed at it is a claim: it can rot into passing everything — the exact failure its own
// non-vacuity guards exist to prevent — and the clean tree it scans every run would never say so,
// because a scanner that has stopped matching and a repo that is genuinely clean produce the same
// green. So the gate is exercised against a file that IS dirty.
//
// The fixture is planted in a TEMP DIR at runtime and never committed. That is not fastidiousness:
// committing a NUL-bearing fixture would inflict the very damage the gate prevents (git classifies
// the file binary — no diff, no blame, no reviewable history) and would red the gate against this
// repo forever after. A gate's fixture must not be the thing the gate forbids. The NUL is written as
// a byte by `Buffer`, so this SOURCE file contains none.
//
// Each case bites one branch:
//   1. it catches a NUL past git's ~8000-byte sniff window — the case that motivates scanning bytes
//      instead of reading git's text-vs-binary verdict, and the one a git-verdict gate would MISS.
//      (Blind: narrow `nulOffset` to a prefix of the buffer ⇒ reds.)
//   2. it accepts a clean file, and says its matcher self-test passed. A gate that flags everything
//      is broken, not strict — and the self-test is the script's own claim to be worth trusting.
//      (Blind: make `nulOffset` return a constant ⇒ reds here and in the self-test.)
//   3. it refuses to pass vacuously: a list that yields ZERO readable files FAILS rather than
//      reporting success over nothing. (Blind: drop the `scanned === 0` guard ⇒ reds.)
//
// Lane: unit. Deterministic — a child process over planted bytes, no network, no timing.

import { execFileSync } from "node:child_process";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { afterAll, beforeAll, describe, expect, it } from "vitest";

const SCRIPT = fileURLToPath(
  new URL("../../../../scripts/assert-no-nul-bytes.mjs", import.meta.url)
);

/** The offending byte, written as a BYTE — never as a literal in this source. */
const NUL = 0;
/** Past git's ~8000-byte sniff window on purpose: the offset git's own verdict misses. */
const PLANT_OFFSET = 12_000;

interface Run {
  readonly status: number;
  readonly output: string;
}

/** Run the gate over explicit paths and capture its REAL exit code — never inferred from silence. */
function runGate(...paths: string[]): Run {
  try {
    const stdout = execFileSync(process.execPath, [SCRIPT, ...paths], {
      encoding: "utf-8",
      stdio: ["ignore", "pipe", "pipe"],
    });
    return { output: stdout, status: 0 };
  } catch (error: unknown) {
    // execFileSync throws on a non-zero exit; the failure text is on stderr. Narrowed structurally
    // rather than asserted: a cast here would be this file claiming a shape it never checked, in a
    // test whose entire subject is a gate that overclaimed.
    const shape: Record<string, unknown> =
      typeof error === "object" && error !== null ? { ...error } : {};
    const { status } = shape;
    return {
      output: typeof shape.stderr === "string" ? shape.stderr : "",
      // A spawn-level failure carries no numeric status; -1 keeps it from reading as the success
      // this gate is testing for.
      status: typeof status === "number" ? status : -1,
    };
  }
}

let dir: string;

describe("assert-no-nul-bytes — the binary tripwire's own tooth", () => {
  beforeAll(() => {
    dir = mkdtempSync(path.join(tmpdir(), "nul-tripwire-"));
  });

  afterAll(() => {
    rmSync(dir, { force: true, recursive: true });
  });

  it("CATCHES a NUL planted past git's 8000-byte sniff window, naming its offset", () => {
    const planted = path.join(dir, "planted.txt");
    // Bytes, not text: `Buffer.alloc` fills with a printable byte, and exactly one NUL sits past the
    // window. A file git would happily diff as text — which is the entire point.
    writeFileSync(
      planted,
      Buffer.concat([
        Buffer.alloc(PLANT_OFFSET, 0x63),
        Buffer.from([NUL]),
        Buffer.alloc(500, 0x64),
      ])
    );
    const run = runGate(planted);
    expect(run.status).toBe(1);
    expect(run.output).toContain(`NUL byte at offset ${PLANT_OFFSET}`);
  });

  it("ACCEPTS a clean file, and reports that its own matcher self-test passed", () => {
    const clean = path.join(dir, "clean.txt");
    // A backslash-zero ESCAPE is text, not a NUL byte. The distinction this gate turns on.
    writeFileSync(
      clean,
      'const answer = 42;\n// a "\\0" escape is TEXT\n',
      "utf-8"
    );
    const run = runGate(clean);
    expect(run.status).toBe(0);
    expect(run.output).toContain("matcher self-test passed");
  });

  it("REFUSES to pass vacuously when the list yields zero readable files", () => {
    // A directory is not a file this gate has anything to say about, so it is skipped — leaving
    // nothing scanned. Reporting success over nothing scanned is how a gate manufactures a green.
    const run = runGate(dir);
    expect(run.status).toBe(1);
    expect(run.output).toContain("passing vacuously");
  });
});
