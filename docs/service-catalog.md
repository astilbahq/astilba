# Service connections and catalog

Visual environments can bind a named service output to another service's environment variable. The connection stays within the deployment unit's private Docker network. It does not publish a port, grant network access or impose startup order. Use **Readiness dependencies** separately when a consumer must wait for a PostgreSQL or HTTP readiness check.

The editor supports the same connection through the canvas and service-list forms. Drawing a connection opens the output and receiving-variable choices; it does not immediately stage a guessed value. Saving creates an immutable configuration revision. A deployment still requires a reviewed plan and explicit apply.

## Outputs

Every service exports `HOST`, its private runtime hostname. PostgreSQL also exports:

| Output | Value |
| --- | --- |
| `PORT` | `5432` |
| `URL` | PostgreSQL URL including the selected secret-backed password and `sslmode=disable` |
| `DATABASE` | Database name |
| `USERNAME` | Database user |
| `PASSWORD` | Immutable password secret reference |

Additional named outputs use these types:

| Type | Configuration | Resolution |
| --- | --- | --- |
| `address` | No value fields | Private runtime hostname; `HOST` is already available. |
| `port` | `port` | Decimal port number. |
| `url` | `scheme`, `port`, optional absolute `path` | URL using the source's private runtime hostname. |
| `variable` | `variable` | A declared variable on the source service or environment, retaining its plain value or immutable secret reference. |

Generic URLs have no embedded credentials or interpolation language. Pass credentials through separate secret-backed variable outputs where the application supports them. A variable output cannot read another binding's generated variable. Reciprocal static references are valid; explicit readiness cycles are rejected.

Output names are unique uppercase variable-style names. Built-in names cannot be replaced. Bindings cannot replace an existing shared/service variable or another binding on the receiver. PostgreSQL is not a binding destination. There are at most 20 custom outputs per service and 100 bindings per specification.

Existing PostgreSQL bindings with no `output` field continue to resolve `URL` and imply PostgreSQL readiness. Newly named references use the separate readiness controls. Existing shared variables remain unchanged.

```json
{
  "name": "API_URL",
  "type": "url",
  "scheme": "http",
  "port": 8080,
  "path": "/api"
}
```

Attach this output to a source application's `outputs`. A binding selects that application's ID and `output: "API_URL"`, the receiver's ID and the receiving variable name. The hostname is computed from the reviewed runtime instance; it is never a user-supplied external address.

## Secrets, cloning and export

Saved revisions and reviewed plans contain references, not hydrated output credentials. Only the assigned agent receives execution secrets. Existing bounded log redaction includes the source secret and resulting container environment values. Output changes participate in revision/plan hashes and stateful automatic-deployment review.

Cloning remaps resource identities. Copying secrets produces independent secret versions. Without copied secrets, source secret variables, outputs backed by those omitted variables and their bindings are omitted; non-secret address/port/URL bindings remain. PostgreSQL receives a fresh initialization credential through the existing clone workflow. Cloning does not copy application data.

Compose export retains private instance addresses and secret placeholders. PostgreSQL URL passwords use a separate `_URLENCODED` placeholder; generic secret variable outputs use the ordinary secret placeholder. Literal dollar signs remain literal. Export does not include data; use the backup and restore workflow when moving a stateful service.

Protected stateless releases do not replace a source with bound consumers: those consumers would otherwise retain the previous private hostname. Such a change uses the standard reviewed deployment path. See [application releases](application-releases.md).

## Maintained recipes and app templates

**Add from service catalog** offers two kinds of entry. A **service recipe** adds one service. An **app template** adds a small application of several connected services, with the secrets, route and backup it needs. Both are ordinary visual specifications: what staging adds is the same configuration you could build by hand, and you own and edit it afterwards. App templates are not Compose files and are unrelated to [Compose environments](compose-deployments.md), whose files stay authoritative and which the catalog never changes.

Staging happens on the server. It copies the entry under fresh service, storage and connection identities, generates each of the entry's secrets into the environment's secret store, and saves the result as a new draft revision. Generated values never reach the browser; the secret store lists them by name, such as `Kaneo · AUTH_SECRET`. Instead of a generated value you can choose an existing secret version for any of them, for example to keep a database password you already hold. Staging needs no recent sign-in, since nobody sees the values, but the editor must have no unsaved changes: save or discard them first. A name already used by a service or secret in the environment is refused; stage under another name. Staging never deploys: review a deployment plan as usual.

