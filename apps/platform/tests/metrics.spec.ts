import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";

test.afterEach(async ({ page }) => {
  await page.unrouteAll({ behavior: "wait" });
});

test("resource history exposes missing, measured and stale data", async ({
  page,
}, info) => {
  await page.clock.install();
  await page.goto("/");
  await page.getByLabel("Email", { exact: true }).fill("owner@example.test");
  await page
    .getByLabel("Password", { exact: true })
    .fill("correct horse battery staple");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "Sign out", exact: true }),
  ).toBeVisible();
  await page.getByRole("link", { name: "Servers", exact: true }).click();
  const session = await (await page.request.get("/api/v1/session")).json();
  const workspace = session.workspaces[0].id;
  const response = await page.request.post(
    `/api/v1/workspaces/${workspace}/servers`,
    {
      headers: {
        "X-CSRF-Token": session.csrfToken,
        Origin: "http://127.0.0.1:15181",
      },
      data: { name: `Metrics ${info.project.name}`, role: "deployment" },
    },
  );
  expect(response.status()).toBe(201);
  const enrollment = await response.json();
  const server = enrollment.server.id;
  await page.reload();
  const card = page.getByRole("article").filter({
    has: page.getByRole("heading", {
      name: `Metrics ${info.project.name}`,
      exact: true,
    }),
  });
  await card.getByText("Resource history", { exact: true }).click();
  await expect(card.getByText(/No samples in this window/)).toBeVisible();
  expect(
    (
      await page.request.post("/agent/v1/enroll", {
        data: {
          serverId: server,
          token: enrollment.token,
          machineId: crypto.randomUUID(),
          credential: "m".repeat(43),
          capabilities: {
            protocol: 1,
            version: "metrics-test",
            os: "linux",
            arch: "arm64",
            docker: true,
          },
          preflight: {
            ready: true,
            osRelease: "ubuntu 24.04",
            dockerVersion: "fixture",
            freeBytes: 1000000000,
            port80Busy: false,
            port443Busy: false,
            existingContainers: 0,
            notes: [],
          },
        },
      })
    ).status(),
  ).toBe(200);
  const report = await page.request.post("/agent/v1/metrics", {
    headers: { Authorization: `Bearer ${server}.${"m".repeat(43)}` },
    data: {
      host: {
        diskSource: "docker",
        cpuPercent: 37,
        memory: { usedBytes: 1073741824, totalBytes: 2147483648 },
        disk: { usedBytes: 95, totalBytes: 100 },
      },
      services: [],
      partial: true,
    },
  });
  expect(report.status()).toBe(204);
  await page.reload();
  await card.getByText("Resource history", { exact: true }).click();
  await expect(
    card.getByRole("definition").filter({ hasText: "37.0%" }),
  ).toBeVisible();
  await expect(card.getByText(/Disk pressure:/)).toBeVisible();
  await expect(card.getByText(/Partial sample:/)).toBeVisible();
  // Seed a long visual history from the real response; storage bounds are covered by the database journey.
  await page.route("**/metrics?hours=24", async (route) => {
    const response = await route.fetch();
    const data = await response.json();
    const latest = data.samples.at(-1);
    data.samples = Array.from({ length: 60 }, (_, i) => ({
      ...latest,
      receivedAt: new Date(
        new Date(latest.receivedAt).getTime() - (59 - i) * 60000,
      ).toISOString(),
    }));
    await route.fulfill({ response, json: data });
  });
  await card.getByLabel("History window").selectOption("24");
  await card.getByText("View sample data", { exact: true }).click();
  await expect(
    card.getByRole("table", { name: /Recorded host readings/ }),
  ).toContainText("37.0%");
  const summary = card.getByText("View sample data", { exact: true });
  await summary.focus();
  await page.keyboard.press("Tab");
  const scroll = card.getByRole("region", { name: "Host history readings" });
  await expect(scroll).toBeFocused();
  await page.keyboard.press("End");
  await expect
    .poll(() => scroll.evaluate((element) => element.scrollTop))
    .toBeGreaterThan(0);
  expect(
    (await new AxeBuilder({ page }).include("main").analyze()).violations,
  ).toEqual([]);
  await page.clock.fastForward(4 * 60000);
  await expect(
    card.getByText(/Stale — current resource usage is unknown/),
  ).toBeVisible();
});
