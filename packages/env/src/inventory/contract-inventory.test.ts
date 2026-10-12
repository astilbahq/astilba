import { describe, expect, it } from "vitest";

import { defineEnvironment, env } from "../authoring/index.ts";
import { getEnvironmentCompilerState } from "../authoring/internal.ts";
import { compileContract } from "../core/index.ts";
import type { ProductCompilation } from "../product/index.ts";
import {
  checkContractInventory,
  compileContractInventory,
  InventoryFailure,
  parseObservedInventory,
} from "./contract-inventory.ts";

const compilation = async (): Promise<ProductCompilation> => {
  const environment = defineEnvironment({
    consumers: { worker: env.server() },
    entries: {
      apiKey: env.private.deployment.secret(),
      sentryDsn: env.private.deployment.secret({ required: false }),
    },
    id: "com.astilba.inventory-test",
    targets: {
      workerDeployment: env.process("worker", {
        apiKey: "API_KEY",
        sentryDsn: "SENTRY_DSN",
      }),
    },
  });
  const state = getEnvironmentCompilerState(environment);
  const compiled = await compileContract(state.contract);
  const bindingPlan = state.bindingPlans.workerDeployment;
  const target = state.targets.workerDeployment;
  if (bindingPlan === undefined || target === undefined) {
    throw new TypeError("Expected a complete inventory test target.");
  }
  return {
    compiled,
    targets: [
      {
        bindingPlan,
        consumer: target.consumer,
      },
    ],
  };
};

const observed = (entries: readonly Readonly<{ name: string }>[]) => ({
  entries,
  format: "astilba.env.observed-name-inventory/v1",
});

const failureCode = (operation: () => unknown): string => {
  try {
    operation();
  } catch (error) {
    if (error instanceof InventoryFailure) {
      return error.code;
    }
    throw error;
  }
  throw new Error("Expected an inventory failure.");
};

