import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import { managedDatabase } from "./fixtures/managed-database";

for (const savedEdit of [false, true]) {
  test(`major upgrade keeps recovery explicit${savedEdit ? " and preserves a newer saved draft" : ""}`, async ({
    page,
    request,
  }, info) => {
    const f = await managedDatabase(
      page,
      request,
      `Upgrade ${info.project.name} ${savedEdit ? "edited" : "current"}`,
      17,
    );
    const destination = await f.post(`${f.prefix}/backup-destinations`, {
      name: "Upgrade storage",
      kind: "filesystem",
      root: "/srv/backups",
      endpoint: "",
      bucket: "",
      region: "",
      access: { accessKey: "", secretKey: "", password: "" },
    });
    const policy = await f.post(`${f.path}/backup-policies`, {
      name: "Upgrade recovery",
      resourceId: f.pg,
      destinationId: destination.id,
      method: "postgres-logical",
      schedule: { hour: 2, minute: 30, timezone: "UTC" },
      retain: 7,
      enabled: true,
      command: [],
      password: "upgrade-browser-repository-canary",
      baseGeneration: 0,
    });
    await page.goto(
      `/?workspace=${f.ws}&project=${f.project.id}&environment=${f.env.id}`,
    );
    const region = page.getByRole("region", {
      name: "Supported major upgrades",
    });
    await region
      .getByText("Review a PostgreSQL 17 → 18 upgrade", { exact: true })
      .click();
    const review = region.getByRole("form", { name: "Review major upgrade" });
    await review.getByLabel("Final backup policy").selectOption(policy.id);
    await review
      .getByRole("button", { name: "Create upgrade plan", exact: true })
      .click();
    const plan = region.getByRole("article");
    await expect(plan.getByRole("heading", { name: /planned/ })).toBeVisible();
    await expect(
      plan.getByRole("button", { name: "Approve cutover and resume services" }),
    ).toHaveCount(0);
    await plan.getByLabel(/I accept downtime/).check();
    await plan
      .getByRole("button", {
        name: "Stop services, back up and verify upgrade",
      })
      .click();
    const prepared = await f.poll();
    expect(prepared.kind).toBe("upgrade");
    expect(prepared.upgrade.installation.access.password).toBe(
      "upgrade-browser-repository-canary",
    );
    expect(await region.textContent()).not.toContain(
      "upgrade-browser-repository-canary",
    );
    const status = {
      phase: "ready",
      step: "verified",
      serving: "none",
      writesPossible: false,
      keepUntil: new Date(Date.now() + 30 * 86400000).toISOString(),
      snapshot: "e".repeat(64),
      note: "Final backup and restore verified; applications remain stopped.",
    };
    await f.report(prepared, undefined, status);
    const cutover = plan.getByRole("button", {
      name: "Approve cutover and resume services",
    });
    await expect(cutover).toBeVisible({ timeout: 12000 });
    await expect(
      plan.getByRole("button", { name: "Return to old database" }),
    ).toBeVisible();
    if (savedEdit) {
      await page.getByText("Advanced specification", { exact: true }).click();
      const editor = page.getByRole("textbox", {
        name: "Specification (JSON)",
      });
      const edited = JSON.parse(await editor.inputValue());
      edited.applications[0].name = "Saved during upgrade";
      await editor.fill(JSON.stringify(edited));
      await page
        .getByRole("button", { name: "Save revision", exact: true })
        .click();
      await expect(
        page.getByRole("button", { name: "Save revision", exact: true }),
      ).toBeDisabled();
    }
    await plan.getByLabel(/Allow new writes/).check();
    await cutover.click();
    const activating = await f.poll();
    expect(activating.upgrade.action).toBe("cutover");
    await f.report(
      activating,
      undefined,
      {
        ...status,
        phase: "attention",
        step: "routes",
        serving: "unknown",
        writesPossible: true,
        note: "Route acknowledgement was lost.",
      },
      "failed",
    );
    await expect(
      plan.getByRole("button", {
        name: "Reconcile new database and resume services",
      }),
    ).toBeVisible({ timeout: 12000 });
    await expect(
      plan.getByRole("button", { name: "Return to old database" }),
    ).toHaveCount(0);
    await expect(
      plan.getByText(/Restarting the old volume would discard them/),
    ).toBeVisible();
    await plan
      .getByRole("button", {
        name: "Reconcile new database and resume services",
      })
      .click();
    const reconcile = await f.poll();
    await f.report(
      reconcile,
      {
        observedAt: new Date().toISOString(),
        resources: [
          {
            id: f.pg,
            kind: "service",
            state: "healthy",
            image:
              "postgres:18.4-alpine3.23@sha256:996d0920e4ff9df1fc19dacb904492f3c1ec0ec1cc338f0ad7123be7731c5f5e",
            drift: false,
            note: "",
            logs: "",
            memoryBytes: 0,
          },
        ],
        applied: true,
        stateHash: "f".repeat(64),
        infrastructureDrift: false,
      },
      {
        ...status,
        phase: "complete",
        step: "complete",
        serving: "new",
        writesPossible: true,
        note: "PostgreSQL 18 is serving.",
      },
    );
    await expect(plan.getByRole("heading", { name: /complete/ })).toBeVisible({
      timeout: 12000,
    });
    await plan
      .getByRole("button", { name: "Load upgraded configuration" })
      .click();
    await expect(
      page.getByRole("button", { name: "Save revision", exact: true }),
    ).toBeDisabled();
    if (savedEdit) {
      const comparison = plan.getByRole("region", {
        name: "Upgraded revision comparison",
      });
      await expect(comparison).toBeVisible();
      await expect(comparison.getByRole("status")).toContainText(
        "was preserved",
      );
      await expect(comparison.locator("pre")).toContainText("postgres:18.4");
      await expect(
        page.getByRole("textbox", { name: "Specification (JSON)" }),
      ).toHaveValue(/Saved during upgrade/);
    } else {
      await page.getByText("Advanced specification", { exact: true }).click();
      await expect(
        page.getByRole("textbox", { name: "Specification (JSON)" }),
      ).toHaveValue(/postgres:18\.4/);
      await page.getByText("Add a service", { exact: true }).click();
      const add = page.getByRole("form", { name: "Add service" });
      await add
        .getByLabel("Service name", { exact: true })
        .fill("New PostgreSQL 17");
      await add
        .getByRole("combobox", { name: "Service kind", exact: true })
        .selectOption("postgres");
      await add
        .getByLabel("PostgreSQL version for a new database")
        .selectOption("17");
      await add
        .getByLabel("Database password secret (PostgreSQL)")
        .selectOption(f.secret.id);
      await add.getByRole("button", { name: "Stage new service" }).click();
      await page
        .getByRole("button", { name: "Save revision", exact: true })
        .click();
      await expect(
        page.getByRole("button", { name: "Save revision", exact: true }),
      ).toBeDisabled();
      const stored = await (await page.request.get(`${f.path}/draft`)).json();
      const created = stored.specification.applications.find(
        (a: { name: string }) => a.name === "New PostgreSQL 17",
      );
      expect(created.source.image).toContain("postgres:17.10-");
      expect(created.mounts[0].target).toBe("/var/lib/postgresql/data");
    }
    const accessibility = await new AxeBuilder({ page })
      .withTags(["wcag2a", "wcag2aa", "wcag21aa", "wcag22aa"])
      .analyze();
    expect(accessibility.violations).toEqual([]);
  });
}
