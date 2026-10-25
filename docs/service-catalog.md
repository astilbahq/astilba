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

## Maintained recipes

**Add from service catalog** stages a fresh copy of an ordinary visual specification. Templates carry immutable multi-platform image identities and create fresh service/storage IDs when staged. PostgreSQL requires a selected existing password secret. No recipe creates a public route, grants host permissions, enables automatic deployment or adds a backup policy.

| Recipe | Deployment and bindings | Backup / restore | Major upgrades |
| --- | --- | --- | --- |
| PostgreSQL 18.4 | Private database and built-in outputs | Tested logical backup and fresh restore | Existing 17.10 → 18.4 workflow only |
| Valkey 9.1.2 cache | Private Redis-compatible cache, `HOST`/`PORT`/`URL` | Not applicable: persistence disabled, data lost on restart | Manual compatibility review |
| Nginx 1.30.6 | HTTP readiness, private HTTP outputs | Not applicable to the welcome-page recipe | Manual compatibility review |
| Uptime Kuma 2.5.7 | HTTP readiness, private HTTP outputs, local retained volume | Manual: stopped-volume tools available; recipe-specific recovery unverified | Manual compatibility review |
| OCI Distribution 3.1.2 | `/v2/` readiness, private HTTP outputs, local retained volume | Manual: stopped-volume tools available; recipe-specific recovery unverified | Manual compatibility review |

“Tested” describes the pinned recipe on the supported Docker baseline, not every future image tag or user modification. Deployment, binding, backup, restore and upgrade support appear separately in the catalog. An image starting successfully does not establish a maintenance guarantee.

Valkey and the registry templates are unauthenticated inside their deployment unit. Configure authentication and appropriate TLS/access restrictions before publishing them. Uptime Kuma needs initial administrator setup through a restricted route. Docker socket access is not included. The private registry recipe does not configure Astilba's build registry integration or make its container hostname reachable from the host/build runner.

Upstream setup references: [PostgreSQL image](https://hub.docker.com/_/postgres), [Valkey persistence](https://valkey.io/topics/persistence/), [Nginx image](https://hub.docker.com/_/nginx), [Uptime Kuma installation](https://github.com/louislam/uptime-kuma/wiki/%F0%9F%94%A7-How-to-Install), and [Distribution deployment](https://distribution.github.io/distribution/about/deploying/).

## Ownership and rollout

Catalog and output editing are available only for dashboard-authoritative visual environments. Git/Compose files remain authoritative in file-managed environments; the catalog never rewrites them. Visual specifications and the ordinary revision API remain the source of truth; there is no separate catalog runtime or hidden synchronization.

Named-output specifications require an agent advertising `serviceOutputs` after negotiating `Astilba-Agent-Service-Outputs: v1`. Management checks support during plan creation, apply and command hydration. Update the controller and agent before deploying those specifications. Legacy specifications with omitted extension fields retain their existing hashes and behavior.

The catalog templates are validated by the visual parser and supported Compose input subset. The native visual suite deploys each recipe with a consumer using its actual private outputs, exercises cache writes and restart behavior, and checks that no application host port is published. Existing PostgreSQL backup/restore and major-upgrade scenarios remain separate checks.
