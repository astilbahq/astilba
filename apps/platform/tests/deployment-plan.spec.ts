import { expect, test } from "@playwright/test";
import { openEnvironmentSection } from "./environment-section";
import { managedDatabase } from "./fixtures/managed-database";

// A plan freezes one saved revision. Once another is saved, here or anywhere else, the panel must
// stop offering that plan for review and apply until a new one is prepared.
test("a prepared plan is withdrawn once another revision is saved", async ({
  page,
  request,
}, info) => {
  const name = `Plan ${info.project.name}`;
  const f = await managedDatabase(page, request, name);
  const servers = await (await page.request.get(`${f.prefix}/servers`)).json();
  const server = servers.find(
    (s: { name: string }) => s.name === `Recovery ${name}`,
  );
  const observed = {
    observedAt: new Date().toISOString(),
    resources: [],
    images: { [f.pg]: f.image },
    applied: false,
    stateHash: "c".repeat(64),
    infrastructureDrift: false,
  };
  await page.goto(
    `/?workspace=${f.ws}&project=${f.project.id}&environment=${f.env.id}`,
  );
  await openEnvironmentSection(page, "Deployments");
  const region = page.getByRole("region", {
    name: "Deployment review and runtime",
  });
  const reviewed = region.getByRole("region", {
    name: "Reviewed deployment plan",
  });
  const apply = region.getByRole("button", {
    name: "Apply reviewed plan",
    exact: true,
  });
  const stale = region.getByText(
    "The saved revision changed. Prepare a new plan.",
    { exact: true },
  );
  const prepare = async () => {
    await region
      .getByRole("combobox", { name: "Deployment server", exact: true })
      .selectOption(server.id);
    await region
      .getByRole("button", { name: "Prepare plan", exact: true })
      .click();
    await f.report(await f.poll(), observed);
    await expect(
      reviewed.getByRole("heading", { name: "Plan ready", exact: true }),
    ).toBeVisible();
    await expect(apply).toBeEnabled();
    await expect(stale).toHaveCount(0);
  };

  // Another tab, member or automation saves; this page learns of it only from the server.
  const saveElsewhere = async (value: string) => {
    const planned = await (await page.request.get(`${f.path}/draft`)).json();
    const saved = await page.request.put(`${f.path}/draft`, {
      headers: f.headers,
      data: {
        baseRevisionId: planned.id,
        specification: {
          ...planned.specification,
          variables: [{ name: "SAVED_ELSEWHERE", value }],
        },
      },
    });
    expect(saved.status(), await saved.text()).toBe(200);
  };

  await prepare();
  await openEnvironmentSection(page, "Configuration");
  await page.getByRole("button", { name: "Service list", exact: true }).click();
  await page.getByRole("button", { name: "Database", exact: true }).click();
  await page
    .getByRole("form", { name: "Service properties" })
    .getByLabel("Service name", { exact: true })
    .fill("Primary database");
  await page
    .getByRole("button", { name: "Save revision", exact: true })
    .click();
  await expect(
    page.getByText("Revision saved.", { exact: true }),
  ).toBeVisible();
  await openEnvironmentSection(page, "Deployments");
  await expect(stale).toBeVisible();
  await expect(reviewed).toHaveCount(0);
  await expect(apply).toHaveCount(0);

  await prepare();
  await apply.focus();
  await saveElsewhere("first");
  await expect(stale).toBeVisible({ timeout: 15000 });
  await expect(stale).toBeFocused();
  await expect(reviewed).toHaveCount(0);
  await expect(apply).toHaveCount(0);
  // The server only plans the latest saved revision, so the editor must load it first.
  await expect(
    region.getByRole("button", { name: "Prepare plan", exact: true }),
  ).toBeDisabled();
  await expect(
    region.getByText(
      "A newer revision was saved. Use Reload current in Configuration before preparing a plan.",
      { exact: true },
    ),
  ).toBeVisible();
  await openEnvironmentSection(page, "Configuration");
  await page
    .getByRole("button", { name: "Reload current", exact: true })
    .click();
  await openEnvironmentSection(page, "Deployments");
  await prepare();
  // Focus moves to the notice only from the withdrawn plan's own controls.
  await page.evaluate(() => (document.activeElement as HTMLElement).blur());
  await saveElsewhere("second");
  await expect(stale).toBeVisible({ timeout: 15000 });
  // A move would happen on the frame after the plan is withdrawn.
  await page.evaluate(
    () =>
      new Promise((done) =>
        requestAnimationFrame(() => requestAnimationFrame(done)),
      ),
  );
  await expect(stale).not.toBeFocused();
});
