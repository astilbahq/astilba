import { expect, test } from "@playwright/test";

test("selected tabs stay visible in forced colours after focus enters a panel", async ({
  page,
}, info) => {
  await page.emulateMedia({ forcedColors: "active", reducedMotion: "reduce" });
  await page.goto(
    "/iframe.html?id=controls-tabs--default&viewMode=story&globals=a11y.manual:!true",
  );
  test.skip(
    !(await page.evaluate(() => matchMedia("(forced-colors: active)").matches)),
    "This browser cannot emulate forced colours.",
  );
  for (const name of ["Overview", "Configuration"]) {
    await page.getByRole("tab", { name, exact: true }).click();
    const selected = page.getByRole("tab", {
      name,
      exact: true,
      selected: true,
    });
    const panel = page.getByRole("tabpanel", { name, exact: true });
    await panel.focus();
    await expect(panel).toBeFocused();
    const paint = await selected.evaluate((element) => {
      const indicator = getComputedStyle(element, "::after");
      return {
        content: indicator.content,
        marker: indicator.backgroundColor,
        surface: getComputedStyle(document.body).backgroundColor,
      };
    });
    expect(paint.content).not.toBe("none");
    expect(paint.marker).not.toBe(paint.surface);
  }
  await page.screenshot({
    path: info.outputPath("tabs-forced-colours.png"),
    fullPage: true,
  });
});
