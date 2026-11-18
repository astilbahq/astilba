import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import { openEnvironmentSection } from "./environment-section";
import { signInAsOwner } from "./fixtures/owner-sign-in";
import { api } from "./fixtures/ports";
import {
  confirmIdentityWithPassword,
  lapseRecentConfirmation,
  lapseRecentConfirmationFor,
} from "./fixtures/step-up";

test("owner issues, saves and revokes an environment token without losing a draft", async ({
  page,
  request,
}, info) => {
  await page.goto("/");
  await signInAsOwner(page);
  const name = `Automation ${info.project.name}`;
  await page.getByRole("button", { name: "New project", exact: true }).click();
  await page.getByLabel("Project name", { exact: true }).fill(name);
  await page
    .getByRole("button", { name: "Create project", exact: true })
    .click();
  await page.getByRole("link", { name: `${name} Open project` }).click();
  await page
    .getByRole("link", { name: "New environment", exact: true })
    .click();
  await page.getByLabel("Environment name", { exact: true }).fill("CI");
  await page
    .getByRole("button", { name: "Create environment", exact: true })
    .click();
  await openEnvironmentSection(page, "Configuration");
  await page.getByText("Advanced specification", { exact: true }).click();
  const editor = page.getByLabel("Specification (JSON)");
  await expect(editor).toBeVisible();
  const staged = `${await editor.inputValue()}\n `;
  await editor.fill(staged);
  await openEnvironmentSection(page, "Settings");
  const panel = page.getByRole("region", { name: "Environment automation" });
  await expect(
    panel.getByText(
      "You’ll be asked to confirm your identity before creating or revoking a token.",
    ),
  ).toBeVisible();
  await panel.getByText("Create automation token", { exact: true }).click();
  await panel.getByLabel("Token name", { exact: true }).fill("CI deploy");
  await panel.getByLabel("Save configuration drafts", { exact: true }).check();
  // A lapsed confirmation asks for identity in place, then creates the token as submitted.
  await lapseRecentConfirmationFor(page, "POST", /\/automation-credentials$/);
  await panel
    .getByRole("button", { name: "Create token", exact: true })
    .click();
  await confirmIdentityWithPassword(page);
  const field = panel.getByRole("textbox", {
    name: "Automation token",
    exact: true,
  });
  await expect(field).toBeVisible();
  const token = await field.inputValue();
  const headers = { Authorization: `Bearer ${token}` };
  const context = await request.get("/api/v1/automation/context", { headers });
  expect(context.status()).toBe(200);
  const scope = await context.json();
  expect(scope.environment).toBe("CI");
  expect(scope.capabilities.sort()).toEqual([
    "configuration:write",
    "environment:read",
  ]);
  await field.focus();
  await page.keyboard.press(info.project.name === "webkit" ? "Alt+Tab" : "Tab");
  await expect(
    panel.getByRole("button", { name: "I have saved the token" }),
  ).toBeFocused();
  expect(
    (
      await new AxeBuilder({ page })
        .include('[aria-label="Environment automation"]')
        .analyze()
    ).violations,
  ).toEqual([]);
  await panel.screenshot({ path: info.outputPath("automation-desktop.png") });
  await page.setViewportSize({ width: 390, height: 844 });
  await panel.screenshot({ path: info.outputPath("automation-mobile.png") });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
  ).toBe(true);
  await panel.getByRole("button", { name: "I have saved the token" }).click();
  await expect(field).toHaveCount(0);
  page.once("dialog", (dialog) => dialog.accept());
  await panel
    .getByRole("button", { name: "Revoke CI deploy", exact: true })
    .click();
  await expect(panel.getByRole("status")).toContainText("CI deploy revoked.");
  expect(
    (await request.get("/api/v1/automation/context", { headers })).status(),
  ).toBe(401);
  await openEnvironmentSection(page, "Configuration");
  await expect(editor).toHaveValue(staged);
  await expect(
    page.getByRole("button", { name: "Save revision", exact: true }),
  ).toBeEnabled();
});

test("a lapsed identity confirmation is asked for only once the token request is valid", async ({
  page,
}, info) => {
  await page.goto("/");
  await signInAsOwner(page);
  await expect(
    page.getByRole("heading", { name: "Projects", exact: true }),
  ).toBeVisible();
  const session = await (await page.request.get("/api/v1/session")).json();
  const headers = { "X-CSRF-Token": session.csrfToken, Origin: api };
  const prefix = `/api/v1/workspaces/${session.workspaces[0].id}`;
  const project = await (
    await page.request.post(`${prefix}/projects`, {
      headers,
      data: { name: `Stale automation ${info.project.name}` },
    })
  ).json();
  const env = await (
    await page.request.post(`${prefix}/projects/${project.id}/environments`, {
      headers,
      data: { name: "CI" },
    })
  ).json();
  const refused = await lapseRecentConfirmation(
    page,
    /\/automation-credentials$/,
  );
  await page.goto(
    `/?workspace=${session.workspaces[0].id}&project=${project.id}&environment=${env.id}`,
  );
  await openEnvironmentSection(page, "Settings");
  const panel = page.getByRole("region", { name: "Environment automation" });
  await panel.getByText("Create automation token", { exact: true }).click();
  await panel.getByLabel("Token name", { exact: true }).fill("CI read");
  const read = panel.getByLabel("Read configuration and runtime status", {
    exact: true,
  });
  await read.uncheck();
  const create = panel.getByRole("button", { name: "Create token" });
  // Input the dashboard would never send is reported without asking for identity.
  await create.click();
  await expect
    .poll(() => read.evaluate((box: HTMLInputElement) => box.validationMessage))
    .toBe("Choose at least one capability.");
  const dialog = page.getByRole("alertdialog", {
    name: "Confirm your identity",
  });
  await expect(dialog).toHaveCount(0);
  // Once the request is valid, identity is confirmed before it is sent.
  await read.check();
  await create.click();
  await confirmIdentityWithPassword(page);
  await expect(
    panel.getByRole("textbox", { name: "Automation token", exact: true }),
  ).toBeVisible();
  expect(refused).toEqual([]);
  // A check that stops a later submission also clears the error the last request left.
  let conflicted = false;
  await page.route(/\/automation-credentials$/, (route) => {
    if (conflicted || route.request().method() !== "POST")
      return route.fallback();
    conflicted = true;
    return route.fulfill({
      status: 409,
      json: {
        code: "conflict",
        message: "A token with this name already exists.",
        fields: ["name"],
        requestId: "browser-fixture",
      },
    });
  });
  await panel.getByRole("button", { name: "I have saved the token" }).click();
  await panel.getByLabel("Token name", { exact: true }).fill("CI read");
  await create.click();
  const failure = panel.getByRole("alert");
  await expect(failure).toHaveText(/A token with this name already exists\./);
  await read.uncheck();
  await create.click();
  await expect
    .poll(() => read.evaluate((box: HTMLInputElement) => box.validationMessage))
    .toBe("Choose at least one capability.");
  await expect(failure).toHaveCount(0);
});
