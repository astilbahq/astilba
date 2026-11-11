import { setTimeout as delay } from "node:timers/promises";

// Brings the local stack's workspace to a known starting point through the real API, as
// its owner would from the dashboard. Every step looks for what an earlier run created
// first, so running it again changes nothing. It stops short of deploying, so the
// developer can plan, apply and observe the first deployment themselves.
export const names = {
  project: "Local app",
  environment: "production",
  server: "Local VM",
  destination: "Local VM disk",
  secret: "GREETING",
};
export const routeDomain = "web.localhost";
const image =
  "busybox:1.37.0@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e";
// A web page from a secret, kept on a volume so that a file backup has something to copy.
const compose = `services:
  web:
    image: ${image}
    restart: unless-stopped
    command: ["sh", "-c", "printf '%s\\\\n' \\"$$GREETING\\" > /www/index.html && exec httpd -f -p 8080 -h /www"]
    environment:
      GREETING: \${GREETING}
    volumes:
      - site:/www
volumes:
  site: {}
x-astilba:
  routes:
    - name: web
      service: web
      protocol: http
      domain: ${routeDomain}
      port: 8080
      tls: 'off'
`;

function client({ api, origin, signal }) {
  let cookie = "";
  let csrf = "";
  async function call(method, path, body, expected) {
    const init = {
      method,
      signal: AbortSignal.any([signal, AbortSignal.timeout(15000)]),
      headers: {
        Origin: origin,
        ...(cookie ? { Cookie: cookie } : {}),
        ...(method === "GET" ? {} : { "X-CSRF-Token": csrf }),
      },
    };
    if (body !== undefined) {
      init.headers["Content-Type"] = "application/json";
      init.body = JSON.stringify(body);
    }
    const response = await fetch(`${api}${path}`, init);
    for (const header of response.headers.getSetCookie()) {
      const pair = header.split(";")[0];
      if (/^(__Host-)?astilba_session=/.test(pair)) cookie = pair;
    }
    const text = await response.text();
    if (!expected.includes(response.status))
      throw new Error(
        `${method} ${path} answered ${response.status}: ${text.slice(0, 300)}`,
      );
    const value = text ? JSON.parse(text) : undefined;
    if (value?.csrfToken) csrf = value.csrfToken;
    return value;
  }
  return {
    get: (path) => call("GET", path, undefined, [200]),
    post: (path, body, expected = [201]) => call("POST", path, body, expected),
    put: (path, body) => call("PUT", path, body, [200]),
  };
}

export async function seed({ api, origin, email, password, signal }) {
  const http = client({ api, origin, signal });
  await http.get("/api/v1/session");
  const session = await http.post(
    "/api/v1/auth/login",
    { email, password },
    [200],
  );
  const workspace = session.workspaces?.[0];
  if (!workspace) throw new Error("The local owner has no workspace");
  const ws = `/api/v1/workspaces/${workspace.id}`;
  const find = (list, name) => list.find((item) => item.name === name);

  const project =
    find(await http.get(`${ws}/projects`), names.project) ??
    (await http.post(`${ws}/projects`, { name: names.project }));
  const environments = `${ws}/projects/${project.id}/environments`;
  const environment =
    find(await http.get(environments), names.environment) ??
    (await http.post(environments, {
      name: names.environment,
      configurationOwner: "compose",
    }));
  const base = `${environments}/${environment.id}`;

  const secret =
    find(await http.get(`${base}/secrets`), names.secret) ??
    (await http.post(`${base}/secrets`, {
      name: names.secret,
      value: "Hello from the local Astilba stack",
    }));

  // Only a draft still holding the initial empty bundle is filled in, so a developer's
  // own edits survive a restart.
  const draft = await http.get(`${base}/draft`);
  const files = draft.specification?.compose?.files ?? [];
  if (files.length === 1 && files[0].content === "services: {}\n")
    await http.put(`${base}/draft`, {
      baseRevisionId: draft.id,
      // Only the fields an owner writes: the stored draft also carries derived ones.
      specification: {
        version: draft.specification.version,
        applications: [],
        variables: [{ name: names.secret, secretVersionId: secret.id }],
        compose: {
          namespace: draft.specification.compose.namespace,
          entrypoints: ["compose.yaml"],
          files: [{ path: "compose.yaml", content: compose }],
        },
      },
    });

  const destinations = await http.get(`${ws}/backup-destinations`);
  if (!find(destinations, names.destination))
    await http.post(`${ws}/backup-destinations`, {
      name: names.destination,
      kind: "filesystem",
      root: "/var/lib/astilba-dev-backups",
      endpoint: "",
      bucket: "",
      region: "",
      access: { accessKey: "", secretKey: "", password: "" },
    });

  const policy = await http.get(`${base}/troubleshooting`);
  if (policy.available && !policy.enabled)
    await http.put(`${base}/troubleshooting`, {
      enabled: true,
      ownersOnly: false,
      generation: policy.generation,
    });

  return {
    workspace: workspace.id,
    environment: environment.id,
    // The server is created only when the launcher has none enrolled, because a code is
    // valid for ten minutes and each one is a credential.
    async enrollment(serverId) {
      if (serverId) {
        const servers = await http.get(`${ws}/servers`);
        if (
          servers.some(
            (server) => server.id === serverId && server.status !== "revoked",
          )
        )
          return http.post(`${ws}/servers/${serverId}/enrollment`);
      }
      return http.post(`${ws}/servers`, {
        name: names.server,
        role: "deployment",
      });
    },
    async awaitServer(serverId) {
      for (let i = 0; i < 90; i++) {
        const server = (await http.get(`${ws}/servers`)).find(
          (s) => s.id === serverId,
        );
        if (server?.status === "online" && server.capabilities?.compose)
          return server;
        await delay(1000, undefined, { signal });
      }
      throw new Error(
        "The local agent did not come online; see `colima ssh` and journalctl -u astilba-agent",
      );
    },
  };
}
