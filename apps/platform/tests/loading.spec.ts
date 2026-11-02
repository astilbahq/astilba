import { randomUUID } from "node:crypto";
import { expect, test } from "@playwright/test";
import { openEnvironmentSection } from "./environment-section";

for (const asset of ["script", "stylesheet", "both"] as const) {
  test(`environment code loads on demand and recovers missing ${asset} before editing`, async ({
    page,
  }) => {
    const scripts: string[] = [];
    page.on("request", (request) => {
      if (request.resourceType() === "script") scripts.push(request.url());
    });
    const environmentScript = /\/assets\/environment-detail-[^/]+\.js$/;
    const environmentStyle = /\/assets\/environment-detail-[^/]+\.css$/;
    const primary = asset === "script" ? "js" : "css";
    await page.goto("/login");
    await expect(
      page.getByRole("heading", { name: "Sign in", exact: true }),
    ).toBeVisible();
    expect(scripts.filter((url) => environmentScript.test(url))).toEqual([]);
    await page.getByLabel("Email", { exact: true }).fill("owner@example.test");
    await page
      .getByLabel("Password", { exact: true })
      .fill("correct horse battery staple");
    await page.getByRole("button", { name: "Sign in", exact: true }).click();
    await expect(
      page.getByRole("heading", { name: "Projects", exact: true }),
    ).toBeVisible();
    expect(scripts.filter((url) => environmentScript.test(url))).toEqual([]);
    const initialScripts = [...new Set(scripts)];

    const session = await (await page.request.get("/api/v1/session")).json();
    const workspace = session.workspaces[0].id;
    const headers = {
      "X-CSRF-Token": session.csrfToken,
      Origin: new URL(page.url()).origin,
    };
    const createdProject = await page.request.post(
      `/api/v1/workspaces/${workspace}/projects`,
      { headers, data: { name: `Deferred editor ${randomUUID()}` } },
    );
    expect(createdProject.status()).toBe(201);
    const project = await createdProject.json();
    const createdEnvironment = await page.request.post(
      `/api/v1/workspaces/${workspace}/projects/${project.id}/environments`,
      { headers, data: { name: "Development" } },
    );
    expect(createdEnvironment.status()).toBe(201);
    const environment = await createdEnvironment.json();

    let release!: () => void;
    const held = new Promise<void>((resolve) => {
      release = resolve;
    });
    const attempts = new Map<string, number>();
    const pattern =
      asset === "both"
        ? /\/assets\/environment-detail-[^/]+\.(js|css)$/
        : asset === "script"
          ? environmentScript
          : environmentStyle;
    await page.route(pattern, async (route) => {
      const extension = new URL(route.request().url()).pathname
        .split(".")
        .pop()!;
      const attempt = (attempts.get(extension) ?? 0) + 1;
      attempts.set(extension, attempt);
      if (attempt === 1) {
        await held;
        await route.fulfill({
          status: 404,
          headers: { "Cache-Control": "no-store" },
          body: "Asset unavailable",
        });
      } else {
        await route.continue();
      }
    });
    try {
      await page.goto(
        `/?workspace=${workspace}&project=${project.id}&environment=${environment.id}`,
        { waitUntil: "domcontentloaded" },
      );
      await expect.poll(() => attempts.get(primary) ?? 0).toBe(1);
      await expect(
        page.getByRole("navigation", { name: "Navigation", exact: true }),
      ).toBeVisible();
      await expect(
        page.getByRole("status").filter({ hasText: "Loading…" }),
      ).toBeVisible();
      await expect(
        page.getByRole("heading", {
          level: 1,
          name: "Development",
          exact: true,
        }),
      ).toBeVisible();
      release();
      if (asset !== "script") {
        await expect(page.getByRole("alert")).toContainText(
          "This environment could not be opened.",
        );
        await expect(page.getByRole("alert").getByRole("button")).toHaveCount(
          0,
        );
        await expect(
          page.getByRole("heading", {
            level: 1,
            name: "This environment could not be opened. Reload the page to try again.",
            exact: true,
          }),
        ).toBeVisible();
        await expect(
          page.getByRole("navigation", { name: "Navigation", exact: true }),
        ).toBeVisible();
        await page
          .getByRole("button", { name: "Reload page", exact: true })
          .click();
      }
      await expect(
        page.getByRole("tab", { name: "Overview", exact: true }),
      ).toBeVisible();
      await expect(
        page.getByRole("heading", { name: "Development", exact: true }),
      ).toBeVisible();
      // With both faults, CSS recovers first; the later JS failure reloads once.
      expect(attempts.get(primary)).toBe(asset === "both" ? 3 : 2);
      if (asset === "both") expect(attempts.get("js")).toBe(2);
      await openEnvironmentSection(page, "Configuration");
      await page.getByText("Advanced specification", { exact: true }).click();
      const editor = page.getByLabel("Specification (JSON)");
      const specification = JSON.parse(await editor.inputValue());
      specification.variables.push({
        name: "LOADED_EDITOR",
        value: "retained",
      });
      const edited = JSON.stringify(specification, null, 2);
      await editor.fill(edited);
      await page.getByRole("tab", { name: "Overview", exact: true }).click();
      await openEnvironmentSection(page, "Configuration");
      await expect(editor).toHaveValue(edited);
      await page
        .getByRole("button", { name: "Save revision", exact: true })
        .click();
      await expect(
        page.getByText("Revision saved.", { exact: true }),
      ).toBeVisible();
      await test.info().attach("script-requests", {
        body: JSON.stringify({ initialScripts, allScripts: scripts }, null, 2),
        contentType: "application/json",
      });
    } finally {
      release();
      await page.unrouteAll({ behavior: "wait" });
    }
  });
}
