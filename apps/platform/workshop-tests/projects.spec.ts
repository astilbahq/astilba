import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";

for (const theme of ["light", "dark"]) {
  test(`project overview and separate creation fit desktop, mobile and RTL in ${theme}`, async ({
    page,
  }, info) => {
    await page.emulateMedia({ reducedMotion: "reduce" });
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto(
      `/iframe.html?id=pages-project-detail--populated&viewMode=story&globals=theme:${theme};a11y.manual:!true`,
    );
    await expect(page.getByRole("heading", { name: "Website" })).toBeVisible();
    const create = page.getByRole("link", { name: "New environment" });
    const background = await create.evaluate(
      (element) => getComputedStyle(element).backgroundColor,
    );
    await create.hover();
    await expect(create).not.toHaveCSS("background-color", background);
    await page.mouse.move(0, 0);
    await page.screenshot({
      path: info.outputPath(`project-${theme}-desktop.png`),
      fullPage: true,
    });
    await page.setViewportSize({ width: 1280, height: 720 });
    await page.screenshot({
      path: info.outputPath(`project-${theme}-user-1280.png`),
      fullPage: true,
    });
    await page.setViewportSize({ width: 390, height: 844 });
    await page.screenshot({
      path: info.outputPath(`project-${theme}-mobile.png`),
      fullPage: true,
    });
    await create.click();
    await expect(
      page.getByRole("heading", { name: "New environment" }),
    ).toBeVisible();
    await expect(page.getByRole("main")).toBeFocused();
    const name = page.getByRole("textbox", { name: "Environment name" });
    await name.fill("Staging");
    await page.screenshot({
      path: info.outputPath(`creation-${theme}-mobile.png`),
      fullPage: true,
    });
    await page.setViewportSize({ width: 1440, height: 900 });
    const cancel = page.getByRole("link", { name: "Cancel", exact: true });
    await cancel.hover();
    await expect(cancel).not.toHaveCSS("background-color", "rgba(0, 0, 0, 0)");
    await page.mouse.move(0, 0);
    await page.screenshot({
      path: info.outputPath(`creation-${theme}-desktop.png`),
      fullPage: true,
    });
    await page.setViewportSize({ width: 1280, height: 720 });
    await page.screenshot({
      path: info.outputPath(`creation-${theme}-user-1280.png`),
      fullPage: true,
    });
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
    await page.setViewportSize({ width: 320, height: 844 });
    for (const id of [
      "pages-project-detail--long-names",
      "pages-new-environment--visual",
    ]) {
      await page.goto(
        `/iframe.html?id=${id}&viewMode=story&globals=theme:${theme};locale:en-XB;a11y.manual:!true`,
      );
      await expect(page.locator("html")).toHaveAttribute("dir", "rtl");
      await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
      expect(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= innerWidth,
        ),
      ).toBe(true);
      expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
      await page.screenshot({
        path: info.outputPath(`${id}-${theme}-rtl-320.png`),
        fullPage: true,
      });
    }
  });
}
