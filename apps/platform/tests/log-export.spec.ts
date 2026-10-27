import { readFile } from "node:fs/promises";
import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";

test("download log snapshots with an explicit window and empty result", async ({
  page,
}, info) => {
  await page.goto("/");
  await page.getByLabel("Email", { exact: true }).fill("owner@example.test");
  await page
    .getByLabel("Password", { exact: true })
    .fill("correct horse battery staple");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await page.getByRole("button", { name: "New project", exact: true }).click();
  await page
    .getByLabel("Project name", { exact: true })
    .fill(`Logs ${info.project.name}`);
  await page
    .getByRole("button", { name: "Create project", exact: true })
    .click();
  await page
    .getByRole("link", { name: `Logs ${info.project.name} Open project` })
    .click();
  await page.getByText("Add an environment", { exact: true }).click();
  await page.getByLabel("Environment name", { exact: true }).fill("Logs");
  await page
    .getByRole("button", { name: "Create environment", exact: true })
    .click();
  const section = page.getByRole("region", { name: "Log export" });
  await section.getByLabel("Log export window").selectOption("6");
  await section.getByRole("button", { name: "Download logs" }).focus();
  const downloaded = page.waitForEvent("download");
  await page.keyboard.press("Enter");
  const download = await downloaded;
  expect(download.suggestedFilename()).toMatch(
    /^astilba-logs-[a-f0-9-]+\.json$/,
  );
  const file = info.outputPath("logs.json");
  await download.saveAs(file);
  const data = JSON.parse(await readFile(file, "utf8"));
  expect(data.entries).toEqual([]);
  expect(data.coverage).toBe("retained-snapshots");
  expect(Date.parse(data.windowEnd) - Date.parse(data.windowStart)).toBe(
    6 * 3600000,
  );
  await expect(section.getByRole("status")).toContainText(
    "no retained log snapshots",
  );
  expect(
    (
      await new AxeBuilder({ page })
        .include('[aria-label="Log export"]')
        .analyze()
    ).violations,
  ).toEqual([]);
});
