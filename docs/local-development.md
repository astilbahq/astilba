# Local development stack

`make dev` runs the real product on a developer's Mac: management and its PostgreSQL built from the working tree, the dashboard with hot reload, and an enrolled agent deploying Compose environments into a real Docker daemon in a disposable Colima VM. Use it for day-to-day work on the dashboard and the services. The hosted dogfood VMs are upgraded only at milestones, for what this stack cannot reproduce (see [the end of this page](#what-stays-with-the-vm-dogfood)).

For dashboard work that needs no agent, `pnpm dev` without `--vm` still runs management and the dashboard against `PLAT_APP_DOCKER_HOST`; see [frontend development](frontend-development.md#local-commands). The [UI workshop](frontend-development.md#ui-workshop) needs neither.

## Start

You need [Colima](https://github.com/abiosoft/colima) 0.10 or later, OpenSSL and OpenSSH, Go 1.27.4, and Node 24.18.1 with the pinned pnpm. From the repository root:

```sh
pnpm install --frozen-lockfile
make dev
```

The first run creates the Colima profile `astilba-dev` and takes a few minutes; later runs start it in about half a minute. Name another profile with `make dev PLAT_DEV_COLIMA_PROFILE=<profile>`, or run `pnpm dev --vm <profile> [--port 54182]` directly. The launcher creates the profile itself and claims it with a marker inside the VM. It refuses Colima's `default` profile, names starting with `colima-` (Colima reads `colima-foo` as profile `foo`) and any existing profile it did not create, and it never selects a Docker context: every command names the profile or its socket. Colima itself adds a `colima-<profile>` context without selecting it.

When it prints **Ready**, open `http://127.0.0.1:54182/login` and sign in as `owner@example.test` / `correct horse battery staple`. That account, the PostgreSQL password, the keyring, the agent's release key and the management certificate exist only in this checkout's private `.artifacts/dev-stack`, which nothing serves. The password is a published local-only test value; management listens on loopback in development mode only.

| Port (default) | What answers                                                                   |
| -------------- | ------------------------------------------------------------------------------ |
| 54182          | The dashboard, through Vite, which proxies `/api` to management                |
| 54181          | Management's API                                                               |
| 54180          | Routes deployed in the VM, by host name, such as `http://web.localhost:54180/` |

`--port N` moves all three to N, N-1 and N-2. Every one binds 127.0.0.1 only.

## What the seed sets up

Each start signs in through the API and makes sure the **Local stack** workspace holds:

- the **Local app** project, with a Compose environment called **production**. Its draft runs a small web server from a pinned BusyBox image that serves the `GREETING` secret from a named volume, with an HTTP route for `web.localhost`;
- the `GREETING` environment secret;
- the **Local VM disk** filesystem backup destination, at the agent's only allowed backup root;
- terminal sessions enabled for that environment;
- the **Local VM** deployment server, enrolled and online.

The seed fills in the draft only while it still holds the initial empty bundle, turns terminal sessions on only before anyone has saved that setting, and finds everything else by name: a restart keeps your changes, though it creates again anything above you deleted or renamed. It does not deploy: the first plan is yours. Once the agent is enrolled, a seed that cannot sign in, because you changed the owner's password or turned on two-factor sign-in, is skipped with a warning and the stack still starts.

## Try the product

- **Plan, apply and observe.** Open the environment, prepare a plan on **Local VM** with host changes allowed (the route publishes port 80 in the VM), apply it and follow the operation. Then open `http://web.localhost:54180/`. Chrome and Firefox resolve `*.localhost` to loopback by themselves; elsewhere use `curl --resolve web.localhost:54180:127.0.0.1 http://web.localhost:54180/`.
- **Secrets.** Change `GREETING`, apply again and reload the page.
- **Backups.** Once the environment is deployed, add a backup policy for the `site` volume with the **Local VM disk** destination, install it and run a backup. Restic 0.19.1 runs in the VM and writes under `/var/lib/astilba-dev-backups`.
- **Terminal sessions.** Management runs with `ASTILBA_TROUBLESHOOTING=single-instance`, and the agent has the host owner's consent. Sessions open on a deployed environment. The dashboard has the session controls and history; it does not have a terminal yet, but the API and both WebSocket streams work, including through Vite's proxy.
- **Agent updates.** Each agent build is signed with the stack's own release key into the catalog management serves, so the Servers page and the enrollment panel show real releases.
- **Email** is printed in the `make dev` terminal instead of being sent.

## Changing code

Dashboard changes reload in the browser. Go changes under `services/platform` rebuild management and the agent; the launcher restarts only the binaries that changed. A new agent build is copied into the VM and restarted, and management restarts with it to load the new signed release. Migrations run before management starts. A build that fails keeps the previous processes running until the next change. `--no-watch` turns this off.

Restarting `make dev` after a change likewise rebuilds only what changed, through Go's build cache. The VM, the database and the enrolled agent are kept.

The agent's journal moves forward with the code. Running an older checkout against a journal a newer agent has migrated may be refused, as it would be in production; start over in that case.

## Stop and start over

Ctrl-C stops the dashboard, management, the tunnel and the database, then stops the VM. Data, the enrolled agent and deployed workloads stay. When the VM starts again, services come back as they would after a host reboot: those with a Compose `restart` policy, like the seeded app's `unless-stopped`, start with Docker, and the others stay stopped until the next deployment.

After the VM restarts, the agent currently reports ingress drift and refuses the next plan until you prepare it as a reconciliation (accept drift). Docker reports the restarted proxy's empty DNS settings differently, which changes the fingerprint the agent compares. This has been seen only in this VM so far; whether a rebooted production host does the same is still to be checked.

`make dev-wipe` (`pnpm dev --vm <profile> --wipe`) deletes the VM with its Docker data, after checking its marker, and this checkout's `.artifacts/dev-stack`. It also deletes a VM whose first start was interrupted before it was marked, and leaves alone any profile this stack never created. If you interrupt the first start, the next `make dev` deletes that half-made VM and creates it again. `pnpm dev --vm <profile> --reset` does the same and then starts afresh.

A launcher holds `.artifacts/dev.lock` while it runs, shared with `pnpm dev`, because both use the same ports. If a forced kill leaves it behind, follow the [recovery steps](frontend-development.md#local-commands): stop the recorded process groups, including the `ssh` tunnel, confirm the listeners are gone, then remove only the lock. `colima stop --profile <profile>` stops a VM left running.

## How it fits together

```text
Mac (127.0.0.1 only)                         Colima VM (profile astilba-dev)
  Vite :54182 ── /api ──► management :54181    PostgreSQL 127.0.0.1:5432
  management ── database ── ssh -L ──────────►   (container on the VM's Docker)
  browser ── :54180 ─────── ssh -L ──────────► the agent's ingress proxy :80
  TLS proxy ◄────────────── ssh -R ◄────────── agent ── https://localhost:9443
```

- **The VM** has no Mac folders mounted and no Colima port forwarding: Colima would otherwise forward the VM's published ports to every Mac interface. One SSH connection, using Lima's own configuration for the VM, carries everything above.
- **The agent** is installed with the product's `install.sh` and systemd unit, plus a drop-in that sets the restic path and backup root. It enrolls through stdin like a real host. The VM gets the exact Compose 5.6.1 package that [guided onboarding](server-onboarding.md) pins, and restic from `services/platform/host/backup-tools.sh`.
- **Management's HTTPS for the agent.** The agent accepts only an HTTPS management origin, and development management serves loopback HTTP. A small proxy in the launcher terminates TLS with a certificate generated for `localhost` and forwards only the agent's paths (`/agent/v1/`, `/agent-artifacts/`, `/agent-public-key.pem`). The certificate goes into the VM's trust store only. The Mac's trust store, DNS and hosts file are never touched.
- **The VM stays off the Mac.** Lima's gateway address forwards to every port the Mac has open on loopback. An nftables table in the VM rejects traffic to that address from deployed containers and from the VM itself, so neither a workload nor a privileged one that escapes to the VM can reach your own local services. Only DNS to Lima's resolver there stays open. A systemd unit loads the table at boot, before Docker starts, so workloads that restart with the VM never run without it. Containers keep internet access.

## Ingress and TLS

Routes use `tls: 'off'` and answer over HTTP on port 54180 under any `*.localhost` name. Use a `*.localhost` name, not `127.0.0.1`: browsers share cookies across ports on one host, so a route on `127.0.0.1` would receive the dashboard's session cookie. Managed TLS needs a public ACME HTTP-01 challenge and a publicly trusted certificate, which the agent checks before it reports a route ready, so it cannot work here. Nothing in the stack asks you to trust a local CA. If you want HTTPS in the browser for your own testing, put your own TLS proxy in front of port 54180; that is your opt-in and outside the stack.

## What stays with the VM dogfood

This stack runs one agent on one Colima VM. Check these on the dogfood VMs at milestones instead:

- **Public ACME and managed TLS:** certificate issuance, renewal and the certificate contact.
- **Real DNS:** generated application names under `ASTILBA_APPLICATION_DOMAIN`, Cloudflare reconciliation and custom domains.
- **Provider networks:** cloud firewalls, metadata services, public addresses, and cross-host private networking between servers.
- **Reboots and unattended upgrades** of a real Ubuntu host, and recovery after them.
- **The supported host baseline:** the VM keeps Colima's Docker Engine, not the pinned 29.9.2, and the guided `bootstrap.sh` path is not exercised.
- **Several servers and preview or build runners**, since there is one deployment server.
- **S3 backup destinations**, which need an HTTPS endpoint the agent trusts.
- **Hosted mode:** billing, signup admission and real mail delivery.
- **Management behind a production reverse proxy**, and more than one management instance.
