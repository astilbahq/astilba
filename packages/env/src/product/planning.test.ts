import { describe, expect, it } from "vitest";

import { defineEnvironment, env } from "../authoring/index.ts";
import type { EnvironmentDefinition } from "../authoring/index.ts";
import { planImpact } from "../planning/index.ts";
import type { ImpactPlan } from "../planning/index.ts";
import { compileProduct } from "./index.ts";

type Variant = Readonly<{
  authVisibility?: "private" | "public";
  databaseBinding?: string;
  includeSeedExtra?: boolean;
  includeUnused?: boolean;
  portMaximum?: number;
  seedModeRequired?: boolean;
}>;

const declaration = (variant: Variant = {}): EnvironmentDefinition => {
  const authSecret =
    variant.authVisibility === "public"
      ? env.public.deployment.text()
      : env.private.deployment.secret();
  const entries = {
    authSecret,
    databaseUrl: env.private.deployment.text({ normalise: "trim" }),
    port: env.public.deployment.integer({
      maximum: variant.portMaximum ?? 65_535,
      minimum: 0,
    }),
    seedMode: env.private.deployment.text({
      normalise: "trim",
      required: variant.seedModeRequired ?? false,
    }),
    ...(variant.includeSeedExtra === true
      ? {
          seedExtra: env.private.deployment.text({
            normalise: "trim",
            required: false,
          }),
        }
      : {}),
    ...(variant.includeUnused === true
      ? {
          unused: env.private.deployment.text({
            required: false,
          }),
        }
      : {}),
  };
  const seedEntries: readonly (
    | "authSecret"
    | "databaseUrl"
    | "seedExtra"
    | "seedMode"
  )[] = [
    "authSecret",
    "databaseUrl",
    "seedMode",
    ...(variant.includeSeedExtra === true ? (["seedExtra"] as const) : []),
  ];
  const definition = {
    consumers: {
      database: env.server(["databaseUrl"]),
      seed: env.server(seedEntries),
      server: env.server(["authSecret", "databaseUrl", "port"]),
    },
    entries,
    id: "com.astilba.planning",
    targets: {
      database: env.process("database", {
        databaseUrl: variant.databaseBinding ?? "DATABASE_URL",
      }),
      seed: env.process("seed", {
        authSecret: "BETTER_AUTH_SECRET",
        databaseUrl: "DATABASE_URL",
        seedMode: "SEED_MODE",
        ...(variant.includeSeedExtra === true
          ? { seedExtra: "SEED_EXTRA" }
          : {}),
      }),
      server: env.process("server", {
        authSecret: "BETTER_AUTH_SECRET",
        databaseUrl: "DATABASE_URL",
        port: "PORT",
      }),
    },
  };
  return defineEnvironment(definition);
};

const plan = async (
  before: EnvironmentDefinition,
  after: EnvironmentDefinition
): Promise<ImpactPlan> =>
  planImpact({
    after: (await compileProduct(after)).snapshot,
    before: (await compileProduct(before)).snapshot,
  });

const actions = (impact: ImpactPlan) =>
  impact.actions.map(({ consumer, kind, reasons, target }) => ({
    consumer,
    kind,
    reasons,
    target,
  }));

type RuleEntry = "smtpHost" | "smtpPass" | "smtpUser";

const ruleDeclaration = (
  ruleEntries: readonly RuleEntry[] | undefined
): EnvironmentDefinition =>
  defineEnvironment({
    consumers: {
      server: env.server(["smtpHost", "smtpPass", "smtpUser"]),
    },
    entries: {
      smtpHost: env.public.deployment.text({ required: false }),
      smtpPass: env.private.deployment.secret({ required: false }),
      smtpUser: env.private.deployment.text({ required: false }),
    },
    id: "com.astilba.rule-planning",
    rules:
      ruleEntries === undefined ? [] : [env.together("smtpRule", ruleEntries)],
    targets: {
      server: env.process("server", {
        smtpHost: "SMTP_HOST",
        smtpPass: "SMTP_PASS",
        smtpUser: "SMTP_USER",
      }),
    },
  });

