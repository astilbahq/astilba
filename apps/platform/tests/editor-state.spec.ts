import { randomUUID } from "node:crypto";
import { expect, type Page, test } from "@playwright/test";
import type { Schema } from "../src/api/client";
import { openEnvironmentSection } from "./environment-section";

async function openEditor(page: Page) {
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
  const workspace = session.workspaces[0].id;
  const headers = {
    "X-CSRF-Token": session.csrfToken,
    Origin: new URL(page.url()).origin,
  };
  const projectName = `Editor state ${randomUUID()}`;
  const createdProject = await page.request.post(
    `/api/v1/workspaces/${workspace}/projects`,
    { headers, data: { name: projectName } },
  );
  expect(createdProject.status()).toBe(201);
  const project = await createdProject.json();
  const createdEnvironment = await page.request.post(
    `/api/v1/workspaces/${workspace}/projects/${project.id}/environments`,
    { headers, data: { name: "Development" } },
  );
  expect(createdEnvironment.status()).toBe(201);
  const environment = await createdEnvironment.json();
  const path = `/api/v1/workspaces/${workspace}/projects/${project.id}/environments/${environment.id}`;
  const initial: Schema["Revision"] = await (
    await page.request.get(`${path}/draft`)
  ).json();
  const appId = randomUUID();
  const specification: Schema["Specification"] = {
    ...initial.specification,
    applications: [
      {
        id: appId,
        name: "api",
        kind: "web",
        source: { kind: "image", image: "nginx:alpine" },
      },
    ],
    volumes: [{ id: randomUUID(), name: "data" }],
    variables: [{ name: "MODE", value: "original" }],
  };
  const updated = await page.request.put(`${path}/draft`, {
    headers,
    data: { baseRevisionId: initial.id, specification },
  });
  expect(updated.status()).toBe(200);
  const revision: Schema["Revision"] = await updated.json();
  const url = `/?workspace=${workspace}&project=${project.id}&environment=${environment.id}`;
  await page.goto(url);
  const layoutRead = page.waitForResponse(
    (response) =>
      response.request().method() === "GET" &&
      response.url().endsWith(`${path}/layout`),
  );
  await openEnvironmentSection(page, "Configuration");
  const layout = await layoutRead;
  expect(layout.status()).toBe(200);
  await layout.finished();
  return { path, headers, projectName, revision, appId };
}

test("acknowledging a conflict keeps the original text unsaved against the new server revision", async ({
  page,
}) => {
  const { path, headers, projectName, revision } = await openEditor(page);
  await page.getByText("Advanced specification", { exact: true }).click();
  const editor = page.getByLabel("Specification (JSON)");
  const original = await editor.inputValue();
  await editor.fill(
    JSON.stringify({
      ...revision.specification,
      variables: [{ name: "MODE", value: "local" }],
    }),
  );
  const remote = await page.request.put(`${path}/draft`, {
    headers,
    data: {
      baseRevisionId: revision.id,
      specification: {
        ...revision.specification,
        variables: [{ name: "MODE", value: "remote" }],
      },
    },
  });
  expect(remote.status()).toBe(200);
  const save = page.getByRole("button", { name: "Save revision", exact: true });
  await save.click();
  await expect(
    page.getByRole("heading", { name: "Another revision was saved" }),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "I have compared and merged my edits" })
    .click();
  await editor.fill(original);
  await expect(save).toBeEnabled();
  await page
    .getByRole("navigation", { name: "Breadcrumb" })
    .getByRole("link", { name: projectName, exact: true })
    .click();
  await page
    .getByRole("alertdialog")
    .getByRole("button", { name: "Keep editing" })
    .click();
  await expect(editor).toHaveValue(original);
  await save.click();
  await expect(
    page.getByText("Revision saved.", { exact: true }),
  ).toBeVisible();
  const stored: Schema["Revision"] = await (
    await page.request.get(`${path}/draft`)
  ).json();
  expect(stored.specification).toEqual(revision.specification);
});

