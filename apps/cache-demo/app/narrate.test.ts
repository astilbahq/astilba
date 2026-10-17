// The narration's tests — the demo's machinery.
//
// The narration is the demo's whole argument, so what is on trial is mostly what it REFUSES to say.
// It must not claim a bound was met, it must not turn a fault it could not observe into a fault the
// system survived, and it must not dress "I could not look" up as "there is nothing there". Those
// are the failures that would make this page a liability rather than an argument, and none of them
// would show up as a crash.
//
// EVERY BRANCH BELOW IS TOOTHED, AND THAT IS A REPAIR RATHER THAN A HABIT. A previous pass over this
// file could delete, invert, or blind eight separate branches of `narrate` with the whole suite
// green: the scope distinction could be inverted unconditionally, the dependency row deleted, the
// terminal disjunct dropped. The worst of them was covered by a test that could not fail — it
// asserted that every derived judgment's `event` is a member of the catalogue, which the TYPE
// already guarantees, so it held just as well when the branch that produced the judgment was gone
// entirely. A test whose subject the type system has already proven is not coverage; it is a green
// light wired to nothing.

import type { Explanation, TelemetryEvent } from "@astilba/cache";
import { TELEMETRY_EVENTS } from "@astilba/cache";
import { describe, expect, it } from "vitest";

import { emptyBusStats, emptyKvStats, noFaults } from "../chaos/dials";
import { sceneOf } from "../chaos/script";
import { POSTURE } from "./bounds";
import type { NarrationInput } from "./narrate";
import { HEALTHY_NOTE, narrate } from "./narrate";

const PRESENT: Explanation = {
  identity: {
    bornEpoch: 3,
    kind: "val",
    scope: { kind: "readable", literal: "pub" },
    tags: ["headline"],
    ttl: { kind: "not-stored" },
  },
  key: "demo:headline",
  kind: "present",
  reader: {
    appliedEpoch: 4,
    channel: "established",
    suspect: false,
    terminal: false,
  },
  tier: "l1",
  verdict: { hardEpoch: 0, softEpoch: 2, validity: "fresh" },
};

/** `PRESENT`, with part of the reader's state replaced. */
function withReader(over: Partial<Explanation["reader"]>): Explanation {
  return { ...PRESENT, reader: { ...PRESENT.reader, ...over } };
}

function input(over: Partial<NarrationInput> = {}): NarrationInput {
  return {
    armedForMs: null,
    awaitingRescueForMs: null,
    busStats: emptyBusStats(),
    coordinatorHeartbeatMs: POSTURE.heartbeatIntervalMs,
    dials: noFaults(),
    events: [],
    filled: false,
    kvStats: emptyKvStats(),
    readMs: 12,
    rescue: null,
    scene: null,
    witness: { explanation: PRESENT, ok: true },
    ...over,
  };
}

/** Every word the narration puts on screen. */
function allText(over: Partial<NarrationInput> = {}): string {
  const narration = narrate(input(over));
  return [
    narration.caveat,
    narration.lifetimeCaveat,
    ...narration.rows.flatMap((r) => [r.label, r.measured, r.documented]),
    ...narration.judgments.map((judgment) => judgment.because),
  ].join("\n");
}

/**
 * Only the words in which the demo makes a CLAIM: what it measured, and the judgments it derived.
 *
 * The scoping is the point rather than a convenience. The caveats exist precisely to disclaim the
 * guarantees, and the documented column exists to explain what a bound permits — both need the
 * vocabulary of promises in order to disclaim them, and a guard that swept them up would be
 * satisfied only by a page that had stopped explaining itself. What must never contain a verdict on
 * the system's performance is the column where the demo speaks for the system.
 */
function claimedText(over: Partial<NarrationInput> = {}): string {
  const narration = narrate(input(over));
  return [
    ...narration.rows.map((r) => r.measured),
    ...narration.judgments.map((judgment) => judgment.because),
  ].join("\n");
}

/** The row with this label, or a failure that names it. `find` returning `undefined` into an
 * optional-chained assertion is how a row silently stops being tested. */
