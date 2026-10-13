import assert from "node:assert/strict";
import { readFile, readdir } from "node:fs/promises";
import nodePath from "node:path";
import { test } from "node:test";

import { parse } from "yaml";

import { cutover, root } from "./identity.mjs";

const { resolve } = nodePath;
const read = (file) => readFile(resolve(root, file), "utf-8");
const trust = JSON.parse(
  await read("tooling/publishing/trusted-publishers.json")
);

test("prepared npm identities contain only Env and coordinated tokens/UI", () => {
  assert.equal(cutover.phase, "prepared-not-activated");
  assert.equal(trust.phase, cutover.phase);
  assert.equal(trust.repository, cutover.repository);
  assert.equal(trust.provider, "github");
  assert.equal(trust.allowPublish, true);
  assert.equal(trust.allowStagePublish, false);
  assert.deepEqual(
    trust.bindings,
    Object.values(cutover.products).flatMap((product) =>
      product.packages.map(({ name }) => ({
        environment: product.environment,
        package: name,
        workflow: product.workflow,
      }))
    )
  );
  assert.deepEqual(Object.keys(cutover.products), ["env", "ui"]);
});

for (const product of ["env", "ui"]) {
  test(`${product} template stays inactive, gated and narrowly permissioned`, async () => {
    const identity = cutover.products[product];
    const source = await read(
      `tooling/publishing/workflows/${identity.workflow}`
    );
    const workflow = parse(source);
    const entry =
      workflow.jobs[product === "env" ? "release-identity" : "verify-packages"];
    assert.match(entry.if, /github\.repository == 'astilbahq\/astilba'/u);
    assert.equal(
      entry.if.includes(`vars.${identity.enableVariable} == 'true'`),
      true
    );
    assert.deepEqual(workflow.permissions, { contents: "read" });
    assert.equal(workflow.jobs.publish.environment, identity.environment);
    assert.deepEqual(workflow.jobs.publish.permissions, {
      contents: "read",
      "id-token": "write",
    });
    for (const [name, job] of Object.entries(workflow.jobs)) {
      if (name !== "publish") {
        assert.equal(job.permissions?.["id-token"], undefined);
      }
      if (job["runs-on"]) {
        assert.equal(job["runs-on"], "ubuntu-latest");
      }
      for (const step of job.steps ?? []) {
        if (step.uses) {
          assert.match(step.uses, /^[\w/-]+@[a-f0-9]{40}$/u);
        }
        if (step.uses?.startsWith("actions/checkout@")) {
          assert.equal(step.with["persist-credentials"], false);
        }
        if (step.run) {
          assert.equal(step.run.includes("${{"), false);
        }
      }
    }
    assert.equal(source.includes("secrets."), false);
    assert.equal(source.includes("@astilba/cache"), false);
    assert.equal(source.includes("--recursive publish"), false);
    const publishRuns = workflow.jobs.publish.steps
      .map((step) => step.run ?? "")
      .join("\n");
    assert.equal(
      publishRuns.includes(`identity.mjs ${product} "$RELEASE_TAG"`),
      true
    );
    const commands = publishRuns
      .split("\n")
      .filter((line) => line.trim().startsWith("npm publish "));
    assert.equal(commands.length, identity.packages.length);
    for (const command of commands) {
      assert.match(
        command,
        /--access public --provenance --ignore-scripts --registry=https:\/\/registry\.npmjs\.org$/u
      );
    }
    const active = await readdir(resolve(root, ".github/workflows"));
    assert.equal(active.includes(identity.workflow), false);
    if (product === "env") {
      assert.deepEqual(workflow.on.push.tags, ["env/v*"]);
      assert.equal(
        workflow.jobs["package-matrix"].uses,
        "./.github/workflows/env-package-matrix.yml"
      );
      assert.equal(workflow.jobs["create-release"].needs, "verify-registry");
    } else {
      assert.deepEqual(workflow.on.release.types, ["published"]);
      assert.equal(commands[0].includes("$tokens"), true);
      assert.equal(commands[1].includes("$ui"), true);
      assert.equal(
        workflow.jobs.publish.env.MANIFEST_SHA256,
        `\${{ needs.verify-packages.outputs.manifest-sha256 }}`
      );
      assert.equal(
        publishRuns.includes(
          'ui-artifacts.mjs verify .artifacts/ui "$MANIFEST_SHA256"'
        ),
        true
      );
    }
  });
}

test("existing hosted gates also audit both inactive publisher templates", async () => {
  const actionlint = await read(".github/workflows/actionlint.yml");
  const zizmor = await read(".github/workflows/zizmor.yml");
  const audit = parse(zizmor).jobs.zizmor.steps.find((step) =>
    step.uses?.startsWith("zizmorcore/zizmor-action@")
  );
  for (const { workflow } of Object.values(cutover.products)) {
    assert.equal(
      actionlint.includes(`tooling/publishing/workflows/${workflow}`),
      true
    );
    assert.equal(
      audit.with.inputs.includes(`tooling/publishing/workflows/${workflow}`),
      true
    );
  }
  const pilot = await read(".github/workflows/pilot.yml");
  assert.equal(pilot.includes("pnpm verify:publisher"), true);
});
