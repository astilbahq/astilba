import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import { gotoNextStory } from "./story-render";

const story = (id: string, globals = "") =>
  `/iframe.html?id=${id}&viewMode=story&globals=a11y.manual:!true${globals}`;

for (const state of ["preview-template", "managed-preview", "viewer"]) {
  test(`${state} does not offer terminal sessions`, async ({ page }) => {
    await page.goto(story(`pages-environment-detail--${state}`));
    await page.getByRole("tab", { name: "Settings", exact: true }).click();
    await expect(
      page.getByRole("heading", { name: "Secrets", exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole("heading", { name: "Terminal sessions", exact: true }),
    ).toHaveCount(0);
  });
}

test("ending a session returns focus and keeps it when cancelled", async ({
  page,
}) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.goto(story("pages-terminal-sessions--open-sessions-for-owner"));
  await page.getByRole("tab", { name: "Settings", exact: true }).click();
  const end = page.getByRole("button", {
    name: "End session on api, instance 1",
    exact: true,
  });
  await end.focus();
  await page.keyboard.press("Enter");
  const dialog = page.getByRole("alertdialog");
  await expect(dialog).toContainText("End this terminal session?");
  await page.keyboard.press("Escape");
  await expect(dialog).toHaveCount(0);
  await expect(end).toBeFocused();
});

for (const theme of ["light", "dark"]) {
  test(`terminal sessions fit desktop, mobile and RTL in ${theme}`, async ({
    page,
  }) => {
    await page.emulateMedia({ reducedMotion: "reduce" });
    // Roles rather than names, because the RTL pseudolocale rewrites the copy.
    const pages = [
      {
        id: "pages-terminal-sessions--open-sessions-for-owner",
        ready: async () => {
          await page.getByRole("tab").last().click();
          await expect(page.getByRole("checkbox").first()).toBeVisible();
          await expect(page.locator("li time").first()).toBeVisible();
        },
      },
      {
        id: "pages-terminal-session-history--history",
        ready: async () => {
          await expect(page.getByRole("table")).toBeVisible();
        },
      },
    ];
    for (const { id, ready } of pages) {
      for (const { width, locale } of [
        { width: 1440, locale: "" },
        { width: 375, locale: "" },
        { width: 320, locale: ";locale:en-XB" },
      ]) {
        await page.setViewportSize({ width, height: 800 });
        await gotoNextStory(page, story(id, `;theme:${theme}${locale}`));
        await ready();
        expect(
          await page.evaluate(
            () =>
              document.documentElement.scrollWidth -
              document.documentElement.clientWidth,
          ),
        ).toBeLessThanOrEqual(0);
        const audit = new AxeBuilder({ page }).exclude(
          "[data-base-ui-focus-guard]",
        );
        expect((await audit.analyze()).violations).toEqual([]);
      }
    }
  });
}