function row(narration: Narration, label: string) {
  const found = narration.rows.find((r) => r.label === label);
  if (found === undefined) {
    throw new Error(
      `no row labelled "${label}" — rows were: ${narration.rows.map((r) => r.label).join(", ")}`
    );
  }
  return found;
}

type Narration = ReturnType<typeof narrate>;

/** The measured cell of one row, for a witness reporting `explanation`. */
function measuredFor(explanation: Explanation, label: string): string {
  return row(narrate(input({ witness: { explanation, ok: true } })), label)
    .measured;
}

describe("the narration renders", () => {
  it("pairs every measured value with a documented bound, and leaves neither blank", () => {
    const narration = narrate(input());
    expect(narration.rows.length).toBeGreaterThan(5);
    for (const r of narration.rows) {
      expect(r.label, "a row with no label").not.toBe("");
      expect(r.measured, `${r.label}: nothing measured`).not.toBe("");
      expect(r.documented, `${r.label}: no bound to compare against`).not.toBe(
        ""
      );
    }
  });

  it("reports the verdict WITH the epochs behind it, never the verdict alone", () => {
    const text = allText();
    expect(text).toMatch(/fresh/u);
    expect(text).toMatch(/soft 2/u);
    expect(text).toMatch(/hard 0/u);
    expect(text).toMatch(/born 3/u);
  });

  it("carries both caveats on every single run", () => {
    const narration = narrate(input());
    expect(narration.caveat).not.toBe("");
    expect(narration.lifetimeCaveat).not.toBe("");
  });
});

describe("the narration asserts no SLO — the hard line", () => {
  it("never claims a bound was met, passed, or satisfied", () => {
    const text = claimedText({ armedForMs: 200_000 });
    // The vocabulary of a page that grades itself. If any of it ever appears here, this demo has
    // started making promises the library's own documentation is careful not to make.
    for (const forbidden of [
      /\bguarantee/iu,
      /\bSLA\b/iu,
      /\bwithin the bound/iu,
      /\bmet the\b/iu,
      /\bPASS\b/u,
      /\bFAIL\b/u,
      /\bcompliant/iu,
    ]) {
      expect(
        text,
        `the narration used SLO vocabulary: ${forbidden.source}`
      ).not.toMatch(forbidden);
    }
  });

  it("phrases every bound as a permission", () => {
    expect(row(narrate(input()), "reader suspicion").documented).toMatch(
      /may raise/u
    );
  });
});

// ── the scope distinction ──────────────────────────────────────────────
//
// THE ONE THAT MATTERS MOST. A scope that is readable AND exactly `pub` is the only thing that makes
// a response shared-cacheable; absent, malformed, or unreadable is non-public and poisons it. Those
// two answers are one ternary apart in `scopeLine` and worlds apart on the wire — an inverted branch
// reports a per-principal entry as public, which is the leak the rule exists to prevent, and nothing
// in this repo would have reported it.
describe("the stored scope preserves the readable/unreadable distinction (E23)", () => {
  const cases = [
    {
      expected: "pub",
      forbidden: /unreadable/u,
      name: "readable pub",
      scope: { kind: "readable", literal: "pub" },
    },
    {
      expected: "usr:9f2",
      forbidden: /unreadable/u,
      name: "readable usr:",
      scope: { kind: "readable", literal: "usr:9f2" },
    },
    {
      expected: "unreadable (fails closed)",
      forbidden: /\bpub\b/u,
      name: "unreadable",
      scope: { kind: "unreadable" },
    },
  ] as const;

  it.each(cases)(
    "reports $name as itself and never as the other",
    ({ scope, expected, forbidden }) => {
      const measured = measuredFor(
        { ...PRESENT, identity: { ...PRESENT.identity, scope } },
        "stored scope"
      );
      expect(measured).toBe(expected);
      expect(
        measured,
        "the scope arms have been collapsed into one another"
      ).not.toMatch(forbidden);
    }
  );

  it("never prints a readable scope and an unreadable one the same way", () => {
    // The mutation this exists to catch is an UNCONDITIONAL inversion. Any single case above could
    // in principle be satisfied by a constant; only their DISAGREEMENT proves the branch is live.
    const readable = measuredFor(
      {
        ...PRESENT,
        identity: {
          ...PRESENT.identity,
          scope: { kind: "readable", literal: "pub" },
        },
      },
      "stored scope"
    );
    const unreadable = measuredFor(
      {
        ...PRESENT,
        identity: { ...PRESENT.identity, scope: { kind: "unreadable" } },
      },
      "stored scope"
    );
    expect(readable).not.toBe(unreadable);
  });
});

