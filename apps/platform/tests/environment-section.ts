import { expect, type Page } from "@playwright/test";

export async function openEnvironmentSection(
  page: Page,
  section: "Configuration" | "Deployments" | "Previews" | "Settings",
) {
  const tab = page.getByRole("tab", { name: section, exact: true });
  await tab.click();
  await expect(tab).toHaveAttribute("aria-selected", "true");
  // A section's parts load separately and move what is below them as they arrive. A click during
  // that can report success without reaching its target, so the journey starts once they settle.
  // This only orders the journey, so a slow machine should not fail it.
  await expect(
    page.getByRole("region", { name: "Environment content", exact: true }),
  ).not.toHaveAttribute("data-loading", { timeout: 20000 });
}
