import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import { signInAsOwner } from "./fixtures/owner-sign-in";

test("an owner allows a private build destination but never cloud metadata", async ({
  page,
}, info) => {
  await page.goto("/");
  await signInAsOwner(page);
  await expect(
    page.getByRole("heading", { name: "Projects", exact: true }),
  ).toBeVisible();
  const session = await (await page.request.get("/api/v1/session")).json();
  const ws = session.workspaces[0].id;
  const name = `Build network ${info.project.name}`;
  const created = await page.request.post(`/api/v1/workspaces/${ws}/servers`, {
    headers: {
      "X-CSRF-Token": session.csrfToken,
      Origin: "http://127.0.0.1:15181",
    },
    data: { name, role: "build" },
  });
  expect(created.ok()).toBe(true);
  await page.getByRole("link", { name: "Servers", exact: true }).click();
  const server = page.getByRole("article").filter({
    has: page.getByRole("heading", { name, exact: true }),
  });
  await server.getByText("Build network", { exact: true }).click();
  await expect(server).toContainText("No private destinations are allowed.");
  const form = server.getByRole("form", { name: `Build network for ${name}` });
  const field = form.getByLabel("Allowed private destinations");
  const save = form.getByRole("button", { name: "Save build network" });

  await field.fill("10.0.4.12:443\n10.0.5.0/24");
  await save.click();
  await expect(form.getByRole("alert")).toContainText("Line 2, “10.0.5.0/24”");
  await expect(field).toHaveValue("10.0.4.12:443\n10.0.5.0/24");

  await field.fill("10.0.4.12:443\n169.254.169.254:80");
  await save.click();
  await expect(form.getByRole("alert")).toContainText(
    "cloud instance metadata",
  );
  await expect(server).toContainText("No private destinations are allowed.");

  await field.fill("10.0.5.0/24:8081\n10.0.4.12:443");
  await save.click();
  await expect(server).toContainText(
    "Also allowed: 10.0.4.12:443, 10.0.5.0/24:8081.",
  );
  await expect(form.getByRole("alert")).toHaveCount(0);
  const results = await new AxeBuilder({ page }).include("main").analyze();
  expect(results.violations).toEqual([]);
});
