import AxeBuilder from "@axe-core/playwright";
import { expect, type Page, test } from "@playwright/test";

async function signIn(page: Page) {
  await page.getByLabel("Email", { exact: true }).fill("owner@example.test");
  await page
    .getByLabel("Password", { exact: true })
    .fill("correct horse battery staple");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
}

test("login preserves the destination and sign-out hides the application", async ({
  page,
  baseURL,
}) => {
  const destination = "/servers?view=all#enrollment";
  await page.goto(destination);
  await expect(page.getByRole("heading", { name: "Sign in" })).toBeVisible();
  const login = new URL(page.url());
  expect(login.pathname).toBe("/login");
  expect(login.searchParams.get("returnTo")).toBe(destination);
  await expect(page.getByLabel("Workspace navigation")).toHaveCount(0);

  await signIn(page);
  await expect(page).toHaveURL(`${baseURL}${destination}`);
  await expect(
    page.getByRole("heading", { name: "Servers", exact: true }),
  ).toBeVisible();
  await page.reload();
  await expect(page.getByLabel("Workspace navigation")).toBeVisible();

  await page.getByRole("button", { name: "Sign out", exact: true }).click();
  await expect(page).toHaveURL(`${baseURL}/login`);
  await expect(page.getByRole("heading", { name: "Sign in" })).toBeVisible();
  await expect(page.getByLabel("Workspace navigation")).toHaveCount(0);
  await page.goBack();
  await expect(page.getByRole("heading", { name: "Sign in" })).toBeVisible();
  await expect(page.getByLabel("Workspace navigation")).toHaveCount(0);
});

test("public auth pages share an accessible layout without application navigation", async ({
  page,
  baseURL,
}) => {
  for (const path of ["/login", "/reset", "/invitation", "/signup"]) {
    await page.goto(path);
    await expect(page).toHaveURL(`${baseURL}${path}`);
    await expect(
      page.getByRole("img", { name: "Astilba", exact: true }),
    ).toBeVisible();
    await expect(page.getByLabel("Workspace navigation")).toHaveCount(0);
  }
  for (const colorScheme of ["light", "dark"] as const) {
    await page.context().clearCookies();
    await page.evaluate(() => localStorage.removeItem("astilba-theme"));
    await page.emulateMedia({ colorScheme });
    await page.goto("/login");
    await expect(page.getByRole("heading", { name: "Sign in" })).toBeVisible();
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
    await page.setViewportSize({ width: 320, height: 640 });
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
  }
});

test("signed-in login visits reject external, API and looping return destinations", async ({
  page,
  baseURL,
}) => {
  await page.goto("/login");
  await signIn(page);
  await expect(page).toHaveURL(`${baseURL}/`);
  for (const returnTo of [
    "https://example.invalid/",
    "//example.invalid/",
    "/\\example.invalid/",
    "/login?returnTo=/servers",
    "/api/v1/session",
  ]) {
    await page.goto(`/login?${new URLSearchParams({ returnTo })}`);
    await expect(page).toHaveURL(`${baseURL}/`);
    await expect(page.getByLabel("Workspace navigation")).toBeVisible();
  }
  await page.goto("/login");
  await expect(page).toHaveURL(`${baseURL}/`);
});

test("pending and failed session checks do not expose application navigation", async ({
  page,
}) => {
  let failSession!: () => void;
  const pending = new Promise<void>((resolve) => {
    failSession = resolve;
  });
  await page.route("**/api/v1/session", async (route) => {
    await pending;
    await route.abort();
  });
  await page.goto("/servers");
  await expect(page.getByRole("status")).toHaveText("Loading…");
  await expect(page.getByLabel("Workspace navigation")).toHaveCount(0);
  failSession();
  await expect(
    page.getByRole("button", { name: "Retry connection" }),
  ).toBeVisible();
  await expect(page.getByLabel("Workspace navigation")).toHaveCount(0);
  await page.unroute("**/api/v1/session");
  await page.getByRole("button", { name: "Retry connection" }).click();
  await expect(page.getByRole("heading", { name: "Sign in" })).toBeVisible();
  expect(new URL(page.url()).pathname).toBe("/login");
});
