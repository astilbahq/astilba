import { describe, expect, it } from "vitest";

import { parseBoundedJsonValue } from "../core/bounded-json.ts";
import { canonicalJson, compileContract } from "../core/index.ts";
import type { ProviderBindingPlan } from "../provider/types.ts";
import { createPlanningSnapshot, PlanningDefinitionError } from "./plan.ts";
import {
  decodePlanningSnapshotBytes,
  PlanningSnapshotDecodeError,
} from "./snapshot.ts";

const TEXT_ENCODER = new TextEncoder();

const snapshotSource = async (): Promise<string> => {
  const compiled = await compileContract({
    consumers: [
      {
        entries: [["com.example.environment", "apiOrigin"]],
        id: "browserApp",
        kind: "browser",
      },
    ],
    entries: [
      {
        codec: {
          abi: "astilba.env.string-code-point/v1",
          kind: "string",
          maxCodePoints: 255,
          minCodePoints: 1,
        },
        fragment: "com.example.environment",
        id: "apiOrigin",
        lifecycle: "deployment",
        required: true,
        visibility: "public",
      },
    ],
    id: "com.example.environment",
  });
  const bindingPlan: ProviderBindingPlan = Object.freeze({
    adapterAbi: "astilba.env.adapter.process-record/v1",
    bindings: Object.freeze([
      Object.freeze({
        channel: "deployment",
        class: "non-confidential",
        entry: "apiOrigin",
        kind: "public_text",
        rawName: "PUBLIC_API_ORIGIN",
      }),
    ]),
    format: "astilba.env.binding-plan/v1",
    target: "browserDeployment",
  });
  const snapshot = createPlanningSnapshot({
    compiled,
    targets: [{ bindingPlan, consumer: "browserApp" }],
  });
  return `${canonicalJson(snapshot)}\n`;
};

const expectDecodeCode = async (
  source: string | Uint8Array,
  code: "ENV_PLANNING_FORMAT_UNSUPPORTED" | "ENV_PLANNING_INVALID"
): Promise<void> => {
  const bytes =
    typeof source === "string" ? TEXT_ENCODER.encode(source) : source;
  await expect(decodePlanningSnapshotBytes(bytes)).rejects.toMatchObject({
    code,
  });
};

