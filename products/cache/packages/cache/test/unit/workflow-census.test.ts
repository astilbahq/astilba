// The BOARD'S LEDGER TOOTH. An aggregate gate's coverage IS its `needs:` list, which means the
// board's completeness is an invariant of a config file — and config invariants rot silently,
// because nothing executes them. A lane added without a `needs:` entry is green by absence: it runs,
// it reports, and it gates nothing, which is indistinguishable from working until the day it matters.
//
// JURISDICTION, stated because an instrument that does not name its limits invites the vacuous green
// one storey up. This test proves the board's LEDGER is complete:
//
//   • every job defined in the workflow is depended upon by the aggregate gate, and
//   • exactly one lane's RESULT is excluded from that gate's pass/fail arithmetic, and
//   • every `jq` filter in that gate agrees on which one — the deciding condition and the
//     failure-reporting branch cannot drift apart.
//
// Both clauses hold AS MODELED — the exclusion set is read as the distinct `del(...)` names in the
// aggregate's script, which is a model of jq-inside-shell-inside-YAML rather than an execution of
// it. That model is what the two mutations below prove in both directions: a planted job reds the
// first clause, a second `del(...)` reds the second. A model nobody exercises is a claim.
//
// It proves NOTHING about any lane's verdict, about whether a lane's tests are adequate, or about
// whether the excluded lane's reds were adjudicated correctly — that judgment lives in the gate
// record and in the merge protocol, and no parser can perform it. This is a bookkeeping check, and
// bookkeeping is exactly what a `needs:` list is.
//
// It parses the workflow WITH A YAML PARSER, never a grep. A regex over YAML agrees with the file
// right up until indentation, anchors, or a block scalar move — and then it reports a confident
// answer about a structure it never understood. The thing under test is a document with a shape; the
// instrument reads that shape.
//
// Lane: unit. Deterministic — a file on disk, parsed. No network, no timing, no runner.

import { readFileSync } from "node:fs";
import path from "node:path";

import { describe, expect, it } from "vitest";
import { parse } from "yaml";

const REPO_ROOT = path.resolve(import.meta.dirname, "../../../..");
const WORKFLOW = path.join(REPO_ROOT, ".github/workflows/ci.yml");

/** The ONE lane whose result may be excluded, and the reason it is singular. Its reds require a
 * reading — a characterized environmental signature versus a novel one — and a `needs:` entry can
 * only read green-or-not-green, so its judgment lives in the gate record. Every
 * other lane is mechanically required. A SECOND name here would mean a second gate had quietly
 * become an opinion, which is the drift this test exists to catch. */
const ADJUDICATED_LANE = "integration";
const AGGREGATE = "required";

interface Workflow {
  readonly jobs: Record<
    string,
    {
      readonly needs?: readonly string[];
      readonly steps?: readonly { readonly run?: string }[];
    }
  >;
}

function loadWorkflow(source = readFileSync(WORKFLOW, "utf-8")): Workflow {
  // The parse target is a workflow file whose shape is asserted immediately below, so a malformed
  // document fails that assertion rather than being silently narrowed. NOTE the directive's
  // placement: `disable-next-line` governs the line that literally follows it, so a multi-line
  // justification above the directive would aim it at another comment and silently do nothing.
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- shape asserted on the next line
  const doc = parse(source) as Workflow;
  expect(doc?.jobs).toBeTypeOf("object");
  return doc;
}

/** The aggregate's whole run script, joined across its steps. */
function aggregateScript(wf: Workflow): string {
  return (wf.jobs[AGGREGATE]?.steps ?? []).map((s) => s.run ?? "").join("\n");
}

/** The exclusion names in ONE expression, de-duplicated and sorted. */
function exclusionsIn(expression: string): string[] {
  const names = [...expression.matchAll(/del\(\.(?<lane>[A-Za-z][\w-]*)\)/gu)]
    .map((m) => m.groups?.lane)
    .filter((n): n is string => n !== undefined);
  return [...new Set(names)].toSorted();
}

/** Every `jq` filter the aggregate runs — one per line that invokes it. There are two by design:
 * the PASS/FAIL condition, which decides the gate, and the failure-reporting branch, which only
 * decides what gets printed. They must carry the SAME exclusions, and the agreement assertion below
 * is what makes that structural rather than customary. */
function jqFilters(wf: Workflow): string[] {
  return aggregateScript(wf)
    .split("\n")
    .filter((l) => l.includes("jq") && l.includes("del("));
}

/** The aggregate's PASS/FAIL expression alone — the `jq` filter inside the `if !` guard, which is
 * the only one whose exclusions change what the gate counts. */
function passFailExpression(wf: Workflow): string {
  const line = jqFilters(wf).find((l) => l.includes("if !"));
  expect(line).toBeTypeOf("string");
  return line ?? "";
}

