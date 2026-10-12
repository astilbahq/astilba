import { describe, expect, it } from "vitest";

import { defineEnvironment, env } from "../index.ts";
import type { EnvironmentDefinition } from "../index.ts";
// @ts-expect-error Structural entry internals are not public exports.
import type { EnvironmentEntry } from "../index.ts";

type RemovedStructuralType = EnvironmentEntry;

const compileTimeContract = (): void => {
  const entries = {
    apiOrigin: env.public.deployment.origin(),
    clientMode: env.public.build.enum(["standard", "compact"]),
    internalValue: env.private.deployment.secret(),
    requestLabel: env.public.request.string({ required: false }),
  };

  const definition = defineEnvironment({
    consumers: {
      server: env.server(),
      web: env.browser(["apiOrigin", "clientMode", "requestLabel"]),
    },
    entries,
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
    },
  });

  const opaque: EnvironmentDefinition = definition;
  // @ts-expect-error EnvironmentDefinition does not expose compiler state.
  void opaque.contract;
  // @ts-expect-error EnvironmentDefinition is intentionally not generic.
  type GenericDefinition = EnvironmentDefinition<typeof entries>;
  const acceptGenericDefinition = (_definition: GenericDefinition): void => {};
  void acceptGenericDefinition;

  defineEnvironment({
    consumers: {
      // @ts-expect-error Consumers cannot select an unknown logical entry.
      server: env.server(["missing"]),
    },
    entries,
    id: "com.example.invalid-selection",
    targets: {
      server: env.process("server", {
        apiOrigin: "API_ORIGIN",
      }),
    },
  });

  defineEnvironment({
    consumers: {
      server: env.server(["apiOrigin"]),
    },
    entries,
    id: "com.example.invalid-binding",
    targets: {
      // @ts-expect-error Process bindings cannot name an unknown entry.
      server: env.process("server", {
        missing: "MISSING",
      }),
    },
  });

  env.public.deployment.integer({
    // @ts-expect-error v0.1 integer builders have no literal default.
    default: 3000,
    maximum: 65_535,
    minimum: 0,
  });

  // @ts-expect-error secret is private-only.
  void env.public.deployment.secret;

  env.private.deployment.opaque({
    // @ts-expect-error Opaque process input is string or optional string only.
    input: { kind: "boolean" },
    output: { kind: "boolean" },
    revision: "1",
    semantics: "example/invalid-input@1",
  });

  env.private.request.opaque({
    input: {
      kind: "optional",
      value: { kind: "string" },
    },
    output: {
      kind: "object",
      properties: [
        {
          name: "region",
          required: true,
          shape: { kind: "string" },
        },
      ],
    },
    revision: "1",
    semantics: "example/configuration@1",
  });

  const acceptRemovedStructuralType = (
    _entry: RemovedStructuralType
  ): void => {};
  void acceptRemovedStructuralType;
};

void compileTimeContract;

describe("authoring type contract", () => {
  it("keeps only the opaque definition type public", () => {
    expect(defineEnvironment).toBeTypeOf("function");
    expect(env.server).toBeTypeOf("function");
  });
});
