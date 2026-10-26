import { createHash, randomUUID } from "node:crypto";
import AxeBuilder from "@axe-core/playwright";
import {
  expect,
  type APIRequestContext,
  type Page,
  test,
} from "@playwright/test";
import { authFixtureSQL } from "./auth-fixture";
import { totp } from "./fixtures/totp";

async function post(request: APIRequestContext, path: string, data?: object) {
  const sessionResponse = await request.get("/api/v1/session");
  const session = await sessionResponse.json();
  const response = await request.post(path, {
    data,
    headers: {
      "X-CSRF-Token": session.csrfToken,
      Origin: new URL(sessionResponse.url()).origin,
    },
  });
  await expect(response).toBeOK();
  return response;
}

async function signOut(page: Page) {
  await expect(
    page.getByRole("heading", { name: "Account security", exact: true }),
  ).toBeVisible();
  const navigation = page.getByRole("button", {
    name: "Open navigation",
    exact: true,
  });
  if (await navigation.isVisible()) await navigation.click();
  await page.getByRole("button", { name: /^Account menu for/ }).click();
  await page.getByRole("menuitem", { name: "Sign out", exact: true }).click();
}

test("two-factor sign-in separates authenticator and recovery modes and preserves the return destination", async ({
  page,
  baseURL,
  browserName,
}, info) => {
  const id = randomUUID();
  const email = `${id}@example.test`;
  const password = "correct horse battery staple";
  // Both browser projects share the loopback login allowance; start this
  // multi-login journey without attempts accumulated by earlier tests.
  const loginIPScope = createHash("sha256")
    .update("password:ip:127.0.0.1")
    .digest("hex");
  // Own a separate account; enrolling MFA must not change the shared fixture owner.
  await authFixtureSQL(
    `DELETE FROM auth_attempts WHERE scope_hash=decode('${loginIPScope}','hex');
     INSERT INTO accounts(id,email,name,password_hash) SELECT '${id}','${email}','MFA test',password_hash FROM accounts WHERE email='owner@example.test'`,
  );
  await post(page.request, "/api/v1/auth/login", { email, password });
  const { secret } = await (
    await post(page.request, "/api/v1/auth/mfa/enroll")
  ).json();
  const enrolledAt = Date.now();
  const enrollmentCode = totp(secret, enrolledAt);
  const { codes } = await (
    await post(page.request, "/api/v1/auth/mfa/confirm", {
      code: enrollmentCode,
    })
  ).json();
  await post(page.request, "/api/v1/auth/logout");

  async function signIn() {
    await page.getByLabel("Email", { exact: true }).fill(email);
    await page.getByLabel("Password", { exact: true }).fill(password);
    await page.getByRole("button", { name: "Sign in", exact: true }).click();
    await expect(
      page.getByRole("heading", { name: "Verify it’s you" }),
    ).toBeFocused();
  }
  await page.goto("/login?returnTo=/account");
  await signIn();
  const codeGroup = page.getByRole("group", {
    name: "Authentication code",
    exact: true,
  });
  const slots = codeGroup.getByRole("textbox");
  const code = page.getByRole("textbox", {
    name: "Authentication code",
    exact: true,
  });
  await expect(slots).toHaveCount(6);
  await expect(code).toHaveValue("");
  await expect(code).toHaveAttribute("inputmode", "numeric");
  await expect(code).toHaveAttribute("autocomplete", "one-time-code");
  await expect(page.getByLabel("Workspace navigation")).toHaveCount(0);
  const verifyBounds = await page
    .getByRole("button", { name: "Verify", exact: true })
    .boundingBox();
  const recoveryBounds = await page
    .getByRole("button", { name: "Use recovery code", exact: true })
    .boundingBox();
  const accountBounds = await page
    .getByRole("button", { name: "Use another account", exact: true })
    .boundingBox();
  if (!verifyBounds || !recoveryBounds || !accountBounds)
    throw new Error("Missing MFA actions");
  expect(recoveryBounds.y).toBeGreaterThanOrEqual(
    verifyBounds.y + verifyBounds.height,
  );
  expect(accountBounds.y).toBeGreaterThanOrEqual(
    recoveryBounds.y + recoveryBounds.height,
  );
  const tab = browserName === "webkit" ? "Alt+Tab" : "Tab";
  for (const control of [
    code,
    page.getByRole("button", { name: "Verify", exact: true }),
    page.getByRole("button", { name: "Use recovery code", exact: true }),
    page.getByRole("button", { name: "Use another account", exact: true }),
  ]) {
    await page.keyboard.press(tab);
    await expect(control).toBeFocused();
  }

  for (const colorScheme of ["light", "dark"] as const) {
    await page.emulateMedia({ colorScheme, reducedMotion: "reduce" });
    for (const [name, width, height] of [
      ["desktop", 1440, 900],
      ["mobile", 390, 844],
    ] as const) {
      await page.setViewportSize({ width, height });
      await page.screenshot({
        path: info.outputPath(`authenticator-${name}-${colorScheme}.png`),
        fullPage: true,
      });
    }
    await page.setViewportSize({ width: 320, height: 700 });
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
  }
  await page.getByRole("button", { name: "Verify", exact: true }).click();
  await expect(code).toBeFocused();
  await expect(codeGroup).toHaveAccessibleDescription(
    "Enter the six-digit code from your authenticator app.",
  );
  for (const slot of await slots.all()) {
    await expect(slot).toHaveAttribute("aria-invalid", "true");
  }
  await expect(slots.nth(5)).toHaveAccessibleName("Character 6 of 6");
  await code.pressSequentially("123");
  await expect(slots.nth(3)).toBeFocused();
  await page.keyboard.press("Backspace");
  await expect(slots.nth(2)).toBeFocused();
  await page.keyboard.press("ArrowLeft");
  await expect(slots.nth(1)).toBeFocused();
  await page.keyboard.press("ArrowRight");
  await expect(slots.nth(2)).toBeFocused();
  await page.keyboard.type("3456");
  for (const slot of await slots.all()) {
    await expect(slot).not.toHaveAttribute("aria-invalid", "true");
  }
  await expect(slots.nth(5)).toBeFocused();
  await page.keyboard.press(tab);
  await expect(
    page.getByRole("button", { name: "Verify", exact: true }),
  ).toBeFocused();
  await expect(page.getByRole("alert")).toHaveCount(0);
  await expect(
    page.getByRole("heading", { name: "Verify it’s you" }),
  ).toBeVisible();
  await code.focus();
  await page.keyboard.press("Control+Backspace");
  // Paste the complete code, including common clipboard spacing, into one slot.
  await code.evaluate((element, value) => {
    const clipboardData = new DataTransfer();
    clipboardData.setData(
      "text/plain",
      `${value.slice(0, 3)} ${value.slice(3)}`,
    );
    element.dispatchEvent(
      new ClipboardEvent("paste", { clipboardData, bubbles: true }),
    );
  }, enrollmentCode);
  await expect
    .poll(() =>
      slots.evaluateAll((inputs) =>
        inputs.map((input) => (input as HTMLInputElement).value).join(""),
      ),
    )
    .toBe(enrollmentCode);
  await page.getByRole("button", { name: "Verify", exact: true }).click();
  await expect(codeGroup).toHaveAccessibleDescription(
    "This authentication code is invalid or has already been used. Try a new code.",
  );
  await expect(code).toBeFocused();
  for (const slot of await slots.all()) {
    await expect(slot).toHaveAttribute("aria-invalid", "true");
  }
  await expect(page.getByRole("alert")).toHaveCount(0);
  await expect
    .poll(() =>
      slots.evaluateAll((inputs) =>
        inputs.map((input) => (input as HTMLInputElement).value).join(""),
      ),
    )
    .toBe(enrollmentCode);

  await page
    .getByRole("button", { name: "Use recovery code", exact: true })
    .click();
  await expect(
    page.getByRole("heading", { name: "Use a recovery code" }),
  ).toBeFocused();
  const recovery = page.getByLabel("Recovery code", { exact: true });
  await expect(recovery).toHaveValue("");
  await expect(recovery).toHaveAttribute("inputmode", "text");
  await expect(page.getByRole("alert")).toHaveCount(0);
  for (const colorScheme of ["light", "dark"] as const) {
    await page.emulateMedia({ colorScheme, reducedMotion: "reduce" });
    for (const [name, width, height] of [
      ["desktop", 1440, 900],
      ["mobile", 390, 844],
    ] as const) {
      await page.setViewportSize({ width, height });
      await page.screenshot({
        path: info.outputPath(`recovery-${name}-${colorScheme}.png`),
        fullPage: true,
      });
    }
    await page.setViewportSize({ width: 320, height: 700 });
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
  }
  await recovery.fill("invalid-recovery-code");
  await page.getByRole("button", { name: "Verify", exact: true }).click();
  await expect(recovery).toHaveAccessibleDescription(
    "This recovery code is invalid or has already been used. Try another code.",
  );
  await expect(recovery).toBeFocused();
  await expect(recovery).toHaveAttribute("aria-invalid", "true");
  await expect(page.getByRole("alert")).toHaveCount(0);
  await expect(recovery).toHaveValue("invalid-recovery-code");
  await page.getByRole("button", { name: "Use an authenticator code" }).click();
  await expect(code).toHaveValue("");
  await expect(page.getByRole("alert")).toHaveCount(0);
  await page
    .getByRole("button", { name: "Use recovery code", exact: true })
    .click();
  await recovery.fill(codes[0]);
  await page.getByRole("button", { name: "Verify", exact: true }).click();
  await expect(page).toHaveURL(`${baseURL}/account`);
  await expect(page.getByText("9 recovery codes remaining.")).toBeVisible();

  await signOut(page);
  await expect(page).toHaveURL(`${baseURL}/login`);
  await expect(
    page.getByRole("heading", { name: "Sign in", exact: true }),
  ).toBeVisible();
  await page.goto("/login?returnTo=/account");
  await signIn();
  await page.getByRole("button", { name: "Use another account" }).click();
  await expect(
    page.getByRole("heading", { name: "Sign in", exact: true }),
  ).toBeVisible();
  await expect(page.getByLabel("Email", { exact: true })).toHaveValue("");
  expect(
    (await (await page.request.get("/api/v1/session")).json()).mfaPending,
  ).toBe(false);
  expect(new URL(page.url()).searchParams.get("returnTo")).toBe("/account");
  await signIn();
  // A successful logout followed by a failed refresh must explain the failure.
  await page.route("**/api/v1/session", (route) => route.abort());
  await page.getByRole("button", { name: "Use another account" }).click();
  await expect(page.getByRole("dialog")).toContainText(
    "The request could not be completed. Try again.",
  );
  await expect(
    page.getByRole("button", { name: "Retry connection" }),
  ).toBeEnabled();
  await page.unroute("**/api/v1/session");
  await page.getByRole("button", { name: "Retry connection" }).click();
  await expect(
    page.getByRole("heading", { name: "Sign in", exact: true }),
  ).toBeVisible();
  await signIn();
  // The next time step is in the API's accepted skew window and is unused.
  await code.fill(totp(secret, enrolledAt + 30000));
  await page.getByRole("button", { name: "Verify", exact: true }).click();
  await expect(page).toHaveURL(`${baseURL}/account`);
  await signOut(page);
  await expect(page).toHaveURL(`${baseURL}/login`);
  await page.goto("http://127.0.0.1:15182/login");
  await signIn();
  await code.fill("12");
  await page.getByRole("button", { name: "Language", exact: true }).click();
  await page
    .getByRole("menuitemradio", { name: "Pseudo (RTL)", exact: true })
    .click();
  await expect(page.locator("html")).toHaveAttribute("dir", "rtl");
  const translatedGroup = page.getByRole("group");
  const translatedSlots = translatedGroup.getByRole("textbox");
  await expect(translatedGroup).toHaveCSS("direction", "ltr");
  await expect(translatedSlots.nth(5)).toHaveAccessibleName(/^⟦/);
  await expect(translatedSlots.nth(0)).toHaveValue("1");
  await expect(translatedSlots.nth(1)).toHaveValue("2");
  await translatedSlots.nth(2).focus();
  await page.keyboard.press("ArrowLeft");
  await expect(translatedSlots.nth(1)).toBeFocused();
  await page.locator('button[type="submit"]').click();
  await expect(translatedSlots.first()).toBeFocused();
  await expect(translatedGroup).toHaveAccessibleDescription(/^⟦/);
  for (const slot of await translatedSlots.all()) {
    await expect(slot).toHaveAttribute("aria-invalid", "true");
  }
  await page.setViewportSize({ width: 320, height: 700 });
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  await page.screenshot({
    path: info.outputPath("authenticator-rtl-320-dark.png"),
    fullPage: true,
  });
  await page.locator('button[aria-haspopup="menu"]').nth(1).click();
  await page
    .getByRole("menuitemradio", { name: "English", exact: true })
    .click();
  await expect(codeGroup).toHaveAccessibleDescription(
    "Enter the six-digit code from your authenticator app.",
  );
  await expect(code).toHaveValue("1");
});
