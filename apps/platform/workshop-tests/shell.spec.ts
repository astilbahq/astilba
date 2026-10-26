import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import { pseudolocalize } from "../src/i18n/pseudo";
import app from "../src/locales/en/app.json" with { type: "json" };
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
    await page.goto(
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
