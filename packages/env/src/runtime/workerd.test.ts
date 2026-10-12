import { describe, expect, it } from "vitest";

import type { ProcessTargetDefinition, StandardSchemaV1 } from "./index.ts";
import {
  checkProcessTarget,
  checkProcessTargetWithSchemas,
  loadProcessTarget,
} from "./workerd.ts";

const firstPartyTarget: ProcessTargetDefinition = {
  bindings: [{ entry: "enabled", source: "ENABLED" }],
  generated: "astilba.env.generated-module/v1",
  lifecycle: "deployment",
  projection: {
    canonicalisation: "astilba.jcs/v1",
    codecAbi: "astilba.env.codec/v1",
    consumer: "worker",
    contract: "example.workerd",
    entries: [
      {
        codec: {
          abi: "astilba.env.boolean-exact/v1",
          blank: "missing",
          falseInput: "false",
          kind: "boolean",
          trueInput: "true",
        },
        identity: ["example.workerd", "enabled"],
        lifecycle: "deployment",
        name: "enabled",
        required: true,
        visibility: "public",
      },
    ],
    format: "astilba.env.projection",
    formatVersion: 1,
    kind: "server",
    projectionAbi: "astilba.env.projection/v1",
  },
};

const opaqueTarget: ProcessTargetDefinition = {
  bindings: [{ entry: "setting", source: "SETTING" }],
  generated: "astilba.env.generated-module/v1",
  lifecycle: "deployment",
  projection: {
    canonicalisation: "astilba.jcs/v1",
    codecAbi: "astilba.env.codec/v1",
    consumer: "worker",
    contract: "example.workerd",
    entries: [
      {
        codec: {
          abi: "astilba.env.opaque/v1",
          input: { kind: "string" },
          kind: "opaque",
          output: { kind: "string" },
          revision: "1",
          semantics: "example/setting@1",
        },
        identity: ["example.workerd", "setting"],
        lifecycle: "deployment",
        name: "setting",
        required: true,
        visibility: "private",
      },
    ],
    format: "astilba.env.projection",
    formatVersion: 1,
    kind: "server",
    projectionAbi: "astilba.env.projection/v1",
  },
};

const unreadableSource = () => {
  let reads = 0;
  return {
    reads: () => reads,
    source: new Proxy(
      {},
      {
        getOwnPropertyDescriptor() {
          reads += 1;
          throw new Error("A refused workerd target read its binding source.");
        },
      }
    ),
  };
};

describe("workerd generated-module runtime", () => {
  it.each(["build", "request"] as const)(
    "refuses the %s lifecycle before source access",
    (lifecycle) => {
      const unreadable = unreadableSource();
      const definition: ProcessTargetDefinition = {
        ...firstPartyTarget,
        lifecycle,
      };

      expect(checkProcessTarget(definition, unreadable.source)).toStrictEqual({
        diagnostics: [{ code: "ENV_CONTRACT_INVALID" }],
        ok: false,
      });
      expect(unreadable.reads()).toBe(0);
      expect(() => loadProcessTarget(definition, unreadable.source)).toThrow(
        "Astilba Env configuration is invalid."
      );
      expect(unreadable.reads()).toBe(0);
    }
  );

  it("refuses opaque execution without reading bindings or invoking a validator", async () => {
    const unreadable = unreadableSource();
    let validatorCalls = 0;
    const setting: StandardSchemaV1<string, string> = {
      "~standard": {
        validate: (value) => {
          validatorCalls += 1;
          return { value: typeof value === "string" ? value : "" };
        },
        vendor: "fixture",
        version: 1,
      },
    };

    await expect(
      checkProcessTargetWithSchemas(opaqueTarget, unreadable.source, {
        setting,
      })
    ).resolves.toStrictEqual({
      diagnostics: [
        {
          code: "ENV_OPAQUE_UNSUPPORTED",
          codec: "astilba.env.opaque/v1",
          consumer: "worker",
          entry: "setting",
          lifecycle: "deployment",
        },
      ],
      ok: false,
    });
    expect(unreadable.reads()).toBe(0);
    expect(validatorCalls).toBe(0);
  });

  it("executes a first-party deployment target", () => {
    const result = checkProcessTarget<{ readonly enabled: boolean }>(
      firstPartyTarget,
      { ENABLED: "true" }
    );
    expect(result.ok).toBe(true);
    if (!result.ok) {
      throw new Error("Expected a successful workerd deployment result.");
    }
    expect(result.value.enabled).toBe(true);
    expect(Object.getPrototypeOf(result.value)).toBeNull();
  });
});