Entries carry multi-platform images pinned by digest. Image pins follow the seven-day release-age rule, and the daily [pin report](continuous-integration.md#pinned-images-and-tools) finds them in the catalog's source and reports rebuilds, newer releases and vulnerabilities. A catalog database is managed PostgreSQL at the current release of its major version (see [supported upgrades](supported-upgrades.md)), never an image of the template's own. Service recipes create no public route, host permissions, automatic deployment or backup policy.

| Entry | Deployment and bindings | Backup / restore | Major upgrades |
| --- | --- | --- | --- |
| PostgreSQL 18.4 | Private database and built-in outputs; generated password, or an existing secret version | Tested logical backup and fresh restore | Existing 17.10 → 18.4 workflow only |
| Valkey 9.1.3 cache | Private Redis-compatible cache, `HOST`/`PORT`/`URL` | Not applicable: persistence disabled, data lost on restart | Manual compatibility review |
| Nginx 1.30.6 | HTTP readiness, private HTTP outputs | Not applicable to the welcome-page recipe | Manual compatibility review |
| Uptime Kuma 2.5.9 (slim, rootless) | HTTP readiness, private HTTP outputs, local retained volume | Manual: stopped-volume tools available; recipe-specific recovery unverified | Manual compatibility review |
| OCI Distribution 3.1.2 | `/v2/` readiness, private HTTP outputs, local retained volume | Manual: stopped-volume tools available; recipe-specific recovery unverified | Manual compatibility review |
| Kaneo 2.49.0 (app template) | Kaneo and its managed PostgreSQL database; `DATABASE_URL` binding with readiness; generated `AUTH_SECRET`; allowlisted public route | Tested logical backup of the database, recommended daily | Manual: Kaneo migrates its database on start and cannot go back |

### Kaneo

[Kaneo](https://github.com/usekaneo/kaneo) (MIT) is a project tracker. The template follows upstream's Compose file: one Kaneo service, which serves its web client and API on port 5173, and a PostgreSQL database. Kaneo keeps all its data in the database unless uploads are configured to go to S3-compatible storage.

- **Route.** Give a domain you control, pointed at the deployment server, or choose a server for a generated application domain. TLS is a managed certificate by default. Kaneo learns its public address from `KANEO_CLIENT_URL`, which staging sets from the route; if you later change the route's domain or TLS, change `KANEO_CLIENT_URL` with it.
- **First account.** Kaneo makes the first account on a new instance its administrator, and has no setup token. So the route must name the client networks allowed to reach it, prefilled with your own network as management sees it. A server reached another way, such as through a VPN or with management on a private network, may see you at a different address: check it. Deploy, create your account, then edit the route to remove or widen the allowlist. The allowlist checks the address that connects to the server, so a CDN or proxy in front of the domain must be part of it.
- **Registration.** `DISABLE_REGISTRATION` is `true`, so after the first account people join by invitation. Set it to `false` to let anyone sign up.
- **Optional features.** Email, sign-in providers, uploads and GitHub need Kaneo's own variables (`SMTP_*`, `GITHUB_*`, `S3_*`); see [its environment reference](https://kaneo.app/docs/core/installation/environment-variables). Kaneo trusts forwarded client addresses only from its own bundled proxy, so behind the platform ingress its rate limits and session records see the ingress address for every client.
- **Backups.** Once the stack is deployed, **Backups** recommends a daily PostgreSQL logical backup of its database, keeping 14 uploads. Choosing it fills in the policy form; an owner still chooses the destination and repository password. Recommendations come from the record of what was staged, which an encrypted workspace transfer does not carry: after an import, create the policy yourself.

### Upgrading what a template staged

A staged entry is yours: catalog updates never change it. When the catalog moves an entry to a new release, only new stagings get it. To upgrade Kaneo, take a verified backup, read the release notes, then change the Kaneo service's image to the new pinned reference and review the plan. Kaneo migrates its database when the new image starts, and an older image cannot use the migrated database, so going back means restoring the backup. The database follows managed PostgreSQL's own release and upgrade workflows.

“Tested” describes the pinned recipe on the supported Docker baseline, not every future image tag or user modification. Deployment, binding, backup, restore and upgrade support appear separately in the catalog. An image starting successfully does not establish a maintenance guarantee.

Valkey and the registry templates are unauthenticated inside their deployment unit. Configure authentication and appropriate TLS/access restrictions before publishing them. Uptime Kuma needs initial administrator setup through a restricted route. Docker socket access is not included. The recipe uses upstream's slim, rootless image: it runs as an unprivileged user and leaves out Chromium and the embedded MariaDB, so choose SQLite or an external MariaDB at setup, and Real Browser monitors need a remote browser. A service staged from the earlier full image keeps that image. Moving it to this one needs a database other than the embedded MariaDB, and its data owned by uid 1000: the full image runs as root, so on its volume the rootless image starts and keeps its SQLite database, but uploads, screenshots and setup changes fail. Before deploying the new image, give the volume's files to uid 1000 on its server while the service is stopped, for example with `docker run --rm --user 0 -v <volume>:/app/data --entrypoint chown <new image> -R 1000:1000 /app/data`; `docker inspect <container>` names the volume under `Mounts`. The private registry recipe does not configure Astilba's build registry integration or make its container hostname reachable from the host/build runner.

Upstream setup references: [PostgreSQL image](https://hub.docker.com/_/postgres), [Valkey persistence](https://valkey.io/topics/persistence/), [Nginx image](https://hub.docker.com/_/nginx), [Uptime Kuma installation](https://github.com/louislam/uptime-kuma/wiki/%F0%9F%94%A7-How-to-Install), and [Distribution deployment](https://distribution.github.io/distribution/about/deploying/).

## Ownership and rollout

Catalog and output editing are available only for dashboard-authoritative visual environments. Git/Compose files remain authoritative in file-managed environments; the catalog never rewrites them. Visual specifications and the ordinary revision API remain the source of truth; there is no separate catalog runtime or hidden synchronization.

Named-output specifications require an agent advertising `serviceOutputs` after negotiating `Astilba-Agent-Service-Outputs: v1`. Management checks support during plan creation, apply and command hydration. Update the controller and agent before deploying those specifications. Legacy specifications with omitted extension fields retain their existing hashes and behavior.

Catalog entries are validated by the visual parser and the supported Compose input subset, and staged in tests without any placeholder identity surviving. The integration suite stages Kaneo through the API, checks that no browser response carries a generated value and that the assigned agent receives them. The native visual suite stages and deploys each entry with a consumer of its actual private outputs, exercises cache writes and restart behavior, and checks that no application host port is published. For Kaneo it also checks that the ingress refuses a client outside the allowlist, that Kaneo answers its readiness check through the opened route and creates its schema in its database, and that removing the stack removes the route. Existing PostgreSQL backup/restore and major-upgrade scenarios remain separate checks.
