import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import { managedDatabase } from "./fixtures/managed-database";

test("protected release review and rollback draft remain separate from deployment", async ({
  page,
  request,
}, info) => {
  const name = `Release ${info.project.name}`;
  const f = await managedDatabase(page, request, name);
  const servers = await (await page.request.get(`${f.prefix}/servers`)).json();
  const server = servers.find(
    (s: { name: string }) => s.name === `Recovery ${name}`,
  );
  const heartbeat = await request.post("/agent/v1/heartbeat", {
    headers: f.agentHeaders,
    data: {
      capabilities: {
        protocol: 1,
        version: "fixture",
        os: "linux",
        arch: "arm64",
        docker: true,
        visual: true,
        safeReleases: true,
        backups: true,
        upgrades: true,
      },
      preflight: {
        ready: true,
        osRelease: "ubuntu 24.04",
        dockerVersion: "fixture",
        freeBytes: 10737418240,
        existingContainers: 0,
        port80Busy: false,
        port443Busy: false,
        notes: [],
      },
    },
  });
  expect(heartbeat.status()).toBe(200);
  const web = crypto.randomUUID(),
    image =
      "busybox:1.37.0@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e";
  const specification = {
    ...f.draft.specification,
    applications: [
      ...f.draft.specification.applications,
      {
        id: web,
        name: "Web",
        kind: "web",
        source: { kind: "image", image },
        variables: [{ name: "VALUE", value: "v1" }],
        health: { port: 8080, path: "/" },
      },
    ],
    routes: [
      {
        id: crypto.randomUUID(),
        targetId: web,
        protocol: "http",
        domain: `release-${info.project.name}.example.test`,
        port: 8080,
        listenPort: 0,
        tls: "off",
        allowCidrs: [],
      },
    ],
  };
  const save = async (base: string) => {
    const r = await page.request.put(`${f.path}/draft`, {
      headers: f.headers,
      data: { baseRevisionId: base, specification },
    });
    expect(r.status(), await r.text()).toBe(200);
    return r.json();
  };
  let draft = await save(f.draft.id);
  const observed = (applied = false) => ({
    observedAt: new Date().toISOString(),
    resources: applied
      ? [
          {
            id: web,
            kind: "service",
            state: "running",
            image,
            drift: false,
            note: "",
            logs: "",
            memoryBytes: 0,
          },
          {
            id: f.pg,
            kind: "service",
            state: "healthy",
            image: f.image,
            drift: false,
            note: "",
            logs: "",
            memoryBytes: 0,
          },
        ]
      : [],
    images: { [web]: image, [f.pg]: f.image },
    applied,
    stateHash: "c".repeat(64),
    infrastructureDrift: false,
  });
  const first = await f.post(`${f.path}/deployment-plans`, {
    baseRevisionId: draft.id,
    serverId: server.id,
    allowHostChanges: true,
    acceptDrift: false,
  });
  await f.report(await f.poll(), observed());
  await f.post(`${f.path}/deployment-plans/${first.id}/apply`, {
    idempotencyKey: crypto.randomUUID(),
  });
  await f.report(await f.poll(), observed(true));
  specification.applications[1].variables[0].value = "v2";
  draft = await save(draft.id);
  await page.goto(
    `/?workspace=${f.ws}&project=${f.project.id}&environment=${f.env.id}`,
  );
  const region = page.getByRole("region", {
    name: "Deployment review and runtime",
  });
  await region
    .getByRole("combobox", { name: "Deployment server", exact: true })
    .selectOption(server.id);
  await region.getByText("Protected release options", { exact: true }).click();
  await region.getByLabel("Require a protected stateless HTTP release").check();
  await region
    .getByLabel("Release command (one argument per line)")
    .fill("sh\n-c\necho migrated");
  await region.getByLabel("Connection drain (seconds)").fill("3");
  await region
    .getByRole("button", { name: "Prepare plan", exact: true })
    .click();
  const resolving = await f.poll();
  expect(resolving.visual.plan.releaseOptions.command).toEqual([
    "sh",
    "-c",
    "echo migrated",
  ]);
  await f.report(resolving, observed());
  await expect(
    region.getByText("Protected stateless HTTP release", { exact: true }),
  ).toBeVisible();
  await region
    .getByRole("region", { name: "Reviewed deployment plan" })
    .screenshot({
      path: `../../.artifacts/plat29/release-review-${info.project.name}.png`,
    });
  await page.setViewportSize({ width: 390, height: 844 });
  await region
    .getByText("Effective configuration and secret references", { exact: true })
    .click();
  await expect(
    region
      .getByRole("region", { name: "Reviewed deployment plan" })
      .locator("pre"),
  ).toBeVisible();
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(390);
  const applyBounds = await region
    .getByRole("button", { name: "Apply reviewed plan", exact: true })
    .boundingBox();
  if (!applyBounds)
    throw new Error("Reviewed plan apply button is not visible");
  expect(applyBounds.x + applyBounds.width).toBeLessThanOrEqual(390);
  expect(
    (
      await new AxeBuilder({ page })
        .include('section[aria-label="Deployment review and runtime"]')
        .analyze()
    ).violations,
  ).toEqual([]);

  await region
    .getByRole("region", { name: "Reviewed deployment plan" })
    .screenshot({
      path: `../../.artifacts/plat29/release-review-mobile-${info.project.name}.png`,
    });
  await page.setViewportSize({ width: 1280, height: 720 });
  await region
    .getByRole("button", { name: "Apply reviewed plan", exact: true })
    .click();
  const deploying = await f.poll();
  expect(deploying.visual.plan.release.options.drainSeconds).toBe(3);
  await f.report(deploying, observed(true));
  const history = region.getByRole("region", { name: "Release history" });
  // History is bounded and ordered newest first; the second applied plan is the rollback target.
  await expect(history.locator("details")).toHaveCount(3, { timeout: 15000 });
  const target = history.locator("details").nth(1);
  await target.locator("summary").click();
  await target
    .getByRole("button", { name: "Create rollback draft for Web", exact: true })
    .click();
  await expect(
    region.getByRole("status").filter({ hasText: "Rollback draft created" }),
  ).toContainText("Rollback draft created");
  const latest = await (await page.request.get(`${f.path}/draft`)).json();
  expect(latest.specification.applications[1].variables[0].value).toBe("v1");
  const operations = await (
    await page.request.get(`${f.prefix}/operations`)
  ).json();
  expect(
    operations.filter(
      (op: { environmentId: string; state: string }) =>
        op.environmentId === f.env.id &&
        !["succeeded", "failed", "cancelled"].includes(op.state),
    ),
  ).toEqual([]);
  await expect(
    region.getByLabel("Release command (one argument per line)"),
  ).toHaveValue("");
  await region
    .getByRole("button", { name: "Prepare plan", exact: true })
    .click();
  const rollbackPlan = await f.poll();
  expect(rollbackPlan.visual.action).toBe("resolve");
  expect(rollbackPlan.visual.plan.releaseOptions.command).toEqual([]);
  await f.report(rollbackPlan, observed());
  await expect(
    region.getByText("Protected stateless HTTP release", { exact: true }),
  ).toBeVisible();
  expect(
    (
      await new AxeBuilder({ page })
        .include('section[aria-label="Deployment review and runtime"]')
        .analyze()
    ).violations,
  ).toEqual([]);
});