// ── the channel row ────────────────────────────────────────────────────
//
// The row that tells a dead bus from a quiet healthy one. `suspect` cannot: a reader whose channel
// never came up has no gap, so it is not suspect, and a dead bus and a serene one produce the same
// `suspect: false`. This row is the page's only defence against that.
describe("the reader's channel state is narrated as a declared state (E28)", () => {
  const cases = [
    { channel: "never-established", expected: /NEVER-ESTABLISHED/u },
    { channel: "lost", expected: /LOST/u },
    { channel: "established", expected: /^established$/u },
  ] as const;

  it.each(cases)("reports $channel distinctly", ({ channel, expected }) => {
    expect(measuredFor(withReader({ channel }), "reader channel")).toMatch(
      expected
    );
  });

  it("gives the three channel states three different sentences", () => {
    const seen = new Set([
      measuredFor(
        withReader({ channel: "never-established" }),
        "reader channel"
      ),
      measuredFor(withReader({ channel: "lost" }), "reader channel"),
      measuredFor(withReader({ channel: "established" }), "reader channel"),
    ]);
    expect(seen.size, "two channel states render identically").toBe(3);
  });

  it("says a never-established channel is NOT redeemed by `not suspect`", () => {
    // The whole lie this arc ended: a deaf reader that looks healthy. The page must carry both facts
    // at once — not suspect AND never-established — and explain that the first does not redeem the
    // second.
    const narration = narrate(
      input({
        witness: {
          explanation: withReader({
            channel: "never-established",
            suspect: false,
          }),
          ok: true,
        },
      })
    );
    expect(row(narration, "reader suspicion").measured).toMatch(/not suspect/u);
    expect(row(narration, "reader channel").measured).toMatch(
      /NEVER-ESTABLISHED/u
    );
    expect(row(narration, "reader suspicion").documented).toMatch(
      /never came up is NOT suspect/u
    );
  });
});

// ── the three arms of the witness ──────────────────────────────────────
describe("the witness's three arms are three different answers (E27)", () => {
  it("reports a read-failed arm as unknown-existence, never as absence", () => {
    // The substitution that would make the witness a liar: "the store did not answer" rendered as
    // "there is nothing there". Scoped to the MEASURED column, where the demo makes its claims.
    const measured = measuredFor(
      { key: "k", kind: "read-failed", reader: PRESENT.reader },
      "explain() — the witness"
    );
    expect(measured).toMatch(/READ-FAILED/u);
    expect(measured).not.toMatch(/\bABSENT\b/u);
    expect(measured).not.toMatch(/no entry/u);
  });

  it("reports an absent arm as absence, and says a miss is a fill", () => {
    const measured = measuredFor(
      { key: "k", kind: "absent", reader: PRESENT.reader, tier: "miss" },
      "explain() — the witness"
    );
    expect(measured).toMatch(/ABSENT/u);
    expect(measured).toMatch(/a miss is a fill/u);
  });

  it("gives read-failed and absent different sentences", () => {
    expect(
      measuredFor(
        { key: "k", kind: "read-failed", reader: PRESENT.reader },
        "explain() — the witness"
      )
    ).not.toBe(
      measuredFor(
        { key: "k", kind: "absent", reader: PRESENT.reader, tier: "miss" },
        "explain() — the witness"
      )
    );
  });

  it("still reports what it CAN observe on an arm with no entry to describe", () => {
    const narration = narrate(
      input({
        kvStats: { ...emptyKvStats(), readsFailed: 4 },
        witness: {
          explanation: {
            key: "k",
            kind: "read-failed",
            reader: PRESENT.reader,
          },
          ok: true,
        },
      })
    );
    expect(row(narration, "faults intercepted").measured).toMatch(/4 reads/u);
    // The reader's own state survives an unreadable store: the reader is not the store.
    expect(row(narration, "reader channel").measured).not.toBe("");
  });
});

