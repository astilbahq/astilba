# Continuous integration

Seven independent workflows run on pull requests, merge groups, pushes to `main`
and manual dispatches; the native, durable-execution, Git provider and canvas
workflows also run nightly. Each has its own concurrency group per workflow and
ref (and, for scheduled workflows, event) and can be retried without waiting for
another workflow's native tests. No workflow uses an event path filter: a planning
job selects the suites a pull request or merge group can affect, and each required
check is a final `always()` gate, so it is reported on every run.

| Workflow                   | Required check                   | Verification                                                                                                                      |
| -------------------------- | -------------------------------- | --------------------------------------------------------------------------------------------------------------------------------- |
| `go.yml`                   | Go verification                  | Durable-execution fixtures, including race detection                                                                              |
| `git-providers.yml`        | Git provider verification        | GitLab/Gitea and source-preserving Compose edits                                                                                  |
| `ci.yml`                   | Native verification              | Selected amd64 and arm64 host suites: fresh bootstrap, Docker, builds, PostgreSQL restore, systemd agent, deployment and recovery |
| `canvas.yml`               | Canvas browser verification      | Type/lint/build, domain tests and Chromium/WebKit journeys                                                                        |
| `application.yml`          | Application verification         | `make verify-app-build` and three parallel Go race lanes, image smoke test, corresponding-source archive and selected vulnerability scan                             |
| `application-browsers.yml` | Application browser verification | Every Chromium/WebKit dashboard journey against a real API/PostgreSQL fixture                                                     |
| `workshop.yml`             | Workshop verification            | Standalone Storybook scenarios and accessibility audits in both browsers                                                          |

Each gate requires a successful planner and then either a successful selected
workload or a skipped unselected one. A failed or cancelled job cannot satisfy
it. All action and image pins, assertions, race detection and uncached test
execution remain required.

## Suite selection

Every suite runs for main pushes, manual and scheduled runs. For a pull request
or merge group, the plan jobs run the base's `scripts/ci-plan.mjs`, so editing the
planner cannot select its own checks. A suite the base planner does not report,
including every suite when the base has no planner, is selected. Pull requests
and merge groups run their own workflow files, so workflow changes still rely on
review. A pull request compares the checked-out merge to its actual first parent;
a merge group compares its head to the event's `base_sha`, because a squash group
is a stack of single-parent commits whose first parent can omit pull requests in
the group. Both include deleted paths and both sides of renames, and skip a suite
only when every changed path is known not to affect it:

| Suite | Skipped when every changed path is |
| --- | --- |
| Native hosts | Markdown under `docs/` or the root README, dashboard source, theme, public assets, browser tests, Storybook or Panda configuration, or the server-only `auth`, `alerts`, `mailer`, `billing` and `automationclient` packages or the CLI |
| Application Go race lanes | documentation or dashboard paths above |
| Application and workshop browsers | documentation |
| Durable execution, Git providers, canvas | outside that experiment, its workflow and the Makefile (canvas also follows the root JavaScript manifests and lockfile) |
| Vulnerability scan | not a Go file, `go.mod`, `go.sum`, `package.json`, root JavaScript manifest or lockfile, the Makefile, the allowlist, the scanner script or its workflows |

`AGENTS.md`, `REVIEW.md`, runtime, contracts, migrations, package/lock files, CI,
scripts, toolchain and unknown paths select the native suite. A change to the
planner or the native workflow selects every suite. A pull request's missing merge
metadata or a Git error in the planner fails every gate; an unreadable base
planner, a merge group without a base or an empty diff selects every suite.
Application checks, builds, the image smoke test and the corresponding-source
archive always run. `make check-ci` exercises these selection contracts using
temporary repositories.

Native tests run only on their owned disposable Ubuntu runners. They never reuse
an existing deployment host or application runtime state.

## Merge queue

`main` requires a merge queue, and every required check also reports on
`merge_group`. A queued pull request is tested on top of `main` and the pull requests
ahead of it, which replaces updating the branch by hand and restarting its checks
whenever `main` moves.

