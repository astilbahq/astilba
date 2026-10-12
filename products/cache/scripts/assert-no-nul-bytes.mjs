// Build assertion: no tracked file carries a NUL byte.
//
// WHY IT IS A GATE AND NOT A COMMENT. A source file carrying a raw NUL is classified binary by git,
// and a binary file has no diff, no blame, and no reviewable history. In a repo whose whole thesis is
// a legible public history, a file that cannot be reviewed is a file that cannot be trusted — and the
// way it arrives is never deliberate: an escape (`\0`) that was meant to stay text gets written as
// the byte itself, and nothing downstream complains. Everything still compiles. The loss is silent,
// which is exactly why it needs a gate rather than a habit.
//
// WHY NOT ASK GIT. The obvious tripwire — fail on any tracked file git classifies as binary — does
// NOT close the class, and the difference is not theoretical. Git sniffs only the FIRST ~8000 BYTES
// when deciding text-vs-binary. Measured, on a scratch repo:
//
//     NUL at offset  3,641 in a  4,882-byte file  →  git reports it BINARY   (caught)
//     NUL at offset 12,000 in a 13,001-byte file  →  git diffs it as TEXT    (MISSED)
//
// So git's verdict is a proxy that agrees with the thing we care about only on small files, and this
// repo's source files routinely exceed 8000 bytes — the very files most likely to hide one. Reading
// git's answer would build a gate that reports success precisely where it is blind. This script
// therefore scans the BYTES: every tracked file, every offset, whatever the size.
//
// NON-VACUITY. A scanner that silently stops matching passes everything, which is worse than no
// scanner — it manufactures a green. Three ways this fails loudly instead:
//   1. the matcher is SELF-TESTED, both directions, against synthetic buffers — including a NUL past
//      git's 8000-byte window, the exact case that motivates the deviation. A matcher that cannot see
//      the NUL it plants, or that flags clean bytes, is not a matcher;
//   2. an EMPTY file list fails — no list, no evidence, no pass;
//   3. reading ZERO files fails, even if the list was non-empty.
//
// Those are the script's claims about itself, and a self-test is only ever that. So the gate is also
// driven from OUTSIDE, over bytes it did not plant: `packages/cache/test/unit/assert-no-nul-bytes.
// test.ts` runs this script against a temp-dir fixture — a NUL past the sniff window is caught with
// its offset, a clean file passes, and a list yielding zero readable files fails rather than
// reporting success over nothing.
//
// USAGE. `node scripts/assert-no-nul-bytes.mjs` scans every tracked file (the CI wiring). Explicit
// paths may be passed instead, which is how that tooth points this gate at a NUL-bearing fixture
// without one ever being committed here — a committed fixture would inflict the damage the gate
// exists to prevent, and would red this gate against its own repo forever after.

import { execFileSync } from "node:child_process";
import { readFileSync, statSync } from "node:fs";

/** The offending byte. Named once: there is exactly one thing this gate is about. */
const NUL = 0;

/** The first NUL's offset in `buffer`, or -1. `indexOf` scans the WHOLE buffer — no sniff window,
 * no size cutoff, which is the entire point of not asking git. */
function nulOffset(buffer) {
  return buffer.indexOf(NUL);
}

/** Prove the matcher can still do its one job, in both directions, before trusting a clean result.
 * The planted NUL sits past git's ~8000-byte sniff window on purpose: that is the case git's own
 * verdict misses, so it is the case this gate must be shown to catch. */
function selfTestFailures() {
  const failures = [];
  const planted = Buffer.concat([
    Buffer.alloc(12_000, 0x63),
    Buffer.from([NUL]),
    Buffer.alloc(1000, 0x64),
  ]);
  if (nulOffset(planted) !== 12_000) {
    failures.push(
      `self-test: the scanner missed a NUL planted at offset 12000 (reported ${nulOffset(planted)}) — it is asserting nothing, and every clean result below is worthless`
    );
  }
  const clean = Buffer.from("const answer = 42;\n// a \\0 escape is TEXT, not a NUL byte\n", "utf8");
  if (nulOffset(clean) !== -1) {
    failures.push(
      `self-test: the scanner flagged clean text at offset ${nulOffset(clean)} — it would fail every file, which is a broken gate, not a strict one`
    );
  }
  return failures;
}

/** Every tracked file, NUL-delimited so a path with spaces or newlines survives the trip. */
function trackedFiles() {
  return execFileSync("git", ["ls-files", "-z"], { encoding: "buffer" })
    .toString("utf8")
    .split("\0")
    .filter((path) => path.length > 0);
}

const explicit = process.argv.slice(2);
const files = explicit.length > 0 ? explicit : trackedFiles();
const failures = selfTestFailures();

if (files.length === 0) {
  failures.push(
    "the file list is EMPTY — nothing was scanned, so this gate is passing vacuously"
  );
}

let scanned = 0;
for (const file of files) {
  // A tracked path can be a directory (a submodule) or absent from the working tree; neither is a
  // file this gate has anything to say about. Everything else is READ, whatever its extension: the
  // damage is the byte, not the file type, and an allowlist is how a class comes back.
  let isFile = false;
  try {
    isFile = statSync(file).isFile();
  } catch {
    isFile = false;
  }
  if (!isFile) {
    continue;
  }
  const buffer = readFileSync(file);
  scanned += 1;
  const offset = nulOffset(buffer);
  if (offset !== -1) {
    failures.push(
      `${file}: NUL byte at offset ${offset} (of ${buffer.length}) — git classifies this file binary, so it has no diff, no blame, and no reviewable history. If a literal escape was meant, write \\0 as two characters.`
    );
  }
}

if (scanned === 0 && failures.length === 0) {
  failures.push(
    `no files were actually READ (${files.length} listed) — the gate is passing vacuously`
  );
}

if (failures.length > 0) {
  for (const failure of failures) {
    console.error(`::error::${failure}`);
  }
  process.exit(1);
}

console.log(
  `assert-no-nul-bytes: ${scanned} tracked files carry no NUL byte at any offset (scanned whole-file, not git's first-8000-bytes verdict); matcher self-test passed.`
);