/** Every result the aggregate deliberately drops, read off the script it actually runs rather than
 * from a comment claiming what it does.
 *
 * DISTINCT names, not occurrences: the same exclusion legitimately appears in each filter, so a
 * count of mentions would red on a faithful script. Note what de-duplication therefore CANNOT see —
 * the two filters diverging — which is why agreement is asserted separately, at each filter. */
function excludedResults(wf: Workflow): string[] {
  return exclusionsIn(aggregateScript(wf));
}

describe("the board's ledger — every job is depended upon, one result is excluded", () => {
  it("LEDGER COMPLETE: no job exists outside the aggregate's needs", () => {
    const wf = loadWorkflow();
    const jobs = Object.keys(wf.jobs).filter((j) => j !== AGGREGATE);
    const needs = wf.jobs[AGGREGATE]?.needs ?? [];

    // Non-vacuity: a board this test could pass over an empty workflow would prove nothing. Both
    // sides must be populated before their equality means anything.
    expect(jobs.length).toBeGreaterThan(5);

    // THE TOOTH. Add a lane and forget its `needs:` entry and this reds — which is the whole point,
    // because that lane would otherwise run, report, and gate nothing.
    expect({
      ungated: jobs.filter((j) => !needs.includes(j)).toSorted(),
      dangling: needs.filter((n) => !jobs.includes(n)).toSorted(),
    }).toStrictEqual({ dangling: [], ungated: [] });
  });

  it("EXCLUSION SET: exactly one lane's result is dropped, and it is the adjudicated one", () => {
    const wf = loadWorkflow();

    // The excluded lane must still be DEPENDED UPON: the `needs:` entry buys the WAIT (the aggregate
    // cannot report before the lane finishes) while the exclusion decides only what counts as
    // failure. Dropping the entry too would let this gate go green while the lane still ran.
    expect(wf.jobs[AGGREGATE]?.needs).toContain(ADJUDICATED_LANE);

    // THE TOOTH. A second exclusion means a second gate has become an opinion.
    expect(excludedResults(wf)).toStrictEqual([ADJUDICATED_LANE]);

    // AND the exclusion must live in the expression that DECIDES, not merely somewhere in the
    // script. De-duplicating names across the whole script — which is right, since the same
    // exclusion legitimately appears twice — blinds a name-anywhere check to the two copies
    // drifting apart. Remove it from the condition while the report's copy remains and the gate
    // silently starts counting the adjudicated lane again.
    expect(passFailExpression(wf)).toContain(`del(.${ADJUDICATED_LANE})`);

    // AND every filter agrees. There are two by design — the deciding condition and the
    // failure-reporting branch — and de-duplication across the script is structurally blind to them
    // drifting apart. Asserting each filter's own exclusion set makes the agreement mechanical
    // instead of customary: change one copy and this reds, whichever copy it was.
    const filters = jqFilters(wf);
    expect(filters.length).toBeGreaterThan(1);
    expect(filters.map(exclusionsIn)).toStrictEqual(
      filters.map(() => [ADJUDICATED_LANE])
    );
  });

  it("NON-VACUITY: the parser reds on a planted ungated job and on a second exclusion", () => {
    // The instrument proves it can see, on documents it is handed rather than on the repo's own —
    // an alarm nobody has made ring is not yet evidence.
    const source = readFileSync(WORKFLOW, "utf-8");

    const withExtraJob = loadWorkflow(
      `${source}\n  planted-ungated-lane:\n    runs-on: ubuntu-latest\n`
    );
    const jobs = Object.keys(withExtraJob.jobs).filter((j) => j !== AGGREGATE);
    const needs = withExtraJob.jobs[AGGREGATE]?.needs ?? [];
    expect(jobs.filter((j) => !needs.includes(j))).toStrictEqual([
      "planted-ungated-lane",
    ]);

    // Target the DECIDING line, not the first textual occurrence. Prose in this workflow now
    // mentions `del(.integration)` when explaining the expiry, and it appears earlier in the file
    // than the code — a first-occurrence replace would mutate a comment and prove nothing. The test
    // caught exactly that when the comment was written, which is the tooth working on itself.
    const conditionLine = source
      .split("\n")
      .find((l) => l.includes("if !") && l.includes("jq"));
    expect(conditionLine).toBeTypeOf("string");
    const withSecondExclusion = loadWorkflow(
      source.replace(
        conditionLine ?? "",
        (conditionLine ?? "").replace(
          "del(.integration)",
          "del(.integration) | del(.conformance)"
        )
      )
    );
    // Sorted and de-duplicated, so this is a genuine second LANE rather than a second mention.
    expect(excludedResults(withSecondExclusion)).toStrictEqual([
      "conformance",
      ADJUDICATED_LANE,
    ]);
  });
});
