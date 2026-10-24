import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import { managedDatabase } from "./fixtures/managed-database";

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