// ── the request-dependencies row ───────────────────────────────────
//
// Taken on every real request and covered by nothing — the row could be deleted outright and no gate
// would have noticed.
describe("the request-dependencies row (E27)", () => {
  it("attributes each dependency with the scope E23's gate reads", () => {
    const measured = measuredFor(
      {
        ...PRESENT,
        requestDependencies: [
          { scope: { kind: "readable", literal: "pub" }, tags: ["headline"] },
        ],
      },
      "request dependencies"
    );
    expect(measured).toMatch(/headline/u);
    expect(measured).toMatch(/readable/u);
  });

  it("reports an empty collector as `none recorded`, not as an absent row", () => {
    // A bound collector that recorded nothing is a REAL answer — the request genuinely consumed no
    // managed dependency. That is a different fact from having no collector at all.
    expect(
      measuredFor(
        { ...PRESENT, requestDependencies: [] },
        "request dependencies"
      )
    ).toMatch(/none recorded/u);
  });

  it("omits the row entirely when no collector is bound — request-scoped means request-scoped", () => {
    // ABSENT, not empty. There is no persisted per-key dependency graph to fall back on, so a
    // narration that printed the row anyway would be implying a lookup that cannot happen.
    expect(
      narrate(input()).rows.some((r) => r.label === "request dependencies"),
      "the dependency row appeared with no collector bound"
    ).toBe(false);
  });
});

describe("the interception counters", () => {
  it("report what the wrappers actually did", () => {
    const { measured } = row(
      narrate(
        input({
          busStats: { dialsPassed: 1, dialsRefused: 2 },
          kvStats: {
            readsFailed: 5,
            readsPassed: 6,
            writesFailed: 0,
            writesPassed: 1,
          },
        })
      ),
      "faults intercepted"
    );
    expect(measured).toMatch(/5 reads/u);
    expect(measured).toMatch(/2 refused/u);
  });
});

describe("both ends of the heartbeat number", () => {
  it("prints the two ends and that they agree when they do", () => {
    const narration = narrate(input());
    expect(row(narration, "heartbeat, both ends").measured).toMatch(
      /they agree/u
    );
    expect(row(narration, "heartbeat, both ends").documented).toMatch(
      /nothing enforcing/u
    );
  });

  it("SHOUTS when they disagree — nothing in the system would otherwise notice", () => {
    expect(
      row(
        narrate(input({ coordinatorHeartbeatMs: 5000 })),
        "heartbeat, both ends"
      ).measured
    ).toMatch(/THEY DISAGREE/u);
  });

  it("reports an unset coordinator end as dormant, not as zero", () => {
    const { measured } = row(
      narrate(input({ coordinatorHeartbeatMs: null })),
      "heartbeat, both ends"
    );
    expect(measured).toMatch(/UNSET/u);
    expect(measured).toMatch(/dormant/u);
  });
});

// ── The poll rescue ─────────────────────────────────────────────────────────
/** The measured "purge → bus-frame cursor" row for a reader waiting on `channel`. */
const waitOn = (channel: Explanation["reader"]["channel"]) =>
  row(
    narrate(
      input({
        awaitingRescueForMs: 2000,
        witness: { explanation: withReader({ channel }), ok: true },
      })
    ),
    "purge → bus-frame cursor"
  ).measured;

