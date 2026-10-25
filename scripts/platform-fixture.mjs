import { createServer, request as httpRequest } from "node:http";
import { createServer as createHttpsServer } from "node:https";
import { execFileSync, spawn } from "node:child_process";
import { randomBytes, randomUUID, generateKeyPairSync, createHash, sign } from "node:crypto";
import { mkdtemp, rm, writeFile, mkdir, readFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { spawnPnpm } from "./platform-tools.mjs";

const root = resolve(import.meta.dirname, "..");
const host = process.env.PLAT_APP_DOCKER_HOST;
if (!host?.startsWith("unix:///"))
  throw new Error("PLAT_APP_DOCKER_HOST must select a disposable local daemon");
const owned = randomUUID();
const name = `astilba-p1-browser-${owned}`;
const password = randomBytes(32).toString("hex");
const directory = await mkdtemp(join(tmpdir(), "astilba-browser-"));
const children = [];
const docker = (args) =>
  execFileSync("docker", ["--host", host, ...args], {
    encoding: "utf8",
    stdio: ["ignore", "pipe", "pipe"],
  });
let created = false;
let stopping = false;
let hostedProxy;
const hostedInfo = join(root, ".artifacts/p8/browser/hosted-fixture.json");
// External provider boundary; all product requests still use the real API and PostgreSQL.
const gitProvider=createServer((request,response)=>{
 response.setHeader("Content-Type","application/json");
 if(request.headers.authorization!=="token browser-api-fixture"){response.writeHead(401).end();return}
 if(request.url.includes("/branches/")){response.end(JSON.stringify({commit:{sha:"a".repeat(40)}}));return}
 response.end(JSON.stringify({id:12,private:true}));
});
await new Promise((resolve,reject)=>{gitProvider.once("error",reject);gitProvider.listen(54185,"127.0.0.1",resolve)});

async function cleanup() {
  if (stopping) return;
  stopping = true;
 gitProvider.close();
 hostedProxy?.close();
 await rm(hostedInfo,{force:true});
  for (const child of children) child.kill("SIGTERM");
  await Promise.all(
    children.map(
      (child) =>
        new Promise((resolve) => {
          if (child.exitCode !== null || child.signalCode !== null) resolve();
          else {
            child.once("exit", resolve);
            setTimeout(() => {
              child.kill("SIGKILL");
              resolve();
            }, 10000).unref();
          }
        }),
    ),
  );
  if (created) {
    const label = docker([
      "inspect",
      "--format",
      '{{ index .Config.Labels "com.astilba.p1-test" }}',
      name,
    ]).trim();
    if (label === owned) docker(["rm", "--force", "--volumes", name]);
  }
  await rm(directory, { recursive: true, force: true });
}
for (const sig of ["SIGINT", "SIGTERM"])
  process.on(sig, () => void cleanup().then(() => process.exit(0)));
try {
  const envfile = join(directory, "postgres.env");
  await writeFile(envfile, `POSTGRES_PASSWORD=${password}\n`, { mode: 0o600 });
  docker([
    "run",
    "-d",
    "--name",
    name,
    "--label",
    `com.astilba.p1-test=${owned}`,
    "-p",
    "127.0.0.1::5432",
    "--env-file",
    envfile,
    "postgres:18.4-alpine3.23@sha256:996d0920e4ff9df1fc19dacb904492f3c1ec0ec1cc338f0ad7123be7731c5f5e",
  ]);
  created = true;
  let ready = false;
  for (let i = 0; i < 100; i++) {
    try {
      docker(["exec", name, "pg_isready", "-h", "127.0.0.1", "-U", "postgres"]);
      ready = true;
      break;
    } catch {
      await new Promise((r) => setTimeout(r, 100));
    }
  }
  if (!ready) throw new Error("PostgreSQL did not become ready");
  const address = docker(["port", name, "5432/tcp"]).trim();
  const binary = join(root, ".artifacts/p1/browser/astilba");
  execFileSync(
    process.execPath,
    [join(root, "scripts/platform-build.mjs"), binary],
    { cwd: root, stdio: "inherit" },
  );
  const releases = join(directory, "releases");
  await mkdir(releases, { mode: 0o700 });
  const keys = generateKeyPairSync("ed25519");
  const releaseKey = join(directory, "release-public.pem");
  await writeFile(releaseKey, keys.publicKey.export({ type: "spki", format: "pem" }), { mode: 0o600 });
  const artifact = Buffer.from("Astilba browser download fixture\n");
  const release = { version: "fixture.download", protocol: 1, arch: "arm64", size: artifact.length, sha256: createHash("sha256").update(artifact).digest("hex") };
  const message = `astilba-agent-v1\n${release.version}\n${release.protocol}\n${release.arch}\n${release.size}\n${release.sha256}\n`;
  release.signature = sign(null, Buffer.from(message), keys.privateKey).toString("base64");
  await writeFile(join(releases, release.sha256), artifact, { mode: 0o600 });
  await writeFile(join(releases, "fixture.json"), JSON.stringify(release), { mode: 0o600 });
  const env = {
    ...process.env,
    ASTILBA_AGENT_RELEASES: releases,
    ASTILBA_AGENT_RELEASE_KEY: releaseKey,
    ASTILBA_DATABASE_URL: `postgres://postgres:${password}@${address}/postgres?sslmode=disable`,
    ASTILBA_KEYRING: join(directory, "keyring.json"),
    ASTILBA_DEVELOPMENT: "true",
    ASTILBA_KEYRING_JSON: "",
    ASTILBA_HOSTED: "false",
  };
  execFileSync(binary, ["keygen", "--out", env.ASTILBA_KEYRING], {
    env,
    stdio: "inherit",
  });
  execFileSync(binary, ["migrate"], { env, stdio: "inherit" });
  execFileSync(
    binary,
    [
      "bootstrap",
      "--email",
      "owner@example.test",
      "--name",
      "Owner",
      "--workspace",
      "Personal",
    ],
    {
      env,
      input: "correct horse battery staple",
      stdio: ["pipe", "inherit", "inherit"],
    },
  );
  const start = (cmd, args, environment, cwd = root) => {
    const options = {
      cwd,
      env: environment,
      stdio: ["ignore", "inherit", "inherit"],
    };
    const child =
      cmd === "pnpm" ? spawnPnpm(args, options) : spawn(cmd, args, options);
    children.push(child);
    child.once("exit", (code) => {
      if (!stopping) void cleanup().then(() => process.exit(code || 1));
    });
  };
  start(binary, ["serve"], {
    ...env,
    ASTILBA_LISTEN: "127.0.0.1:54181",
    ASTILBA_ORIGIN: "http://127.0.0.1:54181",
  });
  start(binary, ["serve"], {
    ...env,
    ASTILBA_LISTEN: "127.0.0.1:54184",
    ASTILBA_ORIGIN: "http://127.0.0.1:54183",
  });
  // Vite exercises dev-only localisation against the same real API/database,
  // with its own permitted browser origin rather than bypassing CSRF checks.
  start(binary, ["serve"], {
    ...env,
    ASTILBA_LISTEN: "127.0.0.1:54188",
    ASTILBA_ORIGIN: "http://127.0.0.1:54182",
  });
  const hostedDatabase = `hosted_${owned.replaceAll("-", "")}`;
  docker(["exec", name, "createdb", "--username", "postgres", hostedDatabase]);
  const hostedEnv = {
    ...env,
    ASTILBA_DATABASE_URL: `postgres://postgres:${password}@${address}/${hostedDatabase}?sslmode=disable`,
    ASTILBA_KEYRING: join(directory, "hosted-keyring.json"),
    ASTILBA_DEVELOPMENT: "false", ASTILBA_HOSTED: "true",
    ASTILBA_HOSTED_COHORT_LIMIT: "5", ASTILBA_HOSTED_SERVER_LIMIT: "2",
    ASTILBA_HOSTED_SUPPORT: "Fixture: asynchronous support weekdays.",
    ASTILBA_LISTEN: "127.0.0.1:54187", ASTILBA_ORIGIN: "https://127.0.0.1:54186",
  };
  execFileSync(binary,["keygen","--out",hostedEnv.ASTILBA_KEYRING],{env:hostedEnv,stdio:"inherit"});
  execFileSync(binary,["migrate"],{env:hostedEnv,stdio:"inherit"});
  for (const project of ["chromium","webkit"]) {
    for (const purpose of ["signup"]) {
      const token=`browser-${project}-${purpose}-invitation-code`;
      const digest=createHash("sha256").update(token).digest("hex");
      docker(["exec", name,"psql","--username","postgres","--dbname",hostedDatabase,"-v","ON_ERROR_STOP=1","-c",`INSERT INTO hosted_invitations(id,email,token_hash,expires_at) VALUES('${randomUUID()}','${project}@example.test',decode('${digest}','hex'),now()+interval '1 day')`]);
    }
  }
  const certificate=join(directory,"loopback.crt"), certificateKey=join(directory,"loopback.key");
  execFileSync("openssl",["req","-x509","-newkey","rsa:2048","-nodes","-keyout",certificateKey,"-out",certificate,"-days","1","-subj","/CN=127.0.0.1","-addext","subjectAltName=IP:127.0.0.1"],{stdio:"ignore"});
  hostedProxy=createHttpsServer({key:await readFile(certificateKey),cert:await readFile(certificate)},(incoming,outgoing)=>{
    const upstream=httpRequest({host:"127.0.0.1",port:54187,path:incoming.url,method:incoming.method,headers:incoming.headers},response=>{outgoing.writeHead(response.statusCode,response.headers);response.pipe(outgoing)});
    upstream.on("error",()=>outgoing.writeHead(502).end());incoming.pipe(upstream);
  });
  await new Promise((resolve,reject)=>{hostedProxy.once("error",reject);hostedProxy.listen(54186,"127.0.0.1",resolve)});
  start(binary,["serve"],hostedEnv);
  await mkdir(join(root,".artifacts/p8/browser"),{recursive:true});
  const operatorEnv=Object.fromEntries(["ASTILBA_DATABASE_URL","ASTILBA_KEYRING","ASTILBA_HOSTED","ASTILBA_HOSTED_COHORT_LIMIT","ASTILBA_HOSTED_SERVER_LIMIT","ASTILBA_HOSTED_SUPPORT"].map(key=>[key,hostedEnv[key]]));
  await writeFile(hostedInfo,JSON.stringify({binary,env:operatorEnv,host,container:name,owner:owned,database:hostedDatabase}),{mode:0o600});
  for (const port of [54181, 54184, 54187, 54188]) {
    let ready = false;
    for (let i = 0; i < 100; i++) {
      try {
        const response = await fetch(`http://127.0.0.1:${port}/healthz`);
        if (response.status === 204) {
          ready = true;
          break;
        }
      } catch {}
      await new Promise((r) => setTimeout(r, 100));
    }
    if (!ready) throw new Error("API fixture did not become ready");
  }
  start(
    "pnpm",
    [
      "exec",
      "wrangler",
      "dev",
      "--local",
      "--ip",
      "127.0.0.1",
      "--port",
      "54183",
      "--var",
      "API_ORIGIN:http://127.0.0.1:54184",
      "--var",
      "DEVELOPMENT:true",
    ],
    { ...process.env, WRANGLER_SEND_METRICS: "false" },
    join(root, "apps/platform"),
  );
  await new Promise(() => {});
} catch (error) {
  await cleanup();
  console.error(error instanceof Error ? error.message : "Fixture failed");
  process.exit(1);
}
