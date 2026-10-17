import type { BusFrame, TagChange } from "@astilba/cache";
import { describe, expect, test } from "vitest";

import {
  buildReaderParts,
  probeEnvelope,
} from "../conformance/fixtures/reader-parts";

// a corrupt replication change holes its whole batch; the reader NEVER partial-applies
//. Red target = current main: `readChange` dropped a present-but-non-number axis
// and `readFrame` kept the readable rest, so a `__delta:6` = {changes:[{tag:"t", hardEpoch:"6"}]}
// (hard axis corrupted to a STRING) parsed to {tag:"t"} — recording `t` as known-clean {0,0} and
// advancing the applied epoch, SILENTLY DELETING a hard purge from history (a serve-dead vector
// under non-canonical KV writes). On the fixed reader that corrupt delta:6 is a HOLE, so the reader
// stays suspect and the warm entry classifies `unknown` — never served fresh through the lost purge.

const REG_ID = "reader-corrupt";
const WARMUP_TAG = "reader-corrupt:w"; // deltas 1..5 touch this, NOT `t`, so `t` is established
const PURGED_TAG = "reader-corrupt:t"; // only by delta:6 — clean under drop-and-apply, absent when holed.
const HEAD = 6;

/** A well-formed delta batch at `seq` (fromEpoch = seq-1, toEpoch = seq — the 1:1 convention). */
function deltaFrame(seq: number, changes: readonly TagChange[]): BusFrame {
  return { changes, fromEpoch: seq - 1, toEpoch: seq };
}

/** A `__state` pointer echoing `REG_ID`, pointing at deltas `1..lastBatch`. */
function pointer(lastBatch: number): unknown {
  return {
    epoch: lastBatch,
    flushedAt: lastBatch,
    lastBatch,
    regId: REG_ID,
    snapBatch: 0,
  };
}

async function put(
  parts: ReturnType<typeof buildReaderParts>,
  key: string,
  value: unknown
): Promise<void> {
  await parts.store.set(key, JSON.stringify(value));
}

describe("reader fails closed on a corrupt replication change (§4.2, F12, E20)", () => {
  test("a delta whose hardEpoch is a STRING holes its batch — the warm entry is NOT served fresh", async () => {
    const parts = buildReaderParts(REG_ID);

    // Warm the mirror to applied-epoch 5 via well-formed deltas 1..5 that touch a WARMUP tag only.
    for (let seq = 1; seq <= 5; seq += 1) {
      await put(
        parts,
        parts.keys.deltaKey(seq),
        deltaFrame(seq, [{ softEpoch: seq, tag: WARMUP_TAG }])
      );
    }

    // The corrupt delta:6: `hardEpoch` is the STRING "6" (not a number) — a garbled hard axis. It
    // is typed as a bespoke shape because `TagChange.hardEpoch` is `number`; the store holds JSON,
    // so the reader parses this back through `readChange`/`readFrame` exactly as a real KV write.
    const corruptDelta6 = {
      changes: [{ hardEpoch: "6", tag: PURGED_TAG }],
      fromEpoch: 5,
      toEpoch: 6,
    };
    await put(parts, parts.keys.deltaKey(6), corruptDelta6);
    await put(parts, parts.keys.stateKey, pointer(6));

    // Establish a fresh episode declaring head 6 over the live channel (reset → hello): the reader
    // is suspect and must reach applied-epoch 6 to converge.
    parts.bus.emit({ kind: "reset" });
    parts.bus.emit({ head: HEAD, kind: "hello" });

    await parts.inval.resync();

    // The warm entry born at epoch 3 tagged `t`. A correctly-applied hard purge at epoch 6 would
    // make it DEAD (6 > 3); the corruption tried to delete that purge from history.
    const { validity } = await parts.inval.validate(
      probeEnvelope([PURGED_TAG], 3),
      "eventual",
      "miss"
    );

    // HEADLINE (serve-dead): FIXED — delta:6 holed ⇒ `t` unknown + map suspect ⇒ fail closed ⇒
    // `unknown`; the entry is NEVER served through the silently-dropped purge. On MAIN
    // (drop-and-continue) `t` was recorded known-clean {0,0} ⇒ classify(born 3) ⇒ `fresh` and the
    // entry IS served — so this assertion REDS on `main` ("expected 'fresh' to be 'unknown'"),
    // which is the serve-dead vector this fixes.
    expect(validity).toBe("unknown");

    // Corroboration: the hole leaves the reader suspect (applied stuck at 5 < declared head 6). On
    // MAIN the reader wrongly CONVERGES (applied reached 6 via the partial-applied frame), so this
    // also reds (suspect is false there).
    expect(parts.inval.suspect).toBeTruthy();
  });
});