- The workflow change must reach `main` before the queue is required. Without a
  `merge_group` trigger the required checks never report and queued pull requests
  wait until they time out.
- Keep "Only merge non-failing pull requests" on. `base_sha` may be `main` or the
  head of the entry ahead, which GitHub does not specify. With the entry ahead, a
  group uses that entry's planner and diffs only its own changes, which is safe
  only because that entry must pass its own run and land first. With the setting
  off, a green group behind it could land a failed or hostile entry ahead.
- A merge group's run does not cancel an earlier run on its ref, because a
  cancelled required check would eject a live group's pull requests. Pull requests
  and merge groups use different refs, so neither can cancel the other. Re-queuing
  with an unchanged base may reuse the ref, in which case the new run waits for the
  dropped group's run, which keeps going because Actions has no trigger for
  `destroyed`. The queue's status check timeout must allow for that wait.
- Merge groups run on a branch in this repository, not in a fork's restricted
  context. Queuing a fork's pull request runs its workflow files and scripts
  with this repository's access, so review them before queuing it.
- Main pushes still run every suite after a group lands.

The queue merges one squashed pull request at a time, builds two at once, starts
with no wait and allows checks 150 minutes. It replaces "Require branches to be
up to date", so `strict` is `false` in `.github/required-status-checks.json` while
the queue is on.

To remove the queue:

1. Land `strict: true` in that file while the queue is on. If nothing can land
   through the queue, land it straight after step 3 instead.
2. Wait for the queue to empty or remove its entries, since GitHub does not
   document what happens to them.
3. In one save, turn off "Require merge queue" and turn on "Require branches to be
   up to date". Never leave both off: stale pull requests could merge.

To turn it back on, make both changes in one save, then land `strict: false`.

## Native lanes

Each architecture runs three lanes, each on its own disposable runner VM. The
Native verification gate requires all six jobs to pass:

| Lane | Verification |
| --- | --- |
| `core` | PLAT-4 host runtime, builds, PostgreSQL restore, managed links, Compose and cross-host deployment, pipeline, preview, release transport and troubleshooting target |
| `recovery` | Agent service enrollment, restart and signed update; cross-host older-backup, drained and upgrade recovery |
| `visual` | Visual deployment, including catalog, backup, restore and major upgrades |

Every lane repeats the same fresh production bootstrap, pinned runtime install and
image pulls, so each suite starts from the same prepared host. The Makefile's
`verify-agent-host-*` targets partition `verify-agent-host`, which runs them in
sequence on a local disposable host. CI no longer runs suites from different lanes
on one VM, so it cannot see residue from one lane break another. For example, an
ingress that survives the preview test's cleanup no longer trips the visual test's
absent-ingress guard in CI; the sequential target still shows it.

`-run` lists select the native tests, so `make check-ci` fails when a
`func TestNative*` that Go builds for `linux/amd64` or `linux/arm64` under
`services/platform` is selected by no lane or by more than one, when a `-run`
alternative matches no test in its package, or when the workflow's `lane` list
differs from the targets `verify-agent-host` runs. The helpers that other tests
re-execute are named exceptions in `scripts/native-lanes.mjs`. It refuses what
could stop a named test running but it cannot read: Makefile conditionals, shell
syntax other than `cd DIR && ... go test`, unlisted `go test` flags, and matrix
`exclude` or `include`. A test that skips itself at run time still counts as
selected.

## Local native agent tests

`make verify-agent-colima` runs selected `internal/agent` native tests as root
inside a Colima VM from macOS, in seconds rather than a full CI run. It
cross-compiles the test binary for the VM's architecture, copies it in, runs it
and removes it, including on failure. It requires an explicitly named profile,
refuses Colima's default profile and never starts one. Use a profile created for
this purpose: tests run as root and act on that VM's Docker daemon.

```bash
colima start -p <disposable-profile> --activate=false --mount none --port-forwarder none --memory 2 --disk 10
```

