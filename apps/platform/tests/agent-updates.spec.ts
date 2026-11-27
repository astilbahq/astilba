import { randomBytes } from "node:crypto";
import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import { acknowledgesReleases } from "./fixtures/agent-releases";
import { signInAsOwner } from "./fixtures/owner-sign-in";
import { api } from "./fixtures/ports";

// The fixture catalog's arm64 releases: download (minimum 0.0.0), floor (0.1.0) and raised
// (0.2.0). An agent running floor installs only releases whose minimum version is at least 0.1.0,
// so the Servers page offers raised, shows download disabled with the reason, and management
// refuses download before it reaches the host.
test("the Servers page offers only releases at or above the agent's floor", async ({
  page,
  request,
}, info) => {
  const name = `Floor ${info.project.name}${info.repeatEachIndex ? ` ${info.repeatEachIndex + 1}` : ""}`;
  await page.goto("/");
  await signInAsOwner(page);
  await expect(page.getByRole("heading", { name: "Projects" })).toBeVisible();
  const session = await (await page.request.get("/api/v1/session")).json();
  const ws = session.workspaces[0].id;
  const headers = { "X-CSRF-Token": session.csrfToken, Origin: api };
  const created = await page.request.post(`/api/v1/workspaces/${ws}/servers`, {
    headers,
    data: { name, role: "deployment" },
  });
  expect(created.status(), await created.text()).toBe(201);
  const enrollment = await created.json();
  const enrolled = await request.post(`${api}/agent/v1/enroll`, {
    data: {
      serverId: enrollment.server.id,
      token: enrollment.token,
      credential: randomBytes(32).toString("base64url"),
      machineId: crypto.randomUUID(),
      capabilities: {
        protocol: 1,
        version: "0.2.0-fixture.floor",
        os: "linux",
        arch: "arm64",
        docker: true,
        ...acknowledgesReleases,
      },
      preflight: {
        ready: true,
        osRelease: "ubuntu 24.04",
        dockerVersion: "fixture",
        freeBytes: 1073741824,
        port80Busy: false,
        port443Busy: false,
        existingContainers: 0,
        notes: [],
      },
    },
  });
  expect(enrolled.status(), await enrolled.text()).toBe(200);

  await page.getByRole("link", { name: "Servers", exact: true }).click();
  const card = page
    .locator("article")
    .filter({ has: page.getByRole("heading", { name, exact: true }) });
  const select = card.getByLabel("Agent release");
  await expect(select).toHaveAccessibleDescription(
    "This agent installs only releases whose minimum version is at least 0.1.0, that of the release it runs.",
  );
  await expect(select).toHaveValue("0.3.0-fixture.raised");
  await expect(
    select.getByRole("option", {
      name: "0.1.0-fixture.download (minimum version 0.0.0, below 0.1.0)",
    }),
  ).toBeDisabled();
  await expect(select.getByRole("option")).toHaveCount(2);
  expect(
    (await new AxeBuilder({ page }).include("main").analyze()).violations,
  ).toEqual([]);

  // A request that bypasses the page is refused too, naming the floor, and makes no operation.
  const refused = await page.request.post(
    `/api/v1/workspaces/${ws}/servers/${enrollment.server.id}/update`,
    { headers, data: { version: "0.1.0-fixture.download" } },
  );
  expect(refused.status()).toBe(400);
  expect((await refused.json()).message).toBe(
    "This server's agent installs only releases whose minimum version is at least 0.1.0, that of the release it runs. Choose one of those.",
  );
  const before = await (
    await page.request.get(`/api/v1/workspaces/${ws}/operations`)
  ).json();
  expect(
    before.filter(
      (op: { serverId: string }) => op.serverId === enrollment.server.id,
    ),
  ).toEqual([]);

  page.once("dialog", (dialog) => dialog.accept());
  await card.getByRole("button", { name: "Update agent" }).click();
  await expect
    .poll(async () => {
      const operations = await (
        await page.request.get(`/api/v1/workspaces/${ws}/operations`)
      ).json();
      return operations
        .filter(
          (op: { serverId: string }) => op.serverId === enrollment.server.id,
        )
        .map((op: { kind: string }) => op.kind);
    })
    .toEqual(["update"]);
});