describe("planning snapshot reader", () => {
  it("allows process targets to omit optional same-lifecycle entries", async () => {
    const contract = "com.example.optional-process-coverage";
    const compiled = await compileContract({
      consumers: [
        {
          entries: [
            [contract, "optionalValue"],
            [contract, "requiredValue"],
          ],
          id: "server",
          kind: "server",
        },
      ],
      entries: [
        {
          codec: {
            abi: "astilba.env.string-code-point/v1",
            kind: "string",
            maxCodePoints: 255,
            minCodePoints: 1,
          },
          fragment: contract,
          id: "optionalValue",
          lifecycle: "deployment",
          required: false,
          visibility: "public",
        },
        {
          codec: {
            abi: "astilba.env.string-code-point/v1",
            kind: "string",
            maxCodePoints: 255,
            minCodePoints: 1,
          },
          fragment: contract,
          id: "requiredValue",
          lifecycle: "deployment",
          required: true,
          visibility: "public",
        },
      ],
      id: contract,
    });
    const processPlan = (entry: "optionalValue" | "requiredValue") =>
      Object.freeze({
        adapterAbi: "astilba.env.adapter.process-record/v1" as const,
        bindings: Object.freeze([
          Object.freeze({
            channel: "deployment" as const,
            class: "non-confidential" as const,
            entry,
            kind: "public_text",
            rawName:
              entry === "requiredValue" ? "REQUIRED_VALUE" : "OPTIONAL_VALUE",
          }),
        ]),
        format: "astilba.env.binding-plan/v1" as const,
        target: "server",
      });

    expect(() =>
      createPlanningSnapshot({
        compiled,
        targets: [
          {
            bindingPlan: processPlan("requiredValue"),
            consumer: "server",
          },
        ],
      })
    ).not.toThrow();
    expect(() =>
      createPlanningSnapshot({
        compiled,
        targets: [
          {
            bindingPlan: processPlan("optionalValue"),
            consumer: "server",
          },
        ],
      })
    ).toThrow(PlanningDefinitionError);
  });

  it("accepts and owns one exact canonical current snapshot", async () => {
    const source = await snapshotSource();
    const decoded = await decodePlanningSnapshotBytes(
      TEXT_ENCODER.encode(source)
    );

    expect(`${canonicalJson(decoded)}\n`).toBe(source);
    expect(Object.isFrozen(decoded)).toBe(true);
    expect(Object.isFrozen(decoded.entries)).toBe(true);
  });

  it("rejects noncanonical bytes, a missing LF, a BOM, and invalid UTF-8", async () => {
    expect.hasAssertions();
    const source = await snapshotSource();
    const body = source.slice(0, -1);

    await expectDecodeCode(` ${source}`, "ENV_PLANNING_INVALID");
    await expectDecodeCode(body, "ENV_PLANNING_INVALID");
    await expectDecodeCode(`\uFEFF${source}`, "ENV_PLANNING_INVALID");
    await expectDecodeCode(
      new Uint8Array([0x7b, 0x22, 0xff, 0x22, 0x7d, 0x0a]),
      "ENV_PLANNING_INVALID"
    );
  });

  it("refuses recognised newer top-level and nested ABIs before use", async () => {
    expect.hasAssertions();
    const source = await snapshotSource();

    await expectDecodeCode(
      source.replace(
        "astilba.env.planning-snapshot/v1",
        "astilba.env.planning-snapshot/v10"
      ),
      "ENV_PLANNING_FORMAT_UNSUPPORTED"
    );
    await expectDecodeCode(
      source.replace(
        "astilba.env.string-code-point/v1",
        "astilba.env.string-code-point/v2"
      ),
      "ENV_PLANNING_FORMAT_UNSUPPORTED"
    );
  });

  it("rejects unknown nested fields and reconstructed digest drift", async () => {
    expect.hasAssertions();
    const source = await snapshotSource();
    const parsed = parseBoundedJsonValue(source.slice(0, -1), {
      maximumArrayItems: 65_536,
      maximumBytes: 8_388_608,
      maximumContainerItems: 262_144,
      maximumDepth: 64,
      maximumObjectKeys: 262_144,
      maximumStringBytes: 1_048_576,
    });
    if (
      typeof parsed !== "object" ||
      parsed === null ||
      Array.isArray(parsed)
    ) {
      throw new TypeError("The test snapshot root is invalid.");
    }
    const entries: unknown = Object.getOwnPropertyDescriptor(
      parsed,
      "entries"
    )?.value;
    if (!Array.isArray(entries)) {
      throw new TypeError("The test snapshot entries are invalid.");
    }
    const entry: unknown = entries[0];
    if (typeof entry !== "object" || entry === null || Array.isArray(entry)) {
      throw new TypeError("The test snapshot entries are invalid.");
    }
    const changedEntry = Object.defineProperties(
      {},
      Object.getOwnPropertyDescriptors(entry)
    );
    Object.defineProperty(changedEntry, "extra", {
      configurable: true,
      enumerable: true,
      value: true,
      writable: true,
    });
    const changedEntries: unknown[] = [];
    for (const item of entries) {
      changedEntries.push(item);
    }
    changedEntries[0] = changedEntry;
    const rootDescriptors = Object.getOwnPropertyDescriptors(parsed);
    Reflect.deleteProperty(rootDescriptors, "entries");
    const changed = Object.defineProperties({}, rootDescriptors);
    Object.defineProperty(changed, "entries", {
      configurable: true,
      enumerable: true,
      value: changedEntries,
      writable: true,
    });
    await expectDecodeCode(
      `${canonicalJson(changed)}\n`,
      "ENV_PLANNING_INVALID"
    );

    const digest = /sha256-[A-Za-z0-9_-]{43}/u.exec(source)?.[0];
    if (digest === undefined) {
      throw new TypeError("The test snapshot digest is missing.");
    }
    const replacement = `${digest.slice(0, -1)}${
      digest.endsWith("A") ? "B" : "A"
    }`;
    await expectDecodeCode(
      source.replace(digest, replacement),
      "ENV_PLANNING_INVALID"
    );
  });

  it("rejects a decoded string and one array above the common limits", async () => {
    expect.hasAssertions();
    await expectDecodeCode(
      `${canonicalJson({
        consumers: [],
        entries: [],
        format: "astilba.env.planning-snapshot/v1",
        padding: "a".repeat(1_048_577),
        rules: [],
        targets: [],
      })}\n`,
      "ENV_PLANNING_INVALID"
    );
    await expectDecodeCode(
      `${canonicalJson({
        consumers: [],
        entries: [],
        format: "astilba.env.planning-snapshot/v1",
        rules: Array.from({ length: 65_537 }, () => null),
        targets: [],
      })}\n`,
      "ENV_PLANNING_INVALID"
    );
  });

  it("uses a stable owned error class", () => {
    const error = new PlanningSnapshotDecodeError("ENV_PLANNING_INVALID");
    expect(error.name).toBe("PlanningSnapshotDecodeError");
    expect(error.code).toBe("ENV_PLANNING_INVALID");
  });
});