describe("contract inventory", () => {
  it("compiles a deterministic value-free target inventory", async () => {
    const input = await compilation();
    const inventory = compileContractInventory(input, "workerDeployment");

    expect(inventory).toStrictEqual({
      entries: [
        {
          entry: "apiKey",
          lifecycle: "deployment",
          name: "API_KEY",
          required: true,
          visibility: "private",
        },
        {
          entry: "sentryDsn",
          lifecycle: "deployment",
          name: "SENTRY_DSN",
          required: false,
          visibility: "private",
        },
      ],
      format: "astilba.env.contract-inventory/v1",
      target: "workerDeployment",
    });
    expect(JSON.stringify(inventory)).not.toContain("value");
    expect(
      JSON.stringify(compileContractInventory(input, "workerDeployment"))
    ).toBe(JSON.stringify(inventory));
  });

  it("distinguishes required absence from optional absence", async () => {
    const inventory = compileContractInventory(
      await compilation(),
      "workerDeployment"
    );
    const requiredMissing = checkContractInventory(
      inventory,
      observed([{ name: "SENTRY_DSN" }]),
      "open"
    );
    expect(requiredMissing.pass).toBe(false);
    expect(requiredMissing.issues).toContainEqual({
      code: "REQUIRED_MISSING",
      entry: "apiKey",
      name: "API_KEY",
    });

    const optionalMissing = checkContractInventory(
      inventory,
      observed([{ name: "API_KEY" }]),
      "open"
    );
    expect(optionalMissing.pass).toBe(true);
    expect(optionalMissing.issues).toContainEqual({
      code: "OPTIONAL_MISSING",
      entry: "sentryDsn",
      name: "SENTRY_DSN",
    });
  });

  it("makes unexpected names depend on explicit ownership", async () => {
    const inventory = compileContractInventory(
      await compilation(),
      "workerDeployment"
    );
    const input = observed([
      { name: "API_KEY" },
      { name: "EXTRA_KEY" },
      { name: "SENTRY_DSN" },
    ]);

    expect(checkContractInventory(inventory, input, "open").pass).toBe(true);
    expect(checkContractInventory(inventory, input, "closed").pass).toBe(false);
  });

  it("accepts name-only observations without inventing provider evidence", async () => {
    const inventory = compileContractInventory(
      await compilation(),
      "workerDeployment"
    );
    expect(
      checkContractInventory(
        inventory,
        observed([{ name: "API_KEY" }, { name: "SENTRY_DSN" }]),
        "closed"
      ).pass
    ).toBe(true);
  });

  it("refuses duplicate, case-folded, malformed, and future observed inputs", () => {
    expect(
      failureCode(() =>
        parseObservedInventory(
          observed([{ name: "API_KEY" }, { name: "api_key" }])
        )
      )
    ).toBe("ENV_OBSERVED_INVALID");
    expect(
      failureCode(() =>
        parseObservedInventory({
          entries: [{ name: "API_KEY", value: "secret" }],
          format: "astilba.env.observed-name-inventory/v1",
        })
      )
    ).toBe("ENV_OBSERVED_INVALID");
    expect(
      failureCode(() =>
        parseObservedInventory({
          entries: [{ kind: "secret_text", name: "API_KEY" }],
          format: "astilba.env.observed-name-inventory/v1",
        })
      )
    ).toBe("ENV_OBSERVED_INVALID");
    expect(
      failureCode(() =>
        parseObservedInventory({
          entries: [],
          format: "astilba.env.observed-name-inventory/v2",
          futureField: true,
        })
      )
    ).toBe("ENV_OBSERVED_UNSUPPORTED");
    const sparse: unknown[] = [];
    sparse.length = 1;
    expect(
      failureCode(() =>
        parseObservedInventory({
          entries: sparse,
          format: "astilba.env.observed-name-inventory/v1",
        })
      )
    ).toBe("ENV_OBSERVED_INVALID");
    let formatReads = 0;
    const accessor: Record<string, unknown> = {};
    Object.setPrototypeOf(accessor, null);
    Object.defineProperty(accessor, "entries", {
      enumerable: true,
      value: [],
    });
    Object.defineProperty(accessor, "format", {
      enumerable: true,
      get() {
        formatReads += 1;
        return "astilba.env.observed-name-inventory/v2";
      },
    });
    expect(failureCode(() => parseObservedInventory(accessor))).toBe(
      "ENV_OBSERVED_INVALID"
    );
    expect(formatReads).toBe(0);
    expect(failureCode(() => parseObservedInventory(null))).toBe(
      "ENV_OBSERVED_INVALID"
    );
  });

  it("handles empty, maximum-bound, and over-bound observed documents", async () => {
    const inventory = compileContractInventory(
      await compilation(),
      "workerDeployment"
    );
    const empty = checkContractInventory(inventory, observed([]), "open");
    expect(empty.pass).toBe(false);
    expect(empty.issues).toHaveLength(2);

    const maximum = [
      { name: "API_KEY" },
      ...Array.from({ length: 2046 }, (_, index) => ({
        name: `EXTRA_${index.toString().padStart(4, "0")}`,
      })),
      { name: "SENTRY_DSN" },
    ];
    expect(
      checkContractInventory(inventory, observed(maximum), "open").pass
    ).toBe(true);
    expect(
      failureCode(() =>
        parseObservedInventory(observed([...maximum, { name: "TOO_MANY" }]))
      )
    ).toBe("ENV_OBSERVED_INVALID");
  });

  it("refuses an unknown target", async () => {
    const input = await compilation();
    expect(failureCode(() => compileContractInventory(input, "nope"))).toBe(
      "ENV_INVENTORY_TARGET_UNKNOWN"
    );
  });

  it("refuses duplicate expected raw names even in a forged compilation", async () => {
    const input = await compilation();
    const target = input.targets[0];
    if (target === undefined) {
      throw new TypeError("Expected an inventory target.");
    }
    const forged: ProductCompilation = {
      ...input,
      targets: [
        {
          ...target,
          bindingPlan: {
            ...target.bindingPlan,
            bindings: target.bindingPlan.bindings.map((binding) => ({
              ...binding,
              rawName: "DUPLICATE_NAME",
            })),
          },
        },
      ],
    };

    expect(
      failureCode(() => compileContractInventory(forged, "workerDeployment"))
    ).toBe("ENV_INVENTORY_INVALID");
  });

  it("refuses forged process classification instead of exposing it as provider kind", async () => {
    const input = await compilation();
    const target = input.targets[0];
    const firstBinding = target?.bindingPlan.bindings[0];
    if (target === undefined || firstBinding === undefined) {
      throw new TypeError("Expected an inventory target binding.");
    }
    const forged: ProductCompilation = {
      ...input,
      targets: [
        {
          ...target,
          bindingPlan: {
            ...target.bindingPlan,
            bindings: [
              { ...firstBinding, kind: "secret_text" },
              ...target.bindingPlan.bindings.slice(1),
            ],
          },
        },
      ],
    };

    expect(
      failureCode(() => compileContractInventory(forged, "workerDeployment"))
    ).toBe("ENV_INVENTORY_INVALID");
  });
});