test("saved canvas positions survive leaving and reopening the environment", async ({
  page,
}) => {
  const { path, appId, projectName } = await openEditor(page);
  await page.getByRole("button", { name: "Service list", exact: true }).click();
  await page.getByRole("button", { name: "api", exact: true }).click();
  const x = page.getByLabel("Position x", { exact: true });
  await x.fill("123");
  await page.getByRole("button", { name: "Save canvas layout" }).click();
  await expect(
    page.getByText("Layout saved. Running services were not changed."),
  ).toBeVisible();
  const stored: Schema["Layout"] = await (
    await page.request.get(`${path}/layout`)
  ).json();
  expect(stored.positions[appId].x).toBe(123);
  await page
    .getByRole("navigation", { name: "Breadcrumb" })
    .getByRole("link", { name: projectName, exact: true })
    .click();
  await page
    .getByRole("link", { name: /^Development .*Open environment$/ })
    .click();
  await openEnvironmentSection(page, "Configuration");
  await page.getByRole("button", { name: "Service list", exact: true }).click();
  await page.getByRole("button", { name: "api", exact: true }).click();
  await expect(x).toHaveValue("123");
  await expect(
    page.getByRole("button", { name: "Save canvas layout" }),
  ).toBeDisabled();
});

test("reload starts a current read instead of reusing an older background response", async ({
  page,
}) => {
  const { path, headers, revision } = await openEditor(page);
  await page.clock.install();
  let release!: () => void;
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });
  let started!: () => void;
  const backgroundStarted = new Promise<void>((resolve) => {
    started = resolve;
  });
  let intercepted = false;
  const pattern = `**${path}/draft`;
  await page.route(pattern, async (route) => {
    if (route.request().method() !== "GET" || intercepted)
      return route.continue();
    intercepted = true;
    const response = await route.fetch();
    expect(response.status()).toBe(200);
    started();
    await held;
    await route.fulfill({ response });
  });
  try {
    await page.clock.runFor(16000);
    await page.evaluate(() =>
      window.dispatchEvent(new Event("visibilitychange")),
    );
    await backgroundStarted;
    const specification: Schema["Specification"] = {
      ...revision.specification,
      variables: [{ name: "MODE", value: "new-current" }],
    };
    const saved = await page.request.put(`${path}/draft`, {
      headers,
      data: { baseRevisionId: revision.id, specification },
    });
    expect(saved.status()).toBe(200);
    await page
      .getByRole("button", { name: "Reload current", exact: true })
      .click();
    await expect(page.getByText("Current revision loaded.")).toBeVisible();
    await page.getByText("Advanced specification", { exact: true }).click();
    const editor = page.getByLabel("Specification (JSON)");
    await expect(editor).toHaveValue(JSON.stringify(specification, null, 2));
  } finally {
    release();
    await page.unrouteAll({ behavior: "wait" });
  }
});

test("discard and reload resets a staged volume name without clearing unrelated form edits", async ({
  page,
}) => {
  const { path } = await openEditor(page);
  await page.getByRole("button", { name: "data", exact: true }).click();
  const name = page.getByRole("textbox", { name: "Volume name", exact: true });
  await name.fill("discarded-data");
  await name.press("Enter");
  await expect(name).toBeFocused();
  await expect(name).toHaveValue("discarded-data");
  await page
    .getByRole("button", { name: "Reload current", exact: true })
    .click();
  await page
    .getByRole("alertdialog")
    .getByRole("button", { name: "Discard and reload" })
    .click();
  await expect(page.getByText("Current revision loaded.")).toBeVisible();
  await expect(name).toHaveValue("data");
  await name.fill("still-editing");
  await page.getByText("Advanced specification", { exact: true }).click();
  const editor = page.getByLabel("Specification (JSON)");
  const specification = JSON.parse(await editor.inputValue());
  specification.variables.push({ name: "OTHER", value: "change" });
  await editor.fill(JSON.stringify(specification, null, 2));
  await page
    .getByRole("button", { name: "Save revision", exact: true })
    .click();
  await expect(
    page.getByText("Revision saved.", { exact: true }),
  ).toBeVisible();
  await expect(name).toHaveValue("still-editing");
  await name.fill("data");
  await page.getByRole("button", { name: "Stage volume name" }).click();
  await expect(
    page.getByRole("button", { name: "Save revision", exact: true }),
  ).toBeDisabled();
  const stored: Schema["Revision"] = await (
    await page.request.get(`${path}/draft`)
  ).json();
  expect(stored.specification.volumes?.[0].name).toBe("data");
});
