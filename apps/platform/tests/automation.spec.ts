import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import { openEnvironmentSection } from "./environment-section";

test("owner issues, saves and revokes an environment token without losing a draft", async ({
  page,
  request,
}, info) => {
  await page.goto("/");
  await page.getByLabel("Email", { exact: true }).fill("owner@example.test");
  await page
    .getByLabel("Password", { exact: true })
    .fill("correct horse battery staple");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
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
    panel.getByRole("link", { name: "Open Account security in a new tab" }),
  ).toHaveAttribute(
    "href",
    `/account?workspace=${new URL(page.url()).searchParams.get("workspace")}`,
  );
  await panel.getByText("Create automation token", { exact: true }).click();
  await panel.getByLabel("Token name", { exact: true }).fill("CI deploy");
  await panel.getByLabel("Save configuration drafts", { exact: true }).check();
  await panel
    .getByRole("button", { name: "Create token", exact: true })
    .click();
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
