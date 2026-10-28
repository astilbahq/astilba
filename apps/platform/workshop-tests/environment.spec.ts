import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";

for (const [state, section] of [
  ["overview", "Deployments"],
  ["compose", "Deployments"],
  ["preview-template", "Settings"],
] as const) {
  test(`${state} repository settings show the unconfigured GitHub App state`, async ({
    page,
  }) => {
    await page.goto(
      `/iframe.html?id=pages-environment-detail--${state}&viewMode=story&globals=a11y.manual:!true`,
    );
    await page.getByRole("tab", { name: section, exact: true }).click();
    await page
      .getByText("Repository, registry and build secret settings", {
        exact: true,
      })
      .click();
    const github = page.getByRole("region", {
      name: "GitHub App",
      exact: true,
    });
    await expect(github).toContainText(
      "The operator hasn’t configured a GitHub App.",
    );
    await expect(github.getByRole("alert")).toHaveCount(0);
    await expect(
      github.getByRole("button", { name: "Retry GitHub setup" }),
    ).toHaveCount(0);
  });
}

for (const theme of ["light", "dark"]) {
  test(`environment overview and configuration fit desktop, mobile and RTL in ${theme}`, async ({
    page,
  }, info) => {
    await page.emulateMedia({ reducedMotion: "reduce" });
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto(
      `/iframe.html?id=pages-environment-detail--overview&viewMode=story&globals=theme:${theme};a11y.manual:!true`,
    );
    await expect(
      page.getByRole("heading", { name: "Services", exact: true }),
    ).toBeVisible();
    for (const [name, width, height] of [
      ["desktop", 1440, 900],
      ["user-1280", 1280, 720],
      ["mobile", 390, 844],
    ] as const) {
      await page.setViewportSize({ width, height });
      expect(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= innerWidth,
        ),
      ).toBe(true);
      await page.screenshot({
        path: info.outputPath(`environment-${theme}-${name}.png`),
        fullPage: true,
      });
    }
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
    await page
      .getByRole("button", { name: "Open configuration", exact: true })
      .click();
    await expect(
      page.getByRole("tab", { name: "Configuration", exact: true }),
    ).toBeFocused();
    await page.setViewportSize({ width: 1440, height: 900 });
    await expect(
      page.getByRole("button", { name: "Service list", exact: true }),
    ).toBeVisible();
    await page.screenshot({
      path: info.outputPath(`configuration-${theme}-desktop.png`),
      fullPage: true,
    });
    await page.setViewportSize({ width: 390, height: 844 });
    await page.screenshot({
      path: info.outputPath(`configuration-${theme}-mobile.png`),
      fullPage: true,
    });
    await page
      .getByRole("link", { name: "React Flow attribution" })
      .scrollIntoViewIfNeeded();
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
    await page.setViewportSize({ width: 320, height: 844 });
    await page.goto(
      `/iframe.html?id=pages-environment-detail--long-names&viewMode=story&globals=theme:${theme};locale:en-XB;a11y.manual:!true`,
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
      path: info.outputPath(`environment-${theme}-rtl-320.png`),
      fullPage: true,
    });
  });
}

test("local sections retain specification and layout drafts without a leave prompt", async ({
  page,
}) => {
  await page.goto(
    "/iframe.html?id=pages-environment-detail--overview&viewMode=story&globals=a11y.manual:!true",
  );
  const overview = page.getByRole("tab", { name: "Overview", exact: true });
  await overview.focus();
  await overview.press("ArrowRight");
  const configuration = page.getByRole("tab", {
    name: "Configuration",
    exact: true,
  });
  await expect(configuration).toBeFocused();
  await configuration.press("Enter");
  await page.getByRole("button", { name: "Service list", exact: true }).click();
  await page.getByRole("button", { name: "api", exact: true }).click();
  const position = page.getByLabel("Position x", { exact: true });
  await position.fill("25");
  await position.press("Tab");
  await overview.click();
  await expect(page.getByRole("status")).toContainText(
    "You have unsaved configuration or layout changes.",
  );
  await configuration.click();
  await expect(position).toHaveValue("25");
  await page.getByText("Advanced specification", { exact: true }).click();
  const editor = page.getByLabel("Specification (JSON)");
  const draft = `${await editor.inputValue()}\n `;
  await editor.fill(draft);
  await page.getByRole("tab", { name: "Settings", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "Secrets", exact: true }),
  ).toBeVisible();
  await expect(page.getByRole("alertdialog")).toHaveCount(0);
  await configuration.click();
  await expect(editor).toHaveValue(draft);
  await expect(
    page.getByRole("button", { name: "Save revision", exact: true }),
  ).toBeEnabled();
});

for (const state of ["viewer", "managed-preview"]) {
  test(`${state} retains read-only configuration and secret controls`, async ({
    page,
  }) => {
    await page.goto(
      `/iframe.html?id=pages-environment-detail--${state}&viewMode=story&globals=a11y.manual:!true`,
    );
    await page
      .getByRole("button", { name: "Open configuration", exact: true })
      .click();
    await expect(
      page.getByRole("button", { name: "Save revision", exact: true }),
    ).toHaveCount(0);
    await page.getByRole("tab", { name: "Settings", exact: true }).click();
    await expect(
      page.getByRole("heading", { name: "Secrets", exact: true }),
    ).toBeVisible();
    await expect(page.getByLabel("Secret name", { exact: true })).toHaveCount(
      0,
    );
    await expect(
      page.getByRole("button", { name: "Create token", exact: true }),
    ).toHaveCount(0);
  });
}

test("read-only Compose source supports sequential focus and keyboard scrolling", async ({
  page,
}) => {
  await page.goto(
    "/iframe.html?id=pages-environment-detail--read-only-compose&viewMode=story&globals=a11y.manual:!true",
  );
  const source = page.getByRole("textbox", {
    name: "Source compose.yaml",
    exact: true,
  });
  await expect(source).toBeVisible();
  await page.getByRole("button", { name: "compose.yaml", exact: true }).focus();
  await page.keyboard.press("Tab");
  await expect(source).toBeFocused();
  await expect(source).toHaveAttribute("contenteditable", "false");
  await source.press("PageDown");
  await expect
    .poll(() =>
      page
        .locator(".compose-code-editor .cm-scroller")
        .evaluate((element) => element.scrollTop),
    )
    .toBeGreaterThan(0);
  const text = await source.textContent();
  await source.press("x");
  await expect(source).toHaveText(text!);
  await source.press("Tab");
  await expect(
    page.getByText("Interpolation values", { exact: true }),
  ).toBeFocused();
});

test("reload confirmation preserves focus, supports cancellation and keeps the draft", async ({
  page,
}) => {
  await page.goto(
    "/iframe.html?id=pages-environment-detail--reload-confirmation&viewMode=story&globals=a11y.manual:!true",
  );
  const dialog = page.getByRole("alertdialog", {
    name: "Discard unsaved configuration?",
  });
  const keep = dialog.getByRole("button", { name: "Keep editing" });
  await expect(keep).toBeFocused();
  await keep.press("Enter");
  await expect(dialog).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: "Reload current", exact: true }),
  ).toBeFocused();
  await expect(page.getByLabel("Specification (JSON)")).toHaveValue(
    /"development"/,
  );
  await page
    .getByRole("button", { name: "Reload current", exact: true })
    .click();
  await dialog.getByRole("button", { name: "Discard and reload" }).click();
  await expect(page.getByRole("status")).toHaveText("Current revision loaded.");
  await expect(
    page.getByRole("button", { name: "Save revision", exact: true }),
  ).toBeDisabled();
});
