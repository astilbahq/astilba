import { createHash, randomUUID } from "node:crypto";
import AxeBuilder from "@axe-core/playwright";
import { type APIRequestContext, expect, test } from "@playwright/test";
import { authFixtureSQL } from "./auth-fixture";
import { confirmCredential } from "./confirm-credential";
import { totp } from "./fixtures/totp";

async function send(request: APIRequestContext, path: string, data?: object) {
  const sessionResponse = await request.get("/api/v1/session");
  const session = await sessionResponse.json();
  return request.post(path, {
    data,
    headers: {
      "X-CSRF-Token": session.csrfToken,
      Origin: new URL(sessionResponse.url()).origin,
    },
  });
}

async function post(request: APIRequestContext, path: string, data?: object) {
  const response = await send(request, path, data);
  await expect(response).toBeOK();
  return response;
}

test("a new workspace guides its owner through two-factor setup before changes", async ({
  page,
}) => {
  const id = randomUUID();
  const email = `${id}@example.test`;
  const password = "correct horse battery staple";
  // Both browser projects share the loopback login allowance; this journey's extra sign-in
  // starts without attempts accumulated by earlier tests.
  const loginIPScope = createHash("sha256")
    .update("login:ip:127.0.0.1")
    .digest("hex");
  // Own a separate account; enrolling must not change the shared fixture owner.
  await authFixtureSQL(
    `DELETE FROM auth_attempts WHERE scope_hash=decode('${loginIPScope}','hex');
     INSERT INTO accounts(id,email,name,password_hash) SELECT '${id}','${email}','New owner',password_hash FROM accounts WHERE email='owner@example.test'`,
  );
  await page.goto("/login");
  await page.getByLabel("Email", { exact: true }).fill(email);
  await page.getByLabel("Password", { exact: true }).fill(password);
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(page).toHaveURL(/\/$/);
  const workspace = await (
    await post(page.request, "/api/v1/workspaces", { name: "New workspace" })
  ).json();
  expect(workspace.requireMfa).toBe(true);

  // Before enrolling, the owner can look around and is told why changes need setup first.
  await page.goto(`/?workspace=${workspace.id}`);
  await expect(page.getByRole("heading", { name: "Projects" })).toBeVisible();
  const notice = page.getByRole("region", {
    name: "Set up two-factor authentication",
  });
  await expect(notice).toContainText(
    "This workspace requires two-factor authentication.",
  );
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  const refused = await send(
    page.request,
    `/api/v1/workspaces/${workspace.id}/projects`,
    { name: "Too soon" },
  );
  expect(refused.status()).toBe(403);
  expect((await refused.json()).code).toBe("mfa_required");

  await notice
    .getByRole("link", { name: "Set up two-factor authentication" })
    .click();
  await expect(page).toHaveURL(
    new RegExp(`/onboarding\\?workspace=${workspace.id}$`),
  );
  await expect(
    page.getByRole("heading", { name: "Set up an authenticator" }),
  ).toBeFocused();
  const progress = page.getByRole("list", { name: "Workspace setup progress" });
  await expect(progress.getByRole("listitem")).toHaveCount(3);
  await page.getByRole("button", { name: "Set up authenticator" }).click();
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
  await confirmCredential(page, { password });
  await expect(
    page.getByRole("heading", { name: "Save your recovery codes" }),
  ).toBeFocused();
  await expect(
    page.getByRole("list", { name: "Recovery codes" }).locator("li"),
  ).toHaveCount(10);
  await page.getByRole("button", { name: "I have saved my codes" }).click();
  await expect(
    page.getByRole("heading", { name: "You’re ready" }),
  ).toBeFocused();
  await page.getByRole("link", { name: "Open projects" }).click();

  await expect(page.getByRole("heading", { name: "Projects" })).toBeVisible();
  await expect(notice).toHaveCount(0);
  await post(page.request, `/api/v1/workspaces/${workspace.id}/projects`, {
    name: "After setup",
  });
});
