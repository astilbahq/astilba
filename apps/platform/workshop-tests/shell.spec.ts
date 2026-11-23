import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import { pseudolocalize } from "../src/i18n/pseudo";
import app from "../src/locales/en/app.json" with { type: "json" };
import { gotoNextStory } from "./story-render";
const messages = pseudolocalize(app) as typeof app;

for (const theme of ["light", "dark"]) {
  test(`shell menus and navigation fit expanded RTL at320px in ${theme}`, async ({
    page,
  }, info) => {
    await page.emulateMedia({ reducedMotion: "reduce" });
    await page.setViewportSize({ width: 1280, height: 900 });
    await page.goto(
      `/iframe.html?id=pages-projects--populated&viewMode=story&globals=theme:${theme};a11y.manual:!true`,
    );
    await expect(
      page.getByRole("heading", { name: "Projects", exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole("link", { name: "Website Open project", exact: true }),
    ).toBeVisible();
    if (theme === "dark")
      await expect(page.locator("html")).toHaveClass(/dark/);
    else await expect(page.locator("html")).not.toHaveClass(/dark/);
    await page.screenshot({
      path: info.outputPath(`projects-${theme}-desktop.png`),
      fullPage: true,
    });
    await page.setViewportSize({ width: 320, height: 844 });
    await page.screenshot({
      path: info.outputPath(`projects-${theme}-mobile.png`),
      fullPage: true,
    });
    await page.emulateMedia({ reducedMotion: "no-preference" });
    await gotoNextStory(
      page,
      `/iframe.html?id=pages-projects--long-names&viewMode=story&globals=theme:${theme};locale:en-XB;a11y.manual:!true`,
    );
    await expect(page.locator("html")).toHaveAttribute("dir", "rtl");
    const open = page.getByRole("button", { name: messages.navigation.open });
    await open.click();
    const drawer = page.getByRole("dialog", {
      name: messages.navigation.label,
    });
    await expect(drawer).toBeVisible();
    await drawer
      .getByRole("button", { name: /Infrastructure and operations/ })
      .click();
    await expect(page.getByRole("menuitem").first()).toBeVisible();
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await page.keyboard.press("Escape");
    await expect(drawer).toBeVisible();
    await expect(drawer).toHaveCSS("opacity", "1");
    expect(
      (
        await new AxeBuilder({ page })
          .exclude("[data-base-ui-focus-guard]")
          .analyze()
      ).violations,
    ).toEqual([]);
    await page.screenshot({
      path: info.outputPath(`shell-${theme}-rtl-mobile.png`),
      fullPage: true,
    });
    await drawer
      .getByRole("button", { name: messages.navigation.close })
      .click();
    await expect(open).toBeFocused();
    await open.click();
    await page.setViewportSize({ width: 1280, height: 900 });
    await drawer
      .getByRole("button", { name: messages.navigation.close })
      .click();
    await expect(page.getByRole("main")).toBeFocused();
    await page.screenshot({
      path: info.outputPath(`shell-${theme}-rtl-desktop.png`),
      fullPage: true,
    });
    await page.setViewportSize({ width: 320, height: 844 });
    await open.click();
    await drawer
      .getByRole("link", { name: messages.navigation.servers })
      .click();
    await expect(
      page.getByRole("heading", { name: "servers", exact: true }),
    ).toBeVisible();
    await expect(drawer).toHaveCount(0);
    await expect(page.getByRole("main")).toBeFocused();
  });
}

// The router resets the scroll position after a navigation, and the shell then moves focus.
// Focusing a main area taller than the screen must not scroll the header, with its navigation
// button, back out of view. The project list is loaded before either navigation to it, so main
// is already that tall when it takes focus.
test("navigating on a small screen keeps the header in view", async ({
  page,
}) => {
  await page.setViewportSize({ width: 320, height: 300 });
  await page.goto(
    "/iframe.html?id=pages-projects--populated&viewMode=story&globals=a11y.manual:!true",
  );
  const main = page.getByRole("main");
  const open = page.getByRole("button", { name: app.navigation.open });
  const website = page.getByRole("link", {
    name: "Website Open project",
    exact: true,
  });
  await website.click();
  await expect(page.getByRole("heading", { name: "Website" })).toBeVisible();
  await main.getByRole("link", { name: "Projects", exact: true }).click();
  await expect(website).toBeVisible();
  await expect(main).toBeFocused();
  await expect(open).toBeInViewport({ ratio: 1 });
  await website.click();
  await expect(page.getByRole("heading", { name: "Website" })).toBeVisible();
  await open.click();
  const drawer = page.getByRole("dialog", { name: app.navigation.label });
  await drawer.getByRole("link", { name: app.navigation.projects }).click();
  await expect(drawer).toHaveCount(0);
  await expect(main).toBeFocused();
  await expect(open).toBeInViewport({ ratio: 1 });
});

// The URL keeps the fragment of the link that led to account security. Only a navigation goes to
// the section it names, not dismissing the drawer later on that page.
for (const dismissal of ["Close", "Escape"])
  test(`dismissing the drawer with its trigger hidden focuses main, not the linked section, with ${dismissal}`, async ({
    page,
  }) => {
    await page.setViewportSize({ width: 820, height: 420 });
    await page.goto(
      "/iframe.html?id=pages-projects--two-factor-required-operator&viewMode=story&globals=a11y.manual:!true",
    );
    const section = page.getByRole("heading", {
      name: "Two-factor authentication",
      exact: true,
    });
    await expect(section).toBeFocused();
    await page.getByRole("button", { name: app.navigation.open }).click();
    const drawer = page.getByRole("dialog", { name: app.navigation.label });
    await expect(drawer).toBeVisible();
    await page.setViewportSize({ width: 1180, height: 420 });
    if (dismissal === "Close")
      await drawer.getByRole("button", { name: app.navigation.close }).click();
    else await page.keyboard.press("Escape");
    await expect(drawer).toHaveCount(0);
    await expect(page.getByRole("main")).toBeFocused();
  });
