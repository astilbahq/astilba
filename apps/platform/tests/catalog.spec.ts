import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import { managedDatabase } from "./fixtures/managed-database";

test("catalog and named connections stage a private revision with secret references", async ({
  page,
  request,
}, info) => {
  const f = await managedDatabase(
    page,
    request,
    `Catalog ${info.project.name}`,
  );
  await page.goto(
    `/?workspace=${f.ws}&project=${f.project.id}&environment=${f.env.id}`,
  );
  await page.getByText("Add from service catalog", { exact: true }).click();
  const recipe = page.getByLabel("Service recipe");
  await expect(recipe).toBeVisible();
  await recipe.selectOption("nginx");
  await expect(
    page.getByText("Backup · not applicable", { exact: true }),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "Stage Nginx recipe", exact: true })
    .click();
  await recipe.selectOption("valkey-cache");
  await expect(
    page.getByText(/Data is intentionally not persisted/),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "Stage Valkey cache recipe", exact: true })
    .click();
  await page.getByRole("button", { name: "Service list", exact: true }).click();
  await page.getByRole("button", { name: "Nginx", exact: true }).click();
  const properties = page.getByRole("region", {
    name: "Properties for Nginx",
    exact: true,
  });
  await properties.getByText("Variables", { exact: true }).click();
  const variable = properties.getByRole("form", {
    name: "Add service variable",
  });
  await variable.getByLabel("Variable name", { exact: true }).fill("TOKEN");
  await variable.getByLabel("Or use secret version").selectOption(f.secret.id);
  await variable
    .getByRole("button", { name: "Stage variable", exact: true })
    .click();
  await properties.getByText("Service outputs", { exact: true }).click();
  const output = properties.getByRole("form", {
    name: "Define service output",
  });
  await output.getByLabel("Output name").fill("AUTH");
  await output.getByLabel("Output type").selectOption("variable");
  await output
    .getByRole("combobox", { name: "Source variable", exact: true })
    .selectOption("TOKEN");
  await output
    .getByRole("button", { name: "Stage output", exact: true })
    .click();
  await page.getByText("Connections", { exact: true }).click();
  const binding = page.getByRole("form", { name: "Create service binding" });
  const connect = async (
    source: string,
    target: string,
    sourceOutput: string,
    name: string,
  ) => {
    await binding
      .getByRole("combobox", { name: "Source service", exact: true })
      .selectOption({ label: source });
    await binding
      .getByRole("combobox", { name: "Source output", exact: true })
      .selectOption(sourceOutput);
    await binding
      .getByRole("combobox", { name: "Receiving service", exact: true })
      .selectOption({ label: target });
    await binding.getByLabel("Variable name", { exact: true }).fill(name);
    await binding
      .getByRole("button", { name: "Stage binding", exact: true })
      .click();
  };
  await connect("Valkey cache", "Nginx", "URL", "CACHE_URL");
  await connect("Nginx", "Valkey cache", "AUTH", "AUTH_TOKEN");
  await connect("Valkey cache", "Nginx", "HOST", "PEER");
  await connect("Nginx", "Valkey cache", "HOST", "PEER");
  await binding
    .getByRole("button", { name: "Stage binding", exact: true })
    .click();
  await expect(
    page.getByRole("alert").filter({ hasText: "already has this variable" }),
  ).toBeVisible();
  await binding.getByLabel("Variable name", { exact: true }).fill("EXTRA_HOST");
  await binding
    .getByRole("button", { name: "Stage binding", exact: true })
    .click();
  const operationsBefore = await (
    await page.request.get(`${f.prefix}/operations`)
  ).json();
  await page
    .getByRole("button", { name: "Save revision", exact: true })
    .click();
  await expect(
    page.getByText("Revision saved.", { exact: true }),
  ).toBeVisible();
  const saved = await (await page.request.get(`${f.path}/draft`)).json();
  expect(saved.specification.applications).toHaveLength(3);
  expect(saved.specification.bindings).toHaveLength(5);
  expect(saved.specification.routes ?? []).toHaveLength(0);
  const nginx = saved.specification.applications.find(
    (a: { name: string }) => a.name === "Nginx",
  );
  expect(nginx.variables[0]).toEqual({
    name: "TOKEN",
    secretVersionId: f.secret.id,
  });
  expect(JSON.stringify(saved)).not.toContain("browser-database-canary");
  expect(await page.content()).not.toContain("browser-database-canary");
  expect(
    await (await page.request.get(`${f.prefix}/operations`)).json(),
  ).toHaveLength(operationsBefore.length);
  await page.setViewportSize({ width: 360, height: 800 });
  const violations = await new AxeBuilder({ page })
    .include('[aria-label="Visual configuration"]')
    .analyze();
  expect(violations.violations).toEqual([]);
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
  ).toBeTruthy();
  await page
    .getByText("Add from service catalog", { exact: true })
    .locator("..")
    .screenshot({
      path: `../../.artifacts/plat30/catalog-${info.project.name}.png`,
    });
});
