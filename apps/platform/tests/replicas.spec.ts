import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import type { Schema } from "../src/api/client";
import { managedDatabase } from "./fixtures/managed-database";

test("replica staging retains failed and conflicting drafts and reviews a complete group", async ({
  page,
  request,
}, info) => {
  const name = `Replicas ${info.project.name}`;
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
        replicas: true,
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
  const web = crypto.randomUUID();
  const image =
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
        health: { port: 8080, path: "/" },
      },
    ],
    routes: [
      {
        id: crypto.randomUUID(),
        targetId: web,
        protocol: "http",
        domain: `replicas-${info.project.name}.example.test`,
        port: 8080,
        listenPort: 0,
        tls: "off",
        allowCidrs: [],
      },
    ],
  };
  const initialSave = await page.request.put(`${f.path}/draft`, {
    headers: f.headers,
    data: { baseRevisionId: f.draft.id, specification },
  });
  expect(initialSave.status(), await initialSave.text()).toBe(200);
  let draft = await initialSave.json();
  const observed = (plan?: Schema["VisualPlan"]) => ({
    observedAt: new Date().toISOString(),
    resources: plan
      ? [
          { id: web, image, state: "running" },
          { id: f.pg, image: f.image, state: "healthy" },
        ].map((resource) => ({
          ...resource,
          kind: "service",
          drift: false,
          note: "",
          logs: "",
          memoryBytes: 0,
          ...(plan.recipe === 3
            ? {
                instances: [
                  plan.instances?.[resource.id] ?? resource.id,
                  ...(plan.replicaInstances?.[resource.id] ?? []),
                ].map((instanceId) => ({ instanceId, state: resource.state })),
              }
            : {}),
        }))
      : [],
    images: { [web]: image, [f.pg]: f.image },
    applied: !!plan,
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
  const initial = await f.poll();
  await f.report(initial, observed(initial.visual.plan));
  await page.goto(
    `/?workspace=${f.ws}&project=${f.project.id}&environment=${f.env.id}`,
  );
  await page.getByRole("button", { name: "Service list", exact: true }).click();
  await page.getByRole("button", { name: "Web", exact: true }).click();
  const properties = page.getByRole("region", { name: "Properties for Web" });
  const count = properties.getByRole("combobox", {
    name: "Replica count",
    exact: true,
  });
  await expect(count).toHaveValue("1");
  await count.selectOption("3");
  await count.press("Tab");
  await expect(properties.getByLabel("Readiness HTTP port")).toBeFocused();

  let fail = true;
  await page.route(`**${f.path}/draft`, async (route) => {
    if (route.request().method() === "PUT" && fail) {
      fail = false;
      await route.fulfill({
        status: 503,
        contentType: "application/json",
        body: JSON.stringify({
          code: "unavailable",
          message: "Temporary save failure.",
        }),
      });
    } else await route.continue();
  });
  const save = page.getByRole("button", { name: "Save revision", exact: true });
  const savedResponse = (status: number) =>
    page.waitForResponse(
      (r) =>
        r.url().endsWith(`${f.path}/draft`) &&
        r.request().method() === "PUT" &&
        r.status() === status,
    );
  const failed = savedResponse(503);
  await save.click();
  await failed;
  await expect(count).toHaveValue("3");
  await expect(save).toBeEnabled();
  const succeeded = savedResponse(200);
  await save.click();
  draft = await (await succeeded).json();
  expect(
    draft.specification.applications.find((a: { id: string }) => a.id === web)
      .replicas,
  ).toBe(3);

  const runtime = page.getByRole("region", {
    name: "Deployment review and runtime",
  });
  await runtime
    .getByRole("combobox", { name: "Deployment server", exact: true })
    .selectOption(server.id);
  await runtime
    .getByRole("button", { name: "Prepare plan", exact: true })
    .click();
  await f.report(await f.poll(), observed());
  const review = runtime.getByRole("region", {
    name: "Reviewed deployment plan",
  });
  await expect(
    review.getByText("Protected stateless HTTP release", { exact: true }),
  ).toBeVisible();
  await expect(
    review.getByText("Web: 3 instances", { exact: true }),
  ).toBeVisible();
  await review.screenshot({
    path: `../../.artifacts/plat34/screenshots/review-${info.project.name}.png`,
  });
  await runtime
    .getByRole("button", { name: "Apply reviewed plan", exact: true })
    .click();
  const command = await f.poll();
  expect(command.visual.plan.replicaInstances[web]).toHaveLength(2);
  await f.report(command, observed(command.visual.plan));
  const instances = runtime.getByText("Instance states (3)", { exact: true });
  await expect(instances).toBeVisible({ timeout: 10000 });
  await instances.click();
  for (const id of command.visual.plan.replicaInstances[web]) {
    await expect(runtime.getByText(id, { exact: true })).toBeVisible();
  }
  await page.setViewportSize({ width: 390, height: 844 });
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(390);
  expect(
    (
      await new AxeBuilder({ page })
        .include('section[aria-label="Properties for Web"]')
        .include('section[aria-label="Deployment review and runtime"]')
        .analyze()
    ).violations,
  ).toEqual([]);
  await properties.screenshot({
    path: `../../.artifacts/plat34/screenshots/mobile-${info.project.name}.png`,
  });

  await count.selectOption("4");
  await count.press("Tab");
  const remoteSpec = structuredClone(draft.specification);
  remoteSpec.applications.find((a: { id: string }) => a.id === web).replicas =
    2;
  const remote = await page.request.put(`${f.path}/draft`, {
    headers: f.headers,
    data: { baseRevisionId: draft.id, specification: remoteSpec },
  });
  expect(remote.status(), await remote.text()).toBe(200);
  const conflict = savedResponse(409);
  await save.click();
  await conflict;
  await expect(count).toHaveValue("4");
  await expect(
    page.getByRole("region", { name: "Conflicting revision" }),
  ).toBeVisible();
});
