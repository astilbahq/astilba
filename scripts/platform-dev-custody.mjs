import { existsSync, readFileSync, renameSync, writeFileSync } from "node:fs";
import { join } from "node:path";

// The local stack's key service. `file` keeps the keyring alone, as a default self-hosted
// installation does. `openbao` also runs OpenBao in the stack's VM and keeps every workspace key
// under its Transit keys, as docs/key-custody-transit.md sets up, so its audit log shows each use.
export const custodyModes = ["file", "openbao"];

// The same release the Transit backend's integration tests run against.
export const openBaoImage =
  "openbao/openbao:2.7.2@sha256:5046911075e137e154ce49ab051502f4ec7cf367d484ae4a4dce665d9b90561f";

// A real server, not `bao server -dev`: dev mode keeps everything in memory, and the stack keeps
// its data across restarts. Storage is one Raft node in the container's own filesystem, which
// stays with the container across stops; OpenBao 2 has no other persistent storage that needs no
// database. Plain HTTP on the container's port, which the VM publishes only on its loopback and the
// stack's tunnel carries to the Mac's. The audit device writes to standard output, so `docker logs`
// shows it; OpenBao creates audit devices only from configuration. The cluster name is what
// `custody init-transit --dev` looks for before it changes anything.
export const openBaoConfig = `cluster_name = "astilba-dev"
storage "raft" {
  path    = "/openbao/file"
  node_id = "astilba-dev"
}
listener "tcp" {
  address     = "0.0.0.0:8200"
  tls_disable = true
}
api_addr     = "http://127.0.0.1:8200"
cluster_addr = "http://127.0.0.1:8201"
audit "file" "stdout" {
  options = {
    file_path = "stdout"
  }
}
`;

// checkCustody refuses a mode the launcher cannot run, and a change of mode on a stack that has
// already registered its key backend: the database then refuses any other.
export function checkCustody({ custody, stack, saved }) {
  if (!custodyModes.includes(custody))
    throw new Error(`--custody must be ${custodyModes.join(" or ")}`);
  if (custody !== "file" && !stack)
    throw new Error(
      `--custody=${custody} runs its key service in the stack's VM; add --vm PROFILE`,
    );
  const before = saved?.custody ?? "file";
  if (saved?.initialized && before !== custody)
    throw new Error(
      `This checkout's stack keeps its keys with --custody=${before}; pass that, or start over with --reset`,
    );
}

// openBaoRun is the `docker run` arguments for the stack's OpenBao.
export function openBaoRun({ name, label, owner, port }) {
  return [
    "run",
    "-d",
    "--name",
    name,
    "--label",
    `${label}=${owner}`,
    "-p",
    `127.0.0.1:${port}:8200`,
    "--env",
    `ASTILBA_OPENBAO_CONFIG=${openBaoConfig}`,
    "--entrypoint",
    "sh",
    openBaoImage,
    "-c",
    'printf "%s" "$ASTILBA_OPENBAO_CONFIG" > /tmp/astilba.hcl && exec bao server -config=/tmp/astilba.hcl',
  ];
}

// openBao initialises the stack's server the first time, keeping its one unseal key and root token
// in a private file in the stack's directory, and unseals it on every start. Both are made by the
// server at runtime and never leave the developer's machine.
export function openBao({ directory, fetch = globalThis.fetch }) {
  const secrets = join(directory, "openbao.json");
  const tokenFile = join(directory, "openbao-root-token");
  async function call(address, method, path, body) {
    const response = await fetch(`${address}/v1/${path}`, {
      method,
      headers: { "content-type": "application/json" },
      body: body === undefined ? undefined : JSON.stringify(body),
      signal: AbortSignal.timeout(10000),
    });
    const text = await response.text();
    if (!response.ok)
      throw new Error(
        `OpenBao ${method} ${path} answered ${response.status}: ${text.slice(0, 200)}`,
      );
    return text ? JSON.parse(text) : {};
  }
  return {
    tokenFile,
    credentials: join(directory, "openbao"),
    // ready reports whether the server answers, sealed or not.
    async ready(address) {
      try {
        await call(address, "GET", "sys/seal-status");
        return true;
      } catch {
        return false;
      }
    },
    // active reports whether the server is unsealed and its Raft node leads, so it serves
    // requests: sys/health answers 200 only then.
    async active(address) {
      try {
        await call(address, "GET", "sys/health");
        return true;
      } catch {
        return false;
      }
    },
    // start readies the stack's OpenBao at address once the tunnel carries it: unsealed, its
    // Raft node leading, and set up by `custody init-transit --dev`, which applies the shipped
    // policies again, so a change to them takes effect on restart. It returns what management
    // needs to sign in as serving, and the variables an operator's command needs.
    async start(address, { waitFor, initTransit }) {
      await waitFor(() => this.ready(address), "OpenBao");
      await this.unseal(address);
      await waitFor(() => this.active(address), "OpenBao's storage");
      const credentials = this.credentials;
      await initTransit([
        "--dev",
        "--address",
        address,
        "--token-file",
        tokenFile,
        "--out",
        credentials,
      ]);
      // The server answers in plain HTTP on loopback, which management allows only in
      // development.
      const operator = {
        ASTILBA_DEVELOPMENT: "true",
        ...transitEnv({ address, credentials, role: "operator" }),
      };
      return {
        env: transitEnv({ address, credentials }),
        operator: Object.entries(operator)
          .map(([k, v]) => `${k}=${v}`)
          .join(" "),
      };
    },
    async unseal(address) {
      const status = await call(address, "GET", "sys/seal-status");
      if (!status.initialized) {
        if (existsSync(secrets))
          throw new Error(
            "The stack's OpenBao lost its storage, and with it every workspace key; start over with --reset",
          );
        const made = await call(address, "PUT", "sys/init", {
          secret_shares: 1,
          secret_threshold: 1,
        });
        writeFileSync(tokenFile, `${made.root_token}\n`, { mode: 0o600 });
        writeFileSync(
          `${secrets}.tmp`,
          JSON.stringify({ unseal: made.keys_base64[0] }),
          { mode: 0o600 },
        );
        renameSync(`${secrets}.tmp`, secrets);
      }
      if (!existsSync(secrets) || !existsSync(tokenFile))
        throw new Error(
          "The stack's OpenBao was initialised without this checkout's unseal key; start over with --reset",
        );
      const { unseal } = JSON.parse(readFileSync(secrets, "utf8"));
      if (status.sealed || !status.initialized) {
        const after = await call(address, "PUT", "sys/unseal", {
          key: unseal,
        });
        if (after.sealed) throw new Error("The stack's OpenBao stayed sealed");
      }
    },
  };
}

// transitEnv is what management needs to use the stack's OpenBao as the role named.
export function transitEnv({ address, credentials, role = "serving" }) {
  return {
    ASTILBA_KEY_BACKEND: "transit",
    ASTILBA_TRANSIT_ADDR: address,
    ASTILBA_TRANSIT_AUTH: "approle",
    ASTILBA_TRANSIT_ROLE_ID_FILE: join(credentials, `${role}-role-id`),
    ASTILBA_TRANSIT_SECRET_ID_FILE: join(credentials, `${role}-secret-id`),
  };
}
