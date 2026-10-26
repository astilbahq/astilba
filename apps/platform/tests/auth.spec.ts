import { createHash, randomUUID } from "node:crypto";
import AxeBuilder from "@axe-core/playwright";
import { expect, type Page, test } from "@playwright/test";
import { authFixtureSQL } from "./auth-fixture";

async function signIn(page: Page) {
  await page.getByLabel("Email", { exact: true }).fill("owner@example.test");
  await page
    .getByLabel("Password", { exact: true })
    .fill("correct horse battery staple");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
}

test("sign-in shows associated inline errors after submit and preserves credential drafts", async ({
  page,
}) => {
  const requests: string[] = [];
  page.on("request", (request) => {
    if (
      request.method() === "POST" &&
      request.url().endsWith("/api/v1/auth/login")
    ) {
      requests.push(request.url());
    }
  });
  await page.goto("/login");
  const email = page.getByLabel("Email", { exact: true });
  const password = page.getByLabel("Password", { exact: true });
  await email.fill("unfinished");
  await password.focus();
  await expect(
    page.getByText("Enter a valid email address.", { exact: true }),
  ).toHaveCount(0);
  await email.clear();
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(email).toBeFocused();
  await expect(email).toHaveAttribute("aria-invalid", "true");
  await expect(email).toHaveAccessibleDescription("Enter your email address.");
  await expect(password).toHaveAccessibleDescription("Enter your password.");
  expect(requests).toHaveLength(0);

  await email.fill("unfinished");
  await expect(email).toHaveAccessibleDescription(
    "Enter a valid email address.",
  );
  await email.fill("owner@example.test");
  await expect(email).not.toHaveAttribute("aria-invalid", "true");
  // Existing credentials are not subjected to the new-password length policy.
  await password.fill("short");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(page.getByRole("alert")).toHaveText(
    "The email or password is incorrect.",
  );
  expect(requests).toHaveLength(1);
  await expect(email).toHaveValue("owner@example.test");
  await expect(password).toHaveValue("short");
  await password.fill("correct horse battery staple");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(page.getByLabel("Workspace navigation")).toBeVisible();
});

test("reset request errors preserve the draft without promising field highlights", async ({
  page,
}) => {
  await page.goto("/reset");
  const email = page.getByLabel("Account email");
  // HTML email syntax permits consecutive dots; the API rejects the request
  // without identifying a field, so the summary must stand on its own.
  await email.fill("local..part@example.test");
  const response = page.waitForResponse(
    (r) =>
      r.url().endsWith("/auth/reset/request") &&
      r.request().method() === "POST",
  );
  await page.getByRole("button", { name: "Send reset link" }).click();
  expect((await response).status()).toBe(400);
  await expect(page.getByRole("alert")).toHaveText(
    "Check the form details and try again.",
  );
  await expect(email).not.toHaveAttribute("aria-invalid", "true");
  await expect(email).toHaveValue("local..part@example.test");
  await email.fill(`${randomUUID()}@example.test`);
  await expect(email).not.toHaveAttribute("aria-invalid", "true");
  await page.getByRole("button", { name: "Send reset link" }).click();
  await expect(
    page.getByRole("heading", { name: "Check your email" }),
  ).toBeFocused();
});

test("non-migrated forms retain native constraints and server error feedback", async ({
  page,
}) => {
  await page.goto("/login");
  await signIn(page);
  const name = page.getByLabel("Project name", { exact: true });
  const submit = page.getByRole("button", {
    name: "Create project",
    exact: true,
  });
  const posts: string[] = [];
  page.on("request", (request) => {
    if (request.method() === "POST" && request.url().endsWith("/projects")) {
      posts.push(request.url());
    }
  });
  await submit.click();
  await expect(name).toBeFocused();
  expect(posts).toHaveLength(0);
  await name.fill("   ");
  await submit.click();
  await expect(name).toHaveAttribute("aria-invalid", "true");
  await expect(page.getByRole("alert")).toContainText("Fields: name");
  await expect(name).toHaveValue("   ");
  const project = `Validation ${randomUUID()}`;
  await name.fill(project);
  await submit.click();
  await expect(
    page.getByRole("link", { name: `${project} Open project →` }),
  ).toBeVisible();
});

