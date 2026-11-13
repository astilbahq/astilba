import { randomUUID } from "node:crypto";
import AxeBuilder from "@axe-core/playwright";
import { expect, type Page, test } from "@playwright/test";
import { openEnvironmentSection } from "./environment-section";
import { signInAsOwner } from "./fixtures/owner-sign-in";

// WebKit exposes Base UI's empty focus guards inside a modal as unnamed buttons.
const audit = (page: Page) =>
  new AxeBuilder({ page }).exclude(
    'span[data-base-ui-focus-guard][data-type="inside"][role="button"]:empty',
  );

test("owner retires an environment, finds it read-only and restores it", async ({
  page,
}, info) => {
  await page.goto("/login");
  await signInAsOwner(page);
  await expect(
    page.getByRole("heading", { name: "Projects", exact: true }),
  ).toBeVisible();
  const session = await (await page.request.get("/api/v1/session")).json();
  const workspaceId: string = session.workspaces[0].id;
  const headers = {
    "X-CSRF-Token": session.csrfToken,
    Origin: new URL(page.url()).origin,
  };
  const projectName = `Retirement ${info.project.name} ${randomUUID().slice(0, 8)}`;
  const createdProject = await page.request.post(
    `/api/v1/workspaces/${workspaceId}/projects`,
    { headers, data: { name: projectName } },
  );
  expect(createdProject.status()).toBe(201);
  const project = await createdProject.json();
  const name = "Staging";
  const createdEnvironment = await page.request.post(
    `/api/v1/workspaces/${workspaceId}/projects/${project.id}/environments`,
    { headers, data: { name } },
  );
  expect(createdEnvironment.status()).toBe(201);
  const environment = await createdEnvironment.json();
  await page.goto(
    `/?workspace=${workspaceId}&project=${project.id}&environment=${environment.id}`,
  );

  await openEnvironmentSection(page, "Settings");
  await page
    .getByRole("button", { name: "Retire environment", exact: true })
    .click();
  const retire = page.getByRole("alertdialog", { name: `Retire ${name}?` });
  await expect(retire).toContainText(
    "Its named data volumes are kept on the server.",
  );
  const retireName = retire.getByLabel("Environment name", { exact: true });
  const confirmRetire = retire.getByRole("button", {
    name: "Retire environment",
    exact: true,
  });
  await expect(retireName).toBeFocused();
  await retireName.fill(name.toLowerCase());
  await expect(confirmRetire).toBeDisabled();
  await retireName.fill(name);
  await expect(confirmRetire).toBeEnabled();
  // Contrast is checked once the dialog and its controls have finished changing.
  await retire.evaluate((element) =>
    Promise.all(
      element
        .getAnimations({ subtree: true })
        .map((animation) => animation.finished),
    ),
  );
  expect(
    (await audit(page).include('[role="alertdialog"]').analyze()).violations,
  ).toEqual([]);
  await confirmRetire.click();
  await expect(retire).toBeHidden();

  // A never-deployed environment has nothing on a server, so it is retired at once.
  const banner = page.getByRole("region", {
    name: "This environment is retired",
    exact: true,
  });
  await expect(
    banner.getByRole("heading", { name: "This environment is retired" }),
  ).toBeFocused();
  await expect(
    page.getByRole("status").filter({ hasText: "Environment retired." }),
  ).toBeVisible();
  await expect(banner).toContainText("You retired it on");
  await expect(banner).toContainText("This environment has no kept volumes.");
  await expect(
    page.getByRole("button", { name: "Retire environment" }),
  ).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: "Save secret version" }),
  ).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: "View deployments", exact: true }),
  ).toBeVisible();
  await openEnvironmentSection(page, "Configuration");
  await expect(
    page.getByRole("heading", { name: "Revision history" }),
  ).toBeVisible();
  await expect(page.getByRole("button", { name: "Save revision" })).toHaveCount(
    0,
  );
  expect((await audit(page).analyze()).violations).toEqual([]);

  await page
    .getByRole("navigation", { name: "Breadcrumb" })
    .getByRole("link", { name: projectName, exact: true })
    .click();
  const environmentLink = page.getByRole("link", {
    name: /^Staging .*Retired.*Open environment$/,
  });
  await expect(environmentLink).toBeHidden();
  await expect(
    page.getByText("Every environment in this project is retired."),
  ).toBeVisible();
  await page.getByText("Retired environments (1)", { exact: true }).click();
  await environmentLink.click();

  await page
    .getByRole("region", { name: "This environment is retired" })
    .getByRole("button", { name: "Restore environment", exact: true })
    .click();
  const restore = page.getByRole("alertdialog", { name: `Restore ${name}?` });
  await expect(restore).toContainText("Nothing is deployed.");
  const confirmRestore = restore.getByRole("button", {
    name: "Restore environment",
    exact: true,
  });
  await expect(confirmRestore).toBeDisabled();
  await restore.getByLabel("Environment name", { exact: true }).fill(name);
  await confirmRestore.click();
  await expect(restore).toBeHidden();
  await expect(
    page.getByRole("heading", { level: 1, name, exact: true }),
  ).toBeFocused();
  await expect(
    page.getByRole("status").filter({
      hasText: "Environment restored. Nothing has been deployed.",
    }),
  ).toBeVisible();
  await expect(banner).toHaveCount(0);
  await openEnvironmentSection(page, "Settings");
  await expect(
    page.getByRole("button", { name: "Retire environment", exact: true }),
  ).toBeVisible();

  await page
    .getByRole("navigation", { name: "Breadcrumb" })
    .getByRole("link", { name: projectName, exact: true })
    .click();
  await expect(
    page.getByRole("link", { name: /^Staging .*Open environment$/ }),
  ).toBeVisible();
  await expect(page.getByText(/^Retired environments/)).toHaveCount(0);
});
