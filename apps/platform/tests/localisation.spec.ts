import { randomUUID } from "node:crypto";
import { readdir, readFile } from "node:fs/promises";
import AxeBuilder from "@axe-core/playwright";
import { expect, type Page, test } from "@playwright/test";
import { invitationLinkFixture, resetLinkFixture } from "./auth-fixture";

const dev = "http://127.0.0.1:15182";

async function chooseLanguage(page: Page, name: string) {
  // Auth's second menu is Language; its accessible name also changes language.
  const trigger = page.locator('button[aria-haspopup="menu"]').nth(1);
  await trigger.click();
  await page.getByRole("menuitemradio", { name, exact: true }).click();
  await expect(trigger).toBeFocused();
}

test("language changes preserve drafts, reveal state, visible client errors and destinations", async ({
  page,
}) => {
  const posts: string[] = [];
  page.on("request", (request) => {
    if (request.method() === "POST") posts.push(request.url());
  });
  await page.goto(`${dev}/login?returnTo=%2Fservers`);
  const email = page.locator('input[name="email"]');
  const password = page.locator('input[name="password"]');
  await email.fill("unfinished");
  await password.fill("unchanged password draft");
  await page
    .getByRole("button", { name: "Show password", exact: true })
    .click();
  await chooseLanguage(page, "Pseudo (expanded)");
  await expect(page.locator('[aria-invalid="true"]')).toHaveCount(0);
  await expect(email).toHaveValue("unfinished");
  await expect(password).toHaveValue("unchanged password draft");
  await expect(password).toHaveAttribute("type", "text");
  await expect(page).toHaveTitle(/^⟦/);
  await page.locator('button[type="submit"]').click();
  await expect(email).toBeFocused();
  await expect(email).toHaveAccessibleDescription(/^⟦/);
  await chooseLanguage(page, "English");
  await expect(email).toHaveAccessibleDescription(
    "Enter a valid email address.",
  );
  await expect(password).not.toHaveAttribute("aria-invalid", "true");
  expect(new URL(page.url()).searchParams.get("returnTo")).toBe("/servers");
  expect(posts).toEqual([]);
  await email.fill("owner@example.test");
  await chooseLanguage(page, "Pseudo (RTL)");
  await expect(email).not.toHaveAttribute("aria-invalid", "true");
  await expect(page.locator("html")).toHaveAttribute("dir", "rtl");
  await expect(page.locator("html")).toHaveAttribute("lang", "en");
  await expect(email).toHaveCSS("direction", "ltr");
  await page.reload();
  await expect(page.locator("html")).toHaveAttribute("dir", "rtl");
});

test("server errors translate without reviving errors cleared by edits", async ({
  page,
}) => {
  const email = `${randomUUID()}-{{email}}@example.test`;
  const { token } = await invitationLinkFixture(email);
  await page.goto(`${dev}/invitation?${new URLSearchParams({ token })}`);
  const name = page.locator('input[name="name"]');
  const password = page.locator('input[name="password"]');
  await name.fill("\u0085");
  await password.fill("localisation invitation password");
  await page.getByRole("button", { name: "Accept invitation" }).click();
  await expect(name).toHaveAccessibleDescription(
    "Check this value and try again.",
  );
  await chooseLanguage(page, "Pseudo (expanded)");
  await expect(name).toHaveAccessibleDescription(/^⟦/);
  await expect(page.getByRole("alert")).toHaveText(/^⟦/);
  await expect(name).toHaveValue("\u0085");
  await expect(password).toHaveValue("localisation invitation password");
  expect(new URL(page.url()).searchParams.get("token")).toBe(token);
  await name.fill("Invited viewer");
  await expect(name).not.toHaveAttribute("aria-invalid", "true");
  await chooseLanguage(page, "English");
  await expect(name).not.toHaveAttribute("aria-invalid", "true");
  await expect(name).toHaveAccessibleDescription("");
  await page.getByRole("button", { name: "Accept invitation" }).click();
  await expect(
    page.getByRole("heading", { name: "Workspace joined" }),
  ).toBeFocused();
  const another = await invitationLinkFixture(email);
  await page.goto(
    `${dev}/invitation?${new URLSearchParams({ token: another.token })}`,
  );
  await expect(page.locator("bdi")).toHaveText(email);
  await chooseLanguage(page, "Pseudo (RTL)");
  await expect(page.locator("bdi")).toHaveText(email);
});

