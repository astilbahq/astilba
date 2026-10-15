import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { readFile, readdir } from "node:fs/promises";
import nodePath from "node:path";
import { describe, it } from "node:test";

import { parse } from "yaml";

const { resolve } = nodePath;
const root = resolve(import.meta.dirname, "../..");
const read = (path) => readFile(resolve(root, path), "utf-8");
const path = "tooling/deployment/workflows/recover-web.yml";
const source = await read(path);
const workflow = parse(source);
const policy = JSON.parse(
  await read("tooling/deployment/recovery/policy.json")
);

describe("protected recovery preparation", () => {
  it("remains uninstalled and independently inactive", async () => {
    const installed = await readdir(resolve(root, ".github/workflows"));
    assert.equal(installed.includes("recover-web.yml"), false);
    assert.equal(policy.phase, "prepared-inactive");
    assert.ok(
      Object.values(policy.acceptance).every((value) => value === false)
    );
    assert.equal(policy.allowSelfApproval, true);
    const environment = JSON.parse(
      await read("tooling/deployment/recovery/environment.json")
    );
    assert.equal(environment.prevent_self_review, false);
    assert.equal(environment.can_admins_bypass, false);
    assert.deepEqual(environment.reviewers, [
      { id: policy.reviewerId, type: "User" },
    ]);
    assert.equal(policy.runtime.sha256, null);
    const forward = JSON.parse(await read("tooling/deployment/cutover.json"));
    assert.ok(
      Object.values(forward.acceptance).every((value) => value === false)
    );
    assert.equal(workflow.concurrency.group, "production-web");
    assert.equal(workflow.concurrency["cancel-in-progress"], false);
    for (const job of Object.values(workflow.jobs)) {
      assert.match(job.if, /vars\.WEB_RECOVERY_ENABLED == 'true'/u);
      assert.match(job.if, /github\.ref == 'refs\/heads\/main'/u);
      for (const step of job.steps) {
        if (step.uses) {
          assert.match(step.uses, /^[^@]+@[0-9a-f]{40}$/u);
        }
      }
    }
  });

  it("binds human approval before backup access and scopes provider credentials", () => {
    assert.deepEqual(Object.keys(workflow.on), ["workflow_dispatch"]);
    assert.deepEqual(workflow.jobs.recover.needs, "plan");
    assert.equal(workflow.jobs.plan.environment, undefined);
    assert.doesNotMatch(
      JSON.stringify(workflow.jobs.plan),
      /secrets\.|id-token/u
    );
    assert.equal(workflow.jobs.recover.environment.name, "production-web");
    assert.equal(workflow.jobs.recover.permissions["id-token"], "write");
    const privileged = workflow.jobs.recover.steps.filter((step) =>
      JSON.stringify(step).includes("secrets.")
    );
    assert.equal(privileged.length, 1);
    assert.equal(
      privileged[0].run,
      "python3 tooling/deployment/recovery/recover.py apply"
    );
    assert.doesNotMatch(
      source,
      /pnpm |npm |npx |docker pull|workflow_run|pull_request_target|ENV_PUBLISHER_ENABLED|WEB_DEPLOYMENTS_ENABLED/u
    );
    const commands = workflow.jobs.recover.steps
      .map((step) => step.run)
      .filter(Boolean);
    assert.ok(
      commands
        .find((command) => command.includes("get-object"))
        .includes('--version-id "$CAPSULE_VERSION"')
    );
    assert.ok(
      commands
        .find((command) => command.includes("get-object"))
        .includes('--version-id "$RUNTIME_VERSION"')
    );
  });

  it("always preserves indeterminate-write evidence without provider output", () => {
    const journal = workflow.jobs.recover.steps.find(
      (step) => step.name === "Retain credential-free recovery journal"
    );
    assert.equal(journal.if, "always()");
    assert.equal(journal.with["retention-days"], 90);
    assert.match(
      journal.with.path,
      /^\$\{\{ runner\.temp \}\}\/recovery-journal\.jsonl$/u
    );
    assert.equal(
      workflow.jobs.recover.steps.find((step) => step.run?.endsWith(" summary"))
        .if,
      "always()"
    );
  });

  it("drafts exact immutable-subject and version-only read access", async () => {
    const trust = JSON.parse(
      await read("tooling/deployment/recovery/aws-trust.json")
    );
    const condition = trust.Statement[0].Condition.StringEquals;
    assert.equal(
      condition["token.actions.githubusercontent.com:aud"],
      "sts.amazonaws.com"
    );
    assert.equal(
      condition["token.actions.githubusercontent.com:sub"],
      `${policy.oidcSubjectPrefix}:environment:production-web`
    );
    const reader = JSON.parse(
      await read("tooling/deployment/recovery/aws-reader.json")
    );
    for (const statement of reader.Statement) {
      assert.equal(statement.Action, "s3:GetObjectVersion");
      assert.ok(statement.Condition.StringEquals["s3:VersionId"]);
      assert.ok(!statement.Resource.includes("*"));
    }
    await Promise.all(
      [
        [".github/workflows/actionlint.yml", "args"],
        [".github/workflows/zizmor.yml", "inputs"],
      ].map(async ([workflowPath, property]) => {
        const checker = parse(await read(workflowPath));
        assert.ok(
          Object.values(checker.jobs)
            .flatMap((job) => job.steps)
            .some((step) => step.with?.[property]?.includes(path))
        );
      })
    );
  });

  it("passes adversarial extraction, authority and partial-failure tests", () => {
    const result = spawnSync(
      "python3",
      [
        "-m",
        "unittest",
        "discover",
        "-s",
        "tooling/deployment/recovery",
        "-p",
        "test_*.py",
        "-v",
      ],
      {
        cwd: root,
        encoding: "utf-8",
        env: { PATH: process.env.PATH, PYTHONDONTWRITEBYTECODE: "1" },
      }
    );
    assert.equal(result.status, 0, result.stdout + result.stderr);
  });
});
