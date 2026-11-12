import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import { gotoNextStory } from "./story-render";

const story = (id: string, globals = "") =>
  `/iframe.html?id=${id}&viewMode=story&globals=a11y.manual:!true${globals}`;

for (const theme of ["light", "dark"]) {
  test(`secret access records fit desktop, mobile and RTL in ${theme}`, async ({
    page,
  }) => {
    await page.emulateMedia({ reducedMotion: "reduce" });
    for (const { width, locale } of [
      { width: 1440, locale: "" },
      { width: 375, locale: "" },
      { width: 320, locale: ";locale:en-XB" },
    ]) {
      await page.setViewportSize({ width, height: 800 });
      await gotoNextStory(
        page,
        story("pages-secret-access--records", `;theme:${theme}${locale}`),
      );
      // Roles rather than names, because the RTL pseudolocale rewrites the copy.
      await expect(page.getByRole("table")).toBeVisible();
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
  });
}
