import { describe, expect, it } from "vitest";

import {
  compileContract,
  ContractDefinitionError,
  enumCodec,
  findProjection,
  opaqueCodec,
  originCodec,
  stringCodec,
} from "./index.ts";
import type { ContractDefinition, EntryDefinition } from "./index.ts";

function fixtureDefinition(): ContractDefinition {
  const entries: EntryDefinition[] = [
    {
      codec: stringCodec({ minCodePoints: 1, maxCodePoints: 64 }),
      fragment: "com.example.platform",
      id: "releaseSha",
      lifecycle: "build",
      required: true,
      visibility: "public",
    },
    {
      codec: originCodec(),
      fragment: "com.example.shared",
      id: "apiOrigin",
      lifecycle: "deployment",
      required: true,
      visibility: "public",
    },
    {
      codec: enumCodec(["debug", "info", "warn", "error"]),
      fragment: "com.example.shared",
      id: "logLevel",
      lifecycle: "deployment",
      required: true,
      visibility: "private",
    },
    {
      codec: stringCodec({ minCodePoints: 16, maxCodePoints: 128 }),
      fragment: "com.example.platform",
      id: "internalApiToken",
      lifecycle: "deployment",
      required: true,
      visibility: "private",
    },
  ];

  return {
    consumers: [
      {
        entries: [
          ["com.example.platform", "releaseSha"],
          ["com.example.shared", "apiOrigin"],
        ],
        id: "web",
        kind: "browser",
      },
      {
        entries: [
          ["com.example.platform", "releaseSha"],
          ["com.example.shared", "logLevel"],
          ["com.example.platform", "internalApiToken"],
        ],
        id: "service",
        kind: "server",
      },
    ],
    entries,
    id: "com.example.contract",
  };
}