describe("exact planning", () => {
  it("isolates a server-only codec change", async () => {
    expect(
      actions(await plan(declaration(), declaration({ portMaximum: 60_000 })))
    ).toStrictEqual([
      {
        consumer: "server",
        kind: "REBUILD_APPLICATION",
        reasons: ["CODEC_CHANGED", "PROJECTION_DIGEST_CHANGED"],
        target: "*",
      },
      {
        consumer: "server",
        kind: "REVALIDATE",
        reasons: ["CODEC_NARROWED", "PROJECTION_DIGEST_CHANGED"],
        target: "server",
      },
      {
        consumer: "server",
        kind: "ACTIVATE_ARTIFACT",
        reasons: ["UPDATED_ARTIFACT_READY"],
        target: "server",
      },
    ]);
  });

  it("isolates a database binding rename", async () => {
    expect(
      actions(
        await plan(
          declaration(),
          declaration({ databaseBinding: "DATABASE_CONNECTION" })
        )
      )
    ).toStrictEqual([
      {
        consumer: "database",
        kind: "RECONFIGURE",
        reasons: ["BINDING_SOURCE_CHANGED"],
        target: "database",
      },
      {
        consumer: "database",
        kind: "REBUILD_ADAPTER",
        reasons: ["BINDING_SOURCE_CHANGED"],
        target: "database",
      },
      {
        consumer: "database",
        kind: "ACTIVATE_ARTIFACT",
        reasons: ["UPDATED_ARTIFACT_READY"],
        target: "database",
      },
    ]);
  });

  it("isolates a seed-only entry addition", async () => {
    const impact = await plan(
      declaration(),
      declaration({ includeSeedExtra: true })
    );
    expect(
      new Set(impact.actions.map((action) => action.consumer))
    ).toStrictEqual(new Set(["seed"]));
    expect(
      actions(impact).map(({ kind, reasons, target }) => ({
        kind,
        reasons,
        target,
      }))
    ).toStrictEqual([
      {
        kind: "ADD_CONFIGURATION",
        reasons: ["TARGET_BINDING_ADDED"],
        target: "seed",
      },
      {
        kind: "REBUILD_APPLICATION",
        reasons: ["PROJECTION_DIGEST_CHANGED", "SELECTED_OPTIONAL_ENTRY_ADDED"],
        target: "*",
      },
      {
        kind: "REVALIDATE",
        reasons: ["PROJECTION_DIGEST_CHANGED"],
        target: "seed",
      },
      {
        kind: "ACTIVATE_ARTIFACT",
        reasons: ["UPDATED_ARTIFACT_READY"],
        target: "seed",
      },
    ]);
  });

  it("flags a private-to-public change wherever the entry is selected", async () => {
    const impact = await plan(
      declaration(),
      declaration({ authVisibility: "public" })
    );
    expect(
      impact.actions
        .filter((action) => action.kind === "SECURITY_REVIEW")
        .map(({ consumer, reasons, target }) => ({
          consumer,
          reasons,
          target,
        }))
    ).toStrictEqual([
      {
        consumer: "seed",
        reasons: ["CONFIDENTIALITY_DOWNGRADE", "PRIVATE_ENTRY_BECAME_PUBLIC"],
        target: "seed",
      },
      {
        consumer: "server",
        reasons: ["CONFIDENTIALITY_DOWNGRADE", "PRIVATE_ENTRY_BECAME_PUBLIC"],
        target: "server",
      },
    ]);
  });

  it("plans an optional-to-required transition only for seed", async () => {
    const impact = await plan(
      declaration(),
      declaration({ seedModeRequired: true })
    );
    expect(
      new Set(impact.actions.map((action) => action.consumer))
    ).toStrictEqual(new Set(["seed"]));
    expect(
      actions(impact).map(({ kind, reasons, target }) => ({
        kind,
        reasons,
        target,
      }))
    ).toStrictEqual([
      {
        kind: "ADD_CONFIGURATION",
        reasons: ["OPTIONAL_ENTRY_BECAME_REQUIRED"],
        target: "seed",
      },
      {
        kind: "REBUILD_APPLICATION",
        reasons: ["PROJECTION_DIGEST_CHANGED", "REQUIREDNESS_CHANGED"],
        target: "*",
      },
      {
        kind: "REVALIDATE",
        reasons: [
          "OPTIONAL_ENTRY_BECAME_REQUIRED",
          "PROJECTION_DIGEST_CHANGED",
        ],
        target: "seed",
      },
      {
        kind: "ACTIVATE_ARTIFACT",
        reasons: ["UPDATED_ARTIFACT_READY"],
        target: "seed",
      },
    ]);
  });

  it("produces no action for an unselected addition", async () => {
    expect(
      actions(await plan(declaration(), declaration({ includeUnused: true })))
    ).toStrictEqual([]);
  });

  it("makes rule add, change, and removal exact while ignoring entry order", async () => {
    const without = ruleDeclaration(undefined);
    const credentials = ruleDeclaration(["smtpUser", "smtpPass"]);
    const reordered = ruleDeclaration(["smtpPass", "smtpUser"]);
    const relayPair = ruleDeclaration(["smtpHost", "smtpPass"]);
    const expected = [
      {
        consumer: "server",
        kind: "REBUILD_APPLICATION",
        reasons: ["PROJECTION_DIGEST_CHANGED"],
        target: "*",
      },
      {
        consumer: "server",
        kind: "REVALIDATE",
        reasons: ["PROJECTION_DIGEST_CHANGED"],
        target: "server",
      },
      {
        consumer: "server",
        kind: "ACTIVATE_ARTIFACT",
        reasons: ["UPDATED_ARTIFACT_READY"],
        target: "server",
      },
    ];

    expect(actions(await plan(without, credentials))).toStrictEqual(expected);
    expect(actions(await plan(credentials, reordered))).toStrictEqual([]);
    expect(actions(await plan(credentials, relayPair))).toStrictEqual(expected);
    expect(actions(await plan(credentials, without))).toStrictEqual(expected);
  });
});
