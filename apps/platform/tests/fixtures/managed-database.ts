import { type APIRequestContext, expect, type Page } from "@playwright/test";
import { signInAsOwner } from "./owner-sign-in";
import { api } from "./ports";

export async function managedDatabase(
  page: Page,
  request: APIRequestContext,
  name: string,
  version = 18,
  workspaceName?: string,
) {
  await page.goto("/");
  await signInAsOwner(page);
  await expect(
    page.getByRole("heading", { name: "Projects", exact: true }),
  ).toBeVisible();
  const session = await (await page.request.get("/api/v1/session")).json();
  const headers = {
    "X-CSRF-Token": session.csrfToken,
    Origin: api,
  };
  const post = async (path: string, data: unknown) => {
    const response = await page.request.post(path, { headers, data });
    expect(response.ok(), await response.text()).toBeTruthy();
    return response.json();
  };
  const ws = workspaceName
    ? (await post("/api/v1/workspaces", { name: workspaceName })).id
    : session.workspaces[0].id;
  const prefix = `/api/v1/workspaces/${ws}`;
  const project = await post(`${prefix}/projects`, {
    name: `Recovery ${name}`,
  });
  const env = await post(`${prefix}/projects/${project.id}/environments`, {
    name: "Database",
  });
  const path = `${prefix}/projects/${project.id}/environments/${env.id}`;
  const secret = await post(`${path}/secrets`, {
    name: "Database password",
    value: "browser-database-canary",
  });
  const releases: {
    image: string;
    major: number;
    current: boolean;
    mount: string;
  }[] = await (await page.request.get(`${prefix}/postgres-releases`)).json();
  const release = releases.find((r) => r.current && r.major === version);
  if (!release) throw new Error(`no current PostgreSQL ${version} release`);
  const pg = crypto.randomUUID(),
    volume = crypto.randomUUID(),
    image = release.image;
  const specification = {
    version: 1,
    variables: [],
    applications: [
      {
        id: pg,
        name: "Database",
        kind: "postgres",
        source: { kind: "image", image },
        postgres: {
          database: "app",
          username: "app",
          passwordSecretVersionId: secret.id,
        },
        mounts: [
          {
            volumeId: volume,
            target: release.mount,
            readOnly: false,
          },
        ],
      },
    ],
    volumes: [{ id: volume, name: "Data" }],
  };
  const saved = await page.request.put(`${path}/draft`, {
    headers,
    data: { baseRevisionId: env.draftRevisionId, specification },
  });
  expect(saved.status(), await saved.text()).toBe(200);
  const draft = await saved.json();
  const enrollment = await post(`${prefix}/servers`, {
    name: `Recovery ${name}`,
    role: "deployment",
  });
  const credential = Buffer.from(
      crypto.getRandomValues(new Uint8Array(32)),
    ).toString("base64url"),
    agentHeaders = {
      Authorization: `Bearer ${enrollment.server.id}.${credential}`,
    };
  expect(
    (
      await request.post("/agent/v1/enroll", {
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
            backups: true,
            schedules: true,
            upgrades: true,
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
          },
        },
      })
    ).status(),
  ).toBe(200);
  const poll = async () => {
    const res = await request.get("/agent/v1/poll", { headers: agentHeaders });
    expect(res.status()).toBe(200);
    const value = await res.json();
    expect(value.command).toBeTruthy();
    return value.command;
  };
  const report = async (
    c: { id: string; generation: number; planHash: string },
    observation?: unknown,
    upgrade?: unknown,
    state = "succeeded",
  ) => {
    const res = await request.post("/agent/v1/report", {
      headers: agentHeaders,
      data: {
        id: c.id,
        generation: c.generation,
        planHash: c.planHash,
        sequence: 1,
        state,
        note: "Fixture result",
        steps: [],
        ...(observation ? { observation } : {}),
        ...(upgrade ? { upgrade } : {}),
      },
    });
    expect(res.status(), await res.text()).toBe(204);
  };
  const plan = await post(`${path}/deployment-plans`, {
    baseRevisionId: draft.id,
    serverId: enrollment.server.id,
    acceptDrift: false,
  });
  await report(await poll(), {
    observedAt: new Date().toISOString(),
    resources: [],
    images: { [pg]: image },
    applied: false,
    stateHash: "a".repeat(64),
    infrastructureDrift: false,
  });
  await post(`${path}/deployment-plans/${plan.id}/apply`, {
    idempotencyKey: crypto.randomUUID(),
    approveHostChanges: true,
  });
  await report(await poll(), {
    observedAt: new Date().toISOString(),
    resources: [
      {
        id: pg,
        kind: "service",
        state: "healthy",
        image,
        drift: false,
        note: "",
        logs: "",
        memoryBytes: 0,
      },
    ],
    applied: true,
    stateHash: "b".repeat(64),
    infrastructureDrift: false,
  });

  return {
    secret,
    ws,
    headers,
    post,
    prefix,
    project,
    env,
    path,
    pg,
    image,
    releases,
    draft,
    agentHeaders,
    poll,
    report,
  };
}
