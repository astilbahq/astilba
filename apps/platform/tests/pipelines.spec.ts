import { createHmac } from "node:crypto";
import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";

test("repository setup, Git service, queued build and cancellation use the real API", async ({
  page,
  request,
}, info) => {
  await page.goto("/");
  await page.getByLabel("Email", { exact: true }).fill("owner@example.test");
  await page
    .getByLabel("Password", { exact: true })
    .fill("correct horse battery staple");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(page.getByLabel("Project name", { exact: true })).toBeVisible();
  const session = await (await page.request.get("/api/v1/session")).json(),
    ws = session.workspaces[0].id;
  const headers = {
    "X-CSRF-Token": session.csrfToken,
    Origin: "http://127.0.0.1:54181",
  };
  const enrollment = await (
    await page.request.post(`/api/v1/workspaces/${ws}/servers`, {
      headers,
      data: { name: `Build ${info.project.name}`, role: "build" },
    })
  ).json();
  const credential = Buffer.from(
    crypto.getRandomValues(new Uint8Array(32)),
  ).toString("base64url");
  const agentHeaders = {
    Authorization: `Bearer ${enrollment.server.id}.${credential}`,
  };
  const enrolled = await request.post(
    "http://127.0.0.1:54181/agent/v1/enroll",
    {
      data: {
        serverId: enrollment.server.id,
        token: enrollment.token,
        credential,
        machineId: crypto.randomUUID(),
        capabilities: {
          protocol: 1,
          version: "fixture",
          os: "linux",
          arch: "arm64",
          docker: true,
          build: true,
          buildControls: true,
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
    },
  );
  expect(enrolled.status()).toBe(200);
  expect(enrolled.headers()["astilba-agent-features"]).toBe("build-controls");
  await page
    .getByLabel("Project name", { exact: true })
    .fill(`Builds ${info.project.name}`);
  await page
    .getByRole("button", { name: "Create project", exact: true })
    .click();
  await page
    .getByRole("link", { name: `Builds ${info.project.name} Open project →` })
    .click();
  await page.getByText("Add an environment", { exact: true }).click();
  await page.getByLabel("Environment name", { exact: true }).fill("Preview");
  await page
    .getByRole("button", { name: "Create environment", exact: true })
    .click();
  await page
    .getByText("Repository, registry and build secret settings", {
      exact: true,
    })
    .click();
  const connect = page.getByRole("form", { name: "Connect repository" });
  await connect
    .getByLabel("Connection name", { exact: true })
    .fill(`Repo ${info.project.name}`);
  await connect
    .getByRole("combobox", { name: "Provider", exact: true })
    .selectOption("gitea");
  await connect
    .getByLabel("Provider origin, such as https://github.com")
    .fill("http://127.0.0.1:54185");
  await connect
    .getByLabel("Repository path, such as team/application")
    .fill("team/app");
  await connect
    .getByLabel("API and submission username", { exact: true })
    .fill("writer");
  await connect
    .getByLabel("API and submission token", { exact: true })
    .fill("browser-api-fixture");
  await connect
    .getByLabel("Read-only clone username", { exact: true })
    .fill("reader");
  await connect
    .getByLabel("Read-only clone token", { exact: true })
    .fill("browser-clone-fixture");
  const connected = page.waitForResponse(
    (r) =>
      r.url().endsWith("/git-connections") && r.request().method() === "POST",
  );
  await connect
    .getByRole("button", { name: "Connect repository", exact: true })
    .click();
  const connection = await (await connected).json();
  await expect(
    page.getByLabel("Webhook secret", { exact: true }),
  ).toBeVisible();
  const webhook = JSON.stringify({
    ref: "refs/heads/main",
    after: "a".repeat(40),
    repository: { id: 12 },
  });
  // Verify the optional Worker preserves webhook signature bytes and routes to the same domain service.
  const response = await request.post(
    `http://127.0.0.1:54183/hooks/git/${connection.connection.id}`,
    {
      headers: {
        "Content-Type": "application/json",
        "X-Gitea-Event": "push",
        "X-Gitea-Delivery": crypto.randomUUID(),
        "X-Gitea-Signature": createHmac("sha256", connection.webhookSecret)
          .update(webhook)
          .digest("hex"),
      },
      data: webhook,
    },
  );
  expect(response.status()).toBe(202);
  await page.getByRole("button", { name: "Dismiss webhook secret" }).click();
  await expect(page.getByLabel("Webhook secret", { exact: true })).toHaveCount(
    0,
  );
  await page.getByText("Add a service from Git", { exact: true }).click();
  const add = page.getByRole("form", { name: "Add Git service" });
  await add.getByLabel("Service name", { exact: true }).fill("web");
  await add
    .getByRole("combobox", { name: "Repository connection", exact: true })
    .selectOption(connection.connection.id);
  await add.getByLabel("Branch", { exact: true }).fill("main");
  await add
    .getByRole("combobox", { name: "Build runner", exact: true })
    .selectOption(enrollment.server.id);
  await add
    .getByRole("button", { name: "Stage Git service", exact: true })
    .click();
  await page
    .getByRole("button", { name: "Save revision", exact: true })
    .click();
  await expect(
    page.getByText("Revision saved.", { exact: true }),
  ).toBeVisible();
  await page.getByText("Build a service", { exact: true }).click();
  const build = page.getByRole("form", { name: "Build a service" });
  await build.getByLabel("Branch", { exact: true }).fill("main");
  await build.getByText("Build resource limits", { exact: true }).click();
  await build.getByLabel("CPU cores", { exact: true }).fill("1");
  await build.getByLabel("Memory limit (MiB)", { exact: true }).fill("512");
  await build
    .getByLabel("Automatic build watch paths", { exact: true })
    .fill("apps/web\npnpm-lock.yaml");
  // Each numeric value is supported, but their combined storage must fit memory.
  await build.getByRole("button", { name: "Queue build", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText(
    "Increase the memory limit",
  );
  await expect(
    build.getByLabel("Memory limit (MiB)", { exact: true }),
  ).toHaveAttribute("aria-invalid", "true");
  await expect(
    build.getByLabel("Memory limit (MiB)", { exact: true }),
  ).toHaveAccessibleDescription(/96 MiB/);
  await expect(
    build.getByLabel("Automatic build watch paths", { exact: true }),
  ).toHaveValue("apps/web\npnpm-lock.yaml");
  await build.getByLabel("Memory limit (MiB)", { exact: true }).fill("2048");
  await build
    .getByLabel("Assignment lifetime (minutes)", { exact: true })
    .fill("2");
  await build
    .getByLabel("Automatic build watch paths", { exact: true })
    .fill("apps/web\napps/web");
  await build.getByRole("button", { name: "Queue build", exact: true }).click();
  await expect(
    build.getByLabel("Automatic build watch paths", { exact: true }),
  ).toHaveAccessibleDescription("Remove duplicate watch paths.");
  await build
    .getByLabel("Automatic build watch paths", { exact: true })
    .fill("../outside");
  await build.getByRole("button", { name: "Queue build", exact: true }).click();
  await expect(
    build.getByLabel("Automatic build watch paths", { exact: true }),
  ).toHaveAttribute("aria-invalid", "true");
  await expect(
    build.getByLabel("Automatic build watch paths", { exact: true }),
  ).toHaveAccessibleDescription(/repository-relative watch paths/);
  await build
    .getByLabel("Automatic build watch paths", { exact: true })
    .fill("apps/web\npnpm-lock.yaml");
  await build.getByRole("button", { name: "Queue build", exact: true }).click();
  await expect(
    page.getByText(
      "Build queued. Existing workloads stay on their applied image until deployment.",
      { exact: true },
    ),
  ).toBeVisible();
  const poll = await request.get("http://127.0.0.1:54181/agent/v1/poll", {
    headers: agentHeaders,
  });
  expect(poll.status()).toBe(200);
  const command = (await poll.json()).command;
  expect(command.kind).toBe("pipeline");
  expect(command.pipeline.commit).toBe("a".repeat(40));
  expect(command.pipeline.build.budget).toEqual({
    cpus: 1,
    memoryMiB: 2048,
    sourceMiB: 256,
    stateMiB: 1024,
    minutes: 2,
  });
  expect(command.pipeline.build.watchPaths).toEqual([
    "apps/web",
    "pnpm-lock.yaml",
  ]);
  expect(command.pipeline.compareCommit).toBeUndefined();
  if (info.project.name === "chromium")
    await build.screenshot({ path: info.outputPath("build-controls.png") });
  expect(Date.parse(command.deadline) - Date.now()).toBeLessThanOrEqual(120000);
  expect(command.pipeline.access.clone.password).toBe("browser-clone-fixture");
  expect(command.pipeline.access.api.password).toBe("");
  const cancelled = page.waitForResponse(
    (r) => r.url().endsWith("/cancel") && r.request().method() === "POST",
  );
  await page.getByRole("button", { name: "Cancel build", exact: true }).click();
  expect((await cancelled).status()).toBe(204);
  const report = await request.post("http://127.0.0.1:54181/agent/v1/report", {
    headers: agentHeaders,
    data: {
      id: command.id,
      generation: command.generation,
      planHash: command.planHash,
      sequence: 2,
      state: "cancelled",
      note: "Cancelled by user.",
      steps: [],
    },
  });
  expect(report.status()).toBe(204);
  await expect(
    page.getByRole("heading", { name: /build · aaaaaaaaaaaa · cancelled/ }),
  ).toBeVisible();

  // A later successful build exercises artifact reflow and identity through editing.
  await build.getByRole("button", { name: "Queue build", exact: true }).click();
  const completed = (
    await (
      await request.get("http://127.0.0.1:54181/agent/v1/poll", {
        headers: agentHeaders,
      })
    ).json()
  ).command;
  const digest = `sha256:${"b".repeat(64)}`;
  const succeeded = await request.post(
    "http://127.0.0.1:54181/agent/v1/report",
    {
      headers: agentHeaders,
      data: {
        id: completed.id,
        generation: completed.generation,
        planHash: completed.planHash,
        sequence: 2,
        state: "succeeded",
        note: "Build complete.",
        steps: [],
        pipeline: {
          commit: completed.pipeline.commit,
          image: `astilba-build-${completed.id}:built`,
          digest,
          architecture: completed.pipeline.build.architecture,
        },
      },
    },
  );
  expect(succeeded.status(), await succeeded.text()).toBe(204);
  await expect(page.getByText(digest, { exact: true })).toBeVisible();
  await page
    .getByRole("button", { name: "Promote artifact to draft", exact: true })
    .click();
  await expect(
    page.getByText(/Artifact promoted to the saved draft/),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "Reload current", exact: true })
    .click();
  await page.getByRole("button", { name: "Service list", exact: true }).click();
  await page.getByRole("button", { name: "web", exact: true }).click();
  const properties = page.getByRole("form", {
    name: "Service properties",
    exact: true,
  });
  await properties
    .getByRole("button", { name: "Stage service changes", exact: true })
    .click();
  await page
    .getByRole("button", { name: "Save revision", exact: true })
    .click();
  await expect(
    page.getByText("Revision saved.", { exact: true }),
  ).toBeVisible();
  const query = new URL(page.url()).searchParams;
  const saved = await (
    await page.request.get(
      "/api/v1/workspaces/" +
        ws +
        "/projects/" +
        query.get("project") +
        "/environments/" +
        query.get("environment") +
        "/draft",
    )
  ).json();
  expect(saved.specification.applications[0].source.artifactId).toBe(
    completed.id,
  );
  await page.emulateMedia({ reducedMotion: "reduce", colorScheme: "dark" });
  const accessibility = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21aa", "wcag22aa"])
    .analyze();
  expect(accessibility.violations).toEqual([]);
  await page.setViewportSize({ width: 390, height: 844 });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
});