```bash
colima ssh -p <disposable-profile> -- docker pull busybox:1.37.0@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e
```

```bash
make verify-agent-colima PLAT_AGENT_COLIMA_PROFILE=<disposable-profile>
```

`--activate=false` leaves the current Docker context alone and `--mount none`
keeps macOS files out of the VM. `--port-forwarder none` keeps ports that tests
publish off the Mac's network: Colima otherwise forwards ports bound to `0.0.0.0`
in the VM to every Mac interface, including fixtures with passwords from this
public source.

`PLAT_AGENT_TESTS` overrides the test pattern. The default runs
`TestNativeReleaseCommandTransport` and `TestNativeTroubleshootingTarget`, the
only native tests that need nothing beyond root, the pinned image above and
Docker with a supported runc and registry access. Interrupting a run removes the
binary, but a test may stop without its cleanup: recreate the profile if fixtures
remain. The rest must be asked for by name:

| Test | Also needs |
| --- | --- |
| `TestNativeComposeDeployment` | Publishes a loopback host port; the pinned Compose plugin and PostgreSQL image |
| `TestNativePipeline` | `/opt/astilba-plat5/bin/zot` and Git `http-backend`; writes `/etc/docker/certs.d` |
| `TestNativeVisualDeployment` | Ingress publishes host ports 80, 443 and 15432; the pinned Compose plugin and `/opt/astilba-platform-tools/restic` |
| `TestNativePreviewEnvironment` | Ingress publishes host ports 80 and 443; the pinned Compose plugin and Git `http-backend` |
| `TestNativeCrossHostDeployment` | Creates host namespaces, bridges and WireGuard links; the WireGuard module, pinned Compose plugin, `wg` and `nft`, `dockerd` and `docker` under `/opt/astilba-plat4` and restic |

This does not replace CI. `CGO_ENABLED=0` rules out the race detector, the run
covers only the VM's architecture, and the VM is not the Ubuntu host image CI
bootstraps. Native verification remains the required check.

## Application Go verification

Application verification runs four jobs in parallel: `make verify-app-build`
(application checks, agent/CLI and dashboard builds and packaging, followed by
the image smoke test and source archive) and three Go race lanes for the
cross-host pair, preview checkpoint recovery and all remaining Go tests.
Anchored selections in the Makefile partition every test between the lanes.
Each retains race detection, uncached execution and a fifteen-minute process
timeout, and any failure fails the Application verification gate. Locally,
`make verify-app-core` runs the build target and then the three lanes
concurrently.

Each integration process owns a separate disposable PostgreSQL container, and
each test creates its own database and temporary resources. Preview recovery has
a separate process budget from the cross-host pair; both are isolated from the
remaining tests.

## Vulnerability scan

`make check-vulnerabilities` runs `govulncheck` (a `services/platform` Go tool) and
OSV Scanner (a release binary pinned by SHA-256 in `scripts/vulnerability-scan.mjs`)
against `pnpm-lock.yaml` and every Go module. The Application workflow calls
`vulnerability-scan.yml` when the suite is selected, and that gate requires it, so
it adds no required context. The same workflow runs daily on its own, so an advisory
published after the last code change fails a scheduled run without rerunning the
application suites; GitHub notifies whoever last changed the schedule. The target is
not part of `check-app` because it needs the Go and OSV vulnerability databases and
a GitHub release download. `make check-ci` runs its offline policy tests.

A selected run can fail on an advisory the pull request did not introduce. That
holds up the merge group too, so land the bump or allowlist entry first.

Findings fail the same way wherever they are. The surface decides how urgent one is
and what an allowlist entry has to name:

| Surface | Finding |
| --- | --- |
| `image`, `agent`, `cli` | A vulnerable function is reachable from `cmd/server`, `cmd/agent` or `cmd/cli`, for the platforms that binary ships to |
| `dashboard` | A lockfile package in the production dependency closure of `@astilba/dashboard` |
| `tooling` | Any other lockfile package, or Go reachable only from other `services/platform` packages |
| `experiment` | A vulnerable function is reachable in `experiments/durable-execution`, `git-compose` or `platform-comparison/app` |

