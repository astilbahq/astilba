import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import {
  mkdir,
  mkdtemp,
  readdir,
  readFile,
  rm,
  writeFile,
} from "node:fs/promises";
import os from "node:os";
import nodePath from "node:path";
import { describe, it } from "node:test";

import { parse } from "yaml";

import { requireActivation } from "./activation.mjs";

const { resolve } = nodePath;
const root = resolve(import.meta.dirname, "../..");
const read = (path) => readFile(resolve(root, path), "utf-8");
const policy = JSON.parse(await read("tooling/deployment/cutover.json"));
const source = await read("tooling/deployment/workflows/deploy-web.yml");
const template = parse(source);
const shaPin = /^[^@]+@[0-9a-f]{40}$/u;
const gate =
  "github.repository == 'astilbahq/astilba' && github.ref == 'refs/heads/main' && vars.WEB_DEPLOYMENTS_ENABLED == 'true'";

describe("inactive deployment preparation", () => {
  it("cannot activate the checked-in policy", () => {
    assert.equal(policy.phase, "prepared-inactive");
    assert.equal(
      Object.values(policy.acceptance).every((value) => value === false),
      true
    );
    assert.throws(() => requireActivation(policy), /inactive/u);
  });

  it("rejects the real activation CLI before any deployment can run", () => {
    const result = spawnSync(
      process.execPath,
      ["tooling/deployment/activation.mjs"],
      {
        cwd: root,
        encoding: "utf-8",
      }
    );
    assert.equal(result.status, 1);
    assert.match(result.stderr, /Deployment preparation is inactive/u);
  });

  it("runs preparation tests and workflow scanners in ordinary credential-free CI", async () => {
    const pilot = parse(await read(".github/workflows/pilot.yml"));
    assert.ok(
      pilot.jobs.ui.steps.some((step) => step.run === "pnpm verify:deployment")
    );
    const actionlint = parse(await read(".github/workflows/actionlint.yml"));
    assert.ok(
      actionlint.jobs.actionlint.steps.some(
        (step) =>
          step.with?.args ===
          "-color tooling/deployment/workflows/deploy-web.yml"
      )
    );
    const zizmor = parse(await read(".github/workflows/zizmor.yml"));
    assert.ok(
      zizmor.jobs.zizmor.steps.some(
        (step) =>
          step.with?.inputs === "tooling/deployment/workflows/deploy-web.yml" &&
          step.with["online-audits"] === false
      )
    );
  });

  it("requires each acceptance independently, not just an enable flag or phase", () => {
    const accepted = structuredClone(policy);
    accepted.phase = "approved-for-activation";
    for (const key of Object.keys(accepted.acceptance)) {
      accepted.acceptance[key] = true;
    }
    assert.doesNotThrow(() => requireActivation(accepted));
    for (const key of Object.keys(accepted.acceptance)) {
      const rejected = structuredClone(accepted);
      rejected.acceptance[key] = false;
      assert.throws(
        () => requireActivation(rejected),
        /Unaccepted deployment gate/u
      );
      const missing = {
        ...accepted,
        acceptance: Object.fromEntries(
          Object.entries(accepted.acceptance).filter(([name]) => name !== key)
        ),
      };
      assert.throws(() => requireActivation(missing));
    }
    for (const key of ["repository", "environment", "enableVariable"]) {
      assert.throws(() => requireActivation({ ...accepted, [key]: "other" }));
    }
  });

  it("keeps the template uninstalled, manual-only and read-only by default", async () => {
    const installed = await readdir(resolve(root, ".github/workflows"));
    assert.equal(installed.includes("deploy-web.yml"), false);
    assert.deepEqual(template.on, { workflow_dispatch: null });
    assert.deepEqual(template.permissions, { contents: "read" });
    assert.deepEqual(Object.keys(template.jobs), ["verify", "deploy"]);
    assert.deepEqual(template.concurrency, {
      "cancel-in-progress": false,
      group: "production-web",
    });
    for (const job of Object.values(template.jobs)) {
      assert.equal(job.if, gate);
      for (const step of job.steps) {
        if (step.uses) {
          assert.match(step.uses, shaPin);
        }
      }
    }
    assert.doesNotMatch(
      source,
      /npm publish|id-token: write|pull_request_target|workflow_run/u
    );
  });

  it("verifies without production access and fails closed before installing", () => {
    const { verify } = template.jobs;
    assert.equal(verify.environment, undefined);
    assert.doesNotMatch(JSON.stringify(verify), /secrets\.|wrangler deploy/u);
    assert.equal(verify.steps[0].with["fetch-depth"], 0);
    const commands = verify.steps.map((step) => step.run).filter(Boolean);
    assert.equal(commands[0], "node tooling/deployment/activation.mjs");
    for (const command of [
      "pnpm install --frozen-lockfile",
      "pnpm test:browser:install:ci",
      "pnpm --dir apps/docs test:browser:install:ci",
      "pnpm verify:ui",
      "pnpm verify:web",
    ]) {
      assert.ok(commands.includes(command));
    }
  });

  it("transfers distinct exact artifacts including hidden discovery files", () => {
    const uploads = template.jobs.verify.steps.filter((step) => step.id);
    const downloads = template.jobs.deploy.steps.filter(
      (step) => step.with?.["artifact-ids"]
    );
    const paths = ["apps/docs/dist", "apps/site/dist", "apps/ui-showcase/dist"];
    assert.equal(uploads.length, 3);
    assert.equal(downloads.length, 3);
    for (const [index, id] of ["docs", "site", "showcase"].entries()) {
      const upload = uploads[index];
      const download = downloads[index];
      assert.equal(upload.id, id);
      assert.equal(upload.with.path, `${paths[index]}/`);
      assert.equal(upload.with["include-hidden-files"], true);
      assert.equal(upload.with["if-no-files-found"], "error");
      assert.equal(upload.with["retention-days"], 7);
      assert.equal(
        template.jobs.verify.outputs[`${id}_id`],
        `\${{ steps.${id}.outputs.artifact-id }}`
      );
      assert.equal(
        template.jobs.verify.outputs[`${id}_digest`],
        `\${{ steps.${id}.outputs.artifact-digest }}`
      );
      assert.equal(
        download.with["artifact-ids"],
        `\${{ needs.verify.outputs.${id}_id }}`
      );
      assert.equal(download.with.path, paths[index]);
      assert.equal(download.with["digest-mismatch"], "error");
    }
  });

  it("executes the real artifact guard and rejects missing, duplicate or malformed inputs", async () => {
    const directory = await mkdtemp(
      resolve(os.tmpdir(), "astilba-deployment-")
    );
    const files = [
      "apps/docs/dist/_headers",
      "apps/docs/dist/docs/index.html",
      "apps/docs/dist/docs/env/overview/index.html",
      "apps/docs/dist/docs/env/overview.md",
      "apps/docs/dist/docs/_llms-txt/astilba-env.txt",
      "apps/docs/dist/docs/.well-known/agent-skills/astilba-env-docs/SKILL.md",
      "apps/docs/dist/docs/sitemap.xml",
      "apps/site/dist/index.html",
      "apps/site/dist/env/index.html",
      "apps/site/dist/cache/index.html",
      "apps/site/dist/llms.txt",
      "apps/site/dist/.well-known/agent-skills/index.json",
      "apps/site/dist/_headers",
      "apps/site/dist/sitemap-site.xml",
      "apps/site/dist/sitemap.xml",
      "apps/ui-showcase/dist/index.html",
      "apps/ui-showcase/dist/_headers",
      "apps/ui-showcase/dist/robots.txt",
      "apps/ui-showcase/dist/assets/example.js",
      "apps/ui-showcase/dist/assets/example.css",
    ];
    const { run } = template.jobs.deploy.steps.find(
      (step) =>
        step.name === "Require exact artifact identities and complete assets"
    );
    const environment = {
      DOCS_DIGEST: "a".repeat(64),
      DOCS_ID: "1",
      PATH: process.env.PATH,
      SHOWCASE_DIGEST: "b".repeat(64),
      SHOWCASE_ID: "2",
      SITE_DIGEST: "c".repeat(64),
      SITE_ID: "3",
    };
    const execute = (overrides = {}) =>
      spawnSync(
        "bash",
        ["--noprofile", "--norc", "-e", "-u", "-o", "pipefail", "-c", run],
        {
          cwd: directory,
          encoding: "utf-8",
          env: { ...environment, ...overrides },
        }
      );
    try {
      await Promise.all(
        files.map(async (file) => {
          const path = resolve(directory, file);
          await mkdir(nodePath.dirname(path), { recursive: true });
          await writeFile(path, "fixture\n");
        })
      );
      assert.equal(execute().status, 0);
      for (const overrides of [
        { DOCS_DIGEST: "" },
        { SITE_DIGEST: "invalid" },
        { SHOWCASE_DIGEST: "f".repeat(63) },
        { DOCS_ID: "" },
        { SITE_ID: "not-an-id" },
        { DOCS_ID: "3" },
        { SHOWCASE_ID: "1" },
        { SHOWCASE_ID: "3" },
      ]) {
        assert.equal(execute(overrides).status, 1);
      }
      await rm(resolve(directory, "apps/docs/dist/_headers"));
      assert.equal(execute().status, 1);
    } finally {
      await rm(directory, { force: true, recursive: true });
    }
  });

  it("requires protected approval, archive checks and fresh main before ordered deploys", () => {
    const { deploy } = template.jobs;
    assert.equal(deploy.needs, "verify");
    assert.equal(deploy.environment.name, "production-web");
    const { steps } = deploy;
    const named = steps.filter((step) => step.name).map((step) => step.name);
    assert.ok(
      named.indexOf("Recheck accepted cutover gates after approval") <
        named.indexOf("Deploy verified apex")
    );
    const identities = steps.find(
      (step) =>
        step.name === "Require exact artifact identities and complete assets"
    );
    assert.match(identities.run, /\^\[0-9a-f\]\{64\}\$/u);
    assert.match(identities.run, /DOCS_ID.*!=.*SITE_ID/u);
    assert.match(identities.run, /\.well-known\/agent-skills/u);
    const commands = steps.map((step) => step.run).filter(Boolean);
    assert.ok(
      commands.includes("pnpm install --frozen-lockfile --ignore-scripts")
    );
    const freshMain = steps.findIndex(
      (step) => step.name === "Reject stale main after approval and install"
    );
    assert.match(
      steps[freshMain].run,
      /git ls-remote --exit-code origin refs\/heads\/main/u
    );
    assert.match(steps[freshMain].run, /current_main_sha.*==.*VERIFIED_SHA/u);
    assert.equal(steps[freshMain].env.VERIFIED_SHA, `\${{ github.sha }}`);
    const firstDeploy = steps.findIndex(
      (step) => step.name === "Deploy verified apex"
    );
    assert.ok(freshMain < firstDeploy);
    assert.deepEqual(
      steps.slice(firstDeploy).map((step) => step.run),
      [
        "pnpm exec wrangler deploy",
        "pnpm --dir apps/docs smoke:site",
        "pnpm exec wrangler deploy",
        "pnpm --dir apps/docs smoke:all",
        "pnpm exec wrangler deploy",
        "pnpm smoke:production",
      ]
    );
    assert.equal(steps[firstDeploy]["working-directory"], "apps/site");
    assert.equal(steps[firstDeploy + 2]["working-directory"], "apps/docs");
    for (const step of steps) {
      if (JSON.stringify(step).includes("secrets.")) {
        assert.equal(step.run, "pnpm exec wrangler deploy");
      }
      assert.equal(step["continue-on-error"], undefined);
    }
  });

  it("preserves live routes and does not silently solve content-history blockers", async () => {
    await Promise.all(
      [
        [
          "wrangler.jsonc",
          "1195ed5305d88fb84821bddeed3df5d86069ef1ce2024a87215c37ad63ce81f6",
        ],
        [
          "apps/docs/wrangler.jsonc",
          "a3fc7f91bb56bf45f76d84344963e48c6aa84708ef37c6f8b4534dfe94385afe",
        ],
        [
          "apps/site/wrangler.jsonc",
          "f78d8c993b5ed5bc7de8c61c65e8b46b692ebe575f40a97f7d5d0ddbe0a19dd7",
        ],
      ].map(async ([path, hash]) => {
        assert.equal(
          createHash("sha256")
            .update(await read(path))
            .digest("hex"),
          hash
        );
      })
    );
    assert.equal(policy.acceptance.contentDatesAndSourceLinks, false);
    assert.equal(policy.acceptance.nonRegistryRollbackRehearsal, false);
  });
});
