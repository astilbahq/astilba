import { describe, expect, it } from "vitest";

import { compileContract, findProjection } from "../core/index.ts";
import { defineEnvironment, env } from "../index.ts";
import type { EnvironmentDefinition } from "../index.ts";
import { getEnvironmentCompilerState } from "./internal.ts";

const declaration = (): EnvironmentDefinition =>
  defineEnvironment({
    consumers: {
      server: env.server(),
      web: env.browser(["apiOrigin", "clientMode", "requestLabel"]),
    },
    entries: {
      apiOrigin: env.public.deployment.origin(),
      clientMode: env.public.build.enum(["standard", "compact"]),
      internalValue: env.private.deployment.secret(),
      requestLabel: env.public.request.string({ required: false }),
    },
    id: "com.example.application",
    targets: {
      serverDeployment: env.process("server", {
        apiOrigin: "API_ORIGIN",
        internalValue: "INTERNAL_VALUE",
      }),
      serverRequest: env.process("server", {
        requestLabel: "REQUEST_LABEL",
      }),
      webBuild: env.process("web", {
        clientMode: "CLIENT_MODE",
      }),
      webDeployment: env.process("web", {
        apiOrigin: "API_ORIGIN",
      }),
      webRequest: env.process("web", {
        requestLabel: "REQUEST_LABEL",
      }),
    },
  });

function forgeBuilderArgument(value: unknown): never {
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- This boundary matrix deliberately bypasses static builder types to verify runtime refusal.
  return value as never;
}

