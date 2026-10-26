import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import { managedDatabase } from "./fixtures/managed-database";

test("general schedule settings retain invalid drafts and save cron and interval generations", async ({
  page,
  request,
}, info) => {
  const { ws, project, env, path, pg, prefix, post, headers, agentHeaders } =
    await managedDatabase(page, request, `Schedules ${info.project.name}`);
  const destination = await post(`${prefix}/backup-destinations`, {
    name: "Schedule recovery disk",
    kind: "filesystem",
    root: "/srv/backups",
    endpoint: "",
    bucket: "",
    region: "",
    access: { accessKey: "", secretKey: "", password: "" },
  });
  await page.goto(
    `/?workspace=${ws}&project=${project.id}&environment=${env.id}`,
  );
  await page
    .getByText("Create backup policy or container job", { exact: true })
    .click();
  const form = page.getByRole("form", { name: "Create backup policy" });
  await form.getByLabel("Policy name").fill("Flexible backup");
  await form.getByLabel("Source resource").selectOption(pg);
  await form.getByLabel("Backup destination").selectOption(destination.id);
  await form
    .getByLabel("Repository password (keep a recovery copy)")
    .fill("browser-schedule-recovery-canary");
  await form
    .getByRole("combobox", { name: "Schedule type", exact: true })
    .selectOption("cron");
  await form
    .getByLabel("Cron expression", { exact: true })
    .fill("0 0 31 FEB *");
  await form.getByLabel("Timezone", { exact: true }).fill("Europe/London");
  await form.getByLabel("Run timeout in seconds", { exact: true }).fill("90");
  await form
    .getByRole("button", { name: "Create policy", exact: true })
    .click();
  await expect(page.getByRole("alert")).toBeVisible();
  await expect(form.getByLabel("Cron expression", { exact: true })).toHaveValue(
    "0 0 31 FEB *",
  );
  await expect(
    form.getByLabel("Run timeout in seconds", { exact: true }),
  ).toHaveValue("90");
  await form
    .getByLabel("Cron expression", { exact: true })
    .fill("0 2 * * MON-FRI");
  await form
    .getByRole("button", { name: "Create policy", exact: true })
    .click();
  const panel = page.getByRole("region", {
    name: "Policy Flexible backup",
    exact: true,
  });
  await expect(panel).toContainText("Cron 0 2 * * MON-FRI (Europe/London)");
  const policies = await (
    await page.request.get(`${path}/backup-policies`)
  ).json();
  const policy = policies.find(
    (p: { name: string }) => p.name === "Flexible backup",
  );
  expect(
    (
      await request.post("/agent/v1/schedule-reports", {
        headers: agentHeaders,
        data: {
          sequence: 1,
          status: {
            enabled: true,
            policyId: policy.id,
            generation: 1,
            next: "2030-07-01T02:00:00Z",
            runs: [],
          },
        },
      })
    ).status(),
  ).toBe(204);
  await expect(panel).toContainText(/Next run:.*(GMT\+1|BST)/, {
    timeout: 12000,
  });
  await panel
    .getByText("Update schedule or refresh deployment", { exact: true })
    .click();
  const update = panel.getByRole("form", {
    name: "Update Flexible backup",
    exact: true,
  });
  await update
    .getByRole("combobox", { name: "Schedule type", exact: true })
    .selectOption("interval");
  await expect(update.getByLabel("Timezone", { exact: true })).toHaveCount(0);
  await update.getByLabel("Interval in minutes", { exact: true }).fill("7");
  await update
    .getByLabel("Existing repository password")
    .fill("browser-schedule-recovery-canary");
  await update
    .getByRole("button", { name: "Save policy generation", exact: true })
    .click();
  await expect(panel).toContainText("Every 7 elapsed minutes, anchored in UTC");
  await expect(panel).toContainText("Current policy is not installed");
  const saved = await (
    await page.request.get(`${path}/backup-policies`)
  ).json();
  expect(saved.find((p: { id: string }) => p.id === policy.id)).toMatchObject({
    generation: 2,
    schedule: { kind: "interval", intervalMinutes: 7, timezone: "UTC" },
    timeoutSeconds: 90,
  });
  await panel.screenshot({
    path: info.outputPath("general-schedules-desktop.png"),
  });
  await page.setViewportSize({ width: 375, height: 812 });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  await panel.screenshot({
    path: info.outputPath("general-schedules-mobile.png"),
  });
  expect(
    (
      await new AxeBuilder({ page })
        .withTags(["wcag2a", "wcag2aa", "wcag21aa", "wcag22aa"])
        .analyze()
    ).violations,
  ).toEqual([]);
  await update.getByLabel("Run timeout in seconds", { exact: true }).fill("45");
  const latest = {
    name: "Flexible backup",
    resourceId: pg,
    destinationId: destination.id,
    method: "postgres-logical",
    schedule: {
      kind: "cron",
      hour: 0,
      minute: 0,
      timezone: "UTC",
      expression: "0 4 * * *",
    },
    timeoutSeconds: 120,
    retain: 7,
    enabled: true,
    command: [],
    password: "browser-schedule-recovery-canary",
    baseGeneration: 2,
  };
  expect(
    (
      await page.request.put(`${path}/backup-policies/${policy.id}`, {
        headers,
        data: latest,
      })
    ).status(),
  ).toBe(200);
  await page.clock.install();
  await page.clock.fastForward(16000);
  await page.evaluate(() =>
    window.dispatchEvent(new Event("visibilitychange")),
  );
  await expect(panel).toContainText("A newer generation is available");
  await expect(
    update.getByRole("combobox", { name: "Schedule type", exact: true }),
  ).toHaveValue("interval");
  await expect(
    update.getByLabel("Run timeout in seconds", { exact: true }),
  ).toHaveValue("45");
  await update
    .getByLabel("Existing repository password")
    .fill("browser-schedule-recovery-canary");
  const conflict = page.waitForResponse(
    (response) =>
      response.url().endsWith(`/backup-policies/${policy.id}`) &&
      response.request().method() === "PUT",
  );
  await update
    .getByRole("button", { name: "Save policy generation", exact: true })
    .click();
  expect((await conflict).status()).toBe(409);
  await expect(
    update.getByLabel("Run timeout in seconds", { exact: true }),
  ).toHaveValue("45");
  const afterConflict = await (
    await page.request.get(`${path}/backup-policies`)
  ).json();
  expect(
    afterConflict.find((p: { id: string }) => p.id === policy.id),
  ).toMatchObject({
    generation: 3,
    schedule: { kind: "cron", expression: "0 4 * * *" },
    timeoutSeconds: 120,
  });

  const factory = await post(`${path}/backup-policies`, {
    ...latest,
    name: "Legacy browser timezone",
    baseGeneration: 0,
    schedule: { hour: 2, minute: 30, timezone: "Factory" },
  });
  expect(
    (
      await request.post("/agent/v1/schedule-reports", {
        headers: agentHeaders,
        data: {
          sequence: 1,
          status: {
            enabled: true,
            policyId: factory.id,
            generation: 1,
            next: "2030-07-01T02:00:00Z",
            runs: [],
          },
        },
      })
    ).status(),
  ).toBe(204);
  await page.reload();
  const legacyPanel = page.getByRole("region", {
    name: "Policy Legacy browser timezone",
    exact: true,
  });
  await expect(legacyPanel).toContainText("Next run:");
  if (info.project.name === "webkit")
    await expect(legacyPanel).toContainText("UTC display");
});

