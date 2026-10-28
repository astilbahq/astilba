# Git providers and source-preserving Compose edits

PLAT-7 is a disposable validation module, separate from the future platform. It tests authenticated Git import, one source-preserving configuration commit and a provider pull/merge request on GitHub, GitLab and Gitea. It does not implement a production connector, deployment trigger or canvas.

## Reproduce

Use Go 1.27.3, Git, Docker and a disposable local daemon with at least 6 GiB available memory (8 GiB used for the two-CPU compatibility run). The fixture starts one provider at a time, binds a random loopback port, creates synthetic credentials and private repositories, and removes only its labelled container and volumes, including their repository state. The hosted GitHub test separately deletes its identity-checked repository and confirms its absence. No host directories are mounted. Containers use the daemon's available CPUs, including [two-core private GitHub runners](https://docs.github.com/en/actions/reference/runners/github-hosted-runners), and retain a 6 GiB memory limit. Do not target a server with existing workloads.

```sh
docker --host "$PLAT7_DOCKER_HOST" pull gitlab/gitlab-ce:19.5.1-ce.0@sha256:bb79dc86267c557fd11e7feb0cd9dd506dd02e991ecdd6cae11ca7d7e5d36fc3
docker --host "$PLAT7_DOCKER_HOST" pull gitea/gitea:28.2.0@sha256:30777c91280d973fa02be48b1ade735bd6859e8f6f7af3b4444d519437852504
make verify-git
```

Set `PLAT7_DOCKER_HOST` to the daemon's explicit `unix://` socket. `make verify` runs this suite plus the durable-execution suite; that suite needs its own `PLAT2_DOCKER_HOST` and pinned images. CI executes those suites in separate required jobs. Pure checks without provider containers: `cd experiments/git-compose && go test -race ./...`.

For the hosted GitHub test, provide `PLAT7_GITHUB_TOKEN` and `PLAT7_GITHUB_OWNER` through the environment, then run `go test -race -count=1 -timeout=5m -v -run '^TestHostedGitHub$' .` in this module. It creates and deletes one private, randomly named `plat7-fixture-*` repository in the specified organization. The fixture token needs create/delete repository permission in addition to the connector permissions below. Never put a token in command arguments, source files or published logs. A failed cleanup is a test failure requiring operator follow-up; do not assume a process killed externally ran its cleanup.

## Contract and boundaries

`Open` fetches an explicit branch into a temporary bare Git repository and imports only selected regular files at the frozen commit. It performs no checkout, submodule operation, hook, credential helper, `.env` read or Git filter. It isolates Git configuration and supplies credentials through URL-scoped process configuration. API and Git errors omit response bodies, tokens and private URLs. Files are limited to 1 MiB each, 4 MiB total and 16 per bundle.

`EditImage` changes only a direct, existing single-line image scalar. It uses YAML node locations and raw byte replacement, then validates the effective Compose model using explicit interpolation values. Ordered files retain their precedence. Tests retain comments, single/double/plain quotes, anchors elsewhere, extensions, Unicode, CRLF and untouched files. The resolved model is never serialized. Nonempty supplied runtime secret values are forbidden anywhere in source output; `Prepare` also checks the original secret inputs so a manual draft cannot remove that guard. This is a fixture contract, not a general secret scanner.

Aliased or anchored target values, anchored/merged/flow service mappings, interpolated targets, multiline scalars, duplicate keys and missing explicit images return a file-editor error. Includes, `extends`, `env_file`, `label_file` and external secret/config files are refused in this experiment rather than resolved outside the declared bundle. P4 still needs a complete explicit source-bundle resolver and richer source diagnostics. These refusals are not a product decision to abandon Compose features.

`Prepare` writes all changed blobs into one commit while retaining unrelated tree entries. `Publish` checks the frozen base, creates a branch only if absent, checks the base again, and opens a provider request. A concurrent base change reports conflict; if it happens after the push, a draft branch can remain for explicit reconciliation. Publication does not merge or deploy. The test harness separately merges the owned fixture review to check re-import and stale-draft rejection. Production needs durable draft/publication state and reconciliation of uncertain API results.