Each binary is scanned with the `GOOS`, `GOARCH` and build tags it ships with. These
choose the source files analysed, not the advisories: an advisory limited to a
platform nothing ships to can still report as reachable, and then needs a bump or an
allowlist entry. A new binary under `cmd` or a Go module outside `experiments` stops
the scan until it is added to `scripts/vulnerability-scan.mjs`.

`dashboard` follows pnpm's production closure, so it includes peers that are never
bundled, such as `typescript`. `tooling` means outside that closure, so review code
that a build tool generates into the bundle by hand. Go advisories whose vulnerable
function is not reachable are listed for information and do not fail.

Not scanned:

- Test-only Go code and Go tools such as `staticcheck`.
- Container base images and the pinned runtime images.
- Tools bundled inside another package. `vite` resolves to
  `@voidzero-dev/vite-plus-core`, which carries Vite, Rolldown and tsdown in its own
  `dist`, so an advisory against those names cannot match it. Check them when
  bumping that package.

The experiments' scans add seconds, so they run every time; canvas dependencies are
`tooling` because they share the lockfile.

### Exceptions

Add an advisory that cannot be fixed yet to `.github/vulnerability-allowlist.json`
in a reviewed pull request:

```json
{
  "id": "GHSA-xxxx-xxxx-xxxx",
  "package": "example-package",
  "surfaces": ["tooling"],
  "reason": "Why it cannot be fixed yet and why the exposure is acceptable.",
  "expires": "2026-12-31"
}
```

`id` may be the advisory or any alias. An entry can expire at most 90 days ahead and
still applies on its expiry date. The scan fails if an entry has expired, the
finding reaches a surface the entry does not name, or the entry no longer matches a
finding, so exceptions cannot outlive what was reviewed.

## Source-size ratchet

`make check-source-size` runs the source-size enforcement fixtures and checks
authored Go and TypeScript/JavaScript files at the repository root and under
`apps`, `services`, `contracts`, `scripts` and `.github`. It is a prerequisite of `check-app`,
so the existing Application verification gate requires it. Files have a
600-physical-line limit; tests have 800. Comments and blank lines count, while a
terminal newline does not add a line. Test suffixes and the `tests` and
`workshop-tests` directories identify tests. Experiments, dependencies, build
outputs and the generated owners listed in `scripts/source-size.mjs` are excluded
by their owned paths; an unrelated package named `coverage` or `dist` is checked.
The checker does not follow source symlinks.

`scripts/source-size-baseline.json` lists exactly the currently oversized files.
Its entries cannot be added or increased against the comparison commit. Reducing
an oversized file requires lowering its allowance; reaching the ordinary limit
removes it. Regenerate eligible reductions with
`node scripts/source-size.mjs --write-baseline`. The first policy commit derives
legacy eligibility from its base's actual files instead of accepting arbitrary
exceptions.

Application CI fetches full history and supplies `SOURCE_SIZE_BASE`: the tested
PR merge's actual first parent, a merge group's `base_sha`, the previous commit
from a push event, or the manual run's first parent. CI and explicit `--base <ref>`
checks require available Git history. Local Git checks otherwise use the common ancestor with
`origin/main`, falling back to local `main` only when that remote ref is absent.
Fetch the missing history or supply an available base when resolution fails;
the checker never fetches automatically.

A Git-free corresponding-source archive can check its embedded sizes and
allowances, with an explicit notice that historical comparison is unavailable.
Archives cannot regenerate allowances or satisfy a CI history check.

## Inline SQL ratchet

`make check-inline-sql` runs as part of `check-app`. It counts Go string literals
that begin an SQL statement in non-test files under `services/platform`, excluding
sqlc and OpenAPI output, and compares each Go package (directory) with the same
comparison base as the source-size ratchet. Statements may move between a
package's files, so oversized files can still be split, but a package's count
cannot grow and a new package cannot introduce inline SQL. A literal is an
approved exception when its own line carries `// inline-sql: <reason>` or the
line above is only that comment. Move statements into
`services/platform/db/queries` to reduce the count; moving legacy inline SQL to
another package counts as new SQL, so convert it to sqlc as part of the move.

