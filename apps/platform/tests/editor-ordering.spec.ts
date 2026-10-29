import { expect, type Page, test } from "@playwright/test";
import { openEnvironmentSection } from "./environment-section";

async function openEditor(
  page: Page,
  configurationOwner: "dashboard" | "compose",
) {
  await page.goto("/");
  await page.getByLabel("Email", { exact: true }).fill("owner@example.test");
  await page
    .getByLabel("Password", { exact: true })
    .fill("correct horse battery staple");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "Projects", exact: true }),
  ).toBeVisible();
  const session = await (await page.request.get("/api/v1/session")).json();
  const headers = {
    "X-CSRF-Token": session.csrfToken,
    Origin: new URL(page.url()).origin,
  };
  const post = async (path: string, data: unknown) => {
    const response = await page.request.post(path, { headers, data });
    expect(response.ok(), await response.text()).toBeTruthy();
    return response.json();
  };
  const workspace = session.workspaces[0].id;
  const project = await post(`/api/v1/workspaces/${workspace}/projects`, {
    name: `Editor ordering ${crypto.randomUUID()}`,
  });
  const environment = await post(
    `/api/v1/workspaces/${workspace}/projects/${project.id}/environments`,
    { name: "Ordering", configurationOwner },
  );
  const path = `/api/v1/workspaces/${workspace}/projects/${project.id}/environments/${environment.id}`;
  await page.goto(
    `/?workspace=${workspace}&project=${project.id}&environment=${environment.id}`,
  );
  await openEnvironmentSection(page, "Configuration");
  return path;
}

test("reload retains edits made while the current revision is loading", async ({
  page,
}) => {
  const path = await openEditor(page, "dashboard");
  await page.getByText("Advanced specification", { exact: true }).click();
  const editor = page.getByRole("textbox", { name: "Specification (JSON)" });
  const specification = JSON.parse(await editor.inputValue());
  let release!: () => void;
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });
  let ready!: () => void;
  const responseReady = new Promise<void>((resolve) => {
    ready = resolve;
  });
  await page.route(`**${path}/draft`, async (route) => {
    if (route.request().method() !== "GET") return route.continue();
    const response = await route.fetch();
    expect(response.status()).toBe(200);
    ready();
    await held;
    await route.fulfill({ response });
  });
  try {
    await page
      .getByRole("button", { name: "Reload current", exact: true })
      .click();
    await responseReady;
    specification.variables.push({ name: "NEWER_DRAFT", value: "retained" });
    const edited = JSON.stringify(specification, null, 2);
    await editor.fill(edited);
    release();
    await expect(
      page.getByText(
        "Your newer edits were retained. Reload again to discard them and load the current revision.",
        { exact: true },
      ),
    ).toBeVisible();
    await expect(editor).toHaveValue(edited);
    await page.screenshot({
      path: test.info().outputPath("reload-retained.png"),
      fullPage: true,
    });
    const save = page.getByRole("button", {
      name: "Save revision",
      exact: true,
    });
    await expect(save).toBeEnabled();
    await save.click();
    await expect(
      page.getByText("Revision saved.", { exact: true }),
    ).toBeVisible();
    const stored = await (await page.request.get(`${path}/draft`)).json();
    expect(stored.specification.variables).toContainEqual({
      name: "NEWER_DRAFT",
      value: "retained",
    });
  } finally {
    release();
    await page.unroute(`**${path}/draft`);
  }
});

test("only the latest Compose image preview can be staged", async ({
  page,
}) => {
  const path = await openEditor(page, "compose");
  const editor = page.getByRole("textbox", {
    name: "Source compose.yaml",
    exact: true,
  });
  await editor.fill(
    "# preserve this comment\nservices:\n  web:\n    image: busybox:1.37.0\n",
  );
  await expect(
    page.getByText("Source validated.", { exact: true }),
  ).toBeVisible();
  await page
    .getByRole("combobox", { name: "Source service", exact: true })
    .selectOption("web");
  let release!: () => void;
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });
  let ready!: () => void;
  const responseReady = new Promise<void>((resolve) => {
    ready = resolve;
  });
  await page.route(`**${path}/compose/preview`, async (route) => {
    const request = route.request();
    if (
      request.method() !== "POST" ||
      request.postDataJSON().edit?.image !== "busybox:stable"
    )
      return route.continue();
    const response = await route.fetch();
    expect(response.status()).toBe(200);
    ready();
    await held;
    await route.fulfill({ response });
  });
  const image = page.getByLabel("New image reference", { exact: true });
  const preview = page.getByRole("button", {
    name: "Preview image edit",
    exact: true,
  });
  const proposal = page.getByRole("region", { name: "Edit image in source" });
  const settled = () =>
    page.waitForResponse(
      (response) =>
        response.url().endsWith("/session") &&
        response.request().method() === "GET",
    );
  try {
    await image.fill("busybox:stable");
    await preview.click();
    await responseReady;
    await image.fill("busybox:latest");
    const latestSettled = settled();
    await preview.click();
    await latestSettled;
    await expect(proposal).toContainText("busybox:latest");
    const olderSettled = settled();
    release();
    await olderSettled;
    await expect(proposal).toContainText("busybox:latest");
    await expect(proposal).not.toContainText("busybox:stable");
    await page
      .getByRole("button", { name: "Stage this source edit", exact: true })
      .click();
    await expect(editor).toContainText("image: busybox:latest");
    await expect(editor).toContainText("# preserve this comment");
  } finally {
    release();
    await page.unroute(`**${path}/compose/preview`);
  }
});
