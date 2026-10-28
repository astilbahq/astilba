import type { Page } from "@playwright/test";

export async function openEnvironmentSection(
  page: Page,
  section: "Configuration" | "Deployments" | "Previews" | "Settings",
) {
  await page.getByRole("tab", { name: section, exact: true }).click();
}
