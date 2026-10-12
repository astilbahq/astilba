import { writeSync } from "node:fs";
import { pathToFileURL } from "node:url";

import { getEnvironmentCompilerState } from "../authoring/internal.ts";
import { compileContract } from "../core/index.ts";
import { encodeCliCompilationV1 } from "../product/compilation.ts";
import type { ProductCompilation } from "../product/compilation.ts";

const OUTPUT_DESCRIPTOR = 3;

const configurationPath = process.argv[2];
if (configurationPath === undefined) {
  throw new TypeError("A configuration path is required.");
}

const imported: unknown = await import(pathToFileURL(configurationPath).href);
if (typeof imported !== "object" || imported === null) {
  throw new TypeError("The configuration module is invalid.");
}
const environment: unknown = Reflect.get(imported, "default");
const state = getEnvironmentCompilerState(environment);
const compiled = await compileContract(state.contract);
const targetNames = Object.keys(state.bindingPlans).toSorted();
const compilation: ProductCompilation = {
  compiled,
  targets: targetNames.map((target) => {
    const targetDefinition = state.targets[target];
    const plan = state.bindingPlans[target];
    if (targetDefinition === undefined || plan === undefined) {
      throw new TypeError("The compiled process target is incomplete.");
    }
    return {
      bindingPlan: plan,
      consumer: targetDefinition.consumer,
    };
  }),
};
const bytes = encodeCliCompilationV1(compilation);

let offset = 0;
while (offset < bytes.byteLength) {
  offset += writeSync(
    OUTPUT_DESCRIPTOR,
    bytes,
    offset,
    bytes.byteLength - offset
  );
}
