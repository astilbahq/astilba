import { randomUUID } from "node:crypto";
import AxeBuilder from "@axe-core/playwright";
import { expect, type Page, test } from "@playwright/test";
import { authFixtureSQL } from "./auth-fixture";

async function login(page: Page) {
  await page.goto("/login");
  await page.getByLabel("Email", { exact: true }).fill("owner@example.test");
  await page
    .getByLabel("Password", { exact: true })
    .fill("correct horse battery staple");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "Projects", exact: true }),
  ).toBeVisible();
  return (await page.request.get("/api/v1/session")).json();
}

async function selectWorkspace(page: Page, name: string) {
  await page.getByRole("button", { name: /^Workspace:/ }).click();
  await page.getByRole("menuitem", { name, exact: true }).click();
}

test("workspace selection survives sections and reload, and unavailable IDs never silently select another workspace", async ({
  page,
}) => {
  const session = await login(page);
  const name = `Navigation ${randomUUID()}`;
  const response = await page.request.post("/api/v1/workspaces", {
    headers: {
      "X-CSRF-Token": session.csrfToken,
      Origin: new URL(page.url()).origin,
    },
    data: { name },
  });
  expect(response.status()).toBe(201);
  const workspace = await response.json();
  await page.reload();
  await selectWorkspace(page, name);
  for (const label of ["Servers", "Alerts", "Workspace", "Projects"]) {
    await page.getByRole("link", { name: label, exact: true }).click();
    await expect
      .poll(() => new URL(page.url()).searchParams.get("workspace"))
      .toBe(workspace.id);
    await expect(
      page.getByRole("button", { name: `Workspace: ${name}` }),
    ).toBeVisible();
  }
  await page.reload();
  await expect(
    page.getByRole("button", { name: `Workspace: ${name}` }),
  ).toBeVisible();
  const unknown = randomUUID();
  const projectRequests: string[] = [];
  page.on("request", (request) => {
    if (request.url().includes(`/workspaces/${unknown}/`))
      projectRequests.push(request.url());
  });
  await page.goto(`/?workspace=${unknown}`);
  await expect(
    page.getByRole("heading", { name: "Workspace unavailable" }),
  ).toBeVisible();
  await page.getByRole("link", { name: "Servers", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "Workspace unavailable" }),
  ).toBeVisible();
  expect(new URL(page.url()).searchParams.get("workspace")).toBe(unknown);
  expect(projectRequests).toEqual([]);
  await page.getByRole("button", { name: /^Account menu for/ }).click();
  await page
    .getByRole("menuitem", { name: "Account security", exact: true })
    .click();
  await expect(
    page.getByRole("heading", { name: "Account security", exact: true }),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "Change password", exact: true })
    .click();
  await page
    .getByLabel("New password", { exact: true })
    .fill("unsent account draft");
  await selectWorkspace(page, name);
  await expect(page.getByLabel("New password", { exact: true })).toHaveValue(
    "unsent account draft",
  );
  await page.route("**/api/v1/session", async (route) => {
    const response = await route.fetch();
    const current = await response.json();
    await route.fulfill({
      response,
      json: {
        ...current,
        workspaces: [],
      },
    });
  });
  await page.goto(`/?workspace=${workspace.id}`);
  await expect(
    page.getByRole("heading", { name: "Workspace unavailable" }),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: `Workspace: ${name}` }),
  ).toHaveCount(0);
  await page
    .getByRole("link", { name: "Return to projects", exact: true })
    .click();
  await expect(
    page.getByRole("heading", { name: "No workspace yet" }),
  ).toBeVisible();
  await expect(
    page.getByText("Create another workspace", { exact: true }),
  ).toBeVisible();
  expect(new URL(page.url()).searchParams.has("workspace")).toBe(false);
});

test("mobile editor navigation keeps both unsaved-change decisions reachable above the drawer", async ({
  page,
}, info) => {
  const session = await login(page);
  const workspaceId = session.workspaces[0].id;
  const headers = {
    "X-CSRF-Token": session.csrfToken,
    Origin: new URL(page.url()).origin,
  };
  const projectResponse = await page.request.post(
    `/api/v1/workspaces/${workspaceId}/projects`,
    { headers, data: { name: `Mobile editor ${info.project.name}` } },
  );
  expect(projectResponse.status()).toBe(201);
  const project = await projectResponse.json();
  const environmentResponse = await page.request.post(
    `/api/v1/workspaces/${workspaceId}/projects/${project.id}/environments`,
    { headers, data: { name: "Development" } },
  );
  expect(environmentResponse.status()).toBe(201);
  const environment = await environmentResponse.json();
  await page.goto(
    `/?workspace=${workspaceId}&project=${project.id}&environment=${environment.id}`,
  );
  const projectURL = page.url();
  await selectWorkspace(page, session.workspaces[0].name);
  await expect(page).toHaveURL(projectURL);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.getByText("Advanced specification", { exact: true }).click();
  const editor = page.getByLabel("Specification (JSON)");
  const draft = `${await editor.inputValue()}\n `;
  await editor.fill(draft);
  await page.getByRole("button", { name: "Open navigation" }).click();
  const drawer = page.getByRole("dialog", { name: "Navigation" });
  await drawer.getByRole("link", { name: "Servers", exact: true }).click();
  const decision = page.getByRole("alertdialog", { name: "Unsaved changes" });
  await expect(decision).toBeVisible();
  await expect(
    decision.getByRole("button", { name: "Keep editing" }),
  ).toBeFocused();
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  await decision.getByRole("button", { name: "Keep editing" }).click();
  await expect(drawer).toBeVisible();
  await drawer.getByRole("link", { name: "Projects", exact: true }).click();
  await decision.getByRole("button", { name: "Discard and leave" }).click();
  await expect(
    page.getByRole("heading", { name: "Projects", exact: true }),
  ).toBeVisible();
  await expect(drawer).toHaveCount(0);
  await expect(page.getByRole("main")).toBeFocused();
});

test("a real view-only membership opens Projects without creation controls", async ({
  page,
  browser,
}) => {
  const owner = await login(page);
  const workspaceId = owner.workspaces[0].id;
  const id = randomUUID();
  const email = `${id}@example.test`;
  await authFixtureSQL(
    `INSERT INTO accounts(id,email,name,password_hash) SELECT '${id}','${email}','Read-only member',password_hash FROM accounts WHERE email='owner@example.test'; INSERT INTO memberships(workspace_id,account_id,role) VALUES('${workspaceId}','${id}','viewer')`,
  );
  const context = await browser.newContext();
  try {
    const viewer = await context.newPage();
    await viewer.goto("http://127.0.0.1:15181/login");
    await viewer.getByLabel("Email", { exact: true }).fill(email);
    await viewer
      .getByLabel("Password", { exact: true })
      .fill("correct horse battery staple");
    await viewer.getByRole("button", { name: "Sign in", exact: true }).click();
    await expect(
      viewer.getByRole("heading", { name: "Projects", exact: true }),
    ).toBeVisible();
    await expect(
      viewer.getByText(
        "You have view-only access. A workspace owner or member can create projects.",
        { exact: true },
      ),
    ).toBeVisible();
    await expect(
      viewer.getByRole("button", { name: "New project", exact: true }),
    ).toHaveCount(0);
    await expect(
      viewer.getByLabel("Project name", { exact: true }),
    ).toHaveCount(0);
  } finally {
    await context.close();
  }
});
