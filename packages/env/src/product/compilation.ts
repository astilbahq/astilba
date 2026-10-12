import { canonicalJson } from "../core/index.ts";
import type { CompiledContract } from "../core/index.ts";
import type { PlanningSnapshotTarget } from "../planning/index.ts";

const MAXIMUM_CLI_COMPILATION_BYTES = 8_388_608;
const TEXT_ENCODER = new TextEncoder();

export type ProductCompilation = Readonly<{
  compiled: CompiledContract;
  targets: readonly PlanningSnapshotTarget[];
}>;

const compareText = (left: string, right: string): number =>
  left < right ? -1 : left === right ? 0 : 1;

const cliCompilationValue = (input: ProductCompilation) => ({
  contract: input.compiled.full.manifest,
  format: "astilba.env.cli-compilation/v1" as const,
  projections: input.compiled.projections
    .map((projection) => projection.manifest)
    .toSorted((left, right) => compareText(left.consumer, right.consumer)),
  targets: [...input.targets]
    .toSorted((left, right) =>
      compareText(left.bindingPlan.target, right.bindingPlan.target)
    )
    .map((target) => ({
      consumer: target.consumer,
      plan: target.bindingPlan,
    })),
});

/**
 * Produces the single exact value-free CliCompilationV1 byte representation.
 *
 * This is an internal boundary shared by the configuration child and every
 * in-process product compilation path; it is not a package export.
 */
export const encodeCliCompilationV1 = (
  input: ProductCompilation
): Uint8Array => {
  const bytes = TEXT_ENCODER.encode(canonicalJson(cliCompilationValue(input)));
  if (bytes.byteLength > MAXIMUM_CLI_COMPILATION_BYTES) {
    throw new TypeError("The compiled declaration is too large.");
  }
  return bytes;
};
