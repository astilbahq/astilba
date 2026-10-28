import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import { openEnvironmentSection } from "./environment-section";

test("guided host preparation and domain staging preserve owner review and conflicts", async ({
  page,
}, info) => {
  await page.goto("/");
  await page.getByLabel("Email", { exact: true }).fill("owner@example.test");
  await page
    .getByLabel("Password", { exact: true })
    .fill("correct horse battery staple");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await page.getByRole("link", { name: "Servers", exact: true }).click();
  const setup = page.getByRole("region", { name: "Server onboarding" });
  await expect(
    setup.getByRole("button", { name: "Create enrollment" }),
  ).toBeDisabled();
  await setup.getByLabel("Certificate contact email").fill("ops@example.test");
  const session = await (await page.request.get("/api/v1/session")).json();
  const ws = session.workspaces[0].id;
  const onboarding = await (
    await page.request.get(`/api/v1/workspaces/${ws}/onboarding`)
  ).json();
  // Cryptographic pinning is exercised by the native host gate; this journey
  // checks that command preparation requires an explicit fingerprint.
  const releases = await (
    await page.request.get(`/api/v1/workspaces/${ws}/agent-releases`)
  ).json();
  await setup.getByLabel("Host architecture").selectOption(releases[0].arch);
  await setup
    .getByLabel("Independently trusted signing-key fingerprint")
    .fill(onboarding.keyFingerprint);
  await setup
    .getByLabel("Allow installing missing Docker and Compose.", {
      exact: false,
    })
    .check();
  await setup.getByLabel("Certificate contact email").fill("unfinished");
  await setup.getByLabel("Server role").selectOption("build");
  await expect(
    setup.locator("code").filter({ hasText: "--apply" }),
  ).toContainText("--role build");
  await expect(
    setup.locator("code").filter({ hasText: "--apply" }),
  ).not.toContainText("--certificate-contact");
  await setup.getByLabel("Server role").selectOption("deployment");
  await setup.getByLabel("Certificate contact email").fill("ops@example.test");
  await expect(
    setup.locator("code").filter({ hasText: "--apply" }),
  ).toContainText("--certificate-contact 'ops@example.test'");
  await setup
    .getByLabel("Server name", { exact: true })
    .fill(`Onboarding ${info.project.name}`);
  await setup
    .getByLabel(
      "I have inspected the preflight report and prepared this host for its selected role",
    )
    .check();
  const created = page.waitForResponse(
    (r) => r.request().method() === "POST" && r.url().endsWith("/servers"),
  );
  await setup.getByRole("button", { name: "Create enrollment" }).click();
  const enrollment = await (await created).json();
  await expect(page.getByLabel("Enrollment code")).toHaveValue(/.{43}/);
  await expect(
    page.getByRole("region", { name: "Enrollment instructions" }),
  ).toContainText("systemctl enable --now");
  await page.getByRole("button", { name: "Hide enrollment code" }).click();
  const server = page.getByRole("article").filter({
    has: page.getByRole("heading", {
      name: enrollment.server.name,
      exact: true,
    }),
  });
  await server.getByText("Application domains", { exact: true }).click();
  const form = server.getByRole("form", {
    name: `Domain addresses for ${enrollment.server.name}`,
  });
  await form.getByLabel("Public IPv4", { exact: true }).fill("198.51.100.7");
  let releaseApproval!: () => void;
  const approvalGate = new Promise<void>((resolve) => {
    releaseApproval = resolve;
  });
  const approvalPattern = `**/servers/${enrollment.server.id}/domain`;
  await page.route(approvalPattern, async (route) => {
    if (route.request().method() !== "PUT") {
      await route.continue();
      return;
    }
    const response = await route.fetch();
    await approvalGate;
    await route.fulfill({ response });
  });
  await form.getByRole("button", { name: "Approve domain addresses" }).click();
  try {
    await expect(
      form.getByLabel("Public IPv4", { exact: true }),
    ).toBeDisabled();
  } finally {
    releaseApproval();
  }
  await expect(form.getByLabel("Public IPv4", { exact: true })).toBeEnabled();
  await page.unroute(approvalPattern);

  await expect(server).toContainText("DNS configuration: manual");
  await form.getByLabel("Public IPv4", { exact: true }).fill("198.51.100.9");
  const headers = {
    "X-CSRF-Token": session.csrfToken,
    Origin: "http://127.0.0.1:15181",
  };
  const domainPath = `/api/v1/workspaces/${ws}/servers/${enrollment.server.id}/domain`;
  expect(
    (
      await page.request.put(domainPath, {
        headers,
        data: { baseGeneration: 1, ipv4: "198.51.100.8", ipv6: "" },
      })
    ).status(),
  ).toBe(200);
  await form.getByRole("button", { name: "Approve domain addresses" }).click();
  await expect(form.getByRole("alert")).toBeVisible();
  await expect(form.getByLabel("Public IPv4", { exact: true })).toHaveValue(
    "198.51.100.9",
  );
  expect((await (await page.request.get(domainPath)).json()).ipv4).toBe(
    "198.51.100.8",
  );
  await expect(
    form.getByRole("button", { name: "Load current addresses" }),
  ).toBeVisible();
  await form.getByRole("button", { name: "Load current addresses" }).click();
  await expect(form.getByLabel("Public IPv4", { exact: true })).toHaveValue(
    "198.51.100.8",
  );
  expect(
    (
      await new AxeBuilder({ page })
        .include('[aria-label="Server onboarding"]')
        .analyze()
    ).violations,
  ).toEqual([]);
  await setup.screenshot({ path: info.outputPath("onboarding-desktop.png") });
  await page.setViewportSize({ width: 390, height: 844 });
  await setup.screenshot({ path: info.outputPath("onboarding-mobile.png") });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  const post = async (path: string, data: unknown) => {
    const response = await page.request.post(path, { headers, data });
    expect(response.ok(), await response.text()).toBeTruthy();
    return response.json();
  };
  const prefix = `/api/v1/workspaces/${ws}`;
  const project = await post(`${prefix}/projects`, {
    name: `Domain route ${info.project.name}`,
  });
  const env = await post(`${prefix}/projects/${project.id}/environments`, {
    name: "Test",
  });
  const app = crypto.randomUUID();
  const envPath = `${prefix}/projects/${project.id}/environments/${env.id}`;
  expect(
    (
      await page.request.put(`${envPath}/draft`, {
        headers,
        data: {
          baseRevisionId: env.draftRevisionId,
          specification: {
            version: 1,
            variables: [],
            applications: [
              {
                id: app,
                name: "Web",
                source: { kind: "image", image: "nginx:alpine" },
              },
            ],
          },
        },
      })
    ).status(),
  ).toBe(200);
  await page.goto(
    `/?workspace=${ws}&project=${project.id}&environment=${env.id}`,
  );
  await openEnvironmentSection(page, "Configuration");
  await page
    .getByText("Generate an application domain", { exact: true })
    .click();
  const generated = page.getByRole("form", {
    name: "Generated application domain",
  });
  await generated
    .getByLabel("Generated domain server")
    .selectOption(enrollment.server.id);
  await generated
    .getByRole("button", { name: "Prepare generated route" })
    .click();
  await expect(
    page.getByRole("button", { name: "Stage generated HTTP route" }),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "Stage generated HTTP route" })
    .click();
  await expect(
    page.getByRole("button", { name: "Save revision", exact: true }),
  ).toBeEnabled();
  const before = await (await page.request.get(`${envPath}/draft`)).json();
  expect(before.specification.routes ?? []).toHaveLength(0);
  await openEnvironmentSection(page, "Configuration");
  await page
    .getByRole("button", { name: "Save revision", exact: true })
    .click();
  await expect
    .poll(
      async () =>
        (
          (await (await page.request.get(`${envPath}/draft`)).json())
            .specification.routes ?? []
        ).length,
    )
    .toBe(1);
  const after = await (await page.request.get(`${envPath}/draft`)).json();
  expect(after.specification.routes[0].domain).toMatch(
    /^a[a-z2-7]{52}\.s[a-f0-9]{32}\.apps\.example\.test$/,
  );
});
