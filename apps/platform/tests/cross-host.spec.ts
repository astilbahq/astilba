import { createHash, randomBytes } from "node:crypto";
import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import type { Schema } from "../src/api/client";
import { managedDatabase } from "./fixtures/managed-database";

test("private network and placement retain failed drafts and review both hosts", async ({
  page,
  request,
}, info) => {
  const name = `Placement ${info.project.name}`;
  const f = await managedDatabase(page, request, name, 18, name);
  const servers: Schema["Server"][] = await (
    await page.request.get(`${f.prefix}/servers`)
  ).json();
  const primary = servers.find((server) => server.name === `Recovery ${name}`);
  expect(primary).toBeTruthy();
  if (!primary) throw new Error("missing applied primary");
  const secondary: Schema["Enrollment"] = await f.post(`${f.prefix}/servers`, {
    name: `Secondary ${info.project.name}`,
    role: "deployment",
  });
  const credential = randomBytes(32).toString("base64url");
  const heartbeat = {
    capabilities: {
      protocol: 1,
      version: "fixture",
      os: "linux",
      arch: "arm64",
      docker: true,
      visual: true,
      backups: true,
      safeReleases: true,
      replicas: true,
      crossHost: true,
    },
    preflight: {
      ready: true,
      osRelease: "ubuntu 24.04",
      dockerVersion: "fixture",
      freeBytes: 10737418240,
      existingContainers: 0,
      port80Busy: false,
      port443Busy: false,
      notes: [],
      crossHostInventory: {
        ingressReady: true,
        cpuMillis: 4000,
        memoryMiB: 4096,
        unboundedInventoryHash: "b".repeat(64),
      },
    },
  };
  const enrolled = await request.post("/agent/v1/enroll", {
    data: {
      serverId: secondary.server.id,
      token: secondary.token,
      machineId: crypto.randomUUID(),
      credential,
      ...heartbeat,
    },
  });
  expect(enrolled.status(), await enrolled.text()).toBe(200);
  const refreshed = await request.post("/agent/v1/heartbeat", {
    headers: f.agentHeaders,
    data: heartbeat,
  });
  expect(refreshed.status(), await refreshed.text()).toBe(200);
  const peers = [
    { ...primary, headers: f.agentHeaders },
    {
      ...secondary.server,
      headers: {
        Authorization: `Bearer ${secondary.server.id}.${credential}`,
      },
    },
  ].map((peer) => ({ ...peer, publicKey: randomBytes(32).toString("base64") }));
  type Peer = (typeof peers)[number];
  const poll = async (peer: Peer) => {
    const response = await request.get("/agent/v1/poll", {
      headers: peer.headers,
      timeout: 10000,
    });
    expect(response.status(), await response.text()).toBe(200);
    const { command }: { command: Schema["ExecutionCommand"] } =
      await response.json();
    expect(command).toBeTruthy();
    return command;
  };
  const report = async (
    peer: Peer,
    command: Schema["ExecutionCommand"],
    result: Partial<Schema["AgentReport"]>,
  ) => {
    const response = await request.post("/agent/v1/report", {
      headers: peer.headers,
      data: {
        id: command.id,
        generation: command.generation,
        planHash: command.planHash,
        sequence: 2,
        state: "succeeded",
        steps: [],
        note: "Browser peer fixture",
        ...result,
      },
    });
    expect(response.status(), await response.text()).toBe(204);
  };
  type MembershipManifest = Record<string, unknown> & {
    generation: number;
    disabled: boolean;
    peers: {
      serverId: string;
      membershipId: string;
      address: string;
      publicKey: string;
      endpoint: string;
    }[];
    exports: unknown[];
    egress: unknown[];
  };
  const membershipHash = (m: MembershipManifest) => {
    expect(m.exports).toEqual([]);
    expect(m.egress).toEqual([]);
    // PostgreSQL reorders command JSON. These membership-only peers encode
    // the agent manifest's wire order, including the nested peer identities.
    const wire = {
      version: m.version,
      networkId: m.networkId,
      membershipId: m.membershipId,
      serverId: m.serverId,
      controllerGeneration: m.controllerGeneration,
      generation: m.generation,
      prefix: m.prefix,
      address: m.address,
      publicKey: m.publicKey,
      listenPort: m.listenPort,
      peers: m.peers.map((peer) => ({
        serverId: peer.serverId,
        membershipId: peer.membershipId,
        address: peer.address,
        publicKey: peer.publicKey,
        endpoint: peer.endpoint,
      })),
      exports: m.exports,
      egress: m.egress,
      disabled: m.disabled,
    };
    return createHash("sha256").update(JSON.stringify(wire)).digest("hex");
  };
  const acknowledgeNetwork = async () => {
    for (let round = 0; round < 4; round++) {
      const network: Schema["ManagedNetwork"] = await (
        await page.request.get(`${f.prefix}/network`)
      ).json();
      const pending = network.members.filter(
        (member) => !["ready", "removed"].includes(member.state),
      );
      if (!pending.length && network.state === "ready") return;
      await Promise.all(
        pending.map(async (member) => {
          const peer = peers.find((peer) => peer.id === member.serverId);
          if (!peer) throw new Error("unknown network peer");
          const command = await poll(peer);
          expect(command.kind).toBe("network");
          const body = command.network as {
            networkId: string;
            membershipId: string;
            manifest?: MembershipManifest;
          };
          await report(peer, command, {
            network: {
              identity: {
                membershipId: body.membershipId,
                publicKey: peer.publicKey,
              },
              networkId: body.networkId,
              generation: body.manifest?.generation ?? 0,
              hash: body.manifest ? membershipHash(body.manifest) : "",
              disabled: body.manifest?.disabled ?? false,
            },
          });
        }),
      );
    }
    throw new Error("network did not acknowledge configuration");
  };

  await page.goto(`/servers?workspace=${f.ws}`);
  const network = page.getByRole("region", { name: "Managed private network" });
  await network
    .getByRole("button", { name: "Configure private network", exact: true })
    .click();
  const form = network.getByRole("form", {
    name: "Configure private network",
    exact: true,
  });
  await form
    .getByLabel("Private IPv4 prefix", { exact: true })
    .fill("10.246.0.0/24");
  const primaryChoice = form.getByLabel(`Include ${primary.name}`, {
    exact: true,
  });
  await primaryChoice.focus();
  await expect(primaryChoice).toBeFocused();
  await primaryChoice.press("Space");
  await expect(primaryChoice).toBeChecked();
  for (const [index, peer] of peers.entries()) {
    await form.getByLabel(`Include ${peer.name}`, { exact: true }).check();
    const fields = form.getByRole("group", { name: peer.name, exact: true });
    await fields
      .getByLabel("Reachable UDP endpoint", { exact: true })
      .fill(`192.0.2.${index + 1}:51820`);
    await fields
      .getByLabel("Allocatable CPU (millicores)", { exact: true })
      .fill("1000");
    await fields
      .getByLabel("Allocatable memory (MiB)", { exact: true })
      .fill("1024");
  }
  await form.getByLabel(/I have reserved capacity/).check();
  let fail = true;
  await page.route(`**${f.prefix}/network`, async (route) => {
    if (route.request().method() === "PUT" && fail) {
      fail = false;
      await route.fulfill({
        status: 503,
        contentType: "application/json",
        body: JSON.stringify({
          code: "unavailable",
          message: "Temporary network save failure.",
        }),
      });
    } else await route.continue();
  });
  const networkSaved = (status: number) =>
    page.waitForResponse(
      (response) =>
        response.url().endsWith(`${f.prefix}/network`) &&
        response.request().method() === "PUT" &&
        response.status() === status,
    );
  let saved = networkSaved(503);
  await form
    .getByRole("button", { name: "Save private network", exact: true })
    .click();
  await saved;
  await expect(
    form
      .getByRole("group", { name: secondary.server.name })
      .getByLabel("Reachable UDP endpoint", { exact: true }),
  ).toHaveValue("192.0.2.2:51820");
  await expect(form.getByLabel(/I have reserved capacity/)).toBeChecked();
  await network.screenshot({ path: info.outputPath("network-desktop.png") });
  saved = networkSaved(200);
  await form
    .getByRole("button", { name: "Save private network", exact: true })
    .click();
  await saved;
  await expect(network).toContainText(
    "Configuration changes await host acknowledgement.",
  );
  await acknowledgeNetwork();
  await expect(network).toContainText("Configuration acknowledged.", {
    timeout: 12000,
  });

  const web = crypto.randomUUID();
  const image =
    "busybox:1.37.0@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e";
  const specification = {
    ...f.draft.specification,
    applications: [
      ...f.draft.specification.applications,
      {
        id: web,
        name: "Web",
        kind: "web",
        source: { kind: "image", image },
        health: { port: 8080, path: "/" },
      },
    ],
    routes: [
      {
        id: crypto.randomUUID(),
        targetId: web,
        protocol: "http",
        domain: `placement-${info.project.name}.example.test`,
        port: 8080,
        listenPort: 0,
        tls: "off",
        allowCidrs: [],
      },
    ],
  };
  const initial = await page.request.put(`${f.path}/draft`, {
    headers: f.headers,
    data: { baseRevisionId: f.draft.id, specification },
  });
  expect(initial.status(), await initial.text()).toBe(200);
  let draft: Schema["Revision"] = await initial.json();
  const sourcePlan = await f.post(`${f.path}/deployment-plans`, {
    baseRevisionId: draft.id,
    serverId: primary.id,
    allowHostChanges: true,
    acceptDrift: false,
  });
  await f.report(await f.poll(), {
    observedAt: new Date().toISOString(),
    resources: [],
    images: { [f.pg]: f.image, [web]: image },
    applied: false,
    stateHash: "c".repeat(64),
    infrastructureDrift: false,
  });
  await f.post(`${f.path}/deployment-plans/${sourcePlan.id}/apply`, {
    idempotencyKey: crypto.randomUUID(),
  });
  await f.report(await f.poll(), {
    observedAt: new Date().toISOString(),
    resources: [
      { id: f.pg, image: f.image, state: "healthy" },
      { id: web, image, state: "running" },
    ].map((resource) => ({
      ...resource,
      kind: "service",
      drift: false,
      note: "",
      logs: "",
      memoryBytes: 0,
    })),
    applied: true,
    stateHash: "d".repeat(64),
    infrastructureDrift: false,
  });
  const desired = structuredClone(draft.specification);
  const webApp = desired.applications.find((app) => app.id === web);
  if (!webApp) throw new Error("missing Web source");
  webApp.replicas = 2;
  const scaled = await page.request.put(`${f.path}/draft`, {
    headers: f.headers,
    data: { baseRevisionId: draft.id, specification: desired },
  });
  expect(scaled.status(), await scaled.text()).toBe(200);
  draft = await scaled.json();
  await page.goto(
    `/?workspace=${f.ws}&project=${f.project.id}&environment=${f.env.id}`,
  );
  await page
    .getByText("Application placement across servers", { exact: true })
    .click();
  const placement = page.locator("details").filter({
    has: page.getByText("Application placement across servers", {
      exact: true,
    }),
  });
  await placement
    .getByRole("button", {
      name: "Set explicit application placement",
      exact: true,
    })
    .click();
  await placement
    .getByRole("combobox", { name: "Primary ingress server", exact: true })
    .selectOption(primary.id);
  await expect(
    placement.getByLabel("Use explicit placement for Database", {
      exact: true,
    }),
  ).toBeDisabled();
  const webFields = placement.getByRole("group", { name: "Web", exact: true });
  await webFields
    .getByLabel(`${primary.name} instances`, { exact: true })
    .fill("1");
  await webFields
    .getByLabel(`${secondary.server.name} instances`, { exact: true })
    .fill("0");
  const save = page.getByRole("button", { name: "Save revision", exact: true });
  const revisionSaved = (status: number) =>
    page.waitForResponse(
      (response) =>
        response.url().endsWith(`${f.path}/draft`) &&
        response.request().method() === "PUT" &&
        response.status() === status,
    );
  let revisionResponse = revisionSaved(400);
  await save.click();
  await revisionResponse;
  await expect(
    webFields.getByLabel(`${primary.name} instances`, { exact: true }),
  ).toHaveValue("1");
  await webFields
    .getByLabel(`${secondary.server.name} instances`, { exact: true })
    .fill("1");
  revisionResponse = revisionSaved(200);
  await save.click();
  draft = await (await revisionResponse).json();
  await webFields
    .getByLabel("CPU per instance (millicores)", { exact: true })
    .fill("600");
  const competing = structuredClone(draft.specification);
  if (!competing.placement) throw new Error("missing saved placement");
  competing.placement.applications[0].cpuMillis = 550;
  const remote = await page.request.put(`${f.path}/draft`, {
    headers: f.headers,
    data: {
      baseRevisionId: draft.id,
      specification: competing,
    },
  });
  expect(remote.status(), await remote.text()).toBe(200);
  revisionResponse = revisionSaved(409);
  await save.click();
  await revisionResponse;
  await expect(
    webFields.getByLabel("CPU per instance (millicores)", { exact: true }),
  ).toHaveValue("600");
  const conflict = page.getByRole("region", { name: "Conflicting revision" });
  await expect(conflict).toBeVisible();
  await conflict
    .getByRole("button", {
      name: "I have compared and merged my edits",
      exact: true,
    })
    .click();
  revisionResponse = revisionSaved(200);
  await save.click();
  await revisionResponse;

  const runtime = page.getByRole("region", {
    name: "Deployment review and runtime",
  });
  await runtime
    .getByRole("combobox", { name: "Deployment server", exact: true })
    .selectOption(primary.id);
  await runtime.getByLabel(/Approve the host permissions listed/).check();
  await runtime
    .getByRole("button", { name: "Prepare plan", exact: true })
    .click();
  await Promise.all(
    peers.map(async (peer) => {
      const command = await poll(peer);
      expect(command.kind).toBe("crosshost");
      const body = command.crosshost as {
        parentId: string;
        groupHash: string;
        phase: string;
        unit: { plan: Schema["VisualPlan"] };
      };
      expect(body.phase).toBe("resolve");
      const p = body.unit.plan;
      const local = p.specification.applications.filter(
        (app) => (p.local?.instances[app.id]?.length ?? 0) > 0,
      );
      await report(peer, command, {
        crosshost: {
          parentId: body.parentId,
          groupHash: body.groupHash,
          phase: body.phase,
          unitId: p.unitId,
          planHash: p.hash,
          observation: {
            observedAt: new Date().toISOString(),
            resources: [],
            images: Object.fromEntries(
              local.map((app) => [app.id, app.source.image]),
            ),
            applied: false,
            stateHash: "c".repeat(64),
            infrastructureDrift: false,
          },
        },
      });
    }),
  );
  const review = runtime.getByRole("region", { name: "Placement changes" });
  await expect(review).toContainText(primary.name);
  await expect(review).toContainText(secondary.server.name);
  await expect(review).toContainText("ingress and persistent services");
  await expect(review).toContainText(image);
  await expect(
    review.getByRole("listitem").filter({ hasText: "Web: 1 → 1 instances" }),
  ).toBeVisible();
  await expect(
    review.getByRole("listitem").filter({ hasText: "Web: 0 → 1 instances" }),
  ).toBeVisible();
  await expect(review).toContainText(f.image);
  await page.emulateMedia({ reducedMotion: "reduce" });
  for (const width of [320, 390]) {
    await page.setViewportSize({ width, height: 844 });
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth),
    ).toBeLessThanOrEqual(width);
  }
  expect(
    (await new AxeBuilder({ page }).include("details").analyze()).violations,
  ).toEqual([]);
  await placement.screenshot({ path: info.outputPath("placement-mobile.png") });
  await review.screenshot({ path: info.outputPath("review-mobile.png") });

  await page.getByRole("button", { name: "Open navigation" }).click();
  await page
    .getByRole("dialog", { name: "Navigation", exact: true })
    .getByRole("link", { name: "Servers", exact: true })
    .click();
  await network
    .getByRole("button", { name: "Configure private network", exact: true })
    .click();
  await form
    .getByLabel(`Include ${secondary.server.name}`, { exact: true })
    .uncheck();
  await form.getByLabel(/I have reserved capacity/).check();
  expect(
    (
      await new AxeBuilder({ page })
        .include('section[aria-label="Managed private network"]')
        .analyze()
    ).violations,
  ).toEqual([]);
  await network.screenshot({ path: info.outputPath("network-mobile.png") });
  saved = networkSaved(200);
  await form
    .getByRole("button", { name: "Save private network", exact: true })
    .click();
  await saved;
  await expect(network).toContainText("removal awaits host acknowledgement");
  await acknowledgeNetwork();
  await expect(network).toContainText(`${secondary.server.name}: removed`, {
    timeout: 12000,
  });
});
