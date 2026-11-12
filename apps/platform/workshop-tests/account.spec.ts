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
    // Two-factor authentication's last action opens its confirmation dialog.
    await page
      .getByRole("main")
      .getByRole("region")
      .nth(-2)
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

for (const theme of ["light", "dark"]) {
  test(`Identity confirmation fits expanded RTL at 320px in ${theme}`, async ({
    page,
  }, info) => {
    await page.setViewportSize({ width: 320, height: 720 });
    await page.goto(
      `/iframe.html?id=pages-account-security--authenticator-enabled&viewMode=story&globals=theme:${theme};locale:en-XB;a11y.manual:!true`,
    );
    await expect(page.locator("html")).toHaveAttribute("dir", "rtl");
    // Identity confirmation is the last section, with one action.
    await page
      .getByRole("main")
      .getByRole("region")
      .last()
      .getByRole("button")
      .click();
    const dialog = page.getByRole("alertdialog");
    await expect(dialog).toBeVisible();
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
      path: info.outputPath(`identity-confirmation-${theme}-rtl320.png`),
      fullPage: true,
    });
  });
}

test("Identity confirmation keeps keyboard focus inside and returns it", async ({
  page,
}) => {
  await page.goto(
    "/iframe.html?id=pages-account-security--authenticator-enabled&viewMode=story&globals=a11y.manual:!true",
  );
  const trigger = page.getByRole("button", { name: "Confirm identity" });
  await trigger.focus();
  await page.keyboard.press("Enter");
  const dialog = page.getByRole("alertdialog", {
    name: "Confirm your identity",
  });
  await expect(dialog).toBeVisible();
  const code = dialog.getByRole("textbox").first();
  await expect(code).toBeFocused();
  const order = [
    dialog.getByRole("button", { name: "Use a recovery code" }),
    dialog.getByRole("button", { name: "Cancel" }),
    dialog.getByRole("button", { name: "Confirm" }),
    code,
  ];
  for (const control of order) {
    await page.keyboard.press("Tab");
    await expect(control).toBeFocused();
  }
  await page.keyboard.press("Shift+Tab");
  await expect(order[2]).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(dialog).toBeHidden();
  await expect(trigger).toBeFocused();
});
