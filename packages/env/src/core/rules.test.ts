import { describe, expect, it } from "vitest";

import {
  compileContract,
  ContractDefinitionError,
  findProjection,
  integerCodec,
  presentTogetherRule,
  resolveLifecycleAll,
  textCodec,
} from "./index.ts";
import type { ContractDefinition } from "./index.ts";

const fragment = "com.example.server";

const definition = (): ContractDefinition => ({
  consumers: [
    {
      entries: [
        [fragment, "authSecret"],
        [fragment, "databaseUrl"],
        [fragment, "port"],
        [fragment, "smtpPass"],
        [fragment, "smtpUser"],
      ],
      id: "server",
      kind: "server",
    },
    {
      entries: [[fragment, "databaseUrl"]],
      id: "database",
      kind: "server",
    },
  ],
  entries: [
    {
      codec: textCodec({
        blank: "missing",
        maxCodePoints: 1024,
        minCodePoints: 1,
        normalise: "preserve",
      }),
      fragment,
      id: "authSecret",
      lifecycle: "deployment",
      required: true,
      visibility: "private",
    },
    {
      codec: textCodec({
        blank: "missing",
        maxCodePoints: 4096,
        minCodePoints: 1,
        normalise: "trim",
      }),
      fragment,
      id: "databaseUrl",
      lifecycle: "deployment",
      required: true,
      visibility: "private",
    },
    {
      codec: integerCodec({
        blank: "missing",
        default: null,
        maximum: 65_535,
        minimum: 0,
      }),
      fragment,
      id: "port",
      lifecycle: "deployment",
      required: true,
      visibility: "public",
    },
    {
      codec: textCodec({
        blank: "missing",
        maxCodePoints: 1024,
        minCodePoints: 1,
        normalise: "preserve",
      }),
      fragment,
      id: "smtpPass",
      lifecycle: "deployment",
      required: false,
      visibility: "private",
    },
    {
      codec: textCodec({
        blank: "missing",
        maxCodePoints: 1024,
        minCodePoints: 1,
        normalise: "trim",
      }),
      fragment,
      id: "smtpUser",
      lifecycle: "deployment",
      required: false,
      visibility: "private",
    },
  ],
  id: "com.example.environment",
  rules: [
    presentTogetherRule("smtpCredentials", [
      [fragment, "smtpUser"],
      [fragment, "smtpPass"],
    ]),
  ],
});

