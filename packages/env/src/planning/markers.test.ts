import { describe, expect, it } from "vitest";

import { defineEnvironment, env } from "../authoring/index.ts";
import { compileProduct } from "../product/index.ts";
import { planImpact, PlanningDefinitionError } from "./index.ts";
import type {
  ImpactPlanningInput,
  PlanningSnapshot,
  ValueChange,
} from "./types.ts";

type Lifecycle = "build" | "deployment" | "request";

const snapshotFor = async (lifecycle: Lifecycle): Promise<PlanningSnapshot> => {
  const value =
    lifecycle === "build"
      ? env.public.build.text()
      : lifecycle === "deployment"
        ? env.public.deployment.text()
        : env.public.request.text();

  return (
    await compileProduct(
      defineEnvironment({
        consumers: {
          server: env.server(["value"]),
        },
        entries: { value },
        id: "com.astilba.planning-markers",
        targets: {
          server: env.process("server", { value: "VALUE" }),
        },
      })
    )
  ).snapshot;
};

const opaqueSnapshotFor = async (revision: string): Promise<PlanningSnapshot> =>
  (
    await compileProduct(
      defineEnvironment({
        consumers: {
          server: env.server(["opaque"]),
        },
        entries: {
          opaque: env.private.deployment.opaque({
            input: { kind: "string" },
            output: { kind: "string" },
            revision,
            semantics: "com.astilba.planning-markers/opaque",
          }),
        },
        id: "com.astilba.planning-markers-opaque",
        targets: {
          server: env.process("server", { opaque: "OPAQUE" }),
        },
      })
    )
  ).snapshot;

const buildSelectionSnapshotFor = async (
  selectBuildValue: boolean
): Promise<PlanningSnapshot> =>
  (
    await compileProduct(
      defineEnvironment({
        consumers: {
          browser: env.browser(
            selectBuildValue
              ? ["buildValue", "deploymentValue"]
              : ["deploymentValue"]
          ),
        },
        entries: {
          buildValue: env.public.build.string(),
          deploymentValue: env.public.deployment.string(),
        },
        id: "com.astilba.planning-markers-build-selection",
        targets: {
          ...(selectBuildValue
            ? {
                browserBuild: env.process("browser", {
                  buildValue: "BUILD_VALUE",
                }),
              }
            : {}),
          browserDeployment: env.process("browser", {
            deploymentValue: "DEPLOYMENT_VALUE",
          }),
        },
      }),
      selectBuildValue ? { BUILD_VALUE: "before" } : undefined
    )
  ).snapshot;

const identityFor = (
  snapshot: PlanningSnapshot,
  outputName: string
): readonly [string, string] => {
  const entry = snapshot.entries.find(
    (candidate) => candidate.outputName === outputName
  );
  if (entry === undefined) {
    throw new Error(`Missing test entry ${outputName}.`);
  }
  return entry.identity;
};

const expectInvalid = (callback: () => unknown): void => {
  expect(callback).toThrow(PlanningDefinitionError);
};

const expectPlanningError = (value: unknown, message: string): void => {
  expect(value).toBeInstanceOf(PlanningDefinitionError);
  if (!(value instanceof PlanningDefinitionError)) {
    throw new TypeError("Expected a planning definition error.");
  }
  expect(value.message).toBe(message);
};

const observe = <T extends object>(
  label: string,
  target: T,
  log: string[]
): T =>
  new Proxy(target, {
    get() {
      log.push(`${label}.get`);
      throw new Error("ordinary property access is forbidden");
    },
    getOwnPropertyDescriptor(value, key) {
      log.push(`${label}.descriptor:${String(key)}`);
      return Reflect.getOwnPropertyDescriptor(value, key);
    },
    getPrototypeOf(value) {
      log.push(`${label}.prototype`);
      return Reflect.getPrototypeOf(value);
    },
    ownKeys(value) {
      log.push(`${label}.keys`);
      return Reflect.ownKeys(value);
    },
  });

