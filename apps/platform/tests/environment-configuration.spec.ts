import { randomUUID } from "node:crypto";
import { expect, test } from "@playwright/test";
import { openEnvironmentSection } from "./environment-section";

test("saved drafts survive reopening, history stays read-only, and reload keeps newer edits", async ({
  page,
}) => {
  await page.goto("/login");
  await page.getByLabel("Email", { exact: true }).fill("owner@example.test");
  await page
    .getByLabel("Password", { exact: true })
    .fill("correct horse battery staple");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "Projects", exact: true }),
  ).toBeVisible();
  const session = await (await page.request.get("/api/v1/session")).json();
  const workspaceId = session.workspaces[0].id;
  const headers = {
    "X-CSRF-Token": session.csrfToken,
    Origin: new URL(page.url()).origin,
  };
  const projectName = `Configuration ${randomUUID()}`;
  const createdProject = await page.request.post(
    `/api/v1/workspaces/${workspaceId}/projects`,
    { headers, data: { name: projectName } },
  );
  expect(createdProject.status()).toBe(201);
  const project = await createdProject.json();
  const createdEnvironment = await page.request.post(
    `/api/v1/workspaces/${workspaceId}/projects/${project.id}/environments`,
    { headers, data: { name: "Development" } },
  );
  expect(createdEnvironment.status()).toBe(201);
  const environment = await createdEnvironment.json();
  await page.goto(
    `/?workspace=${workspaceId}&project=${project.id}&environment=${environment.id}`,
  );
  await openEnvironmentSection(page, "Configuration");
  await page.getByText("Advanced specification", { exact: true }).click();
  const editor = page.getByLabel("Specification (JSON)");
  const specification = JSON.parse(await editor.inputValue());
  specification.variables = [{ name: "MODE", value: "saved" }];
  await editor.fill(JSON.stringify(specification, null, 2));
  await page
    .getByRole("button", { name: "Save revision", exact: true })
    .click();
  await expect(
    page.getByText("Revision saved.", { exact: true }),
  ).toBeVisible();
  const saved = await editor.inputValue();
  await page
    .getByRole("navigation", { name: "Breadcrumb" })
    .getByRole("link", { name: projectName, exact: true })
    .click();
  await page
    .getByRole("link", { name: /^Development .*Open environment$/ })
    .click();
  await openEnvironmentSection(page, "Configuration");
  await page.getByText("Advanced specification", { exact: true }).click();
  await expect(editor).toHaveValue(saved);

  specification.variables[0].value = "local";
  const local = JSON.stringify(specification, null, 2);
  await editor.fill(local);
  await page
    .getByRole("button", { name: "Compare revision", exact: true })
    .last()
    .click();
  const comparison = page.getByRole("region", {
    name: "Revision comparison",
    exact: true,
  });
  await expect(
    comparison.getByRole("heading", {
      name: "Revision comparison",
      exact: true,
    }),
  ).toBeFocused();
  await expect(comparison.getByLabel("Your editor text")).toHaveValue(local);
  await expect(comparison.getByRole("textbox").last()).toHaveAttribute(
    "readonly",
    "",
  );
  await expect(
    page.getByRole("heading", { name: "Another revision was saved" }),
  ).toHaveCount(0);
  await comparison.getByRole("button", { name: "Close comparison" }).click();
  await expect(
    page.getByRole("button", { name: "Compare revision", exact: true }).last(),
  ).toBeFocused();
  const put = page.waitForResponse(
    (response) =>
      response.request().method() === "PUT" &&
      response.url().endsWith("/draft"),
  );
  await page
    .getByRole("button", { name: "Save revision", exact: true })
    .click();
  expect((await put).status()).toBe(200);
  await expect(
    page.getByText("Revision saved.", { exact: true }),
  ).toBeVisible();

  let started!: () => void;
  const requestStarted = new Promise<void>((resolve) => {
    started = resolve;
  });
  let release!: () => void;
  const responseReleased = new Promise<void>((resolve) => {
    release = resolve;
  });
  const routePattern = `**/environments/${environment.id}/draft`;
  await page.route(routePattern, async (route) => {
    const response = await route.fetch();
    started();
    await responseReleased;
    await route.fulfill({ response });
  });
  try {
    await page
      .getByRole("button", { name: "Reload current", exact: true })
      .click();
    await requestStarted;
    specification.variables[0].value = "newer-than-reload";
    const newer = JSON.stringify(specification, null, 2);
    await editor.fill(newer);
    release();
    await expect(page.getByRole("status")).toContainText(
      "your newer edits were kept",
    );
    await expect(editor).toHaveValue(newer);
  } finally {
    release();
    await page.unroute(routePattern);
  }
});