describe("first-party contract rules", () => {
  it("puts a selected rule in version 2 while leaving an unselected projection at version 1", async () => {
    const compiled = await compileContract(definition());
    const withoutRulesDefinition = definition();
    delete (withoutRulesDefinition as { rules?: unknown }).rules;
    const withoutRules = await compileContract(withoutRulesDefinition);
    const server = findProjection(compiled, "server");
    const database = findProjection(compiled, "database");
    const databaseWithoutRules = findProjection(withoutRules, "database");

    expect(compiled.full.manifest.formatVersion).toBe(2);
    expect(server?.manifest.formatVersion).toBe(2);
    expect(database?.manifest.formatVersion).toBe(1);
    expect(database?.text).not.toContain("present-together");
    expect(database?.text).toBe(databaseWithoutRules?.text);
    expect(database?.digest).toBe(databaseWithoutRules?.digest);
  });

  it("keeps absent and empty rules byte-identical", async () => {
    const absent = definition();
    delete (absent as { rules?: unknown }).rules;
    const empty = { ...absent, rules: [] };
    const [compiledAbsent, compiledEmpty] = await Promise.all([
      compileContract(absent),
      compileContract(empty),
    ]);

    expect(compiledEmpty.full.text).toBe(compiledAbsent.full.text);
    expect(compiledEmpty.full.digest).toBe(compiledAbsent.full.digest);
    expect(compiledEmpty.projections.map((item) => item.text)).toStrictEqual(
      compiledAbsent.projections.map((item) => item.text)
    );
  });

  it("canonicalises rule and declaration order", async () => {
    const ordinary = definition();
    const ordinaryRules = ordinary.rules;
    if (ordinaryRules === undefined) {
      throw new TypeError("test rules are missing");
    }
    const reversed = {
      ...ordinary,
      consumers: ordinary.consumers.map((consumer) => ({
        ...consumer,
        entries: [...consumer.entries].toReversed(),
      })),
      entries: [...ordinary.entries].toReversed(),
      rules: ordinaryRules.map((rule) => ({
        ...rule,
        entries: [...rule.entries].toReversed(),
      })),
    };
    const [compiledOrdinary, compiledReversed] = await Promise.all([
      compileContract(ordinary),
      compileContract(reversed),
    ]);

    expect(compiledReversed.full.text).toBe(compiledOrdinary.full.text);
    expect(compiledReversed.full.digest).toBe(compiledOrdinary.full.digest);
    expect(compiledReversed.projections.map((item) => item.text)).toStrictEqual(
      compiledOrdinary.projections.map((item) => item.text)
    );
  });

  it("reports aggregate entry failures and a safe co-presence failure", async () => {
    const compiled = await compileContract(definition());
    const server = findProjection(compiled, "server");
    if (server === undefined) {
      throw new Error("server projection missing");
    }
    const bindings = [
      { entry: "authSecret", source: "BETTER_AUTH_SECRET" },
      { entry: "databaseUrl", source: "DATABASE_URL" },
      { entry: "port", source: "PORT" },
      { entry: "smtpPass", source: "SMTP_PASS" },
      { entry: "smtpUser", source: "SMTP_USER" },
    ] as const;

    const invalid = resolveLifecycleAll(
      server.manifest,
      "deployment",
      bindings,
      {
        PORT: "invalid",
      }
    );
    expect(invalid).toStrictEqual({
      diagnostics: [
        {
          code: "ENV_MISSING_VALUE",
          codec: "astilba.env.text/v1",
          consumer: "server",
          entry: "authSecret",
          lifecycle: "deployment",
        },
        {
          code: "ENV_MISSING_VALUE",
          codec: "astilba.env.text/v1",
          consumer: "server",
          entry: "databaseUrl",
          lifecycle: "deployment",
        },
        {
          code: "ENV_INVALID_VALUE",
          codec: "astilba.env.integer/v1",
          consumer: "server",
          entry: "port",
          lifecycle: "deployment",
        },
      ],
      ok: false,
    });

    const unpaired = resolveLifecycleAll(
      server.manifest,
      "deployment",
      bindings,
      {
        BETTER_AUTH_SECRET: "  raw secret  ",
        DATABASE_URL: "  postgres://local  ",
        PORT: "3000",
        SMTP_USER: " api ",
      }
    );
    expect(unpaired).toStrictEqual({
      diagnostics: [
        {
          code: "ENV_RULE_VIOLATION",
          consumer: "server",
          entries: ["smtpPass", "smtpUser"],
          lifecycle: "deployment",
          rule: "smtpCredentials",
        },
      ],
      ok: false,
    });
  });

  it("rejects a consumer that selects only part of a rule", async () => {
    const base = definition();
    await expect(
      compileContract({
        ...base,
        consumers: [
          {
            entries: [[fragment, "smtpUser"]],
            id: "partial",
            kind: "server",
          },
        ],
      })
    ).rejects.toThrow(ContractDefinitionError);
  });

  it("rejects a non-null integer descriptor default", async () => {
    const base = definition();
    const privateDefault = {
      codec: {
        abi: "astilba.env.integer/v1",
        blank: "missing",
        default: 1,
        kind: "integer",
        maximum: 10,
        minimum: 0,
      },
      fragment,
      id: "privateDefault",
      lifecycle: "deployment" as const,
      required: true,
      visibility: "private" as const,
    };
    await expect(
      compileContract({
        ...base,
        consumers: [
          {
            entries: [[fragment, "privateDefault"]],
            id: "private",
            kind: "server",
          },
        ],
        // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- This invalid private default entry is deliberately injected to prove contract refusal.
        entries: [...base.entries, privateDefault] as never,
        rules: [],
      })
    ).rejects.toThrow(ContractDefinitionError);
  });
});
