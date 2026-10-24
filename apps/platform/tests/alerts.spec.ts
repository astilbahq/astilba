import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import { managedDatabase } from "./fixtures/managed-database";

test("operational inbox records and acknowledges a real failed deployment", async ({
  page,
  request,
}, info) => {
  test.setTimeout(100000);
  const fixture = await managedDatabase(
    page,
    request,
    `Alerts ${info.project.name}`,
  );
  const serverId =
    fixture.agentHeaders.Authorization.split(" ")[1].split(".")[0];
  const plan = await fixture.post(`${fixture.path}/deployment-plans`, {
    baseRevisionId: fixture.draft.id,
    serverId,
    allowHostChanges: false,
    acceptDrift: false,
  });
  await fixture.report(await fixture.poll(), {
    observedAt: new Date().toISOString(),
    resources: [],
    images: { [fixture.pg]: fixture.image },
    applied: false,
    stateHash: "b".repeat(64),
    infrastructureDrift: false,
  });
  await fixture.post(`${fixture.path}/deployment-plans/${plan.id}/apply`, {
    idempotencyKey: crypto.randomUUID(),
  });
  const failed = await fixture.poll();
  await fixture.report(
    failed,
    {
      observedAt: new Date().toISOString(),
      resources: [
        {
          id: fixture.pg,
          kind: "service",
          state: "stopped",
          image: fixture.image,
          drift: false,
          note: "",
          logs: "",
          memoryBytes: 0,
        },
      ],
      applied: false,
      stateHash: "b".repeat(64),
      infrastructureDrift: false,
    },
    undefined,
    "needs_attention",
  );

  await page.getByRole("link", { name: "Alerts", exact: true }).click();
  const alert = page.getByRole("article").filter({ hasText: failed.id });
  await expect(alert).toBeVisible({ timeout: 70000 });
  await expect(
    alert.getByRole("heading", { name: "Deployment needs review" }),
  ).toBeVisible();
  await alert.getByRole("button", { name: "Acknowledge alert" }).click();
  await expect(alert).toHaveCount(0);
  await page
    .getByRole("combobox", { name: "Show", exact: true })
    .selectOption("all");
  await expect(alert).toContainText("Acknowledged");

  await page.getByLabel("Email verified owners").check();
  await page
    .getByRole("button", { name: "Save notification delivery" })
    .click();
  await expect
    .poll(
      async () =>
        (
          await (
            await page.request.get(`${fixture.prefix}/alert-settings`)
          ).json()
        ).emailEnabled,
    )
    .toBe(true);
  await page.reload();
  await expect(page.getByLabel("Email verified owners")).toBeChecked();
  expect(
    (await new AxeBuilder({ page }).include("main").analyze()).violations,
  ).toEqual([]);
});