test("new account forms validate Unicode passwords and keep errors accessible at narrow widths", async ({
  page,
}) => {
  const posts: string[] = [];
  page.on("request", (request) => {
    if (request.method() === "POST") posts.push(request.url());
  });
  await page.setViewportSize({ width: 320, height: 700 });
  for (const colorScheme of ["light", "dark"] as const) {
    await page.emulateMedia({ colorScheme });
    await page.goto("/reset?token=validation-only");
    const password = page.getByLabel("New password", { exact: true });
    await password.fill("🔐".repeat(8));
    await page.getByRole("button", { name: "Set password" }).click();
    await expect(password).toBeFocused();
    await expect(password).toHaveAccessibleDescription(
      "Use at least 15 characters for your password.",
    );
    await password.fill("🔐".repeat(129));
    await expect(password).toHaveAccessibleDescription(
      "Use 128 characters or fewer for your password.",
    );
    await password.fill("🔐".repeat(128));
    await expect(password).not.toHaveAttribute("aria-invalid", "true");

    await page.goto("/invitation?token=validation-only");
    const name = page.getByLabel("Your name");
    await name.fill("   ");
    await page.getByRole("button", { name: "Accept invitation" }).click();
    await expect(name).toBeFocused();
    await expect(name).toHaveAccessibleDescription("Enter your name.");
    await expect(
      page.getByLabel("Choose a password"),
    ).toHaveAccessibleDescription("Enter your password.");
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await name.fill("界".repeat(81));
    await expect(name).toHaveAccessibleDescription(
      "Use 80 characters or fewer for your name.",
    );
    await name.fill("界".repeat(80));
    await expect(name).not.toHaveAttribute("aria-invalid", "true");
  }
  expect(posts).toHaveLength(0);
});

test("sign-in keeps credential fields together in focus order and supports skipping appearance", async ({
  page,
  baseURL,
  browserName,
}) => {
  const tab = browserName === "webkit" ? "Alt+Tab" : "Tab";
  const backTab = browserName === "webkit" ? "Alt+Shift+Tab" : "Shift+Tab";
  const skip = page.getByRole("link", { name: "Skip to content" });
  const email = page.getByLabel("Email", { exact: true });
  const password = page.getByLabel("Password", { exact: true });
  const recovery = page.getByRole("link", { name: "Forgot password?" });
  const sequence = [
    skip,
    page.getByRole("button", { name: "Appearance", exact: true }),
    email,
    password,
    page.getByRole("button", { name: "Show password", exact: true }),
    recovery,
    page.getByRole("button", { name: "Sign in", exact: true }),
  ];
  for (const width of [1280, 320]) {
    await page.setViewportSize({ width, height: 720 });
    await page.goto("/login");
    await expect(email).toBeVisible();
    const labelBounds = await page
      .locator("label")
      .filter({ hasText: /^Password$/ })
      .boundingBox();
    const recoveryBounds = await recovery.boundingBox();
    const passwordBounds = await password.boundingBox();
    expect(recoveryBounds!.x).toBeGreaterThanOrEqual(
      labelBounds!.x + labelBounds!.width,
    );
    expect(recoveryBounds!.y + recoveryBounds!.height).toBeLessThan(
      passwordBounds!.y,
    );
    for (const control of sequence) {
      await page.keyboard.press(tab);
      await expect(control).toBeFocused();
    }
    for (const control of sequence.slice(0, -1).reverse()) {
      await page.keyboard.press(backTab);
      await expect(control).toBeFocused();
    }
    await page.keyboard.press("Enter");
    await page.keyboard.press(tab);
    await expect(email).toBeFocused();
  }

  await page
    .locator("label")
    .filter({ hasText: /^Password$/ })
    .click();
  await expect(password).toBeFocused();
  await page
    .locator("label")
    .filter({ hasText: /^Email$/ })
    .click();
  await expect(email).toBeFocused();
  await recovery.press("Enter");
  await expect(page).toHaveURL(`${baseURL}/reset`);
  await page.getByRole("link", { name: "Back to sign in" }).click();
  await expect(page).toHaveURL(`${baseURL}/login`);
});

