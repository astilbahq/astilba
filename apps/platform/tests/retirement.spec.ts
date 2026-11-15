import { randomUUID } from "node:crypto";
import AxeBuilder from "@axe-core/playwright";
import { expect, type Locator, type Page, test } from "@playwright/test";
import { openEnvironmentSection } from "./environment-section";
import { signInAsOwner } from "./fixtures/owner-sign-in";

// WebKit exposes Base UI's empty focus guards inside a modal as unnamed buttons.
const audit = (page: Page) =>
  new AxeBuilder({ page }).exclude(
    'span[data-base-ui-focus-guard][data-type="inside"][role="button"]:empty',
  );

// Signs in as the owner and creates a project with one never-deployed environment, which has
// nothing on a server and so is retired or purged at once.
async function createEnvironment(page: Page, project: string) {
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
  const projectName = `${project} ${randomUUID().slice(0, 8)}`;
  const createdProject = await page.request.post(
    `/api/v1/workspaces/${workspaceId}/projects`,
    { headers, data: { name: projectName } },
  );
  expect(createdProject.status()).toBe(201);
  const projectId: string = (await createdProject.json()).id;
  const name = "Staging";
  const createdEnvironment = await page.request.post(
    `/api/v1/workspaces/${workspaceId}/projects/${projectId}/environments`,
    { headers, data: { name } },
  );
  expect(createdEnvironment.status()).toBe(201);
  const environmentId: string = (await createdEnvironment.json()).id;
  return {
    headers,
    name,
    projectName,
    api: `/api/v1/workspaces/${workspaceId}/projects/${projectId}/environments/${environmentId}`,
    url: `/?workspace=${workspaceId}&project=${projectId}&environment=${environmentId}`,
  };
}

// Contrast is checked once a dialog and its controls have finished changing.
async function auditDialog(page: Page, dialog: Locator) {
  await dialog.evaluate((element) =>
    Promise.all(
      element
        .getAnimations({ subtree: true })
        .map((animation) => animation.finished),
    ),
  );
  expect(
    (await audit(page).include('[role="alertdialog"]').analyze()).violations,
  ).toEqual([]);
}

test("owner retires an environment, finds it read-only and restores it", async ({
  page,
}, info) => {
  const { name, projectName, url } = await createEnvironment(
    page,
    `Retirement ${info.project.name}`,
  );
  await page.goto(url);

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
  await auditDialog(page, retire);
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

test("owner retires a secret, then deletes a retired environment's retained data", async ({
  page,
}, info) => {
  const { headers, name, projectName, api, url } = await createEnvironment(
    page,
    `Purge ${info.project.name}`,
  );
  for (const secret of ["OLD_TOKEN", "KEPT_TOKEN"]) {
    const created = await page.request.post(`${api}/secrets`, {
      headers,
      data: { name: secret, value: "journey-value" },
    });
    expect(created.status()).toBe(201);
  }
  await page.goto(url);

  // Nothing names the secret, so retiring it erases its values and takes it out of the list.
  await openEnvironmentSection(page, "Settings");
  await page
    .getByRole("button", { name: "Retire secret OLD_TOKEN", exact: true })
    .click();
  const retireSecret = page.getByRole("alertdialog", {
    name: "Retire OLD_TOKEN?",
  });
  await expect(retireSecret).toContainText(
    "The value of every version is erased.",
  );
  await auditDialog(page, retireSecret);
  await retireSecret
    .getByRole("button", { name: "Retire secret", exact: true })
    .click();
  await expect(retireSecret).toBeHidden();
  await expect(
    page.getByRole("status").filter({
      hasText:
        "Secret OLD_TOKEN retired. The values of all its versions are erased.",
    }),
  ).toBeVisible();
  await expect(page.getByText("OLD_TOKEN", { exact: true })).toHaveCount(0);
  await expect(page.getByText("KEPT_TOKEN", { exact: true })).toBeVisible();

  const retired = await page.request.post(`${api}/retire`, {
    headers,
    data: { confirmName: name },
  });
  expect(retired.status()).toBe(201);
  expect((await retired.json()).state).toBe("retired");
  await page.goto(url);

  const banner = page.getByRole("region", {
    name: "This environment is retired",
    exact: true,
  });
  await expect(banner).toContainText("This environment has no kept volumes.");
  await banner
    .getByRole("button", { name: "Delete retained data", exact: true })
    .click();
  const purge = page.getByRole("alertdialog", {
    name: `Delete the retained data of ${name}?`,
  });
  await expect(purge).toContainText(
    "It keeps no data volumes, so nothing is deleted from its server.",
  );
  await expect(purge).toContainText(
    "Backups at the workspace’s backup destination are kept.",
  );
  const purgeName = purge.getByLabel("Environment name", { exact: true });
  const confirmPurge = purge.getByRole("button", {
    name: "Delete retained data",
    exact: true,
  });
  await expect(purgeName).toBeFocused();
  await expect(confirmPurge).toBeDisabled();
  await purgeName.fill(name);
  await auditDialog(page, purge);
  await confirmPurge.click();
  await expect(purge).toBeHidden();

  // With no volumes it is purged at once, and nothing about it can be changed or restored.
  const purged = page.getByRole("region", {
    name: "This environment’s retained data is deleted",
    exact: true,
  });
  await expect(
    purged.getByRole("heading", {
      name: "This environment’s retained data is deleted",
    }),
  ).toBeFocused();
  await expect(
    page.getByRole("status").filter({ hasText: "Retained data deleted." }),
  ).toBeVisible();
  await expect(purged).toContainText(
    "You asked to delete its retained data on",
  );
  // Nothing was on a server, so the banner does not say volumes were deleted.
  await expect(purged).toContainText(
    "It kept no data volumes, and its secret values were erased.",
  );
  await expect(page.getByRole("button", { name: /^Restore/ })).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: "Delete retained data" }),
  ).toHaveCount(0);
  // Its secret values were erased, so its secrets are no longer listed.
  await openEnvironmentSection(page, "Settings");
  await expect(
    page.getByRole("heading", { name: "Secrets", exact: true }),
  ).toBeVisible();
  await expect(page.getByText("KEPT_TOKEN", { exact: true })).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: "Retire environment" }),
  ).toHaveCount(0);
  expect((await audit(page).analyze()).violations).toEqual([]);
  const restore = await page.request.post(`${api}/restore`, {
    headers,
    data: { confirmName: name },
  });
  expect(restore.status()).toBe(409);

  await page
    .getByRole("navigation", { name: "Breadcrumb" })
    .getByRole("link", { name: projectName, exact: true })
    .click();
  await page.getByText("Retired environments (1)", { exact: true }).click();
  await expect(
    page.getByRole("link", {
      name: /^Staging .*Retained data deleted.*Open environment$/,
    }),
  ).toBeVisible();
});