## Browser shards

Application and workshop workflows each run Chromium and WebKit with two shards
per browser. Each runner owns its ports, outputs and fixture. Application tests
retain one worker and file-level serial execution; each shard still builds its
own dashboard and embedded Go server and uses disposable PostgreSQL. The browser
container's cleanup verifies its ownership label.

Workshop tests use isolated browser contexts and API fixtures without a backend.
They run with two workers and `fullyParallel: true`, allowing individual cases in
the single spec file to distribute across shards. The static workshop is built
on each runner. Accessibility audits and scenario completion assertions remain
enabled. `make verify-app` remains the complete unsharded local check.

## Required checks

`.github/required-status-checks.json` is the payload for the required-status-checks
endpoint. Nothing applies it automatically, and `make check-ci` compares only its
contexts with the workflows, not with live protection. To apply it:

1. Use the copy on current `origin/main`. Apply a new check after it has landed and
   reported, so GitHub pins it to the app that reports it. A pull request that
   renames or removes a required check cannot report the old name, so update it to
   contain current `origin/main` and apply its copy before merging, once its new
   checks have succeeded on that update and the queue is empty. After a rename,
   other open pull requests need that change before they can queue.
2. Confirm in the branch settings that "Require merge queue" is on if the file says
   `strict: false` and off if it says `strict: true`. The protection endpoint does
   not show the queue, so reading protection back cannot catch this.
3. Back up branch protection, then replace only its required status checks:
   `gh api --method PATCH repos/astilbahq/astilba/branches/main/protection/required_status_checks --input .github/required-status-checks.json`.
4. Read protection back and confirm its strict setting and contexts match the file,
   and that every entry in `checks` has an `app_id`.

Do not temporarily remove protection.

## Native Go cache

The host jobs cache downloaded Go modules and compilation outputs under the
runner's temporary directory. The key includes the operating system,
architecture, lane, pinned bootstrap/toolchain configuration, module files and Go
sources. A matching toolchain cache can seed a changed source tree; Go checks
build inputs before reuse. Test commands retain `-race -count=1`.

Only `GOCACHE` and `GOMODCACHE` are forwarded through `sudo` to the disposable
native test commands. Runtime state, Docker storage, databases and credentials
are outside the cache. A cache miss still runs the complete
suite and builds the missing dependencies.

## Comparing changes

Compare a successful baseline with a first cache-miss run and a subsequent
cache-hit run of the candidate. Keep application/test sources, runner labels,
tool versions and assertions fixed. Include cache restoration/saving and all
job setup/cleanup in the measurements.

- Elapsed time: earliest job start to latest job completion; report queue delay
  separately if it materially changes the result.
- Estimated runner minutes: round each job's elapsed seconds up to a whole
  minute, then sum every job, including the final gate. Keep amd64 and arm64
  subtotals because their billing rates differ.
- Reconcile against the billing platform's usage report when available. The
  [legacy workflow usage endpoint is being retired](https://github.blog/changelog/2025-02-02-actions-get-workflow-usage-and-get-workflow-run-usage-endpoints-closing-down/)
  and a zero response does not establish that a run consumed no allowance.
  Job timestamps provide an estimate, not an account invoice or remaining quota.

Use the complete jobs response, including all pages and any retries, when
comparing costs. Do not compare a partial/failed run with a successful run or
attribute hosted-runner variability to the change from a single sample.
Retain parallel jobs only after comparing their duplicated setup cost with
their elapsed-time benefit.

GitHub documents [per-job rounding and runner rates](https://docs.github.com/en/billing/reference/actions-runner-pricing)
and [cache scope and security](https://docs.github.com/en/actions/reference/workflows-and-actions/dependency-caching).