test("backup setup, restore verification and separate cutover use the real API", async ({
  page,
  request,
}, info) => {
  const {
    ws,
    project,
    env,
    path,
    pg,
    image,
    draft,
    agentHeaders,
    poll,
    report,
  } = await managedDatabase(page, request, info.project.name);
  await page.goto(
    `/?workspace=${ws}&project=${project.id}&environment=${env.id}`,
  );
  await page
    .getByText("Add a reusable backup destination", { exact: true })
    .click();
  const dest = page.getByRole("form", { name: "Add backup destination" });
  await dest
    .getByLabel("Destination name")
    .fill(`Recovery disk ${info.project.name}`);
  await dest
    .getByLabel("Operator-approved filesystem root")
    .fill("/srv/backups");
  await dest
    .getByRole("button", { name: "Add destination", exact: true })
    .click();
  await page
    .getByText("Create backup policy or container job", { exact: true })
    .click();
  const form = page.getByRole("form", { name: "Create backup policy" });
  await form.getByLabel("Policy name").fill("Daily database");
  await form.getByLabel("Source resource").selectOption(pg);
  await expect(
    form
      .getByLabel("Backup destination")
      .locator("option")
      .filter({ hasText: `Recovery disk ${info.project.name}` }),
  ).toHaveCount(1);
  await form.getByLabel("Backup destination").selectOption({
    label: `Recovery disk ${info.project.name} — same server failure boundary`,
  });
  await form
    .getByLabel("Repository password (keep a recovery copy)")
    .fill("browser-repository-canary");
  await form.getByLabel("Daily time", { exact: true }).fill("02:30");
  await form
    .getByRole("button", { name: "Create policy", exact: true })
    .click();
  const panel = page.getByRole("region", { name: "Policy Daily database" });
  await expect(panel).toBeVisible();
  expect(await page.locator("body").textContent()).not.toContain(
    "browser-repository-canary",
  );
  await panel
    .getByRole("button", { name: "Install schedule", exact: true })
    .click();
  const install = await poll();
  expect(install.backup.installation.access.password).toBe(
    "browser-repository-canary",
  );
  await report(install);
  const requestKeys: string[] = [];
  let loseResponse = true;
  const commandPattern = "**/backup-policies/*/commands";
  await page.route(commandPattern, async (route) => {
    const data = route.request().postDataJSON();
    if (data.action === "backup") {
      requestKeys.push(data.requestKey);
      if (loseResponse) {
        loseResponse = false;
        const accepted = await route.fetch();
        expect(accepted.status()).toBe(202);
        await route.abort();
        return;
      }
    }
    await route.continue();
  });
  await panel.getByRole("button", { name: "Run now", exact: true }).click();
  await expect(panel.getByRole("alert")).toBeVisible();
  const manual = await poll();
  expect(manual.backup.targetId).toBe("00000000-0000-0000-0000-000000000000");
  await report(manual);
  const retried = page.waitForResponse(
    (response) =>
      response.url().endsWith("/commands") && response.status() === 202,
  );
  await panel.getByRole("button", { name: "Run now", exact: true }).click();
  const retryOperation = await (await retried).json();
  expect(retryOperation.id).toBe(manual.id);
  expect(requestKeys).toHaveLength(2);
  expect(requestKeys[0]).toBe(requestKeys[1]);
  await page.unroute(commandPattern);
  const policy = install.backup.installation.policy,
    now = new Date().toISOString(),
    snapshot = "a".repeat(64),
    runId = crypto.randomUUID(),
    targetId = crypto.randomUUID();
  const run = {
    id: runId,
    policyId: policy.id,
    generation: 1,
    scheduledAt: now,
    startedAt: now,
    finishedAt: now,
    state: "succeeded",
    note: "Uploaded successfully",
    snapshot,
    verified: false,
    protected: false,
    pruned: false,
    exitCode: null,
    manifest: {
      recipe: 1,
      policyId: policy.id,
      runId,
      environmentId: env.id,
      resourceId: pg,
      method: "postgres-logical",
      image,
      database: "app",
      username: "app",
      createdAt: now,
      roles: [],
      extensions: ["plpgsql"],
      sha256: "b".repeat(64),
      bytes: 1000,
    },
    targetId: "00000000-0000-0000-0000-000000000000",
  };
  const scheduleReport = async (sequence: number, runs: unknown[]) => {
    const res = await request.post("/agent/v1/schedule-reports", {
      headers: agentHeaders,
      data: {
        sequence,
        status: {
          enabled: true,
          policyId: policy.id,
          generation: 1,
          next: now,
          runs,
        },
      },
    });
    expect(res.status(), await res.text()).toBe(204);
  };
  await scheduleReport(1, [run]);
  await expect(panel.getByText(/uploaded, restore unverified/)).toBeVisible({
    timeout: 12000,
  });
  await expect(
    panel.getByRole("button", { name: "Prepare cutover draft" }),
  ).toHaveCount(0);
  await panel
    .getByRole("button", { name: "Restore snapshot into fresh storage" })
    .click();
  const restore = await poll();
  expect(restore.backup.snapshot).toBe(snapshot);
  await report(restore);
  const restored = {
    ...run,
    id: crypto.randomUUID(),
    verified: true,
    targetId,
    note: "Fresh restore verified",
  };
  await scheduleReport(2, [run, restored]);
  await expect(
    panel.getByRole("button", { name: "Prepare cutover draft" }),
  ).toBeVisible({ timeout: 12000 });
  let releaseResponse: () => void = () => {};
  let responseReady: () => void = () => {};
  const gate = new Promise<void>((resolve) => {
    releaseResponse = resolve;
  });
  const ready = new Promise<void>((resolve) => {
    responseReady = resolve;
  });
  await page.route("**/backup-policies/*/cutover", async (route) => {
    const response = await route.fetch();
    expect(response.status()).toBe(200);
    responseReady();
    await gate;
    await route.fulfill({ response });
  });
  await panel.getByRole("button", { name: "Prepare cutover draft" }).click();
  await ready;
  await page.getByText("Advanced specification", { exact: true }).click();
  const editor = page.getByRole("textbox", { name: "Specification (JSON)" });
  const edited = JSON.parse(await editor.inputValue());
  edited.applications[0].name = "Edit made while cutover was pending";
  await editor.fill(JSON.stringify(edited, null, 2));
  releaseResponse();
  await expect(panel.getByText(/Your newer edits were retained/)).toBeVisible();
  await expect(editor).toHaveValue(/Edit made while cutover was pending/);
  const next = await (await page.request.get(`${path}/draft`)).json();
  expect(next.specification.applications[0].mounts[0].volumeId).toBe(targetId);
  expect((await (await page.request.get(path)).json()).appliedRevisionId).toBe(
    draft.id,
  );
  const accessibility = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21aa", "wcag22aa"])
    .analyze();
  expect(accessibility.violations).toEqual([]);
});
