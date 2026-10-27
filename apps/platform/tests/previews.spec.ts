import { randomBytes, randomUUID } from "node:crypto";
import { createServer } from "node:http";
import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";

test("preview policy retains failed inputs and approves only the displayed fork commit", async ({
  page,
}, info) => {
  const sha = "b".repeat(40);
  const provider = createServer((request, response) => {
    response.setHeader("Content-Type", "application/json");
    if (request.url?.includes("/branches/")) {
      response.end(JSON.stringify({ commit: { sha } }));
      return;
    }
    if (request.url?.endsWith("/pulls/7")) {
      response.end(
        JSON.stringify({
          number: 7,
          state: "open",
          base: { ref: "main", repo: { id: 12 } },
          head: { sha, repo: { id: 99 } },
        }),
      );
      return;
    }
    if (request.url?.includes("/pulls?")) {
      response.end(JSON.stringify([{ number: 7 }]));
      return;
    }
    response.end(JSON.stringify({ id: 12, private: true }));
  });
  await new Promise<void>((resolve, reject) => {
    provider.once("error", reject);
    provider.listen(0, "127.0.0.1", resolve);
  });
  try {
    const address = provider.address();
    if (!address || typeof address === "string")
      throw new Error("provider fixture unavailable");
    await page.goto("/");
    await page.getByLabel("Email", { exact: true }).fill("owner@example.test");
    await page
      .getByLabel("Password", { exact: true })
      .fill("correct horse battery staple");
    await page.getByRole("button", { name: "Sign in", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Projects" })).toBeVisible();
    const session = await (await page.request.get("/api/v1/session")).json();
    const ws = session.workspaces[0].id;
    const headers = {
      "X-CSRF-Token": session.csrfToken,
      Origin: "http://127.0.0.1:15181",
    };
    const prefix = `/api/v1/workspaces/${ws}`;
    const host = await page.request.post(`${prefix}/servers`, {
      headers,
      data: {
        name: `Preview host ${info.project.name}`,
        role: "combined",
        previewOnly: true,
      },
    });
    expect(host.status()).toBe(201);
    const enrollment = await host.json();
    const enrolled = await page.request.post("/agent/v1/enroll", {
      data: {
        serverId: enrollment.server.id,
        token: enrollment.token,
        credential: randomBytes(32).toString("base64url"),
        machineId: randomUUID(),
        capabilities: {
          protocol: 1,
          version: "fixture",
          os: "linux",
          arch: "arm64",
          docker: true,
          build: true,
          buildControls: true,
          visual: true,
          previews: true,
          serviceOutputs: true,
          credentialFreeVisualCancel: true,
        },
        preflight: {
          ready: true,
          osRelease: "ubuntu 24.04",
          dockerVersion: "fixture",
          freeBytes: 8589934592,
          port80Busy: false,
          port443Busy: false,
          existingContainers: 0,
          notes: [],
        },
      },
    });
    expect(enrolled.status(), await enrolled.text()).toBe(200);
    const name = `Previews ${info.project.name}`;
    await page
      .getByRole("button", { name: "New project", exact: true })
      .click();
    await page.getByLabel("Project name", { exact: true }).fill(name);
    await page
      .getByRole("button", { name: "Create project", exact: true })
      .click();
    await page.getByRole("link", { name: `${name} Open project` }).click();
    await page
      .getByRole("link", { name: "New environment", exact: true })
      .click();
    await page
      .getByLabel("Environment name", { exact: true })
      .fill("PR template");
    await page
      .getByLabel("Configuration", { exact: true })
      .selectOption("preview_template");
    await page
      .getByRole("button", { name: "Create environment", exact: true })
      .click();
    await expect(
      page.getByText("Preview template ·", { exact: false }),
    ).toBeVisible();
    await page.getByText("Advanced specification", { exact: true }).click();
    await page.getByLabel("Specification (JSON)").fill(
      JSON.stringify({
        version: 1,
        variables: [],
        applications: [
          {
            id: randomUUID(),
            name: "Worker",
            kind: "worker",
            source: {
              kind: "image",
              image:
                "busybox:1.37.0@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e",
            },
          },
        ],
      }),
    );
    await page
      .getByRole("button", { name: "Save revision", exact: true })
      .click();
    await expect(
      page.getByRole("button", { name: "Save revision", exact: true }),
    ).toBeDisabled();
    await page
      .getByText("Repository, registry and build secret settings", {
        exact: true,
      })
      .click();
    const connect = page.getByRole("form", { name: "Connect repository" });
    await connect
      .getByLabel("Connection name", { exact: true })
      .fill(`Preview source ${info.project.name}`);
    await connect
      .getByRole("combobox", { name: "Provider", exact: true })
      .selectOption("gitea");
    await connect
      .getByLabel("Provider origin, such as https://github.com", {
        exact: true,
      })
      .fill(`http://127.0.0.1:${address.port}`);
    await connect
      .getByLabel("Repository path, such as team/application", { exact: true })
      .fill("team/app");
    await connect
      .getByLabel("API and submission username", { exact: true })
      .fill("api");
    await connect
      .getByLabel("API and submission token", { exact: true })
      .fill("browser-api-fixture");
    await connect
      .getByLabel("Read-only clone username", { exact: true })
      .fill("reader");
    await connect
      .getByLabel("Read-only clone token", { exact: true })
      .fill("browser-preview-clone");
    await connect
      .getByRole("button", { name: "Connect repository", exact: true })
      .click();
    await expect(page.getByLabel("Webhook URL", { exact: true })).toBeVisible();
    await page
      .getByRole("button", { name: "Dismiss webhook secret", exact: true })
      .click();
    const connections = await (
      await page.request.get(`${prefix}/git-connections`)
    ).json();
    const connection = connections.find(
      (c: { name: string }) => c.name === `Preview source ${info.project.name}`,
    );
    expect(connection).toBeDefined();
    await page
      .getByText("Repository, registry and build secret settings", {
        exact: true,
      })
      .click();
    const panel = page.getByRole("region", { name: "Pull-request previews" });
    await panel.getByText("Set up automatic previews", { exact: true }).click();
    await expect(
      panel.getByRole("link", { name: "Open Account security", exact: true }),
    ).toHaveAttribute("href", `/account?workspace=${ws}`);
    const form = panel.getByRole("form", { name: "Preview policy" });
    await form
      .getByRole("combobox", { name: "Git-built service", exact: true })
      .selectOption({ label: "Worker" });
    await form
      .getByRole("combobox", { name: "Git connection", exact: true })
      .selectOption(connection.id);
    await form
      .getByRole("combobox", { name: "Preview-only server", exact: true })
      .selectOption(enrollment.server.id);
    await form.getByLabel("Maximum active previews", { exact: true }).fill("1");
    await form
      .getByLabel("Maximum concurrent builds", { exact: true })
      .fill("2");
    await form
      .getByLabel("I approve automatic code execution", { exact: false })
      .check();
    await form
      .getByRole("button", { name: "Enable automatic previews", exact: true })
      .click();
    await expect(panel.getByRole("alert")).toContainText(
      "Concurrent builds cannot exceed",
    );
    await expect(
      form.getByRole("combobox", { name: "Preview-only server", exact: true }),
    ).toHaveValue(enrollment.server.id);
    await expect(
      form.getByLabel("Hours to retain a closed preview", { exact: true }),
    ).toHaveValue("24");
    await page.screenshot({
      path: info.outputPath("preview-policy-desktop.png"),
      fullPage: true,
    });
    await page.setViewportSize({ width: 375, height: 812 });
    await page.screenshot({
      path: info.outputPath("preview-policy-mobile.png"),
      fullPage: true,
    });
    await page.setViewportSize({ width: 1365, height: 1000 });
    await form
      .getByLabel("Maximum concurrent builds", { exact: true })
      .fill("1");
    await form
      .getByRole("button", { name: "Enable automatic previews", exact: true })
      .click();
    await expect(
      panel.getByText("Automatic previews enabled", { exact: false }),
    ).toBeVisible();
    await expect(
      panel.getByText("approval required", { exact: true }),
    ).toBeVisible({ timeout: 20000 });
    await expect(panel.getByText(sha, { exact: true })).toBeVisible();
    await expect(
      panel.getByText("Source repository ID 99", { exact: false }),
    ).toBeVisible();
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
    await page.setViewportSize({ width: 375, height: 812 });
    await page.screenshot({
      path: info.outputPath("previews-mobile.png"),
      fullPage: true,
    });
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await page.setViewportSize({ width: 1365, height: 1000 });
    await page.screenshot({
      path: info.outputPath("previews-desktop.png"),
      fullPage: true,
    });
    page.once("dialog", async (dialog) => {
      expect(dialog.message()).toContain("source repository ID 99");
      expect(dialog.message()).toContain(sha);
      await dialog.accept();
    });
    await panel
      .getByRole("button", { name: "Approve this fork commit", exact: true })
      .click();
    await expect(panel.getByText("building", { exact: true })).toBeVisible({
      timeout: 20000,
    });
    await panel
      .getByRole("link", { name: "Open preview environment", exact: true })
      .click();
    await expect(
      page.getByText("Managed pull-request preview ·", { exact: false }),
    ).toBeVisible();
    await expect(
      page.getByRole("button", { name: "Save revision", exact: true }),
    ).toHaveCount(0);
    await expect(page.getByRole("alert")).toHaveCount(0);
  } finally {
    await new Promise<void>((resolve, reject) =>
      provider.close((error) => (error ? reject(error) : resolve())),
    );
  }
});