describe("root authoring", () => {
  it("returns an opaque definition while retaining private compiler access", async () => {
    const definition = declaration();
    const state = getEnvironmentCompilerState(definition);
    const compiled = await compileContract(state.contract);
    const web = findProjection(compiled, "web");

    expect(Reflect.ownKeys(definition)).toStrictEqual([]);
    expect(Object.isFrozen(definition)).toBe(true);
    expect(web?.manifest.kind).toBe("public");
    expect(Object.getPrototypeOf(state.targetLifecycles)).toBeNull();
    expect(Object.entries(state.targetLifecycles)).toStrictEqual([
      ["serverDeployment", "deployment"],
      ["serverRequest", "request"],
      ["webBuild", "build"],
      ["webDeployment", "deployment"],
      ["webRequest", "request"],
    ]);
    expect(state.bindingPlans.serverDeployment?.bindings).toStrictEqual([
      {
        channel: "deployment",
        class: "non-confidential",
        entry: "apiOrigin",
        kind: "public_text",
        rawName: "API_ORIGIN",
      },
      {
        channel: "deployment",
        class: "confidential",
        entry: "internalValue",
        kind: "private_text",
        rawName: "INTERNAL_VALUE",
      },
    ]);
  });

  it("keeps every builder product opaque and authentic", () => {
    const entry = env.public.deployment.string();
    const consumer = env.server(["value"]);
    const target = env.process("server", { value: "VALUE" });
    const rule = env.together("related", ["first", "second"]);

    for (const product of [entry, consumer, target, rule]) {
      expect(Reflect.ownKeys(product)).toStrictEqual([]);
      expect(Object.isFrozen(product)).toBe(true);
    }

    // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- Forge an opaque entry to prove runtime authenticity rejects non-builder values.
    const forgedDefinition = {
      consumers: { server: env.server(["value"]) },
      entries: { value: Object.freeze({}) },
      id: "com.example.forged",
      targets: {
        server: env.process("server", { value: "VALUE" }),
      },
    } as never;
    expect(() => defineEnvironment(forgedDefinition)).toThrow(
      /this env instance/u
    );
  });

  it("copies authoring inputs and never invokes accessors", () => {
    const selection = ["value"] as const;
    const bindings = { value: "VALUE" };
    const consumer = env.server(selection);
    const target = env.process("server", bindings);

    Reflect.set(selection, 0, "changed");
    bindings.value = "CHANGED";

    const definition = defineEnvironment({
      consumers: { server: consumer },
      entries: { value: env.public.deployment.string() },
      id: "com.example.owned-inputs",
      targets: { server: target },
    });
    expect(
      getEnvironmentCompilerState(definition).bindingPlans.server?.bindings
    ).toStrictEqual([
      {
        channel: "deployment",
        class: "non-confidential",
        entry: "value",
        kind: "public_text",
        rawName: "VALUE",
      },
    ]);

    let reads = 0;
    const options = Object.defineProperty({}, "required", {
      enumerable: true,
      get: () => {
        reads += 1;
        return true;
      },
    });
    expect(() => env.public.deployment.string(options)).toThrow(
      /observed safely/u
    );
    expect(reads).toBe(0);
  });

  it("captures nested JSON and opaque shapes without ordinary reads", () => {
    let reads = 0;
    const observations: string[] = [];
    const descriptorOnly = <T extends object>(label: string, target: T): T =>
      new Proxy(target, {
        get() {
          reads += 1;
          throw new Error("ordinary shape access is forbidden");
        },
        getOwnPropertyDescriptor(value, key) {
          observations.push(`${label}.descriptor:${String(key)}`);
          return Reflect.getOwnPropertyDescriptor(value, key);
        },
        getPrototypeOf(value) {
          observations.push(`${label}.prototype`);
          return Reflect.getPrototypeOf(value);
        },
        ownKeys(value) {
          observations.push(`${label}.keys`);
          return Reflect.ownKeys(value);
        },
      });

    const jsonShape = descriptorOnly("json", {
      items: descriptorOnly("json.items", { kind: "string" } as const),
      kind: "array" as const,
      maximumItems: 1,
      minimumItems: 0,
    });
    const opaqueInput = descriptorOnly("opaque.input", {
      kind: "string" as const,
    });
    const opaqueOutput = descriptorOnly("opaque.output", {
      kind: "optional" as const,
      value: descriptorOnly("opaque.output.value", {
        kind: "string" as const,
      }),
    });

    expect(() => env.public.deployment.json(jsonShape)).not.toThrow();
    expect(() =>
      env.private.deployment.opaque({
        input: opaqueInput,
        output: opaqueOutput,
        revision: "1",
        semantics: "com.example.authoring-proxy@1",
      })
    ).not.toThrow();
    expect(reads).toBe(0);
    expect(observations).toStrictEqual([
      "json.prototype",
      "json.keys",
      "json.descriptor:items",
      "json.descriptor:kind",
      "json.descriptor:maximumItems",
      "json.descriptor:minimumItems",
      "json.items.prototype",
      "json.items.keys",
      "json.items.descriptor:kind",
      "opaque.input.prototype",
      "opaque.input.keys",
      "opaque.input.descriptor:kind",
      "opaque.input.prototype",
      "opaque.input.keys",
      "opaque.input.descriptor:kind",
      "opaque.output.prototype",
      "opaque.output.keys",
      "opaque.output.descriptor:kind",
      "opaque.output.descriptor:value",
      "opaque.output.value.prototype",
      "opaque.output.value.keys",
      "opaque.output.value.descriptor:kind",
    ]);
  });

  it("rejects malformed erased builder values through the contract boundary", () => {
    const invalid = [
      [
        "boolean blank",
        () => env.public.deployment.boolean(forgeBuilderArgument({ blank: 0 })),
      ],
      [
        "boolean false input",
        () =>
          env.public.deployment.boolean(
            forgeBuilderArgument({ falseInput: 0 })
          ),
      ],
      [
        "boolean true input",
        () =>
          env.public.deployment.boolean(forgeBuilderArgument({ trueInput: 0 })),
      ],
      [
        "integer blank",
        () =>
          env.public.deployment.integer(
            forgeBuilderArgument({
              blank: 0,
              maximum: 1,
              minimum: 0,
            })
          ),
      ],
      [
        "integer maximum",
        () =>
          env.public.deployment.integer(
            forgeBuilderArgument({
              maximum: "1",
              minimum: 0,
            })
          ),
      ],
      [
        "integer minimum",
        () =>
          env.public.deployment.integer(
            forgeBuilderArgument({
              maximum: 1,
              minimum: "0",
            })
          ),
      ],
      [
        "JSON blank",
        () =>
          env.public.deployment.json(
            { kind: "string" },
            forgeBuilderArgument({ blank: 0 })
          ),
      ],
      [
        "safe-integer blank",
        () =>
          env.public.deployment.safeInteger(
            forgeBuilderArgument({
              blank: 0,
              maximum: 1,
              minimum: 0,
            })
          ),
      ],
      [
        "safe-integer maximum",
        () =>
          env.public.deployment.safeInteger(
            forgeBuilderArgument({
              maximum: "1",
              minimum: 0,
            })
          ),
      ],
      [
        "safe-integer minimum",
        () =>
          env.public.deployment.safeInteger(
            forgeBuilderArgument({
              maximum: 1,
              minimum: "0",
            })
          ),
      ],
      [
        "string maximum",
        () =>
          env.public.deployment.string(
            forgeBuilderArgument({
              maximumCodePoints: "1",
            })
          ),
      ],
      [
        "string minimum",
        () =>
          env.public.deployment.string(
            forgeBuilderArgument({
              minimumCodePoints: "0",
            })
          ),
      ],
      [
        "string-list empty items",
        () =>
          env.public.deployment.stringList(
            forgeBuilderArgument({
              emptyItems: 0,
            })
          ),
      ],
      [
        "string-list maximum item code points",
        () =>
          env.public.deployment.stringList(
            forgeBuilderArgument({
              maximumItemCodePoints: "1",
            })
          ),
      ],
      [
        "string-list maximum items",
        () =>
          env.public.deployment.stringList(
            forgeBuilderArgument({
              maximumItems: "1",
            })
          ),
      ],
      [
        "string-list minimum item code points",
        () =>
          env.public.deployment.stringList(
            forgeBuilderArgument({
              minimumItemCodePoints: "1",
            })
          ),
      ],
      [
        "string-list minimum items",
        () =>
          env.public.deployment.stringList(
            forgeBuilderArgument({
              minimumItems: "0",
            })
          ),
      ],
      [
        "text blank",
        () => env.public.deployment.text(forgeBuilderArgument({ blank: 0 })),
      ],
      [
        "text maximum",
        () =>
          env.public.deployment.text(
            forgeBuilderArgument({
              maximumCodePoints: "1",
            })
          ),
      ],
      [
        "text minimum",
        () =>
          env.public.deployment.text(
            forgeBuilderArgument({
              minimumCodePoints: "0",
            })
          ),
      ],
      [
        "text normalisation",
        () =>
          env.public.deployment.text(forgeBuilderArgument({ normalise: 0 })),
      ],
      [
        "opaque revision",
        () =>
          env.private.deployment.opaque(
            forgeBuilderArgument({
              input: { kind: "string" },
              output: { kind: "string" },
              revision: 0,
              semantics: "example/configuration@1",
            })
          ),
      ],
      [
        "opaque semantics",
        () =>
          env.private.deployment.opaque(
            forgeBuilderArgument({
              input: { kind: "string" },
              output: { kind: "string" },
              revision: "1",
              semantics: 0,
            })
          ),
      ],
      [
        "secret blank",
        () => env.private.deployment.secret(forgeBuilderArgument({ blank: 0 })),
      ],
      [
        "secret maximum",
        () =>
          env.private.deployment.secret(
            forgeBuilderArgument({
              maximumCodePoints: "1",
            })
          ),
      ],
      [
        "secret minimum",
        () =>
          env.private.deployment.secret(
            forgeBuilderArgument({
              minimumCodePoints: "0",
            })
          ),
      ],
      [
        "safe-integer shape",
        () =>
          env.public.deployment.json(
            forgeBuilderArgument({
              kind: "safe-integer",
              maximum: "1",
              minimum: 0,
            })
          ),
      ],
      [
        "array shape",
        () =>
          env.public.deployment.json(
            forgeBuilderArgument({
              items: { kind: "string" },
              kind: "array",
              maximumItems: "1",
              minimumItems: 0,
            })
          ),
      ],
      [
        "object shape",
        () =>
          env.public.deployment.json(
            forgeBuilderArgument({
              kind: "object",
              properties: [
                {
                  name: 0,
                  required: true,
                  shape: { kind: "string" },
                },
              ],
            })
          ),
      ],
    ] as const;

    for (const [, invoke] of invalid) {
      expect(invoke).toThrow("Astilba Env contract definition is invalid.");
    }
  });

  it("serialises integer defaults as null and exposes secret only privately", () => {
    const definition = defineEnvironment({
      consumers: {
        server: env.server(["port"]),
      },
      entries: {
        port: env.public.deployment.integer({
          maximum: 65_535,
          minimum: 0,
        }),
      },
      id: "com.example.integer",
      targets: {
        server: env.process("server", { port: "PORT" }),
      },
    });
    const state = getEnvironmentCompilerState(definition);

    expect(state.contract.entries[0]?.codec).toMatchObject({
      default: null,
      kind: "integer",
    });
    expect("secret" in env.public.deployment).toBe(false);
    expect("secret" in env.private.deployment).toBe(true);
  });

  it("rejects duplicate consumer selections and server-only browser entries", () => {
    expect(() => env.browser(["value", "value"])).toThrow(/duplicate/u);

    expect(() =>
      defineEnvironment({
        consumers: {
          web: env.browser(["publicText"]),
        },
        entries: {
          publicText: env.public.deployment.text(),
        },
        id: "com.example.invalid-browser",
        targets: {
          web: env.process("web", {
            publicText: "PUBLIC_TEXT",
          }),
        },
      })
    ).toThrow(/browser-portable/u);
  });

  it("requires one complete lifecycle per process target", () => {
    expect(() =>
      defineEnvironment({
        consumers: {
          server: env.server(),
        },
        entries: {
          first: env.public.deployment.string(),
          second: env.public.deployment.string(),
          third: env.public.request.string(),
        },
        id: "com.example.incomplete-target",
        targets: {
          deployment: env.process("server", {
            first: "FIRST",
          }),
          request: env.process("server", {
            third: "THIRD",
          }),
        },
      })
    ).toThrow(/one lifecycle exactly once/u);

    expect(() =>
      defineEnvironment({
        consumers: {
          server: env.server(),
        },
        entries: {
          deploymentValue: env.public.deployment.string(),
          requestValue: env.public.request.string(),
        },
        id: "com.example.mixed-target",
        targets: {
          mixed: env.process("server", {
            deploymentValue: "DEPLOYMENT_VALUE",
            requestValue: "REQUEST_VALUE",
          }),
        },
      })
    ).toThrow(/exactly one complete lifecycle/u);
  });

  it("requires exactly one build target for browser build entries", () => {
    expect(() =>
      defineEnvironment({
        consumers: {
          web: env.browser(),
        },
        entries: {
          buildValue: env.public.build.string(),
          deploymentValue: env.public.deployment.string(),
        },
        id: "com.example.missing-build-target",
        targets: {
          deployment: env.process("web", {
            deploymentValue: "DEPLOYMENT_VALUE",
          }),
        },
      })
    ).toThrow(/exactly one build target/u);

    expect(() =>
      defineEnvironment({
        consumers: {
          web: env.browser(),
        },
        entries: {
          buildValue: env.public.build.string(),
        },
        id: "com.example.duplicate-build-target",
        targets: {
          first: env.process("web", {
            buildValue: "BUILD_VALUE",
          }),
          second: env.process("web", {
            buildValue: "SECOND_BUILD_VALUE",
          }),
        },
      })
    ).toThrow(/exactly one build target/u);
  });

  it("fails browser envelopes with unavoidable key, depth, or byte overflow", () => {
    const requiredEntries = Object.fromEntries(
      Array.from({ length: 249 }, (_, index) => [
        `value${index}`,
        env.public.deployment.boolean(),
      ])
    );
    const requiredNames = Object.keys(requiredEntries);
    const requiredBindings = Object.fromEntries(
      requiredNames.map((name, index) => [name, `VALUE_${index}`])
    );

    expect(() =>
      defineEnvironment({
        consumers: {
          web: env.browser(requiredNames),
        },
        entries: requiredEntries,
        id: "com.example.required-key-overflow",
        targets: {
          web: env.process("web", requiredBindings),
        },
      })
    ).toThrow(/minimum bootstrap envelope/u);

    const deeplyNested = {
      items: {
        items: {
          items: {
            items: {
              items: {
                items: {
                  items: { kind: "string" },
                  kind: "array",
                  maximumItems: 1,
                  minimumItems: 0,
                },
                kind: "array",
                maximumItems: 1,
                minimumItems: 0,
              },
              kind: "array",
              maximumItems: 1,
              minimumItems: 0,
            },
            kind: "array",
            maximumItems: 1,
            minimumItems: 0,
          },
          kind: "array",
          maximumItems: 1,
          minimumItems: 0,
        },
        kind: "array",
        maximumItems: 1,
        minimumItems: 0,
      },
      kind: "array",
      maximumItems: 1,
      minimumItems: 0,
    } as const;

    expect(() =>
      defineEnvironment({
        consumers: {
          web: env.browser(["deep"]),
        },
        entries: {
          deep: env.public.deployment.json(deeplyNested),
        },
        id: "com.example.required-depth-overflow",
        targets: {
          web: env.process("web", { deep: "DEEP" }),
        },
      })
    ).toThrow(/minimum bootstrap envelope/u);

    expect(() =>
      defineEnvironment({
        consumers: {
          web: env.browser(["large"]),
        },
        entries: {
          large: env.public.deployment.string({
            minimumCodePoints: 65_535,
          }),
        },
        id: "com.example.required-byte-overflow",
        targets: {
          web: env.process("web", { large: "LARGE" }),
        },
      })
    ).toThrow(/minimum bootstrap envelope/u);
  });

  it("omits optional entries from the browser feasibility witness", () => {
    const optionalEntries = Object.fromEntries(
      Array.from({ length: 249 }, (_, index) => [
        `value${index}`,
        env.public.deployment.boolean({ required: false }),
      ])
    );
    const names = Object.keys(optionalEntries);
    const bindings = Object.fromEntries(
      names.map((name, index) => [name, `VALUE_${index}`])
    );

    expect(() =>
      defineEnvironment({
        consumers: {
          web: env.browser(names),
        },
        entries: optionalEntries,
        id: "com.example.optional-key-control",
        targets: {
          web: env.process("web", bindings),
        },
      })
    ).not.toThrow();

    expect(() =>
      defineEnvironment({
        consumers: {
          web: env.browser(["deep", "large"]),
        },
        entries: {
          deep: env.public.deployment.json(
            {
              items: {
                items: {
                  items: {
                    items: {
                      items: {
                        items: {
                          items: { kind: "string" },
                          kind: "array",
                          maximumItems: 1,
                          minimumItems: 0,
                        },
                        kind: "array",
                        maximumItems: 1,
                        minimumItems: 0,
                      },
                      kind: "array",
                      maximumItems: 1,
                      minimumItems: 0,
                    },
                    kind: "array",
                    maximumItems: 1,
                    minimumItems: 0,
                  },
                  kind: "array",
                  maximumItems: 1,
                  minimumItems: 0,
                },
                kind: "array",
                maximumItems: 1,
                minimumItems: 0,
              },
              kind: "array",
              maximumItems: 1,
              minimumItems: 0,
            },
            { required: false }
          ),
          large: env.public.deployment.string({
            minimumCodePoints: 65_535,
            required: false,
          }),
        },
        id: "com.example.optional-controls",
        targets: {
          web: env.process("web", {
            deep: "DEEP",
            large: "LARGE",
          }),
        },
      })
    ).not.toThrow();
  });
});
