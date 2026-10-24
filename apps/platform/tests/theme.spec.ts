import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";

test("a system event cannot discard a manual choice awaiting its snapshot", async ({
  page,
}, testInfo) => {
  test.skip(
    testInfo.project.use.reducedMotion === "reduce",
    "Reduced motion applies choices immediately.",
  );
  await page.emulateMedia({ colorScheme: "light" });
  await page.addInitScript(() => {
    // Hold only the browser scheduling boundary to reproduce the event ordering.
    const pending: (() => void)[] = [];
    Object.defineProperty(document, "startViewTransition", {
      value: (update: () => void) => {
        let finish!: () => void;
        const finished = new Promise<void>((resolve) => {
          finish = resolve;
        });
        pending.push(() => {
          update();
          finish();
        });
        return { finished, skipTransition() {} };
      },
    });
    document.addEventListener("release-theme-updates", () => {
      for (const update of pending.splice(0)) update();
    });
  });
  await page.goto("/login");
  await page.getByRole("button", { name: "Appearance", exact: true }).click();
  const dark = page.getByRole("menuitemradio", { name: "Dark", exact: true });
  await dark.click();
  await page.evaluate(() => {
    matchMedia("(prefers-color-scheme: dark)").addEventListener(
      "change",
      () => {
        document.documentElement.dataset.testSystemChanged = "";
      },
      { once: true },
    );
  });
  await page.emulateMedia({ colorScheme: "dark" });
  await expect(page.locator("html")).toHaveAttribute(
    "data-test-system-changed",
  );
  await page.evaluate(() => {
    document.dispatchEvent(new Event("release-theme-updates"));
  });
  await expect(dark).toBeChecked();
  await expect(page.locator("html")).toHaveClass("dark");
  await page.emulateMedia({ colorScheme: "light" });
  await expect(page.locator("html")).toHaveClass("dark");
  await expect(dark).toBeChecked();
  await page.reload();
  await expect(page.locator("html")).toHaveClass("dark");
});

test("theme transitions synchronize controls and restore their own timings", async ({
  page,
}) => {
  await page.emulateMedia({ colorScheme: "light" });
  await page.goto("/login");
  const root = page.locator("html");
  const main = page.getByRole("main");
  const input = page.getByLabel("Email", { exact: true });
  await expect(root).not.toHaveAttribute("data-theme-transition");
  const inputDuration = await input.evaluate(
    (element) => getComputedStyle(element).transitionDuration,
  );
  const lightBackground = await main.evaluate(
    (element) => getComputedStyle(element).backgroundColor,
  );
  const reduced = await page.evaluate(
    () => matchMedia("(prefers-reduced-motion: reduce)").matches,
  );

  if (reduced) {
    await page.emulateMedia({ colorScheme: "dark" });
    await expect(root).toHaveClass("dark");
    await expect(root).not.toHaveAttribute("data-theme-transition");
    await expect(input).toHaveCSS("transition-duration", "0s");
    expect(
      await page.evaluate(
        () =>
          document.getAnimations().filter((a) => a instanceof CSSTransition)
            .length,
      ),
    ).toBe(0);
    return;
  }

  await page.emulateMedia({ colorScheme: "dark" });
  await expect(root).toHaveClass("dark");
  const during = await page.evaluate(() => {
    const root = document.documentElement;
    const oldView = getComputedStyle(root, "::view-transition-old(root)");
    const newView = getComputedStyle(root, "::view-transition-new(root)");
    return {
      oldDuration: oldView.animationDuration,
      newDuration: newView.animationDuration,
      inputDuration: getComputedStyle(document.querySelector("input")!)
        .transitionDuration,
      buttonDuration: getComputedStyle(document.querySelector("button")!)
        .transitionDuration,
      color: getComputedStyle(document.querySelector("input")!).color,
      background: getComputedStyle(document.querySelector("input")!)
        .backgroundColor,
    };
  });
  expect(during.oldDuration).not.toBe("0s");
  expect(during.oldDuration).toBe(during.newDuration);
  expect(during.inputDuration).toBe("0s");
  expect(during.buttonDuration).toBe("0s");
  await expect(root).not.toHaveAttribute("data-theme-transition");
  await expect(input).toHaveCSS("transition-duration", inputDuration);
  // The snapshot crossfade must capture fully resolved colours, without trailing text fades.
  await expect(input).toHaveCSS("color", during.color);
  await expect(input).toHaveCSS("background-color", during.background);

  await page.emulateMedia({ colorScheme: "light" });
  await page.emulateMedia({ colorScheme: "dark" });
  await page.emulateMedia({ colorScheme: "light" });
  await expect(root).not.toHaveClass("dark");
  await expect(root).not.toHaveAttribute("data-theme-transition");
  await expect(main).toHaveCSS("background-color", lightBackground);
  await expect(input).toHaveCSS("transition-duration", inputDuration);
});