describe("contract compilation", () => {
  it("rejects symbol keys before observing property descriptors", async () => {
    let descriptorReads = 0;
    const symbolKey = Symbol("unsupported");
    const definition = new Proxy(
      {},
      {
        getOwnPropertyDescriptor: () => {
          descriptorReads += 1;
          return {
            configurable: true,
            enumerable: true,
            value: "com.example.contract",
            writable: true,
          };
        },
        getPrototypeOf: () => Object.prototype,
        ownKeys: () => ["id", symbolKey],
      }
    );

    await expect(compileContract(definition)).rejects.toThrow(
      ContractDefinitionError
    );
    expect(descriptorReads).toBe(0);
  });

  it("is independent of declaration, selection, and enum order", async () => {
    const first = fixtureDefinition();
    const second = fixtureDefinition();
    const secondEntries = [...second.entries].toReversed().map((entry) =>
      entry.id === "logLevel"
        ? {
            ...entry,
            codec: enumCodec(["warn", "info", "error", "debug"]),
          }
        : entry
    );
    const secondConsumers = [...second.consumers]
      .toReversed()
      .map((consumer) => ({
        ...consumer,
        entries: [...consumer.entries].toReversed(),
      }));

    const left = await compileContract(first);
    const right = await compileContract({
      ...second,
      consumers: secondConsumers,
      entries: secondEntries,
    });

    expect(right.full.bytes).toStrictEqual(left.full.bytes);
    expect(right.full.digest).toBe(left.full.digest);
    expect(
      right.projections.map((projection) => projection.digest)
    ).toStrictEqual(left.projections.map((projection) => projection.digest));
  });

  it("keeps private metadata out of the browser projection", async () => {
    const compiled = await compileContract(fixtureDefinition());
    const web = findProjection(compiled, "web");
    const service = findProjection(compiled, "service");

    expect(web?.manifest.kind).toBe("public");
    expect(web?.text).not.toContain("internalApiToken");
    expect(web?.text).not.toContain("logLevel");
    expect(web?.text).not.toContain("astilba.env.opaque");
    expect(web?.text).not.toContain(compiled.full.digest);
    expect(service?.text).toContain("internalApiToken");
    expect(Object.isFrozen(web?.manifest)).toBe(true);
  });

  it("leaves an unselecting projection unchanged", async () => {
    const baseline = await compileContract(fixtureDefinition());
    const extended = fixtureDefinition();
    const withUnused = await compileContract({
      ...extended,
      entries: [
        ...extended.entries,
        {
          codec: stringCodec({ minCodePoints: 0, maxCodePoints: 10 }),
          fragment: "com.example.unused",
          id: "unused",
          lifecycle: "deployment",
          required: false,
          visibility: "private",
        },
      ],
    });

    expect(findProjection(withUnused, "web")?.digest).toBe(
      findProjection(baseline, "web")?.digest
    );
    expect(withUnused.full.digest).not.toBe(baseline.full.digest);
  });

  it("rejects private build entries and private browser selections", async () => {
    const baseline = fixtureDefinition();
    const privateBuild: EntryDefinition = {
      codec: stringCodec({ minCodePoints: 1, maxCodePoints: 10 }),
      fragment: "com.example.private",
      id: "privateBuild",
      lifecycle: "build",
      required: true,
      visibility: "private",
    };
    await expect(
      compileContract({
        ...baseline,
        entries: [...baseline.entries, privateBuild],
      })
    ).rejects.toThrow(ContractDefinitionError);

    const web = baseline.consumers.find((consumer) => consumer.id === "web");
    if (web === undefined) {
      throw new Error("Fixture web consumer is missing.");
    }
    await expect(
      compileContract({
        ...baseline,
        consumers: [
          {
            ...web,
            entries: [
              ...web.entries,
              ["com.example.platform", "internalApiToken"],
            ],
          },
        ],
      })
    ).rejects.toThrow(ContractDefinitionError);
  });

  it("rejects case-fold collisions in identities and output names", async () => {
    const baseline = fixtureDefinition();
    const collidingIdentity: EntryDefinition = {
      codec: stringCodec({ minCodePoints: 1, maxCodePoints: 64 }),
      fragment: "com.example.platform",
      id: "releasesha",
      lifecycle: "build",
      required: true,
      visibility: "public",
    };
    await expect(
      compileContract({
        ...baseline,
        entries: [...baseline.entries, collidingIdentity],
      })
    ).rejects.toThrow(ContractDefinitionError);

    const outputCollision: EntryDefinition = {
      codec: stringCodec({ minCodePoints: 1, maxCodePoints: 64 }),
      fragment: "com.example.other",
      id: "otherRelease",
      lifecycle: "deployment",
      output: "releasesha",
      required: true,
      visibility: "public",
    };
    const web = baseline.consumers[0];
    if (web === undefined) {
      throw new Error("Fixture consumer is missing.");
    }
    await expect(
      compileContract({
        ...baseline,
        consumers: [
          {
            ...web,
            entries: [...web.entries, ["com.example.other", "otherRelease"]],
          },
        ],
        entries: [...baseline.entries, outputCollision],
      })
    ).rejects.toThrow(ContractDefinitionError);
  });

  it("rejects a duplicate identity inside one consumer selection", async () => {
    const baseline = fixtureDefinition();
    const web = baseline.consumers[0];
    const selected = web?.entries[0];
    if (web === undefined || selected === undefined) {
      throw new Error("Fixture consumer selection is missing.");
    }

    await expect(
      compileContract({
        ...baseline,
        consumers: [
          {
            ...web,
            entries: [...web.entries, selected],
          },
        ],
      })
    ).rejects.toThrow(ContractDefinitionError);
  });

  it("records an opaque descriptor only in a server projection", async () => {
    const baseline = fixtureDefinition();
    const opaque: EntryDefinition = {
      codec: opaqueCodec({
        input: { kind: "string" },
        output: { kind: "string" },
        revision: "1",
        semantics: "acme/private-code@1",
      }),
      fragment: "com.example.opaque",
      id: "privateCode",
      lifecycle: "deployment",
      required: true,
      visibility: "private",
    };
    const service = baseline.consumers.find(
      (consumer) => consumer.id === "service"
    );
    if (service === undefined) {
      throw new Error("Fixture service consumer is missing.");
    }
    const compiled = await compileContract({
      ...baseline,
      consumers: [
        ...baseline.consumers.filter((consumer) => consumer.id !== "service"),
        {
          ...service,
          entries: [...service.entries, ["com.example.opaque", "privateCode"]],
        },
      ],
      entries: [...baseline.entries, opaque],
    });

    expect(findProjection(compiled, "service")?.text).toContain(
      "acme/private-code@1"
    );
    expect(findProjection(compiled, "web")?.text).not.toContain("privateCode");
  });
});
