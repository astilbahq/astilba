/* References: §4.1, §4.4. */








import { runRegistryContract } from "../conformance/runner/registry";
import { makeDoRegistry } from "./registry-harness";

runRegistryContract(makeDoRegistry, {
  label: "doRegistry (real Coordinator Durable Object)",
});
