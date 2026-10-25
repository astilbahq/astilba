import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";

async function login(page: import("@playwright/test").Page) {
  await page.goto("/");
  await page.getByLabel("Email", { exact: true }).fill("owner@example.test");
  await page
    .getByLabel("Password", { exact: true })
    .fill("correct horse battery staple");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "Built on your terms." }),
  ).toBeVisible();
}
test("real API: create, conflict, secret, keyboard and reflow", async ({
  page,
  browser,
}, info) => {
  await page.emulateMedia({ colorScheme: "light" });
  await login(page);
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  await page.getByLabel("Project name", { exact: true }).fill("   ");
  await page
    .getByRole("button", { name: "Create project", exact: true })
    .click();
  await expect(
    page.getByLabel("Project name", { exact: true }),
  ).toHaveAttribute("aria-invalid", "true");
  await expect(page.getByRole("alert")).toContainText("Fields: name");
  await page
    .getByLabel("Project name", { exact: true })
    .fill(`Website ${info.project.name}`);
  await page
    .getByRole("button", { name: "Create project", exact: true })
    .click();
  await page
    .getByRole("link", { name: `Website ${info.project.name} Open project →` })
    .click();
  await page.getByText("Add an environment", { exact: true }).click();
  await page.getByLabel("Environment name", { exact: true }).fill("Production");
  await page
    .getByRole("button", { name: "Create environment", exact: true })
    .click();
  await page.getByText("Advanced specification", { exact: true }).click();
  const editor = page.getByLabel("Specification (JSON)");
  await expect(editor).toBeVisible();
  const saved = await editor.inputValue();
  const draftRoute = "**/environments/*/draft";
  let rejectResponse!: (status: number) => void;
  const rejected = new Promise<number>((resolve) => {
    rejectResponse = resolve;
  });
  let releaseResponse!: () => void;
  const released = new Promise<void>((resolve) => {
    releaseResponse = resolve;
  });
  await page.route(draftRoute, async (route) => {
    if (route.request().method() !== "PUT") return route.continue();
    const response = await route.fetch();
    rejectResponse(response.status());
    await released;
    await route.fulfill({ response });
  });
  await editor.fill(JSON.stringify({ ...JSON.parse(saved), version: 0 }));
  await page
    .getByRole("button", { name: "Save revision", exact: true })
    .click();
  expect(await rejected).toBe(400);
  await page.getByRole("button", { name: "Compare revision" }).first().click();
  await page
    .getByRole("button", { name: "I have compared and merged my edits" })
    .click();
  await editor.fill("{");
  await page
    .getByRole("button", { name: "Save revision", exact: true })
    .click();
  await expect(page.getByRole("alert")).toBeVisible();
  const settled = page.waitForResponse(
    (response) =>
      response.url().endsWith("/session") &&
      response.request().method() === "GET",
  );
  releaseResponse();
  await settled;
  await page.unroute(draftRoute);
  await editor.fill(saved);
  await expect(page.getByRole("alert")).toHaveCount(0);
  await expect(editor).toHaveValue(saved);
  await expect(
    page.getByRole("button", { name: "Save revision", exact: true }),
  ).toBeDisabled();
  const context = await browser.newContext({
    storageState: await page.context().storageState(),
  });
  const other = await context.newPage();
  await other.goto(page.url());
  await other.getByText("Advanced specification", { exact: true }).click();
  await expect(other.getByLabel("Specification (JSON)")).toBeVisible();
  const spec = JSON.parse(await editor.inputValue());
  spec.applications.push({
    id: crypto.randomUUID(),
    name: "web",
    source: { kind: "image", image: "nginx:stable" },
  });
  await editor.fill(JSON.stringify(spec, null, 2));
  await page.route(draftRoute, async (route) => {
    if (route.request().method() !== "PUT") return route.continue();
    await route.fulfill({
      status: 503,
      json: {
        code: "internal_error",
        message: "Save service unavailable.",
        requestId: "unavailable-save",
      },
    });
  });
  await page
    .getByRole("button", { name: "Save revision", exact: true })
    .click();
  const serverError = page
    .getByRole("alert")
    .filter({ hasText: "Save service unavailable." });
  await expect(serverError).toBeVisible();
  await editor.fill(`${JSON.stringify(spec, null, 2)}\n`);
  await expect(serverError).toBeVisible();
  await expect(editor).toHaveValue(`${JSON.stringify(spec, null, 2)}\n`);
  await page.unroute(draftRoute);
  await page
    .getByRole("button", { name: "Save revision", exact: true })
    .click();
  await expect(
    page.getByText("Revision saved.", { exact: true }),
  ).toBeVisible();
  const loser = JSON.parse(
    await other.getByLabel("Specification (JSON)").inputValue(),
  );
  loser.variables.push({ name: "NODE_ENV", value: "production" });
  await other
    .getByLabel("Specification (JSON)")
    .fill(JSON.stringify(loser, null, 2));
  await other
    .getByRole("button", { name: "Save revision", exact: true })
    .click();
  await expect(
    other.getByText("Another revision was saved", { exact: true }),
  ).toBeVisible();
  expect(await other.getByLabel("Specification (JSON)").inputValue()).toContain(
    "NODE_ENV",
  );
  await context.close();
  await page.getByLabel("Secret name", { exact: true }).fill("TOKEN");
  await page
    .getByLabel("Secret value", { exact: true })
    .fill("browser-secret-canary");
  await page
    .getByRole("button", { name: "Save secret version", exact: true })
    .click();
  await expect(
    page.getByText("Secret version saved. Its value cannot be read back."),
  ).toBeVisible();
  await expect(page.getByLabel("Secret value", { exact: true })).toHaveValue(
    "",
  );
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);

  // The saved dashboard preference also governs the canvas when the OS differs.
  const root = page.locator("html");
  await page.emulateMedia({ colorScheme: "dark" });
  const canvas = page.locator(".visual-canvas .react-flow");
  const bodyColor = () =>
    page.locator("body").evaluate((el) => getComputedStyle(el).backgroundColor);
  // View transitions apply the palette asynchronously; sample it after that commit.
  await expect(root).toHaveClass("dark");
  await expect(canvas).toHaveCSS("background-color", await bodyColor());
  await page.emulateMedia({ colorScheme: "light" });
  await expect(root).not.toHaveClass("dark");
  await page.getByRole("button", { name: "Appearance", exact: true }).click();
  await page.getByRole("menuitemradio", { name: "Dark", exact: true }).click();
  await expect(root).toHaveClass("dark");
  await expect(canvas).toHaveCSS("background-color", await bodyColor());
  await page.getByRole("menuitemradio", { name: "Light", exact: true }).click();
  await expect(root).not.toHaveClass("dark");
  await page.keyboard.press("Escape");
  await page.getByRole("button", { name: "Service list", exact: true }).click();
  await page.getByRole("button", { name: "web", exact: true }).click();
  const layout = page.getByLabel("Position x", { exact: true });
  await layout.fill("25");
  await page.getByLabel("Position y", { exact: true }).fill("75");
  // Switching through raw editing must not discard a separately staged layout.
  const layoutDraft = await editor.inputValue();
  await editor.fill(`${layoutDraft} `);
  await page
    .getByRole("button", { name: "Save revision", exact: true })
    .click();
  await expect(layout).toHaveValue("25");
  // Make cached data stale while leaving network and rendering timers untouched.
  await page.clock.setFixedTime(new Date(Date.now() + 60000));
  const refreshed = page.waitForResponse(
    (response) =>
      response.url().endsWith("/layout") &&
      response.request().method() === "GET",
    { timeout: 5000 },
  );
  await page.evaluate(() => {
    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      value: "hidden",
    });
    window.dispatchEvent(new Event("visibilitychange"));
    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      value: "visible",
    });
    window.dispatchEvent(new Event("visibilitychange"));
  });
  await refreshed;
  await expect(layout).toHaveValue("25");
  await page
    .getByRole("button", { name: "Save canvas layout", exact: true })
    .click();
  await expect(
    page.getByText("Layout saved. Running services were not changed.", {
      exact: true,
    }),
  ).toBeVisible();
  await page.setViewportSize({ width: 375, height: 812 });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  await page.goto(page.url());
  await expect(
    page.getByRole("heading", { name: "Configuration", exact: true }),
  ).toBeVisible();
  await page.keyboard.press(info.project.name === "webkit" ? "Alt+Tab" : "Tab");
  await expect(
    page.getByRole("link", { name: "Skip to content" }),
  ).toBeFocused();
  await page.getByText("Advanced specification", { exact: true }).click();
  await expect(page.getByLabel("Specification (JSON)")).toBeVisible();
});
test("Worker same-origin path preserves login, cookies and source notices", async ({
  page,
}) => {
  await page.goto("http://127.0.0.1:54183/");
  await page.getByLabel("Email", { exact: true }).fill("owner@example.test");
  await page
    .getByLabel("Password", { exact: true })
    .fill("correct horse battery staple");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "Built on your terms." }),
  ).toBeVisible();
  await page.reload();
  await expect(
    page.getByRole("heading", { name: "Built on your terms." }),
  ).toBeVisible();
  await page.getByRole("link", { name: "Licence & source" }).click();
  await expect(page.locator("body")).toContainText("Corresponding source:");
});

