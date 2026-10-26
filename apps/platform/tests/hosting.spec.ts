import { execFileSync } from "node:child_process";
import { createHash, randomUUID } from "node:crypto";
import { readFile } from "node:fs/promises";
import { resolve } from "node:path";
import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import jsQR from "jsqr";
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
  await page
    .getByRole("combobox", { name: "Workspace", exact: true })
    .selectOption({ label: workspaceName });
  await page
    .getByLabel("Project name", { exact: true })
    .fill("Imported project");
  await page
    .getByRole("button", { name: "Create project", exact: true })
    .click();
  await page
    .getByRole("link", { name: "Imported project Open project →" })
    .click();
  await page.getByText("Add an environment", { exact: true }).click();
  await page.getByLabel("Environment name", { exact: true }).fill("Production");
  await page
    .getByRole("button", { name: "Create environment", exact: true })
    .click();
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
  await page
    .getByRole("combobox", { name: "Workspace", exact: true })
    .selectOption({ label: workspaceName });
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
    await hosted.goto(`${origin}/signup`);
    await hosted
      .getByLabel("Beta invitation code")
      .fill(`browser-${info.project.name}-signup-invitation-code`);
    await hosted
      .getByLabel("Workspace name")
      .fill(`Hosted ${info.project.name}`);
    await hosted.getByLabel("Your name").fill("Beta owner");
    await hosted
      .getByLabel("Password", { exact: true })
      .fill("correct horse battery staple");
    await hosted
      .getByLabel("Beta invitation code")
      .fill("invalid-beta-invitation");
    await hosted
      .getByRole("button", { name: "Create invited workspace" })
      .click();
    await expect(hosted.getByRole("alert")).toContainText(
      "This invitation link is invalid or has expired.",
    );
    await expect(
      hosted.getByRole("form", { name: "Hosted signup" }),
    ).toHaveAccessibleDescription(
      /^This invitation link is invalid or has expired\./,
    );
    await hosted
      .getByLabel("Beta invitation code")
      .fill(`browser-${info.project.name}-signup-invitation-code`);
    await hosted
      .getByRole("button", { name: "Create invited workspace" })
      .click();
    await expect(
      hosted.getByText("Your workspace is ready.", { exact: false }),
    ).toBeVisible();
    await hosted.getByRole("link", { name: "Set up account security" }).click();
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
        `INSERT INTO hosted_invitations(id,email,token_hash,expires_at) VALUES('${randomUUID()}','${info.project.name}@example.test',decode('${hash}','hex'),now()+interval '1 day')`,
      ],
      { stdio: "pipe" },
    );
    await hosted.goto(`${origin}/signup`);
    await hosted.getByLabel("Beta invitation code").fill(code);
    await hosted.getByLabel("Workspace name").fill("Requires MFA");
    await hosted
      .getByRole("button", { name: "Create invited workspace" })
      .click();
    await expect(hosted.getByRole("alert")).toContainText(
      "Set up two-factor authentication before changing this workspace.",
    );
    await hosted.goto(`${origin}/account`);
    await expect(
      hosted.getByText("Email verified.", { exact: true }),
    ).toBeVisible();
    await hosted.getByRole("button", { name: "Set up authenticator" }).click();
    const uri = await hosted
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
    const secret = new URL(uri ?? "").searchParams.get("secret");
    expect(secret).toBeTruthy();
    await hosted.getByLabel("Six-digit code").fill(totp(secret ?? ""));
    await hosted
      .getByRole("button", { name: "Enable two-factor authentication" })
      .click();
    await expect(
      hosted.getByText("Save these recovery codes somewhere safe.", {
        exact: false,
      }),
    ).toBeVisible();
    await expect(qr).toHaveCount(0);
    await hosted.getByRole("button", { name: "I have saved my codes" }).click();
    await hosted.getByRole("link", { name: "Projects", exact: true }).click();
    await hosted
      .getByLabel("Project name", { exact: true })
      .fill("Subscription gated");
    await hosted
      .getByRole("button", { name: "Create project", exact: true })
      .click();
    await expect(hosted.getByRole("alert")).toContainText(
      "active workspace subscription",
    );

    // The operator boundary grants a test entitlement; billing itself uses real
    // application admission and PostgreSQL. No paid provider call is made.
    const current = await (
      await hosted.request.get(`${origin}/api/v1/session`)
    ).json();
    execFileSync(
      fixture.binary,
      [
        "grant-management",
        "--workspace",
        current.workspaces[0].id,
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
    await hosted
      .getByRole("combobox", { name: "Workspace", exact: true })
      .selectOption({ label: workspaceName });
    await hosted
      .getByRole("link", { name: "Imported project Open project →" })
      .click();
    await hosted.getByRole("link", { name: "Production", exact: true }).click();
    await expect(
      hosted.getByText("IMPORTED_TOKEN", { exact: true }).first(),
    ).toBeVisible();
    await expect(hosted.locator("body")).not.toContainText(
      "portable-browser-canary",
    );
    await hosted.getByRole("link", { name: "Projects", exact: true }).click();
    await hosted
      .getByRole("combobox", { name: "Workspace", exact: true })
      .selectOption({ label: workspaceName });
    const importedWorkspace = await hosted
      .getByRole("combobox", { name: "Workspace", exact: true })
      .inputValue();
    await hosted.goto(
      `${origin}/members?workspace=${importedWorkspace}&billing=returned`,
    );
    await expect(
      hosted.getByRole("combobox", { name: "Workspace", exact: true }),
    ).toHaveValue(importedWorkspace);
  } finally {
    await context.close();
  }
});
