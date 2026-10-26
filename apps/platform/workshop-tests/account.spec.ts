import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";

for (const theme of ["light", "dark"]) {
  test(`Account security and confirmation fit expanded RTL at320px in ${theme}`, async ({
    page,
  }, info) => {
    await page.setViewportSize({ width: 320, height: 720 });
    await page.goto(
      `/iframe.html?id=pages-account-security--authenticator-enabled&viewMode=story&globals=theme:${theme};locale:en-XB;a11y.manual:!true`,
    );
    await expect(page.locator("html")).toHaveAttribute("dir", "rtl");
    await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await page
      .getByRole("main")
      .getByRole("region")
      .last()
      .getByRole("button")
      .last()
      .click();
    const dialog = page.getByRole("alertdialog");
    await expect(dialog).toBeVisible();
    // Measure contrast after the entrance fade has settled.
    await expect(dialog).toHaveCSS("opacity", "1");
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    const audit = new AxeBuilder({ page }).exclude(
      "[data-base-ui-focus-guard]",
    );
    expect((await audit.analyze()).violations).toEqual([]);
    await page.screenshot({
      path: info.outputPath(`account-dialog-${theme}-rtl320.png`),
      fullPage: true,
    });
  });
}
