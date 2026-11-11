import { expect, type Page } from "@playwright/test";

/**
 * Answers the next matching request as the server does once a session's identity
 * confirmation has lapsed. The fixture's clock cannot move, so this stands in for the wait;
 * the confirmation that follows and the retried request reach the real API.
 */
export async function lapseRecentConfirmationFor(
  page: Page,
  method: string,
  url: string | RegExp,
) {
  let refused = false;
  await page.route(url, async (route) => {
    if (refused || route.request().method() !== method) return route.fallback();
    refused = true;
    await route.fulfill({
      status: 403,
      contentType: "application/json",
      body: JSON.stringify({
        code: "recent_auth_required",
        message: "Confirm your identity before this action.",
        requestId: "browser-fixture",
      }),
    });
  });
}

/** Confirms the fixture owner's identity in the shared dialog, by keyboard. */
export async function confirmIdentityWithPassword(page: Page) {
  const dialog = page.getByRole("alertdialog", {
    name: "Confirm your identity",
  });
  await expect(dialog).toBeVisible();
  const password = dialog.getByLabel("Password", { exact: true });
  await expect(password).toBeFocused();
  await password.fill("correct horse battery staple");
  await page.keyboard.press("Enter");
  await expect(dialog).toBeHidden();
}
