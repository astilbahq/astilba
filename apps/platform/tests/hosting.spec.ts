import { execFileSync } from "node:child_process";
import { createHash, randomUUID } from "node:crypto";
import { readFile } from "node:fs/promises";
import { resolve } from "node:path";
import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import jsQR from "jsqr";
import { openEnvironmentSection } from "./environment-section";
import { totp } from "./fixtures/totp";

test("hosted signup, MFA, billing admission and encrypted workspace import", async ({
  page,
  browser,
}, info) => {
  test.setTimeout(120000);
  const workspaceName = `Portable ${info.project.name}`;
  await page.goto("/");
  await page.getByLabel("Email", { exact: true }).fill("owner@example.test");
  await page
    .getByLabel("Password", { exact: true })
    .fill("correct horse battery staple");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await page.getByText("Create another workspace", { exact: true }).click();
  const creation = page
    .getByRole("button", { name: "Create workspace", exact: true })
    .locator("..");
  await creation.getByLabel("Workspace name").fill(workspaceName);
  await creation
    .getByRole("button", { name: "Create workspace", exact: true })
    .click();
  await page.getByRole("button", { name: /^Workspace:/ }).click();
  await page
    .getByRole("menuitem", { name: workspaceName, exact: true })
    .click();
  await page.getByRole("button", { name: "New project", exact: true }).click();
  await page
    .getByLabel("Project name", { exact: true })
    .fill("Imported project");
  await page
    .getByRole("button", { name: "Create project", exact: true })
    .click();
  await page
    .getByRole("link", { name: "Imported project Open project" })
    .click();
  await page
    .getByRole("link", { name: "New environment", exact: true })
    .click();
  await page.getByLabel("Environment name", { exact: true }).fill("Production");
  await page
    .getByRole("button", { name: "Create environment", exact: true })
    .click();
  await openEnvironmentSection(page, "Settings");
  await page.getByLabel("Secret name", { exact: true }).fill("IMPORTED_TOKEN");
  await page
    .getByLabel("Secret value", { exact: true })
    .fill("portable-browser-canary");
  await page
    .getByRole("button", { name: "Save secret version", exact: true })
    .click();
  await expect(
    page.getByText("Secret version saved. Its value cannot be read back."),
  ).toBeVisible();
  await page.getByRole("link", { name: "Workspace", exact: true }).click();
  await page.getByRole("button", { name: /^Workspace:/ }).click();
  await page
    .getByRole("menuitem", { name: workspaceName, exact: true })
    .click();
  const passphrase = "browser transfer archive passphrase";
  await page
    .getByLabel("Archive passphrase (at least 20 characters)")
    .fill(passphrase);
  const download = page.waitForEvent("download");
  await page
    .getByRole("button", { name: "Download encrypted workspace" })
    .click();
  const file = info.outputPath("workspace.astilba");
  await (await download).saveAs(file);

  const context = await browser.newContext({ ignoreHTTPSErrors: true });
  try {
    const hosted = await context.newPage();
    const origin = "https://127.0.0.1:15186";
    await hosted.goto(`${origin}/signup?token=invalid-beta-invitation`);
    await expect(
      hosted.getByRole("heading", { name: "Invalid beta invitation" }),
    ).toBeVisible();
    await expect(hosted.locator("form")).toHaveCount(0);
    await hosted.goto(
      `${origin}/signup?token=browser-${info.project.name}-signup-invitation-code`,
    );
    await expect(hosted.locator('input[name="token"]')).toHaveCount(0);
    await hosted
      .getByRole("button", { name: "Create invited workspace" })
      .click();
    await expect(hosted.getByLabel("Workspace name")).toBeFocused();
    await expect(
      hosted.getByLabel("Workspace name"),
    ).toHaveAccessibleDescription("Enter a workspace name.");
    await hosted
      .getByLabel("Workspace name")
      .fill(`Hosted ${info.project.name}`);
    await hosted.getByLabel("Your name").fill("Beta owner");
    await hosted
      .getByLabel("Password", { exact: true })
      .fill("correct horse battery staple");
    let failedAvailabilityChecks = 0;
    await hosted.route("**/api/v1/hosting", (route) => {
      failedAvailabilityChecks++;
      return route.abort();
    });
    await hosted.clock.setSystemTime(new Date(Date.now() + 20000));
    await hosted.evaluate(() => {
      Object.defineProperty(document, "visibilityState", {
        configurable: true,
        value: "hidden",
      });
      document.dispatchEvent(new Event("visibilitychange", { bubbles: true }));
      Object.defineProperty(document, "visibilityState", {
        configurable: true,
        value: "visible",
      });
      document.dispatchEvent(new Event("visibilitychange", { bubbles: true }));
      delete (document as unknown as Record<string, unknown>).visibilityState;
    });
    await expect.poll(() => failedAvailabilityChecks).toBeGreaterThan(0);
    await expect(hosted.getByLabel("Workspace name")).toHaveValue(
      `Hosted ${info.project.name}`,
    );
    await expect(hosted.getByLabel("Your name")).toHaveValue("Beta owner");
    await expect(hosted.getByLabel("Password", { exact: true })).toHaveValue(
      "correct horse battery staple",
    );
    await hosted.unroute("**/api/v1/hosting");
    await hosted.clock.setSystemTime(new Date());
    let releaseSession!: () => void;
    const heldSession = new Promise<void>((resolve) => {
      releaseSession = resolve;
    });
    await hosted.route(
      "**/api/v1/session",
      async (route) => {
        await heldSession;
        await route.continue();
      },
      { times: 1 },
    );
    await hosted
      .getByRole("button", { name: "Create invited workspace" })
      .click();
    try {
      await expect(
        hosted.getByRole("heading", { name: "Set up an authenticator" }),
      ).toBeFocused();
    } finally {
      releaseSession();
    }
    await expect(hosted).toHaveURL(/\/onboarding[?]workspace=/);
    const onboardingURL = hosted.url();
    const createdWorkspace = new URL(onboardingURL).searchParams.get(
      "workspace",
    );
    expect(createdWorkspace).toBeTruthy();
    await hosted.route(
      "**/api/v1/workspaces/*/subscription",
      async (route) => {
        await context.clearCookies();
        await route.continue({
          headers: { ...route.request().headers(), cookie: "" },
        });
      },
      { times: 1 },
    );
    await hosted.reload();
    await hosted
      .getByRole("link", { name: "Sign in to continue", exact: true })
      .click();
    await hosted
      .getByLabel("Email", { exact: true })
      .fill(`${info.project.name}@example.test`);
    await hosted
      .getByLabel("Password", { exact: true })
      .fill("correct horse battery staple");
    await hosted.getByRole("button", { name: "Sign in", exact: true }).click();
    await expect(hosted).toHaveURL(onboardingURL);
    await expect(
      hosted.getByRole("heading", { name: "Set up an authenticator" }),
    ).toBeVisible();
    // Issue the next invitation after signup claims the first one. The MFA
    // rejection must roll this invitation back so the later import can use it.
    const fixture = JSON.parse(
      await readFile(
        resolve(
          import.meta.dirname,
          "../../../.artifacts/p8/browser/hosted-fixture.json",
        ),
        "utf8",
      ),
    );
    const ownership = execFileSync(
      "docker",
      [
        "--host",
        fixture.host,
        "inspect",
        "--format",
        '{{ index .Config.Labels "com.astilba.p1-test" }}',
        fixture.container,
      ],
      { encoding: "utf8" },
    ).trim();
    expect(ownership).toBe(fixture.owner);
    const code = `browser-${info.project.name}-import-invitation-code`;
    const hash = createHash("sha256").update(code).digest("hex");
    const fixtureSQL = (statement: string) =>
      execFileSync(
        "docker",
        [
          "--host",
          fixture.host,
          "exec",
          fixture.container,
          "psql",
          "--username",
          "postgres",
          "--dbname",
          fixture.database,
          "-v",
          "ON_ERROR_STOP=1",
          "-c",
          statement,
        ],
        { stdio: "pipe" },
      );
    fixtureSQL(
      `INSERT INTO hosted_invitations(id,email,token_hash,expires_at) VALUES('${randomUUID()}','${info.project.name}@example.test',decode('${hash}','hex'),now()+interval '1 day')`,
    );
    await hosted.goto(
      `${origin}/signup?${new URLSearchParams({ token: code })}`,
    );
    await hosted.getByLabel("Workspace name").fill("Requires MFA");
    await hosted
      .getByRole("button", { name: "Create invited workspace" })
      .click();
    await expect(hosted.getByRole("alert")).toContainText(
      "Set up two-factor authentication before changing this workspace.",
    );
    await hosted.goto(onboardingURL);
    await expect(
      hosted.getByRole("heading", { name: "Set up an authenticator" }),
    ).toBeVisible();
    await hosted.getByRole("button", { name: "Set up authenticator" }).click();
    let uri = await hosted
      .getByRole("link", { name: "Open authenticator app" })
      .getAttribute("href");
    const qr = hosted.getByRole("img", { name: "Authenticator setup QR code" });
    const pixels = await qr.evaluate(async (svg) => {
      const image = new Image();
      image.src = `data:image/svg+xml;charset=utf-8,${encodeURIComponent(new XMLSerializer().serializeToString(svg))}`;
      await image.decode();
      const canvas = document.createElement("canvas");
      canvas.width = image.naturalWidth;
      canvas.height = image.naturalHeight;
      const context = canvas.getContext("2d");
      if (!context) throw new Error("Canvas unavailable");
      context.drawImage(image, 0, 0);
      return {
        data: Array.from(
          context.getImageData(0, 0, canvas.width, canvas.height).data,
        ),
        width: canvas.width,
        height: canvas.height,
      };
    });
    expect(
      jsQR(Uint8ClampedArray.from(pixels.data), pixels.width, pixels.height)
        ?.data,
    ).toBe(uri);
    let secret = new URL(uri ?? "").searchParams.get("secret");
    expect(secret).toBeTruthy();
    fixtureSQL(
      `UPDATE accounts SET totp_pending_expiry=now()-interval '1 second' WHERE email='${info.project.name}@example.test'`,
    );
    await hosted
      .getByRole("group", { name: "Authentication code", exact: true })
      .getByRole("textbox")
      .first()
      .pressSequentially(totp(secret ?? ""));
    await hosted
      .getByRole("button", { name: "Enable two-factor authentication" })
      .click();
    await expect(
      hosted
        .getByRole("group", { name: "Authentication code", exact: true })
        .getByRole("textbox")
        .first(),
    ).toHaveAttribute("aria-invalid", "true");
    await expect(
      hosted.getByRole("group", { name: "Authentication code", exact: true }),
    ).toHaveAccessibleDescription(
      "This authentication code is invalid or has already been used. Try a new code.",
    );
    await hosted
      .getByRole("button", { name: "Start setup again", exact: true })
      .click();
    const oldURI = uri;
    await expect
      .poll(async () =>
        hosted
          .getByRole("link", { name: "Open authenticator app" })
          .getAttribute("href"),
      )
      .not.toBe(oldURI);
    uri = await hosted
      .getByRole("link", { name: "Open authenticator app" })
      .getAttribute("href");
    secret = new URL(uri ?? "").searchParams.get("secret");
    await hosted
      .getByRole("group", { name: "Authentication code", exact: true })
      .getByRole("textbox")
      .first()
      .pressSequentially(totp(secret ?? ""));
    await hosted
      .getByRole("button", { name: "Enable two-factor authentication" })
      .click();
    await expect(
      hosted.getByRole("heading", { name: "Save your recovery codes" }),
    ).toBeFocused();
    await expect(
      hosted
        .getByRole("list", { name: "Recovery codes", exact: true })
        .locator("li"),
    ).toHaveCount(10);
    await expect(qr).toHaveCount(0);
    await hosted.reload();
    await expect(
      hosted.getByRole("heading", { name: "Save your recovery codes" }),
    ).toBeVisible();
    await expect(hosted.locator("li code")).toHaveCount(0);
    await hosted
      .getByRole("button", { name: "Generate replacement codes" })
      .click();
    await expect(
      hosted
        .getByRole("list", { name: "Recovery codes", exact: true })
        .locator("li"),
    ).toHaveCount(10);
    const visibleCode = await hosted.locator("li code").first().innerText();
    expect(
      await hosted.evaluate(
        () =>
          Object.values(localStorage).join(" ") +
          Object.values(sessionStorage).join(" "),
      ),
    ).not.toContain(visibleCode);
    await hosted.getByRole("button", { name: "I have saved my codes" }).click();
    await expect(
      hosted.getByRole("heading", { name: "Set up your subscription" }),
    ).toBeFocused();
    await hosted.reload();
    await expect(
      hosted.getByRole("heading", { name: "Set up your subscription" }),
    ).toBeVisible();
    await hosted
      .getByRole("link", { name: "Continue later", exact: true })
      .click();
    await hosted
      .getByRole("button", { name: "New project", exact: true })
      .click();
    await hosted
      .getByLabel("Project name", { exact: true })
      .fill("Subscription gated");
    await hosted
      .getByRole("button", { name: "Create project", exact: true })
      .click();
    await expect(hosted.getByRole("alert")).toHaveText(
      "An active workspace subscription is required. Open Workspace to finish setup.",
    );

    // The operator boundary grants a test entitlement; billing itself uses real
    // application admission and PostgreSQL. No paid provider call is made.
    await hosted.getByRole("link", { name: "Workspace", exact: true }).click();
    await hosted.getByRole("link", { name: "Finish workspace setup" }).click();
    await expect(
      hosted.getByRole("heading", { name: "Set up your subscription" }),
    ).toBeVisible();
    execFileSync(
      fixture.binary,
      [
        "grant-management",
        "--workspace",
        createdWorkspace!,
        "--until",
        new Date(Date.now() + 3600000).toISOString(),
        "--servers",
        "2",
      ],
      {
        env: { ...process.env, ...fixture.env, ASTILBA_KEYRING_JSON: "" },
        stdio: "pipe",
      },
    );
    await hosted.reload();
    await expect(
      hosted.getByRole("heading", { name: "You’re ready" }),
    ).toBeVisible();
    await hosted
      .getByRole("link", { name: "Open projects", exact: true })
      .click();
    await hosted.getByRole("link", { name: "Workspace", exact: true }).click();
    await expect(
      hosted.getByRole("heading", { name: "Workspace subscription" }),
    ).toBeVisible();
    await expect(
      hosted.getByText("Status: granted.", { exact: false }),
    ).toBeVisible();
    expect(
      (await new AxeBuilder({ page: hosted }).analyze()).violations,
    ).toEqual([]);

    await hosted.getByRole("link", { name: "Projects", exact: true }).click();
    await hosted
      .getByText("Import an encrypted workspace", { exact: true })
      .click();
    const importForm = hosted.getByRole("form", { name: "Import workspace" });
    await importForm
      .getByLabel("Encrypted workspace archive")
      .setInputFiles(file);
    await importForm
      .getByLabel("Archive passphrase", { exact: true })
      .fill(passphrase);
    await importForm.getByLabel("Beta workspace invitation code").fill(code);
    await importForm
      .getByLabel("Make me the imported workspace owner", { exact: false })
      .check();
    await importForm
      .getByRole("button", { name: "Import workspace", exact: true })
      .click();
    await expect(
      hosted.getByText("Workspace imported.", { exact: false }),
    ).toBeVisible();
    await hosted.getByRole("button", { name: /^Workspace:/ }).click();
    await hosted
      .getByRole("menuitem", { name: workspaceName, exact: true })
      .click();
    await hosted
      .getByRole("link", { name: "Imported project Open project" })
      .click();
    await hosted
      .getByRole("link", {
        name: "Production Visual configuration Open environment",
        exact: true,
      })
      .click();
    await openEnvironmentSection(hosted, "Settings");
    await expect(
      hosted.getByText("IMPORTED_TOKEN", { exact: true }).first(),
    ).toBeVisible();
    await expect(hosted.locator("body")).not.toContainText(
      "portable-browser-canary",
    );
    await hosted
      .getByRole("navigation", { name: "Navigation", exact: true })
      .getByRole("link", { name: "Projects", exact: true })
      .click();
    await hosted.getByRole("button", { name: /^Workspace:/ }).click();
    await hosted
      .getByRole("menuitem", { name: workspaceName, exact: true })
      .click();
    const importedWorkspace = new URL(hosted.url()).searchParams.get(
      "workspace",
    )!;
    await hosted.goto(
      `${origin}/members?workspace=${importedWorkspace}&billing=returned`,
    );
    await expect(
      hosted.getByRole("button", { name: `Workspace: ${workspaceName}` }),
    ).toBeVisible();
    expect(new URL(hosted.url()).searchParams.get("workspace")).toBe(
      importedWorkspace,
    );
    await hosted
      .getByRole("link", { name: "account security", exact: true })
      .click();
    await expect(
      hosted.getByRole("heading", { name: "Account security", exact: true }),
    ).toBeVisible();
    expect(new URL(hosted.url()).searchParams.get("workspace")).toBe(
      importedWorkspace,
    );
    await hosted.getByRole("link", { name: "Workspace", exact: true }).click();
    expect(new URL(hosted.url()).searchParams.get("workspace")).toBe(
      importedWorkspace,
    );
  } finally {
    await context.close();
  }
});