test("password visibility preserves focus, value and native sign-in submission", async ({
  page,
  baseURL,
  browserName,
}) => {
  await page.goto("/login");
  await page.getByLabel("Email", { exact: true }).fill("owner@example.test");
  const password = page.getByLabel("Password", { exact: true });
  const value = "correct horse battery staple";
  await password.fill(value);
  await expect(password).toHaveAttribute("type", "password");
  await expect(password).toHaveAttribute("autocomplete", "current-password");
  await password.evaluate((element: HTMLInputElement) =>
    element.setSelectionRange(8, 13),
  );
  await page
    .getByRole("button", { name: "Show password", exact: true })
    .click();
  await expect(password).toHaveAttribute("type", "text");
  await expect(password).toHaveValue(value);
  await expect(password).toBeFocused();
  expect(
    await password.evaluate((element: HTMLInputElement) => [
      element.selectionStart,
      element.selectionEnd,
    ]),
  ).toEqual([8, 13]);
  await expect(page).toHaveURL(`${baseURL}/login`);

  await page.keyboard.press(browserName === "webkit" ? "Alt+Tab" : "Tab");
  const hide = page.getByRole("button", { name: "Hide password", exact: true });
  await expect(hide).toBeFocused();
  await hide.press("Enter");
  await expect(password).toHaveAttribute("type", "password");
  const show = page.getByRole("button", { name: "Show password", exact: true });
  await expect(show).toBeFocused();
  await show.press("Space");
  await expect(password).toHaveAttribute("type", "text");
  await expect(password).toHaveValue(value);
  await expect(page).toHaveURL(`${baseURL}/login`);

  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(page).toHaveURL(`${baseURL}/`);
  await expect(page.getByLabel("Workspace navigation")).toBeVisible();
});

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
    await expect(page.getByRole("main")).toBeVisible();
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

test("input hover and focus share an edge without shifting the form", async ({
  page,
}) => {
  await page.goto("/login");
  const email = page.getByLabel("Email", { exact: true });
  const submit = page.getByRole("button", { name: "Sign in", exact: true });
  const idle = await email.evaluate(
    (element) => getComputedStyle(element).borderColor,
  );
  const before = await email.boundingBox();
  await email.hover();
  await expect
    .poll(() =>
      email.evaluate((element) => getComputedStyle(element).borderColor),
    )
    .not.toBe(idle);
  await email.evaluate((element) =>
    Promise.all(element.getAnimations().map((animation) => animation.finished)),
  );
  const hovered = await email.evaluate(
    (element) => getComputedStyle(element).borderColor,
  );
  // Read the first focused style before an outline-colour animation can settle.
  const initialOutline = await email.evaluate((element) => {
    element.focus();
    return getComputedStyle(element).outlineColor;
  });
  expect(initialOutline).toBe(hovered);
  await expect(email).toHaveCSS("border-color", hovered);
  await expect(email).toHaveCSS("outline-color", hovered);
  await expect(email).toHaveCSS("outline-offset", "0px");
  const focused = await email.evaluate((element) => {
    const style = getComputedStyle(element);
    return parseFloat(style.borderTopWidth) + parseFloat(style.outlineWidth);
  });
  expect(focused).toBe(2);
  expect(await email.boundingBox()).toEqual(before);

  await submit.focus();
  await expect(submit).toHaveCSS("outline-style", "solid");
  const buttonColor = await submit.evaluate(
    (element) => getComputedStyle(element).backgroundColor,
  );
  await expect(submit).toHaveCSS("outline-color", buttonColor);
  await submit.hover();
  await expect
    .poll(() =>
      submit.evaluate((element) => getComputedStyle(element).backgroundColor),
    )
    .not.toBe(buttonColor);
  await submit.evaluate((element) =>
    Promise.all(element.getAnimations().map((animation) => animation.finished)),
  );
  await expect
    .poll(() =>
      submit.evaluate((element) => {
        const style = getComputedStyle(element);
        return style.outlineColor === style.backgroundColor;
      }),
    )
    .toBe(true);

  // Exercise the shared control's explicit error state independently of API validation.
  await email.evaluate((element) =>
    element.setAttribute("aria-invalid", "true"),
  );
  await email.evaluate((element) =>
    Promise.all(element.getAnimations().map((animation) => animation.finished)),
  );
  const invalid = await email.evaluate(
    (element) => getComputedStyle(element).borderColor,
  );
  expect(invalid).not.toBe(idle);
  await email.hover();
  await email.focus();
  await expect(email).toHaveCSS("border-color", invalid);
  await expect(email).toHaveCSS("outline-color", invalid);
});

