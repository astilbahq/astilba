import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";

export const requireActivation = (policy) => {
  assert.equal(
    policy.phase,
    "approved-for-activation",
    "Deployment preparation is inactive"
  );
  assert.equal(policy.repository, "astilbahq/astilba");
  assert.equal(policy.environment, "production-web");
  assert.equal(policy.enableVariable, "WEB_DEPLOYMENTS_ENABLED");
  const gates = [
    "artifactAndRouteComparison",
    "contentDatesAndSourceLinks",
    "legacyDeployHandover",
    "maintainerActivationApproval",
    "nonRegistryRollbackRehearsal",
    "protectedEnvironmentAndCredentials",
  ];
  assert.deepEqual(Object.keys(policy.acceptance).toSorted(), gates);
  for (const gate of gates) {
    assert.equal(
      policy.acceptance[gate],
      true,
      `Unaccepted deployment gate: ${gate}`
    );
  }
};

if (import.meta.main) {
  requireActivation(
    JSON.parse(
      await readFile(new URL("cutover.json", import.meta.url), "utf-8")
    )
  );
}
