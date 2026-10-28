import { randomUUID } from "node:crypto";
import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import { authFixtureSQL } from "./auth-fixture";
import { totp } from "./fixtures/totp";

test("a cancelled session refresh cannot replace renewed credentials", async ({
  page,
}) => {
  const account = randomUUID();
  const email = `${account}@example.test`;
  await authFixtureSQL(
    `INSERT INTO accounts(id,email,name,password_hash) SELECT '${account}','${email}','Session renewal test',password_hash FROM accounts WHERE email='owner@example.test'`,
  );
  // Hold an already-decoded response so aborting its transport cannot discard it.
  await page.addInitScript(() => {
    const boundary = window as Window & {
      holdSessionResponse?: boolean;
      releaseSessionResponse?: () => void;
      sessionResponseDecoded?: boolean;
      sessionResponseReleased?: boolean;
    };
    const held = new Promise<void>((resolve) => {
      boundary.releaseSessionResponse = resolve;
    });
    const fetch = window.fetch.bind(window);
    window.fetch = async (...args) => {
      const response = await fetch(...args);
      if (
        boundary.holdSessionResponse &&
        new URL(response.url).pathname === "/api/v1/session"
      ) {
        boundary.holdSessionResponse = false;
        const json = response.json.bind(response);
        const text = response.text.bind(response);
        const hold = async <T>(read: () => Promise<T>) => {
          const value = await read();
          boundary.sessionResponseDecoded = true;
          await held;
          boundary.sessionResponseReleased = true;
          return value;
        };
        response.json = () => hold(json);
        response.text = () => hold(text);
      }
      return response;
    };
  });
  await page.goto("/login?returnTo=/account");
  await page.getByLabel("Email", { exact: true }).fill(email);
  await page
    .getByLabel("Password", { exact: true })
    .fill("correct horse battery staple");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "Account security", exact: true }),
  ).toBeVisible();
  await page.evaluate(() => {
    (window as Window & { holdSessionResponse?: boolean }).holdSessionResponse =
      true;
  });
  await page.clock.setSystemTime(Date.now() + 20000);
  await page.evaluate(() =>
    document.dispatchEvent(new Event("visibilitychange", { bubbles: true })),
  );
  await page.waitForFunction(
    () =>
      (window as Window & { sessionResponseDecoded?: boolean })
        .sessionResponseDecoded,
  );

  await page
    .getByRole("button", { name: "Change password", exact: true })
    .click();
  await page
    .getByLabel("New password", { exact: true })
    .fill("renewed private browser password");
  const refreshed = page.waitForResponse(
    (response) => new URL(response.url()).pathname === "/api/v1/session",
  );
  await page
    .getByRole("button", { name: "Update password", exact: true })
    .click();
  await expect(page.getByRole("status")).toHaveText(
    "Password updated. Your other sessions have been signed out.",
  );
  await (await refreshed).finished();
  await page.evaluate(() => {
    (
      window as Window & { releaseSessionResponse?: () => void }
    ).releaseSessionResponse?.();
  });
  await page.waitForFunction(
    () =>
      (window as Window & { sessionResponseReleased?: boolean })
        .sessionResponseReleased,
  );

  const enrollment = page.waitForResponse(
    (response) =>
      new URL(response.url()).pathname === "/api/v1/auth/mfa/enroll",
  );
  await page
    .getByRole("button", { name: "Set up authenticator", exact: true })
    .click();
  expect((await enrollment).status()).toBe(200);
  await expect(
    page.getByRole("link", { name: "Open authenticator app" }),
  ).toBeVisible();
});