test("expanded RTL auth remains usable at narrow widths in both themes", async ({
  page,
}, info) => {
  const { token } = await resetLinkFixture();
  const invitation = await invitationLinkFixture();
  await page.setViewportSize({ width: 320, height: 720 });
  for (const colorScheme of ["light", "dark"] as const) {
    await page.emulateMedia({ colorScheme });
    for (const path of [
      "/login",
      `/reset?${new URLSearchParams({ token })}`,
      `/invitation?${new URLSearchParams({ token: invitation.token })}`,
    ]) {
      await page.goto(`${dev}${path}`);
      await chooseLanguage(page, "Pseudo (RTL)");
      await page.locator('button[type="submit"]').click();
      await expect(page.locator('[aria-invalid="true"]').first()).toBeFocused();
      await expect(
        page.locator('[aria-invalid="true"]').first(),
      ).toHaveAccessibleDescription(/^⟦/);
      expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
      expect(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= innerWidth,
        ),
      ).toBe(true);
      if (path === "/login") {
        // The RTL reveal target must fit wholly inside the reserved input padding.
        const password = page.locator('input[name="password"]');
        const toggle = page.locator("button[aria-controls]").last();
        const inputBox = (await password.boundingBox())!;
        const toggleBox = (await toggle.boundingBox())!;
        const padding = await password.evaluate((element) =>
          parseFloat(getComputedStyle(element).paddingLeft),
        );
        expect(toggleBox.x + toggleBox.width).toBeLessThanOrEqual(
          inputBox.x + padding,
        );
      }
      await page.screenshot({
        path: info.outputPath(
          `${colorScheme}-${new URL(page.url()).pathname.slice(1)}.png`,
        ),
        fullPage: true,
      });
      const appearance = page.locator('button[aria-haspopup="menu"]').first();
      await appearance.press("ArrowDown");
      await expect(page.getByRole("menuitemradio").first()).toBeFocused();
      await page.keyboard.press("Escape");
      await expect(appearance).toBeFocused();
      await chooseLanguage(page, "English");
    }
  }
});

test("reset completion state survives language changes and shared copy updates", async ({
  page,
}, info) => {
  await page.goto(`${dev}/reset`);
  await page.getByLabel("Account email").fill(`${randomUUID()}@example.test`);
  await page.getByRole("button", { name: "Send reset link" }).click();
  await expect(
    page.getByRole("heading", { name: "Check your email" }),
  ).toBeFocused();
  await chooseLanguage(page, "Pseudo (expanded)");
  await expect(page.locator("form")).toHaveCount(0);
  await expect(page.getByRole("status")).toHaveText(/^⟦/);
  await page.screenshot({
    path: info.outputPath("expanded-reset-complete.png"),
    fullPage: true,
  });
  await chooseLanguage(page, "English");
  await expect(
    page.getByRole("heading", { name: "Check your email" }),
  ).toBeVisible();
});

test("storage failures do not prevent language changes or rendering", async ({
  page,
}) => {
  await page.addInitScript(() => {
    Object.defineProperty(window, "localStorage", {
      get() {
        throw new DOMException("Blocked", "SecurityError");
      },
    });
  });
  await page.goto(`${dev}/reset`);
  await chooseLanguage(page, "Pseudo (expanded)");
  await expect(page.getByRole("heading", { level: 1 })).toHaveText(/^⟦/);
  await chooseLanguage(page, "English");
  await expect(
    page.getByRole("heading", { name: "Reset your password" }),
  ).toBeVisible();
});

test("production excludes pseudolocales and ignores unsupported stored preferences", async ({
  page,
}) => {
  await page.addInitScript(() =>
    localStorage.setItem("astilba-locale", "en-XB"),
  );
  await page.goto("/login");
  await expect(page.getByRole("heading", { name: "Sign in" })).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Language", exact: true }),
  ).toHaveCount(0);
  await expect(page.locator("html")).toHaveAttribute("dir", "ltr");
  const assets = new URL("../dist/assets/", import.meta.url);
  for (const file of await readdir(assets)) {
    if (!file.endsWith(".js")) continue;
    const source = await readFile(new URL(file, assets), "utf8");
    expect(source).not.toContain("Pseudo (expanded)");
    expect(source).not.toContain("Pseudo (RTL)");
    expect(source).not.toContain("pseudolocalize");
    expect(source).not.toContain("⟦");
    expect(source).not.toContain("workshop-csrf");
    expect(source).not.toContain("workshop-request");
    expect(source).not.toContain("mockServiceWorker");
  }
});