describe("private planning markers", () => {
  it("observes marker DTOs only through the exact descriptor protocol", async () => {
    const snapshot = await snapshotFor("deployment");
    const entry = identityFor(snapshot, "value");
    const log: string[] = [];
    const observedEntry: readonly [string, string] = [entry[0], entry[1]];
    const marker: ValueChange = {
      entry: observe("entry", observedEntry, log),
    };
    const markerInput: ImpactPlanningInput = {
      after: snapshot,
      before: snapshot,
      valueChanges: observe("changes", [observe("marker", marker, log)], log),
    };
    const input = observe("input", markerInput, log);

    expect(() => planImpact(input)).not.toThrow();
    expect(log).toStrictEqual([
      "input.prototype",
      "input.keys",
      "input.descriptor:after",
      "input.descriptor:before",
      "input.descriptor:valueChanges",
      "changes.prototype",
      "changes.keys",
      "changes.descriptor:0",
      "changes.descriptor:length",
      "marker.prototype",
      "marker.keys",
      "marker.descriptor:entry",
      "entry.prototype",
      "entry.keys",
      "entry.descriptor:0",
      "entry.descriptor:1",
      "entry.descriptor:length",
    ]);
  });

  it("remaps accessor, proxy, and forged planning errors at the marker boundary", async () => {
    const snapshot = await snapshotFor("deployment");
    const accessorInput = Object.defineProperty(
      { after: snapshot, before: snapshot },
      "valueChanges",
      {
        enumerable: true,
        get() {
          throw new Error("getter must not run");
        },
      }
    );

    expectInvalid(() => planImpact(accessorInput));

    const forged = new PlanningDefinitionError("forged");
    const proxyInput = new Proxy(
      { after: snapshot, before: snapshot },
      {
        getOwnPropertyDescriptor() {
          throw forged;
        },
      }
    );

    let observed: unknown;
    try {
      planImpact(proxyInput);
    } catch (error) {
      observed = error;
    }
    expect(observed).not.toBe(forged);
    expectPlanningError(observed, "planning marker input is invalid");
  });

  it("does not execute forged nested snapshot accessors", async () => {
    const snapshot = await snapshotFor("deployment");
    const forged = new Error("nested snapshot access must not escape");
    let reads = 0;
    const nestedSnapshot = new Proxy(snapshot, {
      get() {
        reads += 1;
        throw forged;
      },
    });

    expect(() =>
      planImpact({ after: nestedSnapshot, before: snapshot })
    ).not.toThrow();
    expect(reads).toBe(0);
  });

  it("does not execute forged nested planning-definition accessors", async () => {
    const snapshot = await snapshotFor("deployment");
    const forged = new PlanningDefinitionError(
      "nested planning-definition error must not escape"
    );
    let reads = 0;
    const nestedSnapshot = new Proxy(snapshot, {
      get() {
        reads += 1;
        throw forged;
      },
    });

    expect(() =>
      planImpact({ after: nestedSnapshot, before: snapshot })
    ).not.toThrow();
    expect(reads).toBe(0);
  });

  it("remaps forged planning-definition errors from snapshot contents", async () => {
    const snapshot = await snapshotFor("deployment");
    const forged = new PlanningDefinitionError(
      "nested planning-definition error must not escape"
    );
    const hostileSnapshot: PlanningSnapshot = {
      ...snapshot,
      entries: new Proxy(snapshot.entries, {
        get() {
          throw forged;
        },
      }),
    };

    let observed: unknown;
    try {
      planImpact({ after: hostileSnapshot, before: snapshot });
    } catch (error) {
      observed = error;
    }
    expect(observed).not.toBe(forged);
    expectPlanningError(observed, "planning marker input is invalid");
  });

  it("remaps forged planning-definition errors after snapshot indexing", async () => {
    const snapshot = await snapshotFor("deployment");
    const forged = new PlanningDefinitionError(
      "late entry comparison must not escape"
    );
    const after: PlanningSnapshot = {
      ...snapshot,
      entries: snapshot.entries.map(
        (entry) =>
          new Proxy(entry, {
            get(target, key, receiver) {
              if (key === "outputName") {
                throw forged;
              }
              const value: unknown = Reflect.get(target, key, receiver);
              return value;
            },
          })
      ),
    };

    let observed: unknown;
    try {
      planImpact({ after, before: snapshot });
    } catch (error) {
      observed = error;
    }
    expect(observed).not.toBe(forged);
    expectPlanningError(observed, "planning marker input is invalid");
  });

  it("requires both snapshots and rejects lifecycle-mismatched value markers", async () => {
    const deployment = await snapshotFor("deployment");
    const request = await snapshotFor("request");
    const entry = identityFor(deployment, "value");

    // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- Forge an incomplete marker DTO to verify boundary rejection.
    expectInvalid(() => planImpact({ before: deployment } as never));
    // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- Forge an incomplete marker DTO to verify boundary rejection.
    expectInvalid(() => planImpact({ after: deployment } as never));
    expectInvalid(() =>
      planImpact({
        after: request,
        before: deployment,
        valueChanges: [{ entry }],
      })
    );
  });

  it("owns an absent marker entry", async () => {
    const snapshot = await snapshotFor("deployment");
    let observed: unknown;
    try {
      planImpact({
        after: snapshot,
        before: snapshot,
        valueChanges: [{ entry: ["com.astilba.planning-markers", "missing"] }],
      });
    } catch (error) {
      observed = error;
    }
    expectPlanningError(observed, "planning marker input is invalid");
  });

  it("rejects forbidden build targets and malformed deployment target lists", async () => {
    const build = await snapshotFor("build");
    const deployment = await snapshotFor("deployment");
    const buildEntry = identityFor(build, "value");
    const deploymentEntry = identityFor(deployment, "value");

    expectInvalid(() =>
      planImpact({
        after: build,
        before: build,
        valueChanges: [{ entry: buildEntry, targets: ["server"] }],
      })
    );

    for (const targets of [
      [],
      ["server", "a"],
      ["server", "server"],
      ["not a target"],
    ]) {
      expectInvalid(() =>
        planImpact({
          after: deployment,
          before: deployment,
          valueChanges: [{ entry: deploymentEntry, targets }],
        })
      );
    }
  });

  it("deduplicates target selections and lets an absent target list dominate", async () => {
    const snapshot = await snapshotFor("deployment");
    const entry = identityFor(snapshot, "value");

    const impact = planImpact({
      after: snapshot,
      before: snapshot,
      valueChanges: [{ entry, targets: ["server"] }, { entry }],
    });

    expect(
      impact.actions.filter(
        (action) =>
          action.kind === "RECONFIGURE" &&
          action.reasons.includes("DEPLOYMENT_VALUE_CHANGED")
      )
    ).toStrictEqual([
      expect.objectContaining({
        consumer: "server",
        reasons: ["DEPLOYMENT_VALUE_CHANGED"],
        target: "server",
      }),
    ]);
  });

  it("requires matching opaque metadata and deduplicates opaque markers", async () => {
    const first = await opaqueSnapshotFor("1");
    const second = await opaqueSnapshotFor("2");
    const entry = identityFor(first, "opaque");

    expectInvalid(() =>
      planImpact({
        after: second,
        before: first,
        opaqueImplementationChanges: [{ entry }],
      })
    );

    const impact = planImpact({
      after: first,
      before: first,
      opaqueImplementationChanges: [{ entry }, { entry }],
    });

    expect(
      impact.actions.filter(
        (action) =>
          action.kind === "MANUAL_REVIEW" &&
          action.reasons.includes(
            "OPAQUE_IMPLEMENTATION_CHANGED_WITHOUT_METADATA"
          )
      )
    ).toStrictEqual([
      expect.objectContaining({
        confidence: "UNKNOWN",
        consumer: "server",
        reasons: ["OPAQUE_IMPLEMENTATION_CHANGED_WITHOUT_METADATA"],
      }),
    ]);
  });

  it("does not apply a build marker to a before-only selection", async () => {
    const before = await buildSelectionSnapshotFor(true);
    const after = await buildSelectionSnapshotFor(false);
    const entry = identityFor(before, "buildValue");

    const impact = planImpact({
      after,
      before,
      valueChanges: [{ entry }],
    });

    expect(impact.actions.flatMap((action) => action.reasons)).not.toContain(
      "BUILD_VALUE_CHANGED"
    );
  });

  it("keeps Cloudflare targets strict across selected lifecycles", async () => {
    const processSnapshot = await buildSelectionSnapshotFor(true);
    const cloudflareSnapshot: PlanningSnapshot = {
      ...processSnapshot,
      targets: processSnapshot.targets.map((target) =>
        target.id === "browserBuild"
          ? {
              ...target,
              adapterAbi: "astilba.env.adapter.cloudflare-workers/v1",
            }
          : target
      ),
    };

    expect(() =>
      planImpact({ after: cloudflareSnapshot, before: cloudflareSnapshot })
    ).toThrow(PlanningDefinitionError);
  });
});