test("appearance follows the system until an explicit preference is saved", async ({
  page,
}) => {
  await page.emulateMedia({ colorScheme: "light" });
  await page.goto("/login");
  const trigger = page.getByRole("button", { name: "Appearance", exact: true });
  await trigger.click();
  await expect(
    page.getByRole("menuitemradio", { name: "System" }),
  ).toBeChecked();
  await page.keyboard.press("Escape");
  await page.getByLabel("Email", { exact: true }).fill("draft@example.test");
  await page.emulateMedia({ colorScheme: "dark" });
  await expect(page.locator("html")).toHaveClass("dark");
  await trigger.click();
  await page.getByRole("menuitemradio", { name: "Light", exact: true }).click();
  await expect(page.locator("html")).not.toHaveClass("dark");
  await expect(page.getByLabel("Email", { exact: true })).toHaveValue(
    "draft@example.test",
  );
  await page.reload();
  await expect(page.locator("html")).not.toHaveClass("dark");
  await trigger.click();
  await expect(
    page.getByRole("menuitemradio", { name: "Light", exact: true }),
  ).toBeChecked();
  await page
    .getByRole("menuitemradio", { name: "System", exact: true })
    .click();
  await expect(page.locator("html")).toHaveClass("dark");
  await page.emulateMedia({ colorScheme: "light" });
  await expect(page.locator("html")).not.toHaveClass("dark");
  await page.keyboard.press("Escape");
  await page.getByRole("link", { name: "Forgot password?" }).click();
  await page.reload();
  await trigger.click();
  await expect(
    page.getByRole("menuitemradio", { name: "System", exact: true }),
  ).toBeChecked();
});

test("appearance menu supports keyboard, dismissal, and accessible narrow layouts", async ({
  page,
  browserName,
}) => {
  await page.goto("/login");
  const trigger = page.getByRole("button", { name: "Appearance", exact: true });
  await trigger.focus();
  await expect(page.getByText("Appearance", { exact: true })).toBeVisible();
  const pageFitsViewport = () =>
    page.evaluate(() => document.documentElement.scrollWidth <= innerWidth);
  // Resize with each floating control still open, including a focused tooltip.
  await page.setViewportSize({ width: 320, height: 640 });
  await expect.poll(pageFitsViewport).toBe(true);
  await expect(page.getByText("Appearance", { exact: true })).toBeVisible();
  await page.setViewportSize({ width: 1280, height: 720 });
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  await trigger.press("ArrowDown");
  const light = page.getByRole("menuitemradio", { name: "Light", exact: true });
  await expect(light).toBeFocused();
  await page.setViewportSize({ width: 320, height: 640 });
  await expect.poll(pageFitsViewport).toBe(true);
  await expect(page.getByRole("menu", { name: "Appearance" })).toBeVisible();
  await expect(light).toBeFocused();
  await page.setViewportSize({ width: 1280, height: 720 });
  await page.keyboard.press("ArrowDown");
  await expect(
    page.getByRole("menuitemradio", { name: "Dark", exact: true }),
  ).toBeFocused();
  await page.keyboard.press("Enter");
  await expect(page.getByRole("menu")).toBeVisible();
  await expect(
    page.getByRole("menuitemradio", { name: "Dark", exact: true }),
  ).toBeChecked();
  await expect(page.locator("html")).toHaveClass("dark");
  await page.keyboard.press("Escape");
  await expect(page.getByRole("menu")).toHaveCount(0);
  await expect(trigger).toBeFocused();

  await trigger.press("ArrowDown");
  await expect(light).toBeFocused();
  await page.keyboard.press(browserName === "webkit" ? "Alt+Tab" : "Tab");
  await expect(page.getByLabel("Email", { exact: true })).toBeFocused();
  await expect(page.getByRole("menu")).toHaveCount(0);

  await page.setViewportSize({ width: 320, height: 640 });
  for (const mode of ["Light", "Dark"] as const) {
    await trigger.click();
    await page.getByRole("menuitemradio", { name: mode, exact: true }).click();
    await expect(page.locator("html")).not.toHaveAttribute(
      "data-theme-transition",
    );
    const menu = page.getByRole("menu", { name: "Appearance" });
    // Measure text contrast after the entrance fade has settled.
    await expect(menu).toHaveCSS("opacity", "1");
    // Base UI's invisible Safari focus guards immediately redirect focus.
    // Their intentional button role triggers axe's accessible-name rule:
    // https://github.com/floating-ui/floating-ui/issues/2823
    expect(
      (
        await new AxeBuilder({ page })
          .exclude("[data-base-ui-focus-guard]")
          .analyze()
      ).violations,
    ).toEqual([]);
    const bounds = await menu.boundingBox();
    expect(bounds).not.toBeNull();
    expect(bounds!.x).toBeGreaterThanOrEqual(0);
    expect(bounds!.x + bounds!.width).toBeLessThanOrEqual(320);
    await expect(menu).toHaveCSS(
      "background-color",
      await page
        .getByRole("main")
        .evaluate((element) => getComputedStyle(element).backgroundColor),
    );
    // The popup can cover the heading at this width; dismiss from the page gutter.
    await page.mouse.click(8, 320);
    await expect(menu).toHaveCount(0);
  }
});

test("blocked browser storage does not prevent sign-in or appearance changes", async ({
  page,
}) => {
  await page.addInitScript(() => {
    Storage.prototype.getItem = () => {
      throw new DOMException("Storage blocked", "SecurityError");
    };
    Storage.prototype.setItem = () => {
      throw new DOMException("Storage blocked", "SecurityError");
    };
  });
  await page.emulateMedia({ colorScheme: "light" });
  await page.goto("/login");
  await page.getByRole("button", { name: "Appearance", exact: true }).click();
  await page.getByRole("menuitemradio", { name: "Dark", exact: true }).click();
  await expect(page.locator("html")).toHaveClass("dark");
  await page.keyboard.press("Escape");
  await page.getByLabel("Email", { exact: true }).fill("owner@example.test");
  await page
    .getByLabel("Password", { exact: true })
    .fill("correct horse battery staple");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "Projects", exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Appearance", exact: true }).click();
  await expect(
    page.getByRole("menuitemradio", { name: "Dark", exact: true }),
  ).toBeChecked();
});
