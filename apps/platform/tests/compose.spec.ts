import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";

test("Compose files remain authoritative through graph edits and independent layout saves", async ({
  page,
}, info) => {
  let releaseInitialProjects = () => {};
  const released = new Promise<void>((resolve) => {
    releaseInitialProjects = resolve;
  });
  let capturedInitialProjects = false;
  await page.route(/\/api\/v1\/workspaces\/[^/]+\/projects$/, async (route) => {
    if (route.request().method() !== "GET" || capturedInitialProjects) {
      await route.continue();
      return;
    }
    const response = await route.fetch();
    capturedInitialProjects = true;
    await released;
    await route.fulfill({ response });
  });
  await page.goto("/");
  await page.getByLabel("Email", { exact: true }).fill("owner@example.test");
  await page
    .getByLabel("Password", { exact: true })
    .fill("correct horse battery staple");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  try {
    await expect.poll(() => capturedInitialProjects).toBe(true);
    await page
      .getByRole("button", { name: "New project", exact: true })
      .click();
    await page
      .getByLabel("Project name", { exact: true })
      .fill(`Compose ${info.project.name}`);
    await page
      .getByRole("button", { name: "Create project", exact: true })
      .click();
    await expect(page.getByRole("status")).toHaveText("Project created.");
  } finally {
    releaseInitialProjects();
  }
  await page
    .getByRole("link", { name: `Compose ${info.project.name} Open project` })
    .click();
  await page
    .getByRole("link", { name: "New environment", exact: true })
    .click();
  await page.getByLabel("Environment name", { exact: true }).fill("Files");
  await page
    .getByLabel("Configuration", { exact: true })
    .selectOption("compose");
  await page
    .getByRole("button", { name: "Create environment", exact: true })
    .click();
  const editor = page.getByRole("textbox", {
    name: "Source compose.yaml",
    exact: true,
  });
  await expect(editor).toBeVisible();
  const source =
    "# keep this comment\nx-company: &company\n  name: example\nservices:\n  web:\n    image: busybox:1.37.0\n    volumes: [data:/data]\nvolumes:\n  data: {}\n";
  await page.getByText("Bundle files and entrypoints", { exact: true }).click();
  await page.getByLabel("New relative file path").fill("compose.yaml");
  await page.getByRole("button", { name: "Add file", exact: true }).click();
  await expect(
    page.getByRole("alert").filter({ hasText: "This file already exists." }),
  ).toBeVisible();
  await page.getByLabel("New relative file path").fill("notes.txt");
  await page.getByRole("button", { name: "Add file", exact: true }).click();
  await page.getByRole("button", { name: "compose.yaml", exact: true }).click();
  await editor.fill(source);
  await expect(
    page.getByText("Source validated.", { exact: true }),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "Save revision", exact: true })
    .click();
  await expect(
    page.getByText("Revision saved.", { exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Service list", exact: true }).click();
  await page.getByRole("button", { name: "web", exact: true }).click();
  await expect(
    page.getByText("Edit an image from the graph", { exact: true }),
  ).toBeVisible();
  await page
    .getByLabel("New image reference", { exact: true })
    .fill("busybox:latest");
  await page
    .getByRole("button", { name: "Preview image edit", exact: true })
    .click();
  await expect(
    page.getByRole("button", { name: "Stage this source edit", exact: true }),
  ).toBeVisible();
  expect(await editor.textContent()).toContain("busybox:1.37.0");
  await page
    .getByRole("button", { name: "Stage this source edit", exact: true })
    .click();
  await expect(editor).toContainText("busybox:latest");
  expect(await editor.textContent()).toContain("# keep this comment");
  await page
    .getByRole("button", { name: "Save revision", exact: true })
    .click();
  await expect(
    page.getByText("Revision saved.", { exact: true }),
  ).toBeVisible();
  const before = await editor.textContent();
  await page.getByLabel("Position x", { exact: true }).fill("75");
  await page
    .getByRole("button", { name: "Save canvas layout", exact: true })
    .click();
  await expect(
    page.getByText("Layout saved. Running services were not changed.", {
      exact: true,
    }),
  ).toBeVisible();
  expect(await editor.textContent()).toBe(before);
  await editor.fill("services:\n  web: [\n");
  await expect(
    page.getByText(
      "Source needs attention; graph shows the last valid source.",
      { exact: true },
    ),
  ).toBeVisible();
  const diagnostic = page.getByRole("button", {
    name: /^compose.yaml:\d+:\d+$/,
  });
  await diagnostic.click();
  await expect(editor).toBeFocused();
  await page.getByRole("button", { name: "Service list", exact: true }).focus();
  await diagnostic.click();
  await expect(editor).toBeFocused();
  await page
    .getByRole("button", { name: "Save revision", exact: true })
    .click();
  await expect(page.getByRole("alert").first()).toBeVisible();
  expect(await editor.textContent()).toContain("web: [");
  await editor.fill(source.replace("busybox:1.37.0", "busybox:latest"));
  await expect(
    page.getByText("Source validated.", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Save revision", exact: true }),
  ).toBeDisabled();
  await expect(page.getByRole("alert")).toHaveCount(0);
  await editor.focus();
  await page.keyboard.press("Tab");
  await expect(editor).not.toBeFocused();
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  await page.screenshot({
    path: `../../.artifacts/p4/editor-${info.project.name}-light.png`,
  });
  await page.getByRole("button", { name: "Appearance", exact: true }).click();
  await page.getByRole("menuitemradio", { name: "Dark", exact: true }).click();
  await page.keyboard.press("Escape");
  // Audit the settled page after the popup and theme transitions finish.
  await expect(
    page.getByRole("region", {
      name: "Appearance",
      exact: true,
      includeHidden: true,
    }),
  ).toHaveCount(0);
  await expect(page.locator("html")).not.toHaveAttribute(
    "data-theme-transition",
  );
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  await editor.scrollIntoViewIfNeeded();
  await page.screenshot({
    path: `../../.artifacts/p4/editor-${info.project.name}-dark.png`,
  });
  await page.setViewportSize({ width: 375, height: 812 });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  await page.reload();
  await expect(editor).toContainText("busybox:latest");
  await page.getByRole("button", { name: "Service list", exact: true }).click();
  await page.getByRole("button", { name: "web", exact: true }).click();
  await expect(page.getByLabel("Position x", { exact: true })).toHaveValue(
    "75",
  );
});
