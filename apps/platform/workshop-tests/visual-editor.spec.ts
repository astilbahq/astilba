import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";

async function fitCanvas(page: Page) {
  await page.getByRole("button", { name: "Fit View", exact: true }).click();
  await expect
    .poll(() =>
      page.locator(".visual-canvas").evaluate((canvas) => {
        const frame = canvas.getBoundingClientRect();
        return Array.from(canvas.querySelectorAll(".react-flow__node")).every(
          (node) => {
            const rect = node.getBoundingClientRect();
            return (
              rect.left >= frame.left &&
              rect.right <= frame.right &&
              rect.top >= frame.top &&
              rect.bottom <= frame.bottom
            );
          },
        );
      }),
    )
    .toBe(true);
  await page.evaluate(() => window.scrollTo(0, 0));
}

for (const theme of ["light", "dark"]) {
  test(`canvas selection, inspector focus and narrow layouts in ${theme}`, async ({
    page,
  }, info) => {
    await page.emulateMedia({ reducedMotion: "reduce" });
    await page.setViewportSize({ width: 1440, height: 1100 });
    await page.goto(
      `/iframe.html?id=pages-environment-detail--canvas&viewMode=story&globals=theme:${theme};a11y.manual:!true`,
    );
    const api = page.getByRole("group", { name: "Configure api", exact: true });
    await expect(api).toBeVisible();
    await api.focus();
    await api.press("Enter");
    const properties = page.getByRole("region", {
      name: "Properties for api",
      exact: true,
    });
    await expect(
      properties.getByRole("textbox", { name: "Service name", exact: true }),
    ).toHaveValue("api");
    for (const [name, width, height] of [
      ["desktop", 1440, 1100],
      ["user-1280", 1280, 900],
    ] as const) {
      await page.setViewportSize({ width, height });
      await fitCanvas(page);
      expect(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= innerWidth,
        ),
      ).toBe(true);
      await page.screenshot({
        path: info.outputPath(`canvas-${theme}-${name}.png`),
        fullPage: true,
      });
    }
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
    await properties.getByRole("button", { name: "Close properties" }).click();
    await expect(
      page.getByRole("group", {
        name: "Services and connections",
        exact: true,
      }),
    ).toBeFocused();
    await expect(properties).toHaveCount(0);
    await page
      .getByRole("button", { name: "Service list", exact: true })
      .click();
    await page.getByRole("button", { name: "api", exact: true }).click();
    await expect(properties).toBeVisible();

    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto(
      `/iframe.html?id=pages-environment-detail--canvas&viewMode=story&globals=theme:${theme};a11y.manual:!true`,
    );
    await expect(
      page.getByRole("button", { name: "Service list", exact: true }),
    ).toHaveAttribute("aria-pressed", "true");
    await page.getByRole("button", { name: "api", exact: true }).click();
    await expect(properties).toBeVisible();
    await expect(
      page.getByRole("button", { name: "api-data", exact: true }),
    ).toBeVisible();
    await page.evaluate(() => window.scrollTo(0, 0));
    await page.screenshot({
      path: info.outputPath(`canvas-${theme}-mobile.png`),
      fullPage: true,
    });
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);

    await page.goto(
      `/iframe.html?id=pages-environment-detail--canvas-long-names&viewMode=story&globals=theme:${theme};locale:en-XB;a11y.manual:!true`,
    );
    await expect(
      page.getByRole("textbox", { name: "Service name", exact: true }),
    ).toHaveValue("customer-facing-international-application-service");
    await expect(page.locator("html")).toHaveAttribute("dir", "rtl");
    await page
      .getByRole("button", { name: "Service list", exact: true })
      .click();
    for (const [name, width] of [
      ["rtl-390", 390],
      ["rtl-320", 320],
    ] as const) {
      await page.setViewportSize({ width, height: 844 });
      await page.evaluate(() => window.scrollTo(0, 0));
      expect(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= innerWidth,
        ),
      ).toBe(true);
      await page.screenshot({
        path: info.outputPath(`canvas-${theme}-${name}.png`),
        fullPage: true,
      });
    }
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  });
}
