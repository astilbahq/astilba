import { expect, type Page, type Route } from "@playwright/test";
import { authFixtureSQL } from "../auth-fixture";

// Answers as the server does when the session's identity confirmation has lapsed.
function refuseForRecentAuthentication(route: Route) {
  return route.fulfill({
    status: 403,
    contentType: "application/json",
    body: JSON.stringify({
      code: "recent_auth_required",
      message: "Confirm your identity before this action.",
      requestId: "browser-fixture",
    }),
  });
}

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
    await refuseForRecentAuthentication(route);
  });
}

/**
 * Stands in for a session whose identity confirmation lapsed before the page loaded, until the
 * owner confirms it again. Until then the dashboard is told the session isn't recent, and every
 * request to `url` other than a read is refused as the server refuses it. The fixture's clock
 * cannot move, so this stands in for the wait; the confirmation, and every request after it,
 * reach the real API. Returns the requests refused, in order: a screen that asks for the
 * confirmation before sending anything leaves it empty.
 */
export async function lapseRecentConfirmation(
  page: Page,
  url: string | RegExp,
) {
  let confirmed = false;
  const refused: string[] = [];
  // Recorded before the page reads the reply, so the request it retries next is never refused.
  await page.route("**/api/v1/auth/reauthenticate", async (route) => {
    const response = await route.fetch();
    if (response.ok()) confirmed = true;
    await route.fulfill({ response });
  });
  await page.route("**/api/v1/session", async (route) => {
    if (confirmed || route.request().method() !== "GET")
      return route.fallback();
    const response = await route.fetch();
    await route.fulfill({
      response,
      json: { ...(await response.json()), recentAuth: false },
    });
  });
  await page.route(url, async (route) => {
    const request = route.request();
    if (confirmed || request.method() === "GET") return route.fallback();
    refused.push(`${request.method()} ${new URL(request.url()).pathname}`);
    await refuseForRecentAuthentication(route);
  });
  return refused;
}

/** Confirms the fixture owner's identity in the shared dialog, by keyboard. */
export async function confirmIdentityWithPassword(page: Page) {
  // Every confirmation spends one of the account's few attempts in a fixed window, and every
  // journey and both browser projects share the fixture owner, so start without the attempts
  // earlier tests spent.
  await authFixtureSQL(
    `DELETE FROM auth_attempts WHERE scope_hash=sha256(convert_to('reauth:' || (SELECT id FROM accounts WHERE email='owner@example.test'), 'UTF8'))`,
  );
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