test("server enrollment is explicit and accessible", async ({ page }, info) => {
  await login(page);
  await page.getByRole("link", { name: "Servers", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "Servers", exact: true }),
  ).toBeVisible();
  await page
    .getByLabel("Server name", { exact: true })
    .fill(`Runner ${info.project.name}`);
  await page.getByLabel("Server role").selectOption("build");
  await page
    .getByLabel("I already prepared the verified agent using the manual guide")
    .check();
  await page
    .getByLabel(
      "I have inspected the preflight report and prepared this host for its selected role",
    )
    .check();
  await page.getByRole("button", { name: "Create enrollment" }).click();
  await expect(
    page.getByRole("region", { name: "Enrollment instructions" }),
  ).toBeVisible();
  await expect(page.getByLabel("Enrollment code")).toHaveValue(/.{43}/);
  await expect(
    page
      .getByRole("article")
      .filter({ hasText: `Runner ${info.project.name}` }),
  ).toContainText("Awaiting enrollment");
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  await page.getByRole("button", { name: "Hide enrollment code" }).click();
  await expect(page.getByLabel("Enrollment code")).toHaveCount(0);
  await page.setViewportSize({ width: 375, height: 812 });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
});

test("Worker forwards agent enrollment and verified downloads", async ({
  page,
}) => {
  await page.goto("http://127.0.0.1:54183/");
  await page.getByLabel("Email", { exact: true }).fill("owner@example.test");
  await page
    .getByLabel("Password", { exact: true })
    .fill("correct horse battery staple");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await page.getByRole("link", { name: "Servers", exact: true }).click();
  await page
    .getByLabel("Server name", { exact: true })
    .fill("Worker transport");
  const responsePromise = page.waitForResponse(
    (r) => r.url().endsWith("/servers") && r.request().method() === "POST",
  );
  await page
    .getByLabel("I already prepared the verified agent using the manual guide")
    .check();
  await page
    .getByLabel(
      "I have inspected the preflight report and prepared this host for its selected role",
    )
    .check();
  await page.getByRole("button", { name: "Create enrollment" }).click();
  const enrollment = await (await responsePromise).json();
  const downloadPromise = page.waitForEvent("download");
  await page.getByRole("link", { name: "Download agent", exact: true }).click();
  expect((await downloadPromise).suggestedFilename()).toBe("astilba-agent");
  const manifestPath = await page
    .getByRole("link", { name: "Signed manifest" })
    .getAttribute("href");
  const manifestResponse = await page.request.get(
    `http://127.0.0.1:54183${manifestPath}`,
  );
  expect(manifestResponse.status()).toBe(200);
  const manifest = await manifestResponse.json();
  const artifact = await page.request.get(
    `http://127.0.0.1:54183/agent-artifacts/${manifest.sha256}`,
  );
  expect(artifact.status()).toBe(200);
  const { createHash, createPublicKey, verify } = await import("node:crypto");
  const publicKey = await page.request.get(
    "http://127.0.0.1:54183/agent-public-key.pem",
  );
  const message = await page.request.get(
    `http://127.0.0.1:54183/agent-artifacts/${manifest.sha256}/message`,
  );
  const signature = await page.request.get(
    `http://127.0.0.1:54183/agent-artifacts/${manifest.sha256}/signature`,
  );
  expect(publicKey.status()).toBe(200);
  expect(message.status()).toBe(200);
  expect(signature.status()).toBe(200);
  expect(
    verify(
      null,
      await message.body(),
      createPublicKey(await publicKey.text()),
      await signature.body(),
    ),
  ).toBe(true);
  const bootstrap = await page.request.get(
    "http://127.0.0.1:54183/onboarding/bootstrap.sh",
  );
  expect(bootstrap.status()).toBe(200);
  expect(bootstrap.headers()["content-type"]).toContain("text/plain");

  expect(
    createHash("sha256")
      .update(await artifact.body())
      .digest("hex"),
  ).toBe(manifest.sha256);
  const enrolled = await page.request.post(
    "http://127.0.0.1:54183/agent/v1/enroll",
    {
      data: {
        serverId: enrollment.server.id,
        token: enrollment.token,
        machineId: crypto.randomUUID(),
        credential: "x".repeat(43),
        capabilities: {
          protocol: 1,
          version: "fixture",
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
    },
  );
  expect(enrolled.status()).toBe(200);
  const unknown = await page.request.get(
    `http://127.0.0.1:54183/agent-artifacts/${"0".repeat(64)}/manifest.json`,
  );
  expect(unknown.status()).toBe(404);
});

test("visual services stay staged until a reviewed plan is applied", async ({
  page,
  request,
}, info) => {
  await login(page);
  const session = await (await page.request.get("/api/v1/session")).json();
  const ws = session.workspaces[0].id;
  const headers = {
    "X-CSRF-Token": session.csrfToken,
    Origin: "http://127.0.0.1:54181",
  };
  const enrollmentResponse = await page.request.post(
    `/api/v1/workspaces/${ws}/servers`,
    {
      headers,
      data: { name: `Visual host ${info.project.name}`, role: "deployment" },
    },
  );
  expect(enrollmentResponse.status()).toBe(201);
  const enrollment = await enrollmentResponse.json();
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
          visual: true,
          serviceOutputs: true,
        },
        preflight: {
          ready: true,
          osRelease: "ubuntu 24.04",
          dockerVersion: "fixture",
          composeVersion: "5.6.1",
          freeBytes: 1073741824,
          port80Busy: false,
          port443Busy: false,
          existingContainers: 0,
          notes: [],
        },
      },
    },
  );
  expect(enrolled.status()).toBe(200);
  await page
    .getByLabel("Project name", { exact: true })
    .fill(`Visual ${info.project.name}`);
  await page
    .getByRole("button", { name: "Create project", exact: true })
    .click();
  await page
    .getByRole("link", { name: `Visual ${info.project.name} Open project →` })
    .click();
  await page.getByText("Add an environment", { exact: true }).click();
  await page.getByLabel("Environment name", { exact: true }).fill("Production");
  await page
    .getByRole("button", { name: "Create environment", exact: true })
    .click();
  await page.getByLabel("Secret name", { exact: true }).fill("DB_PASSWORD");
  await page
    .getByLabel("Secret value", { exact: true })
    .fill("visual-browser-canary");
  await page
    .getByRole("button", { name: "Save secret version", exact: true })
    .click();
  await expect(
    page.getByText("Secret version saved. Its value cannot be read back."),
  ).toBeVisible();
  await page.getByText("Add a service", { exact: true }).click();
  const add = page.getByRole("form", { name: "Add service" });
  await add.getByLabel("Service name", { exact: true }).fill("Website");
  const image =
    "busybox:1.37.0@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e";
  await add.getByLabel("Container image (web or worker)").fill(image);
  await add.getByRole("button", { name: "Stage new service" }).click();
  await add.getByLabel("Service name", { exact: true }).fill("Database");
  await add.getByLabel("Service kind").selectOption("postgres");
  const passwordOption = await add
    .getByLabel("Database password secret (PostgreSQL)")
    .locator("option")
    .filter({ hasText: "DB_PASSWORD" })
    .getAttribute("value");
  if (!passwordOption) throw new Error("Database secret option missing");
  await add
    .getByLabel("Database password secret (PostgreSQL)")
    .selectOption(passwordOption);
  await add.getByRole("button", { name: "Stage new service" }).click();
  await page.getByText("Connections", { exact: true }).click();
  const binding = page.getByRole("form", { name: "Create service binding" });
  await binding
    .getByLabel("Source service")
    .selectOption({ label: "Database" });
  await binding
    .getByLabel("Receiving service")
    .selectOption({ label: "Website" });
  await binding.getByRole("button", { name: "Stage binding" }).click();
  await page.getByText("HTTP and TCP routes", { exact: true }).click();
  const route = page.getByRole("form", { name: "Route properties" });
  await route.getByLabel("Target service").selectOption({ label: "Website" });
  await route.getByLabel("Domain", { exact: true }).fill("visual.example.test");
  await route.getByLabel("TLS policy").selectOption("off");
  await route.getByRole("button", { name: "Stage route" }).click();
  const operationsBefore = await (
    await page.request.get(`/api/v1/workspaces/${ws}/operations`)
  ).json();
  expect(
    operationsBefore.filter(
      (o: { kind: string; serverId: string }) =>
        o.kind === "visual" && o.serverId === enrollment.server.id,
    ),
  ).toHaveLength(0);
  await page
    .getByRole("button", { name: "Save revision", exact: true })
    .click();
  await expect(
    page.getByText("Revision saved.", { exact: true }),
  ).toBeVisible();
  await page.getByLabel("Deployment server").selectOption(enrollment.server.id);
  await page
    .getByLabel(
      "Approve the host permissions listed in the plan, including proxy, ports, certificates and elevated container privileges",
    )
    .check();
  await page.getByRole("button", { name: "Prepare plan", exact: true }).click();
  // A protocol fixture acts as the remote host; the native test covers Docker effects.
  const poll = async () => {
    const response = await request.get("http://127.0.0.1:54181/agent/v1/poll", {
      headers: agentHeaders,
    });
    expect(response.status()).toBe(200);
    const value = await response.json();
    expect(value.command).toBeTruthy();
    return value.command;
  };
  const resolve = await poll();
  expect(resolve.visual.action).toBe("resolve");
  expect(JSON.stringify(resolve)).not.toContain("visual-browser-canary");
  const images = Object.fromEntries(
    resolve.visual.plan.specification.applications.map(
      (a: { id: string; source: { image: string } }) => [a.id, a.source.image],
    ),
  );
  const report = async (
    command: typeof resolve,
    observation: unknown,
    state = "succeeded",
  ) => {
    const response = await request.post(
      "http://127.0.0.1:54181/agent/v1/report",
      {
        headers: agentHeaders,
        data: {
          id: command.id,
          generation: command.generation,
          planHash: command.planHash,
          sequence: 2,
          state,
          steps: [],
          note: "Protocol fixture",
          observation,
        },
      },
    );
    expect(response.status()).toBe(204);
  };
  await report(resolve, {
    observedAt: new Date().toISOString(),
    resources: [],
    images,
    applied: false,
    stateHash: "a".repeat(64),
    infrastructureDrift: false,
  });
  await expect(
    page.getByRole("heading", { name: "Plan ready", exact: true }),
  ).toBeVisible();
  expect(await page.locator("body").textContent()).not.toContain(
    "visual-browser-canary",
  );
  await page
    .getByRole("button", { name: "Apply reviewed plan", exact: true })
    .click();
  const deploy = await poll();
  expect(deploy.visual.action).toBe("deploy");
  expect(Object.values(deploy.visual.secrets)).toContain(
    "visual-browser-canary",
  );
  await report(deploy, {
    observedAt: new Date().toISOString(),
    resources: deploy.visual.plan.specification.applications.map(
      (a: { id: string; kind: string }) => ({
        id: a.id,
        kind: "service",
        state: a.kind === "postgres" ? "healthy" : "running",
        image: images[a.id],
        drift: false,
        note: "",
        logs: "",
        memoryBytes: 1048576,
      }),
    ),
    applied: true,
    stateHash: "b".repeat(64),
    infrastructureDrift: false,
  });
  await expect(page.getByText("1.0 MiB in use").first()).toBeVisible();
  await expect(page.getByText("visual · succeeded").last()).toBeVisible();
  await page.getByRole("button", { name: "Service list", exact: true }).click();
  await page.getByRole("button", { name: "Website", exact: true }).click();
  const properties = page.getByRole("form", { name: "Service properties" });
  await properties
    .getByLabel("Service name", { exact: true })
    .fill("Updated website");
  await properties
    .getByLabel("Command arguments (one per line)")
    .fill("sh\n-c\nsleep 1");
  await page.getByText("Variables", { exact: true }).click();
  const variable = page.getByRole("form", { name: "Add service variable" });
  await variable.getByLabel("Variable name").fill("MESSAGE");
  await variable
    .getByLabel("Plain value", { exact: true })
    .fill("second revision");
  await variable.getByRole("button", { name: "Stage variable" }).click();
  await expect(
    properties.getByLabel("Service name", { exact: true }),
  ).toHaveValue("Updated website");
  await expect(
    properties.getByLabel("Command arguments (one per line)"),
  ).toHaveValue("sh\n-c\nsleep 1");
  await expect(
    page.getByRole("button", { name: "Apply reviewed plan" }),
  ).toBeDisabled();
  await page
    .getByRole("button", { name: "Save revision", exact: true })
    .click();
  await expect(
    page.getByText("The saved revision changed. Prepare a new plan."),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Apply reviewed plan" }),
  ).toBeDisabled();
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  await page.getByLabel("Position x", { exact: true }).fill("99");
  await page
    .getByRole("button", { name: "Save canvas layout", exact: true })
    .click();
  await expect(
    page.getByText("Layout saved. Running services were not changed.", {
      exact: true,
    }),
  ).toBeVisible();
  await page
    .getByRole("button", {
      name: "Remove service; retain data volumes",
      exact: true,
    })
    .click();
  await page
    .getByRole("button", { name: "Save revision", exact: true })
    .click();
  // A position belonging to a removed service must not reject the next layout save.
  await page.getByRole("button", { name: "Database", exact: true }).click();
  await page.getByLabel("Position x", { exact: true }).fill("123");
  const layoutResponse = page.waitForResponse(
    (r) => r.url().endsWith("/layout") && r.request().method() === "PUT",
  );
  await page
    .getByRole("button", { name: "Save canvas layout", exact: true })
    .click();
  expect((await layoutResponse).status()).toBe(200);
});