test("account credentials use shared forms and clear secrets and drafts after a cross-tab account change", async ({
  page,
  context,
}) => {
  const accountA = randomUUID();
  const accountB = randomUUID();
  const emailA = `${accountA}@example.test`;
  const emailB = `${accountB}@example.test`;
  await authFixtureSQL(
    `INSERT INTO accounts(id,email,name,password_hash) SELECT '${accountA}','${emailA}','Security test A',password_hash FROM accounts WHERE email='owner@example.test'; INSERT INTO accounts(id,email,name,password_hash) SELECT '${accountB}','${emailB}','Security test B',password_hash FROM accounts WHERE email='owner@example.test'`,
  );
  await page.goto("/login?returnTo=/account");
  await page.getByLabel("Email", { exact: true }).fill(emailA);
  await page
    .getByLabel("Password", { exact: true })
    .fill("correct horse battery staple");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "Account security", exact: true }),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "Change password", exact: true })
    .click();
  await page.getByLabel("New password", { exact: true }).fill("short");
  await page
    .getByRole("button", { name: "Update password", exact: true })
    .click();
  await expect(
    page.getByLabel("New password", { exact: true }),
  ).toHaveAccessibleDescription(
    "Use at least 15 characters for your password.",
  );
  await page
    .getByLabel("New password", { exact: true })
    .fill("updated private browser password");
  await page
    .getByRole("button", { name: "Update password", exact: true })
    .click();
  await expect(page.getByRole("status")).toHaveText(
    "Password updated. Your other sessions have been signed out.",
  );
  await expect(page.getByLabel("New password", { exact: true })).toHaveCount(0);

  await page
    .getByRole("button", { name: "Set up authenticator", exact: true })
    .click();
  const uri = await page
    .getByRole("link", { name: "Open authenticator app" })
    .getAttribute("href");
  const secret = new URL(uri!).searchParams.get("secret")!;
  await page
    .getByRole("group", { name: "Authentication code", exact: true })
    .getByRole("textbox")
    .first()
    .pressSequentially(totp(secret));
  await page
    .getByRole("button", { name: "Enable two-factor authentication" })
    .click();
  const codes = page.getByRole("list", { name: "Recovery codes", exact: true });
  await expect(codes.locator("li")).toHaveCount(10);
  const firstCode = await codes.locator("code").first().innerText();
  await page.getByRole("button", { name: "I have saved my codes" }).click();
  await expect(codes).toHaveCount(0);

  let replacements = 0;
  page.on("request", (request) => {
    if (
      request.method() === "POST" &&
      request.url().endsWith("/auth/mfa/recovery-codes")
    )
      replacements++;
  });
  await page
    .getByRole("button", { name: "Replace recovery codes", exact: true })
    .click();
  const dialog = page.getByRole("alertdialog");
  await expect(dialog.getByRole("button", { name: "Cancel" })).toBeFocused();
  await dialog.press("Escape");
  await expect(dialog).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: "Replace recovery codes", exact: true }),
  ).toBeFocused();
  expect(replacements).toBe(0);
  await page
    .getByRole("button", { name: "Replace recovery codes", exact: true })
    .click();
  await dialog
    .getByRole("button", { name: "Replace codes", exact: true })
    .click();
  await expect(codes.locator("li")).toHaveCount(10);
  await expect(codes.locator("code").first()).not.toHaveText(firstCode);
  const shownCode = await codes.locator("code").first().innerText();
  await page
    .getByRole("button", { name: "Change password", exact: true })
    .click();
  await page
    .getByLabel("New password", { exact: true })
    .fill("discard this account A draft");

  const other = await context.newPage();
  await other.goto("/account");
  await other.getByRole("button", { name: /^Account menu for/ }).click();
  await other.getByRole("menuitem", { name: "Sign out", exact: true }).click();
  await other.getByLabel("Email", { exact: true }).fill(emailB);
  await other
    .getByLabel("Password", { exact: true })
    .fill("correct horse battery staple");
  await other.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(other.getByLabel("Workspace navigation")).toBeVisible();
  await page.clock.setSystemTime(Date.now() + 20000);
  await page.evaluate(() =>
    document.dispatchEvent(new Event("visibilitychange", { bubbles: true })),
  );
  await expect(
    page.getByRole("main").getByText(emailB, { exact: true }),
  ).toBeVisible();
  await expect(codes).toHaveCount(0);
  await expect(page.getByLabel("New password", { exact: true })).toHaveCount(0);
  expect(await page.locator("body").innerText()).not.toContain(shownCode);
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
});
