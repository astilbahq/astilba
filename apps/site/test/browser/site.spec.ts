import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";

declare global {
  interface Window {
    __setAstilbaFinePointer: (value: boolean) => void;
  }
}

const expectNoAxeViolations = async (page: Page): Promise<void> => {
  const results = await new AxeBuilder({ page }).analyze();

  expect(results.violations).toEqual([]);
};

const resolveCssColor = async (
  page: Page,
  customProperty: string
): Promise<string> => {
  const value = await page.evaluate(
    (property) =>
      getComputedStyle(document.documentElement)
        .getPropertyValue(property)
        .trim(),
    customProperty
  );

  const hex = /^#([\da-f]{2})([\da-f]{2})([\da-f]{2})$/i.exec(value);
  if (!hex) {
    return value;
  }

  const [, red, green, blue] = hex;
  return `rgb(${Number.parseInt(red, 16)}, ${Number.parseInt(
    green,
    16
  )}, ${Number.parseInt(blue, 16)})`;
};

test("right-clicking the Astilba brand reveals the interface showcase", async ({
  page,
}) => {
  await page.addInitScript(() => {
    const query = "(hover: hover) and (pointer: fine)";
    const originalMatchMedia = window.matchMedia.bind(window);
    const finePointer = originalMatchMedia(query);
    let matches = false;

    Object.defineProperty(finePointer, "matches", {
      configurable: true,
      get: () => matches,
    });
    window.matchMedia = (value) =>
      value === query ? finePointer : originalMatchMedia(value);
    window.__setAstilbaFinePointer = (value) => {
      matches = value;
    };
  });
  await page.route("https://ui.astilba.com/", async (route) => {
    await route.fulfill({
      body: "<title>Astilba Interface</title>",
      contentType: "text/html",
      status: 200,
    });
  });
  await page.goto("/");

  const brand = page.getByRole("link", { name: "Astilba home" });
  await expect(brand).toHaveAttribute("href", "/");
  await brand.click({ button: "right" });
  await expect(page).toHaveURL("/");

  await page.evaluate(() => {
    window.__setAstilbaFinePointer(true);
  });
  await brand.click({ button: "right" });

  await expect(page).toHaveURL("https://ui.astilba.com/");
});

test("the public home distinguishes the alpha and preview products", async ({
  page,
}) => {
  await page.goto("/");

  await expect(
    page.getByRole("heading", {
      level: 1,
      name: "Infrastructure that tells you where its guarantees end.",
    })
  ).toBeVisible();
  await expect(page.getByText("0.3.0 is a public alpha")).toBeVisible();
  await expect(
    page.getByText("Cache remains a development preview")
  ).toBeVisible();
  const configure = page
    .getByRole("link", { name: "Configure a Node application" })
    .first();
  await expect(configure).toHaveAttribute("href", "/docs/env/quickstart/");
  await configure.hover();
  await expect(configure).toHaveCSS(
    "background-color",
    await resolveCssColor(page, "--astilba-colors-surface-action-primary-hover")
  );
  await expect(configure).toHaveCSS(
    "color",
    await resolveCssColor(page, "--astilba-colors-ink-on-primary")
  );
  await expect(
    page.getByRole("link", { name: "View docs source" })
  ).toHaveAttribute("href", "https://github.com/astilbahq/docs");
  await expect(
    page.locator('a[href*="github.com/astilbahq/cache"]')
  ).toHaveCount(0);
  await expectNoAxeViolations(page);
});

test("the Env page presents the public-alpha boundary", async ({ page }) => {
  await page.goto("/env/");

  await expect(
    page.getByRole("heading", {
      level: 1,
      name: "Configure once; expose only what each artifact needs.",
    })
  ).toBeVisible();
  await expect(page.getByText("@astilba/env", { exact: true })).toBeVisible();
  await expect(
    page.getByText("0.3.0 · public alpha · local-first")
  ).toBeVisible();
  await expect(
    page.getByRole("link", { name: "View the source" })
  ).toHaveAttribute("href", "https://github.com/astilbahq/env");
  await expect(
    page.getByRole("heading", {
      level: 2,
      name: "Public for evaluation; deliberately not stable",
    })
  ).toBeVisible();
  await expect(page.getByText(/@astilba\/env\/next/)).toHaveCount(0);
  await expectNoAxeViolations(page);
});

test("the Cache page never presents an installation path", async ({ page }) => {
  await page.goto("/cache/");

  await expect(
    page.getByRole("heading", {
      level: 1,
      name: "Cache expensive server work without hiding the hard parts.",
    })
  ).toBeVisible();
  await expect(page.getByText("No npm package")).toBeVisible();
  await expect(page.getByText(/pnpm add|npm install/)).toHaveCount(0);
  await expect(
    page.getByRole("link", { name: "Inspect the docs source" })
  ).toHaveAttribute("href", "https://github.com/astilbahq/docs");
  await expect(
    page.locator('a[href*="github.com/astilbahq/cache"]')
  ).toHaveCount(0);
  await expectNoAxeViolations(page);
});

test("theme state persists across public-site pages", async ({ page }) => {
  await page.goto("/");
  const mobileMenu = page.locator("[data-mobile-menu-open]");

  await expect(mobileMenu).toHaveCSS("inline-size", "40px");
  await expect(mobileMenu).toHaveCSS("block-size", "40px");
  await expect(mobileMenu).toHaveCSS("padding-inline-start", "0px");
  await expect(mobileMenu).toHaveCSS("padding-inline-end", "0px");
  await expect(mobileMenu.locator("svg")).toHaveCSS("width", "18px");
  await expect(mobileMenu.locator("svg")).toHaveCSS("height", "18px");
  await page.getByRole("button", { name: "Switch to light theme" }).click();

  await expect(page.locator("html")).toHaveAttribute("data-theme", "light");
  await expect
    .poll(() => page.evaluate(() => localStorage.getItem("starlight-theme")))
    .toBe("light");

  await page.goto("/env/");
  await expect(page.locator("html")).toHaveAttribute("data-theme", "light");
});

test("the mobile menu restores focus and the layout does not overflow", async ({
  page,
}) => {
  await page.goto("/");

  const opener = page.getByRole("button", { name: "Open navigation" });

  if (await opener.isVisible()) {
    await opener.click();
    await expect(
      page.getByRole("dialog", { name: "Navigation" })
    ).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(opener).toBeFocused();
  } else {
    await expect(opener).toBeHidden();
    await expect(
      page.getByRole("navigation", { name: "Primary" })
    ).toBeVisible();
  }

  const dimensions = await page.evaluate(() => ({
    clientWidth: document.documentElement.clientWidth,
    scrollWidth: document.documentElement.scrollWidth,
  }));
  expect(dimensions.scrollWidth).toBe(dimensions.clientWidth);
});