describe("the poll rescue row", () => {
  it("prints the epochs on both sides of the number, not just the number", () => {
    const { measured } = row(
      narrate(
        input({
          rescue: {
            afterMs: 7300,
            fromEpoch: 4,
            path: "carrier-proven",
            toEpoch: 5,
          },
        })
      ),
      "purge → bus-frame cursor"
    );
    expect(measured).toMatch(/4 → 5/u);
    expect(measured).toMatch(/7\.3s/u);
    expect(measured).toMatch(/only path left that moves this cursor/u);
  });

  it("REFUSES to pick a path when the channel was up — it says it cannot tell", () => {
    // The rescue's claim is that the POLL path did the work. With a channel up, a frame had somewhere
    // to arrive and the demo cannot know which path carried it — so it says so, out loud, rather
    // than taking credit for the more interesting one. Spoken indeterminacy is the approved line.
    const { measured } = row(
      narrate(
        input({
          rescue: {
            afterMs: 900,
            fromEpoch: 4,
            path: "indeterminate",
            toEpoch: 5,
          },
        })
      ),
      "purge → bus-frame cursor"
    );
    expect(measured).toMatch(/cannot tell which/u);
    expect(measured).not.toMatch(/only path left/u);
  });

  it("REFUSES to call an own-ack a rescue — the unsupported carrier credit", () => {
    // Crediting the poll carrier here would be unsupported: the epoch moved in ~2ms against a 5s
    // cadence with the bus dead, but that advance is an own-ack — this isolate issued the purge and
    // `expire()`'s ack taught its own reader on the spot. Zero dials had succeeded, so a dial-counter
    // check would agree enthusiastically; it looks EXACTLY like a carrier rescue. The row must name
    // this path and refuse the credit.
    const { measured } = row(
      narrate(
        input({
          rescue: { afterMs: 2, fromEpoch: 0, path: "own-ack", toEpoch: 1 },
        })
      ),
      "purge → bus-frame cursor"
    );
    expect(measured).toMatch(/NOT A RESCUE/u);
    // It must name the real cause: the reader was taught by the ack of the purge it issued itself.
    expect(measured).toMatch(/issued the purge/u);
    expect(measured).toMatch(/ack taught its reader/u);
    // And it must take NO credit. The own-ack note is free to EXPLAIN what a real carrier rescue
    // would need — that sentence is the honest part. What it may never do is claim the poll path did
    // the work, which is `carrier`'s own sentence and must not appear here.
    expect(
      measured,
      "the demo credited a poll carrier that did nothing"
    ).not.toMatch(/only path left that moves this cursor/u);
    expect(measured).not.toMatch(/and this is its number/u);
  });

  it("reports an unmoved cursor as a missing FRAME, never as a reader that has not converged", () => {
    // The mislabel this guards against: reporting "the reader has not applied it yet" while the
    // reader has demonstrably learned the purge by live check. This row watches a bus cursor; it must
    // say so, and it must point at the row where convergence actually shows.
    const { measured } = row(
      narrate(
        input({
          awaitingRescueForMs: 2000,
          witness: {
            explanation: withReader({ channel: "never-established" }),
            ok: true,
          },
        })
      ),
      "purge → bus-frame cursor"
    );
    expect(measured).toMatch(/this cursor has not moved/u);
    expect(measured).toMatch(/verdict row/u);
    expect(
      measured,
      "the row libelled a reader that had converged"
    ).not.toMatch(/has not applied it yet/u);
  });

  // ── THE WAITING ARM READS THE CHANNEL ───────────────────────────────
  //
  // This arm used to hard-code a dead bus: it announced that the cursor may never move and that the
  // socket is not there, with no reference to `reader.channel` anywhere in the function. So it printed
  // verbatim on a healthy system, two rows below a channel row reading `established`, contradicting
  // it in the same table. The counterfactual is the pair below: same observable (a purge issued, the
  // cursor at zero), opposite channel, and the sentences must not agree.
  it("does NOT assert a dead bus while the channel reads established", () => {
    // MUTATION: hard-code the dead-bus sentence back into the waiting arm. This goes red.
    const { measured } = row(
      narrate(
        input({
          awaitingRescueForMs: 2000,
          witness: {
            explanation: withReader({ channel: "established" }),
            ok: true,
          },
        })
      ),
      "purge → bus-frame cursor"
    );
    expect(
      measured,
      "the demo declared a socket that is not there, on a reader holding one"
    ).not.toMatch(/socket that is not there/u);
    expect(measured).toMatch(/channel is UP/u);
  });

  it("gives an established channel and a dead one two different explanations for the same wait", () => {
    expect(
      waitOn("established"),
      "the waiting arm says the same thing whatever the reader's channel is doing"
    ).not.toBe(waitOn("never-established"));
  });

  it("invents no reader state to explain a wait when the witness raised", () => {
    const { measured } = row(
      narrate(
        input({
          awaitingRescueForMs: 2000,
          witness: { error: "boom", ok: false },
        })
      ),
      "purge → bus-frame cursor"
    );
    expect(measured).toMatch(/witness raised/u);
    expect(measured).not.toMatch(/socket that is not there/u);
    expect(measured).not.toMatch(/channel is UP/u);
  });

  it("declares the cadence it RUNS as this deployment's, beside the shipped one", () => {
    // The demo turns `maxSyncLag` down to make the rescue watchable. Saying so is the difference
    // between a demonstration and a misrepresentation.
    const { documented } = row(narrate(input()), "purge → bus-frame cursor");
    expect(documented).toMatch(/THIS DEPLOYMENT'S/u);
    expect(documented).toMatch(/turned down/u);
    expect(documented).toMatch(/60\.0s/u);
  });
});

describe("the read cost row", () => {
  it("says when the factory ran, because that is what makes the read a fill", () => {
    expect(row(narrate(input({ filled: true })), "read cost").measured).toMatch(
      /factory RAN/u
    );
    expect(
      row(narrate(input({ filled: false })), "read cost").measured
    ).toMatch(/served from a tier/u);
  });
});

/** The `because` of the degradation judgment for a reader in this state. */
function becauseFor(over: Partial<Explanation["reader"]>): string {
  return (
    narrate(
      input({ witness: { explanation: withReader(over), ok: true } })
    ).judgments.find((j) => j.event === TELEMETRY_EVENTS.registry_degraded)
      ?.because ?? ""
  );
}

describe("the judgments the demo derives", () => {
  it("derives nothing from a healthy run", () => {
    expect(narrate(input()).judgments).toStrictEqual([]);
  });

  it("derives registry_degraded from a suspect reader, and owns the threshold", () => {
    const judgment = narrate(
      input({
        witness: { explanation: withReader({ suspect: true }), ok: true },
      })
    ).judgments.find((j) => j.event === TELEMETRY_EVENTS.registry_degraded);
    expect(judgment).toBeDefined();
    expect(judgment?.because).toMatch(
      /the demo's threshold, not the library's/u
    );
  });

  // ── A TOOTH PER ARM: which degradation fact gets named ─────────────────────
  //
  // `degradedBecause` picks the most specific TRUE fact about the reader, and its arms could be
  // SWAPPED — or one replaced with a garbage string — with the whole suite green, because the only
  // test claiming this jurisdiction asserted the constant suffix that every arm carries. A suffix
  // every branch shares cannot tell the branches apart. Each arm is now driven to the exact word it
  // owes, and the arms are checked against each other so a swap cannot hide behind a match.
  describe("names WHICH degradation fact it found — one arm at a time", () => {
    it("names TERMINAL for a terminal reader, and not the other arms", () => {
      const because = becauseFor({ suspect: false, terminal: true });
      expect(because).toMatch(/\bterminal\b/u);
      expect(because).not.toMatch(/\bsuspect\b/u);
      expect(because).not.toMatch(/polling into failures/u);
    });

    it("names SUSPECT for a suspect reader, and not the other arms", () => {
      // The arm with no tooth at all: its only test asserted the shared suffix, so swapping this
      // label with `terminal`'s changed nothing anywhere in the suite.
      const because = becauseFor({ suspect: true, terminal: false });
      expect(because).toMatch(/\bsuspect\b/u);
      expect(because).not.toMatch(/\bterminal\b/u);
      expect(because).not.toMatch(/polling into failures/u);
    });

    it("names the swallowed ticks when the reader itself is neither", () => {
      // The third arm, reached only when the reader is healthy and the LOG is what is degraded.
      const because =
        narrate(
          input({
            events: [{ type: TELEMETRY_EVENTS.poll_tick_failed }],
            witness: {
              explanation: withReader({ suspect: false, terminal: false }),
              ok: true,
            },
          })
        ).judgments.find((j) => j.event === TELEMETRY_EVENTS.registry_degraded)
          ?.because ?? "";
      expect(because).toMatch(/polling into failures/u);
      expect(because).not.toMatch(/\bterminal\b/u);
    });

    it("prefers TERMINAL over suspect — the most specific true fact, not the first one found", () => {
      const because = becauseFor({ suspect: true, terminal: true });
      expect(
        because,
        "a reader that has stopped claiming to know was reported merely suspicious"
      ).toMatch(/\bterminal\b/u);
    });

    it("gives the three arms three different sentences", () => {
      // Non-vacuity for the four above: any single one could be satisfied by a constant. Only their
      // mutual disagreement proves three live branches.
      const seen = new Set([
        becauseFor({ suspect: false, terminal: true }),
        becauseFor({ suspect: true, terminal: false }),
        narrate(
          input({
            events: [{ type: TELEMETRY_EVENTS.poll_tick_failed }],
            witness: { explanation: withReader({}), ok: true },
          })
        ).judgments.find((j) => j.event === TELEMETRY_EVENTS.registry_degraded)
          ?.because ?? "",
      ]);
      expect(seen.size, "two degradation arms render identically").toBe(3);
    });
  });

  it("derives registry_degraded from a TERMINAL reader alone", () => {
    // The `reader.terminal` disjunct, which nothing reached. `suspect` is false and no tick failed,
    // so this judgment can ONLY come from the terminal arm — drop `reader.terminal` from the
    // disjunction and this is the test that goes red.
    const judgment = narrate(
      input({
        witness: {
          explanation: withReader({ suspect: false, terminal: true }),
          ok: true,
        },
      })
    ).judgments.find((j) => j.event === TELEMETRY_EVENTS.registry_degraded);
    expect(judgment, "a terminal reader derived no degradation").toBeDefined();
    expect(judgment?.because).toMatch(/terminal/u);
  });

  it("derives registry_degraded from swallowed ticks — the raw fact the library DOES emit", () => {
    const events: TelemetryEvent[] = [
      { type: TELEMETRY_EVENTS.poll_tick_failed },
    ];
    expect(narrate(input({ events })).judgments.map((j) => j.event)).toContain(
      TELEMETRY_EVENTS.registry_degraded
    );
  });

  it("derives registry_degraded when the witness RAISED, and says the reader was never observed", () => {
    // THE ARM THE TAUTOLOGY LEFT UNCOVERED. Its only previous test asserted that every judgment's
    // `event` is a catalogue member — which the TYPE guarantees, so it passed just as happily with
    // this entire branch deleted. What must be true is that the raise produces a judgment AT ALL,
    // and that the judgment reports what it actually knows.
    const judgment = narrate(
      input({ witness: { error: "boom", ok: false } })
    ).judgments.find((j) => j.event === TELEMETRY_EVENTS.registry_degraded);
    expect(
      judgment,
      "a raised witness derived no degradation at all"
    ).toBeDefined();
    expect(judgment?.because).toMatch(/the witness raised/u);
    expect(judgment?.because).toMatch(/never observed/u);
  });

  it("does not invent a reader state when the witness raised", () => {
    // The dishonesty this arm exists to avoid: reporting `suspect` or `terminal` for a reader whose
    // state was never read. The judgment must name the raise, not the reader.
    const because =
      narrate(input({ witness: { error: "boom", ok: false } })).judgments.find(
        (j) => j.event === TELEMETRY_EVENTS.registry_degraded
      )?.because ?? "";
    expect(because).not.toMatch(/\bis suspect\b/u);
    expect(because).not.toMatch(/\bis terminal\b/u);
  });

  // ── state_stale rests on the SYSTEM's evidence, not on the demo's button ───
  //
  // It used to fire on `armedForMs >= POSTURE.maxSyncLagMs` — how long ago someone pressed Arm — and
  // then state as FACT that "the mirror pointer has gone unread", while every mirror read on the page
  // was succeeding. A stopwatch on a button cannot establish anything about a pointer. It now reads
  // the reader's own flush-marker-anchored silence measurement, surfaced as `suspect`: the system
  // measuring itself, which the demo may read but may not impersonate.
  describe("state_stale", () => {
    it("fires when the READER's own silence measurement raised suspicion", () => {
      const judgment = narrate(
        input({
          witness: { explanation: withReader({ suspect: true }), ok: true },
        })
      ).judgments.find((j) => j.event === TELEMETRY_EVENTS.state_stale);
      expect(
        judgment,
        "the reader said the pointer went silent and the demo said nothing"
      ).toBeDefined();
      expect(judgment?.because).toMatch(
        /the demo's threshold, not the library's/u
      );
    });

    it("does NOT fire off the Arm button, however long ago it was pressed", () => {
      // THE COUNTERFACTUAL. The credited mechanism (a pointer gone unread) is ABSENT — the reader is
      // healthy and not suspect — and the old observable is not merely identical but extreme: armed
      // for forty times the poll cadence. The old threshold fires here. MUTATION: restore
      // `input.armedForMs >= POSTURE.maxSyncLagMs` as the condition and this goes red.
      const judgments = narrate(
        input({
          armedForMs: POSTURE.maxSyncLagMs * 40,
          witness: { explanation: withReader({ suspect: false }), ok: true },
        })
      ).judgments.map((j) => j.event);
      expect(
        judgments,
        "the demo timed its own button and reported the result as a fact about the mirror pointer"
      ).not.toContain(TELEMETRY_EVENTS.state_stale);
    });

    it("cites no clock of its own — the reader measured it, the demo only read the answer", () => {
      const because =
        narrate(
          input({
            armedForMs: POSTURE.maxSyncLagMs * 40,
            witness: { explanation: withReader({ suspect: true }), ok: true },
          })
        ).judgments.find((j) => j.event === TELEMETRY_EVENTS.state_stale)
          ?.because ?? "";
      // The armed-for duration must not appear in the judgment's reasoning: it is a fact about the
      // demo's furniture, and this judgment is about the system.
      expect(because).not.toMatch(/armed/iu);
      expect(because).toMatch(/reader's OWN silence measurement/u);
    });
  });
});

// ── The healthy camera note names its baseline (FIX: it asserted a state the page falsified) ──
describe("the healthy-page note", () => {
  it("does not claim every counter reads zero — the page's own loader falsifies that before render", () => {
    // STRUCTURALLY IMPOSSIBLE, which is what makes this worth a tooth rather than a proofread: the
    // route's loader reads the probe and witnesses it, both through the wrapper, both incrementing
    // the PASS counters, before a byte of the HTML exists. The note asserted that
    // "every counter below reads zero" on a page that had already made it false.
    // MUTATION: restore "every counter below reads zero" and this goes red.
    expect(
      HEALTHY_NOTE,
      "the note claims a total that its own page has already contradicted"
    ).not.toMatch(/every counter below reads zero/u);
  });

  it("names its baseline: the page's own loader traffic is stated, not hidden", () => {
    expect(HEALTHY_NOTE).toMatch(/loader/u);
    expect(HEALTHY_NOTE).toMatch(/own traffic/u);
    // What genuinely IS zero on a healthy page, said precisely.
    expect(HEALTHY_NOTE).toMatch(/FAULT counter/u);
  });
});

describe("the scene metadata", () => {
  it("states what MAY follow, never what will", () => {
    for (const line of sceneOf("registry-outage").mayHappen) {
      expect(line).toMatch(/\bmay\b|\bstops\b|\bloses\b/u);
    }
  });
});
