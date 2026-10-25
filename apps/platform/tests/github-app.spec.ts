import { createHmac } from "node:crypto";
import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";

test("GitHub App authorization returns across sites, selects a repository and disconnects", async ({
  page,
}, info) => {
  await page.goto("/");
  await page.getByLabel("Email", { exact: true }).fill("owner@example.test");
  await page
    .getByLabel("Password", { exact: true })
    .fill("correct horse battery staple");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await page
    .getByLabel("Project name", { exact: true })
    .fill(`GitHub ${info.project.name}`);
  await page
    .getByRole("button", { name: "Create project", exact: true })
    .click();
  await page
    .getByRole("link", { name: `GitHub ${info.project.name} Open project →` })
    .click();
  await page.getByText("Add an environment", { exact: true }).click();
  await page.getByLabel("Environment name", { exact: true }).fill("Preview");
  await page
    .getByRole("button", { name: "Create environment", exact: true })
    .click();
  await page
    .getByText("Repository, registry and build secret settings", {
      exact: true,
    })
    .click();
  await page
    .getByRole("button", { name: "Continue with GitHub", exact: true })
    .click();
  await expect(page).toHaveURL(/\/github\/callback$/);
  await expect(page.getByText("Authorized as", { exact: false })).toBeVisible();
  await expect(
    page.getByRole("option", { name: "page-one-0 — installation unavailable" }),
  ).toHaveJSProperty("disabled", true);
  await page
    .getByRole("button", { name: "Next installations", exact: true })
    .click();
  await page
    .getByRole("combobox", { name: "GitHub installation", exact: true })
    .selectOption("2");
  await expect(
    page.getByRole("option", {
      name: "team/read-only — administrator access required",
    }),
  ).toHaveJSProperty("disabled", true);
  await page
    .getByRole("combobox", { name: "GitHub repository", exact: true })
    .selectOption("12");
  await page
    .getByLabel("Connection name", { exact: true })
    .fill(`App ${info.project.name}`);
  const axe = await new AxeBuilder({ page }).analyze();
  expect(axe.violations).toEqual([]);
  await page.screenshot({
    path: `../../.artifacts/plat46/github-${info.project.name}-desktop.png`,
    fullPage: true,
  });
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(
    page.getByRole("button", { name: "Connect selected repository" }),
  ).toBeVisible();
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  await page.screenshot({
    path: `../../.artifacts/plat46/github-${info.project.name}-mobile.png`,
    fullPage: true,
  });
  await page
    .getByRole("button", { name: "Connect selected repository" })
    .click();
  await expect(
    page.getByText("Repository connected.", { exact: false }),
  ).toBeVisible();
  const session = await (await page.request.get("/api/v1/session")).json();
  const ws = session.workspaces[0].id;
  const connections = await (
    await page.request.get(`/api/v1/workspaces/${ws}/git-connections`)
  ).json();
  const connection = connections.find(
    (c: { name: string }) => c.name === `App ${info.project.name}`,
  );
  expect(connection.authentication).toBe("github_app");
  expect(JSON.stringify(connections)).not.toContain(
    "github-browser-user-proof",
  );
  // The optional Worker must preserve App webhook bytes and authentication.
  const body = JSON.stringify({
    action: "added",
    installation: { id: 2, app_id: 1 },
  });
  const response = await page.request.post(
    "http://127.0.0.1:54183/hooks/github-app",
    {
      headers: {
        "Content-Type": "application/json",
        "X-GitHub-Event": "installation_repositories",
        "X-GitHub-Delivery": crypto.randomUUID(),
        "X-Hub-Signature-256": `sha256=${createHmac("sha256", "github-browser-webhook-secret-fixture").update(body).digest("hex")}`,
      },
      data: body,
    },
  );
  expect(response.status()).toBe(202);
  const disconnected = await page.request.post(
    `/api/v1/workspaces/${ws}/git-connections/${connection.id}/disconnect`,
    {
      headers: {
        Origin: "http://127.0.0.1:54181",
        "X-CSRF-Token": session.csrfToken,
      },
    },
  );
  expect(disconnected.status()).toBe(204);
  const after = await (
    await page.request.get(`/api/v1/workspaces/${ws}/git-connections`)
  ).json();
  expect(after.find((c: { id: string }) => c.id === connection.id).status).toBe(
    "reauthorization_required",
  );
});