test("password recovery requests an email link and preserves a failed password draft", async ({
  page,
}) => {
  await page.goto("/reset");
  await expect(page.getByLabel("New password", { exact: false })).toHaveCount(
    0,
  );
  await expect(page.getByText("Already have a code?")).toHaveCount(0);
  await page.getByLabel("Account email").fill(`${randomUUID()}@example.test`);
  await page.getByRole("button", { name: "Send reset link" }).click();
  await expect(
    page.getByRole("heading", { name: "Check your email" }),
  ).toBeFocused();
  await expect(page.getByRole("status")).toContainText(
    "If this account exists",
  );
  await expect(page.locator("input")).toHaveCount(0);
  await page.getByRole("link", { name: "Back to sign in" }).click();
  await page.getByRole("link", { name: "Forgot password?" }).click();
  await expect(page.getByLabel("Account email")).toBeVisible();

  await page.goto("/reset?token=invalid-test-token");
  await expect(
    page.getByRole("heading", { name: "Set a new password" }),
  ).toBeVisible();
  await expect(page.getByLabel("Reset code from email")).toHaveCount(0);
  await expect(page.getByLabel("Account email")).toHaveCount(0);
  await page
    .getByLabel("New password", { exact: false })
    .fill("replacement browser password");
  await page.getByRole("button", { name: "Set password" }).click();
  await expect(page.getByRole("alert")).toContainText(
    "This reset link is invalid or has expired",
  );
  await expect(page.getByLabel("New password", { exact: false })).toHaveValue(
    "replacement browser password",
  );
  await page.getByRole("link", { name: "Back to sign in" }).click();
  await page.getByRole("link", { name: "Forgot password?" }).click();
  expect(new URL(page.url()).search).toBe("");
  await expect(page.getByLabel("Account email")).toBeVisible();

  await page.setViewportSize({ width: 320, height: 640 });
  for (const colorScheme of ["light", "dark"] as const) {
    await page.evaluate(() => localStorage.removeItem("astilba-theme"));
    await page.emulateMedia({ colorScheme });
    for (const path of ["/reset", "/reset?token=preview-only"]) {
      await page.goto(path);
      await expect(
        page.getByRole("heading", {
          level: 1,
          name: path.includes("token=")
            ? "Set a new password"
            : "Reset your password",
          exact: true,
        }),
      ).toBeVisible();
      await expect
        .poll(() =>
          page
            .locator("html")
            .evaluate((element) => element.classList.contains("dark")),
        )
        .toBe(colorScheme === "dark");
      expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
      expect(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= innerWidth,
        ),
      ).toBe(true);
    }
  }
});

test("a reset URL changes the password through the real API and cannot be reused", async ({
  page,
  baseURL,
}) => {
  const newPassword = ` ${"🔐".repeat(13)} `;
  const id = randomUUID();
  const email = `${id}@example.test`;
  const token = randomUUID();
  const hash = createHash("sha256").update(token).digest("hex");
  // Seed a separate account in the owned disposable database; never reset the shared owner.
  await authFixtureSQL(
    `INSERT INTO accounts(id,email,name,password_hash) SELECT '${id}','${email}','Reset test',password_hash FROM accounts WHERE email='owner@example.test'; INSERT INTO password_resets(account_id,token_hash,expires_at) VALUES('${id}',decode('${hash}','hex'),now()+interval '30 minutes')`,
  );
  const path = `/reset?${new URLSearchParams({ token })}`;
  await page.goto(path);
  await page.getByLabel("New password", { exact: false }).fill(newPassword);
  await page.getByRole("button", { name: "Set password" }).click();
  await expect(
    page.getByRole("heading", { name: "Password changed" }),
  ).toBeFocused();
  await expect(page.locator("input")).toHaveCount(0);
  await page.getByRole("link", { name: "Back to sign in" }).click();
  await page.getByLabel("Email", { exact: true }).fill(email);
  await page.getByLabel("Password", { exact: true }).fill(newPassword);
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "Sign out", exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Sign out", exact: true }).click();
  await expect(page).toHaveURL(`${baseURL}/login`);
  await page.goto(path);
  await page
    .getByLabel("New password", { exact: false })
    .fill("another browser password");
  await page.getByRole("button", { name: "Set password" }).click();
  await expect(page.getByRole("alert")).toContainText(
    "This reset link is invalid or has expired",
  );
});

