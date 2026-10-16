import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { describe, it } from "node:test";

import { parse } from "yaml";

const root = new URL("../../", import.meta.url);
const source = await readFile(
  new URL(".github/workflows/production-smoke.yml", root),
  "utf-8"
);
const workflow = parse(source);

describe("inactive production smoke transfer", () => {
  it("keeps scheduled and manual runs independently gated to canonical main", () => {
    assert.deepEqual(workflow.on, {
      schedule: [{ cron: "47 * * * *" }],
      workflow_dispatch: null,
    });
    assert.deepEqual(Object.keys(workflow.jobs), ["smoke"]);
    assert.equal(
      workflow.jobs.smoke.if,
      "github.repository == 'astilbahq/astilba' && github.ref == 'refs/heads/main' && vars.WEB_SMOKES_ENABLED == 'true'"
    );
    assert.deepEqual(workflow.permissions, { contents: "read" });
    assert.deepEqual(workflow.concurrency, {
      "cancel-in-progress": false,
      group: "production-smoke",
    });
  });

  it("runs only the existing apex and Docs checks without an install or credentials", () => {
    const { smoke } = workflow.jobs;
    assert.equal(smoke["runs-on"], "ubuntu-24.04");
    assert.equal(smoke["timeout-minutes"], 25);
    assert.equal(smoke.environment, undefined);
    assert.equal(smoke.env, undefined);
    assert.equal(smoke.steps.length, 4);
    assert.deepEqual(smoke.steps[0].with, { "persist-credentials": false });
    assert.deepEqual(smoke.steps[1].with, { "node-version": "24.18.1" });
    assert.equal(
      smoke.steps[0].uses,
      "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1"
    );
    assert.equal(
      smoke.steps[1].uses,
      "actions/setup-node@820762786026740c76f36085b0efc47a31fe5020"
    );
    assert.deepEqual(
      smoke.steps.filter((step) => step.run).map((step) => step.run),
      [
        "node apps/site/scripts/smoke-production.mjs",
        "node apps/docs/scripts/smoke-production.mjs",
      ]
    );
    for (const step of smoke.steps) {
      assert.equal(
        step.if,
        step.run === "node apps/docs/scripts/smoke-production.mjs"
          ? `\${{ !cancelled() }}`
          : undefined
      );
      assert.equal(step.env, undefined);
      assert.equal(step["continue-on-error"], undefined);
    }
    assert.doesNotMatch(
      source,
      /secrets\.|id-token|environment:|npm |pnpm |wrangler |workflow_run|pull_request_target|WEB_DEPLOYMENTS_ENABLED|ENV_PUBLISHER_ENABLED/u
    );
  });

  it("allows the existing sequential request and retry budgets plus setup margin", async () => {
    const [apex, docs] = await Promise.all(
      ["apps/site", "apps/docs"].map((directory) =>
        readFile(
          new URL(`${directory}/scripts/smoke-production.mjs`, root),
          "utf-8"
        )
      )
    );
    for (const [script, prefix] of [
      [apex, "ASTILBA_SITE"],
      [docs, "ASTILBA_DOCS"],
    ]) {
      for (const [setting, value] of [
        ["ATTEMPTS", "6"],
        ["REQUEST_TIMEOUT_MS", "10_000"],
        ["RETRY_DELAY_MS", "2_000"],
      ]) {
        assert.match(
          script,
          new RegExp(
            `readInteger\\(\\s*"${prefix}_SMOKE_${setting}",\\s*${value},`,
            "u"
          )
        );
      }
    }
    assert.equal(apex.match(/await request\(/gu).length, 9);
    const mcp = docs.slice(
      docs.indexOf("const checkMcp ="),
      docs.indexOf("const runChecks =")
    );
    assert.equal(mcp.match(/await callMcp\(/gu).length, 5);
    assert.match(mcp, /await notifyMcpInitialized\(\)/u);
    const apexRequestSeconds = 6 * 9 * 10;
    const docsRequestSeconds = 6 * (1 + 6) * 10;
    const retryBackoffSeconds = 5 * 2 + 2 + 4 + 8 + 16 + 30;
    const setupMarginSeconds = 5 * 60;
    assert.ok(
      workflow.jobs.smoke["timeout-minutes"] * 60 >=
        apexRequestSeconds +
          docsRequestSeconds +
          retryBackoffSeconds +
          setupMarginSeconds
    );
  });

  it("does not accept deployment or restart offline recovery", async () => {
    const policy = JSON.parse(
      await readFile(new URL("tooling/deployment/cutover.json", root), "utf-8")
    );
    assert.equal(policy.phase, "prepared-inactive");
    assert.ok(
      Object.values(policy.acceptance).every((value) => value === false)
    );
    const recovery = JSON.parse(
      await readFile(
        new URL("tooling/deployment/recovery/policy.json", root),
        "utf-8"
      )
    );
    assert.equal(recovery.phase, "prepared-inactive");
    assert.ok(
      Object.values(recovery.acceptance).every((value) => value === false)
    );
  });
});