GitLab's [branch-name cache can briefly reject a newly pushed source branch](https://support.gitlab.com/hc/en-us/articles/29678927524380-Intermittent-Source-branch-does-not-exist-when-creating-merge-requests). That exact create-validation rejection is retried within the existing 30-second API budget, after rechecking the source commit and frozen base. Other validation errors and ambiguous outcomes, including transport failures, are not retried.

## Provider requirements

| Provider | Exercised authentication | Connectivity and webhook requirements |
| --- | --- | --- |
| GitHub hosted | HTTPS Git plus REST bearer token. Fixture uses an account token with `repo` and `delete_repo`; production should use a scoped GitHub App with metadata read, contents write and pull requests write for selected repositories. | Outbound HTTPS to API and Git hosts; inbound HTTPS webhook endpoint. Verify `X-Hub-Signature-256` over original bytes and the push event/delivery headers. |
| GitLab CE 19.5.1 | Bootstrap administrator creates a private project, then a project access token with Maintainer role, `api` and `write_repository` runs the connector workflow. | Configured instance URL for HTTPS Git and `/api/v4`; inbound push hooks use `X-Gitlab-Token`, `X-Gitlab-Event` and event UUID. Fresh project-token access is probed with bounded metadata reads before import because it can initially return 404. Root-path loopback HTTP is used only for the disposable local test. |
| Gitea 28.2.0 | Synthetic user's access token with `write:repository` for API and HTTPS Git, plus `write:user` for the fixture's `/user/repos` creation endpoint. The connector itself does not need repository-creation permission. | Configured instance URL for Git and `/api/v1`; inbound hooks verify bare-hex HMAC-SHA256 in `X-Gitea-Signature`, event and delivery headers. Root-path loopback HTTP is used only locally. |

Real instances require trusted TLS and network reachability from the component doing the import; private instances cannot be assumed reachable from a hosted control plane. A customer-side relay/runner and private CA distribution remain production integration work. Cross-origin redirects are disabled. Hosted GitLab, GitHub Enterprise, path-prefix installations, private CAs, SSH transport and live hosted webhook delivery are not exercised by this spike.

`PushReceiver` tests provider-specific authentication, payload bounds, repository/ref identity and concurrent duplicate deliveries. Its in-memory delivery set is test-only. P1/P5 must persist replay state, fetch and authorize the exact commit, reconcile reordered events and enforce deployment policy. Passing synthetic signed payloads does not prove a provider can reach an actual public webhook endpoint.

## Dependencies and evidence

Direct dependencies: compose-go v2.16.2 (Apache-2.0), distribution/reference v0.6.0 (Apache-2.0), go.yaml.in/yaml/v3 v3.0.5 (MIT/Apache-2.0). Versions and transitive modules are fixed in `go.mod`/`go.sum`; compose-go also uses its own yaml/v4 parser. The source-edit result is revalidated by compose-go, so parser differences cannot silently change the effective image. The GitLab CE image contains MIT-licensed GitLab CE; Gitea is MIT-licensed. Images include third-party components and retain their upstream licensing.

On 22 October 2026, local arm64 checks passed the real hosted GitHub workflow and the pinned GitLab/Gitea workflows, including ten consecutive GitLab runs after the token-readiness fix. Every workflow checks request creation, unchanged base, branch collision, provider merge, exact re-import and stale-draft rejection. The full `make verify` gate also covers the existing durable-execution/transport suite. CI repeats the self-hosted provider checks on Ubuntu amd64; the hosted GitHub test remains separately opt-in. The P0 experiment 7 entry in [the implementation plan](../../docs/platform-implementation-plan.md) records the resulting boundary.

Primary references: [GitHub pull requests](https://docs.github.com/en/rest/pulls/pulls), [GitLab merge requests](https://docs.gitlab.com/api/merge_requests/), [GitLab project access tokens](https://docs.gitlab.com/user/project/settings/project_access_tokens/), [Gitea API](https://docs.gitea.com/api/), [Compose specification](https://github.com/compose-spec/compose-spec).
