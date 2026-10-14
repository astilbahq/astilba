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
  assert.equal(cutover.phase, "publishing-disabled");
  assert.equal(cutover.products.env.phase, "workflow-installed-disabled");
  assert.equal(cutover.products.ui.phase, "prepared-not-activated");
  assert.equal(trust.phase, "prepared-not-activated");
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
  test(`${product} publisher stays gated and narrowly permissioned`, async () => {
    const identity = cutover.products[product];
    const source = await read(
      `${product === "env" ? ".github/workflows" : "tooling/publishing/workflows"}/${identity.workflow}`
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
    assert.equal(active.includes(identity.workflow), product === "env");
    const inactive = await readdir(
      resolve(root, "tooling/publishing/workflows")
    );
    assert.equal(inactive.includes(identity.workflow), product === "ui");
    assert.equal(
      workflow.jobs["verify-registry"].env.RELEASE_COMMIT,
      `\${{ github.sha }}`
    );
    const auditRuns = workflow.jobs["verify-registry"].steps
      .map((step) => step.run ?? "")
      .join("\n");
    const artifactPath =
      product === "env" ? "packages/env/.artifacts/release" : ".artifacts/ui";
    assert.equal(
      auditRuns.includes(
        `${product} "$RELEASE_TAG" "$RELEASE_COMMIT" ${artifactPath}`
      ),
      true
    );
    if (product === "env") {
      assert.equal(
        entry.if,
        [
          "github.repository == 'astilbahq/astilba'",
          "vars.ENV_PUBLISHER_ENABLED == 'true'",
          "github.ref_type == 'tag'",
        ].join(" && ")
      );
      assert.deepEqual(Object.keys(workflow.on), ["push"]);
      assert.deepEqual(workflow.on.push.tags, ["env/v*"]);
      assert.deepEqual(Object.keys(workflow.jobs), [
        "release-identity",
        "package-matrix",
        "publish",
        "verify-registry",
        "create-release",
      ]);
      for (const [job, dependency] of [
        ["package-matrix", "release-identity"],
        ["publish", "package-matrix"],
        ["verify-registry", "publish"],
        ["create-release", "verify-registry"],
      ]) {
        assert.equal(workflow.jobs[job].needs, dependency);
        assert.equal(workflow.jobs[job].if, undefined);
      }
      assert.equal(workflow.jobs["package-matrix"].secrets, undefined);
      assert.equal(workflow.jobs["package-matrix"].permissions, undefined);
      assert.deepEqual(workflow.jobs["create-release"].permissions, {
        contents: "write",
      });
      assert.equal(
        workflow.jobs["package-matrix"].uses,
        "./.github/workflows/env-package-matrix.yml"
      );
      assert.equal(workflow.jobs["create-release"].needs, "verify-registry");
    } else {
      assert.deepEqual(workflow.on.release.types, ["published"]);
      assert.equal(workflow.jobs["verify-registry"]["timeout-minutes"], 40);
      assert.equal(
        entry.if,
        [
          "github.repository == 'astilbahq/astilba'",
          "vars.UI_PUBLISHER_ENABLED == 'true'",
          "github.event.release.prerelease == false",
          "startsWith(github.event.release.tag_name, 'ui/v')",
        ].join(" && ")
      );
      assert.equal(workflow.jobs.publish.needs, "verify-packages");
      assert.equal(workflow.jobs.publish.if, undefined);
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

test("existing hosted gates audit active Env and inactive UI publishers", async () => {
  const actionlint = await read(".github/workflows/actionlint.yml");
  const zizmor = await read(".github/workflows/zizmor.yml");
  const audit = parse(zizmor).jobs.zizmor.steps.find((step) =>
    step.uses?.startsWith("zizmorcore/zizmor-action@")
  );
  for (const { workflow } of [cutover.products.ui]) {
    assert.equal(
      actionlint.includes(`tooling/publishing/workflows/${workflow}`),
      true
    );
    assert.equal(
      audit.with.inputs.includes(`tooling/publishing/workflows/${workflow}`),
      true
    );
  }
  assert.equal(audit.with.inputs.split(" ")[0], ".");
  assert.equal(
    parse(actionlint).jobs.actionlint.steps.some(
      (step) =>
        step.uses?.startsWith("docker://docker.io/rhysd/actionlint:") &&
        step.with.args === "-color"
    ),
    true
  );
  const pilot = await read(".github/workflows/pilot.yml");
  assert.equal(pilot.includes("pnpm verify:publisher"), true);
});