test("invitation URLs hide code entry, preserve failed drafts and join a new account", async ({
  page,
}) => {
  await page.goto("/invitation");
  await expect(
    page.getByText(
      "Open the invitation link in your email to join a workspace.",
    ),
  ).toBeVisible();
  await expect(page.locator("input")).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: "Accept invitation" }),
  ).toHaveCount(0);
  await page.goto("/invitation?token=invalid-invitation");
  await expect(page.getByLabel("Invitation code from email")).toHaveCount(0);
  await page.getByLabel("Your name").fill("Invited viewer");
  await page
    .getByLabel("Choose a password")
    .fill("new invited account password");
  await page.getByRole("button", { name: "Accept invitation" }).click();
  await expect(page.getByRole("alert")).toContainText(
    "This invitation link is invalid or has expired.",
  );
  await expect(page.getByLabel("Your name")).toHaveValue("Invited viewer");
  await expect(page.getByLabel("Choose a password")).toHaveValue(
    "new invited account password",
  );

  const token = randomUUID();
  const hash = createHash("sha256").update(token).digest("hex");
  const workspace = randomUUID();
  const email = `${randomUUID()}@example.test`;
  await authFixtureSQL(
    `INSERT INTO workspaces(id,name) VALUES('${workspace}','Invited workspace'); INSERT INTO invitations(workspace_id,id,email,role,token_hash,expires_at) VALUES('${workspace}','${randomUUID()}','${email}','viewer',decode('${hash}','hex'),now()+interval '1 day')`,
  );
  const path = `/invitation?${new URLSearchParams({ token })}`;
  await page.goto(path);
  await page
    .getByLabel("Choose a password")
    .fill("new invited account password");
  const name = page.getByLabel("Your name");
  // Go trims NEXT LINE as whitespace; JavaScript does not. This exercises
  // actual field-specific server validation and focus on repeated attempts.
  for (const rejectedName of ["\u0085", "\u0085\u0085"]) {
    await name.fill(rejectedName);
    await expect(name).not.toHaveAttribute("aria-invalid", "true");
    await page.getByRole("button", { name: "Accept invitation" }).click();
    await expect(name).toBeFocused();
    await expect(name).toHaveAccessibleDescription(
      "Check this value and try again.",
    );
    await expect(name).toHaveValue(rejectedName);
    await expect(page.getByLabel("Choose a password")).toHaveValue(
      "new invited account password",
    );
  }
  await name.fill("Invited viewer");
  await page.getByRole("button", { name: "Accept invitation" }).click();
  await expect(
    page.getByRole("heading", { name: "Workspace joined" }),
  ).toBeFocused();
  await expect(page.locator("input")).toHaveCount(0);
  const session = await (await page.request.get("/api/v1/session")).json();
  expect(session.user.email).toBe(email);
  expect(session.workspaces).toContainEqual(
    expect.objectContaining({ id: workspace, role: "viewer" }),
  );
  await page.getByRole("link", { name: "Open projects", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "Sign out", exact: true }),
  ).toBeVisible();
  await page.goto(path);
  await expect(page.getByLabel("Choose a password")).toHaveCount(0);
  await page.getByRole("button", { name: "Accept invitation" }).click();
  await expect(page.getByRole("alert")).toContainText(
    "This invitation link is invalid or has expired.",
  );
});

test("existing accounts return to their invitation after signing in", async ({
  page,
  baseURL,
}) => {
  const token = randomUUID();
  const hash = createHash("sha256").update(token).digest("hex");
  const workspace = randomUUID();
  await authFixtureSQL(
    `INSERT INTO workspaces(id,name) VALUES('${workspace}','Another invited workspace'); INSERT INTO invitations(workspace_id,id,email,role,token_hash,expires_at) VALUES('${workspace}','${randomUUID()}','owner@example.test','viewer',decode('${hash}','hex'),now()+interval '1 day')`,
  );
  const path = `/invitation?${new URLSearchParams({ token })}`;
  await page.goto(path);
  await page.getByRole("link", { name: "Back to sign in" }).click();
  expect(new URL(page.url()).searchParams.get("returnTo")).toBe(path);
  await signIn(page);
  await expect(page).toHaveURL(`${baseURL}${path}`);
  await expect(
    page.getByText("Signed in as owner@example.test."),
  ).toBeVisible();
  await expect(page.locator("input")).toHaveCount(0);
  await page.getByRole("button", { name: "Accept invitation" }).click();
  await expect(
    page.getByRole("heading", { name: "Workspace joined" }),
  ).toBeFocused();
  const session = await (await page.request.get("/api/v1/session")).json();
  expect(session.workspaces).toContainEqual(
    expect.objectContaining({ id: workspace, role: "viewer" }),
  );
});
