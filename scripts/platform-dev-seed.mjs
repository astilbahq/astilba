import { setTimeout as delay } from "node:timers/promises";
import { backupRoot } from "./platform-dev-host.mjs";

// Brings the local stack's workspace to a known starting point through the real API, as
// its owner would from the dashboard. Every step looks for what an earlier run created
// first and leaves anything the developer changed alone; only something missing by name
// is created again. It stops short of deploying, so the developer can plan, apply and
// observe the first deployment themselves.
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
  if (session.mfaPending)
    throw new Error(
      "The local owner now signs in with two-factor authentication, so the seed cannot sign in",
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
      root: backupRoot,
      endpoint: "",
      bucket: "",
      region: "",
      access: { accessKey: "", secretKey: "", password: "" },
    });

  // Only a policy nobody has saved yet, so turning sessions off stays off.
  const policy = await http.get(`${base}/troubleshooting`);
  if (policy.available && !policy.enabled && policy.generation === 0)
    await http.put(`${base}/troubleshooting`, {
      enabled: true,
      ownersOnly: false,
      generation: policy.generation,
    });

  // The agent's state directory belongs to its server, so losing it means starting over.
  async function present(serverId) {
    const server = (await http.get(`${ws}/servers`)).find(
      (item) => item.id === serverId,
    );
    if (!server || server.status === "revoked")
      throw new Error(
        `The ${names.server} server was revoked or deleted; start over with --reset`,
      );
    return server;
  }

  return {
    workspace: workspace.id,
    environment: environment.id,
    // A server is created only when the launcher has none, because a code is valid for
    // ten minutes and each one is a credential. An enrollment that was interrupted renews
    // the same server's code: the agent's state directory stays bound to that server.
    async enrollment(serverId) {
      if (serverId) {
        await present(serverId);
        return http.post(`${ws}/servers/${serverId}/enrollment`);
      }
      return http.post(`${ws}/servers`, {
        name: names.server,
        role: "deployment",
      });
    },
    async awaitServer(serverId) {
      for (let i = 0; i < 90; i++) {
        const server = await present(serverId);
        if (server.status === "online" && server.capabilities?.compose)
          return server;
        await delay(1000, undefined, { signal });
      }
      throw new Error(
        "The local agent did not come online; see `colima ssh` and journalctl -u astilba-agent",
      );
    },
  };
}
