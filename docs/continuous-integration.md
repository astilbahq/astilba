# Continuous integration

Seven independent workflows run on pull requests, merge groups, pushes to `main`
and manual dispatches; the native, durable-execution, Git provider and canvas
workflows also run nightly. Each can be retried without waiting for another
workflow's native tests. Within a workflow, runs share a concurrency group only per
pull request or merge group ref; every main push, manual and scheduled run has its
own, so no run can cancel it. No workflow uses an event path filter: a planning
job selects the suites a pull request or merge group can affect, and each required
check is a final `always()` gate, so it is reported on every run.

| Workflow                   | Required check                   | Verification                                                                                                                      |
| -------------------------- | -------------------------------- | --------------------------------------------------------------------------------------------------------------------------------- |
| `go.yml`                   | Go verification                  | Durable-execution fixtures, including race detection                                                                              |
| `git-providers.yml`        | Git provider verification        | GitLab/Gitea and source-preserving Compose edits                                                                                  |
| `ci.yml`                   | Native verification              | Selected amd64 and arm64 host suites: fresh bootstrap, Docker, builds, PostgreSQL restore, systemd agent, deployment and recovery |
| `canvas.yml`               | Canvas browser verification      | Type/lint/build, domain tests and Chromium/WebKit journeys                                                                        |
| `application.yml`          | Application verification         | `make verify-app-build` and seven parallel Go race lanes, image smoke test, corresponding-source archive and selected vulnerability scan                             |
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
| Native hosts | Markdown under `docs/` other than `docs/hosting.md`, whose proxy configurations the native tests run, or the root README, dashboard source, theme, public assets, browser tests, Storybook or Panda configuration, or the server-only `auth`, `alerts`, `mailer`, `billing` and `automationclient` packages or the CLI |
| Application Go race lanes | documentation or dashboard paths above |
| Application and workshop browsers | documentation |
| Durable execution, Git providers, canvas | outside that experiment, its workflow and the Makefile (durable execution and Git providers also follow the image pull action, canvas the root JavaScript manifests and lockfile, the pnpm install action and script, and all three `scripts/fetch-attempts.mjs`) |
| Vulnerability scan | not a Go file, `go.mod`, `go.sum`, `package.json`, root JavaScript manifest or lockfile, the pnpm install action or script, `scripts/fetch-attempts.mjs`, the Makefile, the allowlist, the scanner script or its workflows |

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
- Main pushes still run every suite after a group lands, and pull requests landing
  seconds apart each get a complete run. Turning off `cancel-in-progress` alone
  would not do this: a concurrency group keeps one waiting run and cancels the
  others, so each push has a group of its own and their runs overlap. `make check-ci`
  evaluates each workflow's concurrency settings to keep this true.

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

Each architecture runs four lanes, each on its own disposable runner VM. The
Native verification gate requires all eight jobs to pass:

| Lane | Verification |
| --- | --- |
| `core` | PLAT-4 host runtime, builds, PostgreSQL restore, managed links, Compose and cross-host deployment, pipeline, preview, release transport, and troubleshooting target, session, cleanup and refusals, including sessions under the production unit and through real nginx, Caddy and Traefik |
| `recovery` | Agent service enrollment, restart and signed update; a troubleshooting session end to end; cross-host older-backup, drained and upgrade recovery |
| `visual` | Visual deployment, including catalog, backup and restore |
| `upgrade` | Application releases and PostgreSQL major upgrades, then an unrelated unit's routes and data, scheduled jobs, later deployments, controller transfer and volume deletion on that host |

Every lane repeats the same fresh production bootstrap, pinned runtime install and
image pulls, so each suite starts from the same prepared host. The Makefile's
`verify-agent-host-*` targets partition `verify-agent-host`, which runs them in
sequence on a local disposable host. CI no longer runs suites from different lanes
on one VM, so it cannot see residue from one lane break another. For example, an
ingress that survives the preview test's cleanup no longer trips the visual test's
absent-ingress guard in CI; the sequential target still shows it. Scheduled jobs,
controller transfer and volume deletion run in both `visual` and `upgrade`, because
they read finished upgrade records.

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
inside a Colima VM from macOS, by default in under a minute rather than a full
CI run. It cross-compiles the test binary for the VM's architecture, copies it
in, runs it and removes it, including on failure. It requires an explicitly named
profile, refuses Colima's default profile and never starts one. Use a profile
created for this purpose: tests run as root and act on that VM's Docker daemon.

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
`TestNativeReleaseCommandTransport`, `TestNativeCompletionWaitsForAWritersHealthCheck`,
`TestNativeLateCompletionReleasesTheReservation`, `TestNativeLateCompletionKeepsAnUnconfirmedReservation`, `TestNativeLateRestoreLeavesTheSourceToResume`,
`TestNativeTroubleshootingTarget` and the native session tests in `troubleshooting_session_host_test.go`. These need nothing
beyond root, the pinned image above and Docker with a supported runc; only
`docker pull` needs a registry. `scripts/native-agent-colima.sh` lists them by
name, so add a new test only after checking that it needs no more. Interrupting a
run removes the binary, but a test may stop without its cleanup: recreate the
profile if fixtures remain. The rest must be asked for by name:

| Test | Also needs |
| --- | --- |
| `TestNativeBackupOfStoppedWriter`, `TestNativeRefusedReservationOfStoppedWriterCompletes`, `TestNativeBackupOfWriterFailingItsHealthCheck`, `TestNativeBackupOfWriterFailingItsReadiness`, `TestNativeReservationRefusesChangedConfiguration`, `TestNativePauseRefusesAWriterStoppedSinceReserving`, `TestNativeWriterAlreadyFailingThatCannotRestart`, `TestNativeAcceptingAWriterThatCannotStart`, `TestNativeAcceptingAWriterThatDoesNotComeBack` | Nothing more, but they wait out readiness probes for about a minute and a half in all |
| `TestNativeComposeDeployment` | Publishes a loopback host port; the pinned Compose plugin and PostgreSQL image |
| `TestNativePipeline` | `/opt/astilba-plat5/bin/zot` and Git `http-backend`; writes `/etc/docker/certs.d`; nftables 1.0.9; creates a host namespace and veth pair for its private fixtures |
| `TestNativeBuildNetwork` | nftables 1.0.9, Git `http-backend` and access to `registry-1.docker.io`; creates a host namespace, veth pair and Docker network |
| `TestNativeVisualDeployment` | Ingress publishes host ports 80, 443 and 15432; the pinned Compose plugin and `/opt/astilba-platform-tools/restic` |
| `TestNativePreviewEnvironment` | Ingress publishes host ports 80 and 443; the pinned Compose plugin, Git `http-backend` and nftables 1.0.9; creates a host namespace and veth pair for its private fixtures |
| `TestNativeCrossHostDeployment`, `TestNativeCrossHostTakeover` | Creates host namespaces, bridges and WireGuard links; the WireGuard module, pinned Compose plugin, `wg` and `nft`, `dockerd` and `docker` under `/opt/astilba-plat4` and restic |

This does not replace CI. `CGO_ENABLED=0` rules out the race detector, the run
covers only the VM's architecture, and the VM is not the Ubuntu host image CI
bootstraps. Native verification remains the required check.

## Application Go verification

Application verification runs eight jobs in parallel: `make verify-app-build`
(application checks, agent/CLI and dashboard builds and packaging, followed by
the image smoke test and source archive) and seven Go race lanes: `crosshost` for
the coordinated placement journey, `preview` for preview checkpoint recovery,
`network` for the private network generation tests named by `APP_NETWORK_TESTS`,
`revoked-host` for moving off a revoked server (`APP_REVOKED_HOST_TESTS`),
`operations` for tests named by `APP_OPERATIONS_TESTS`, `m-to-z` for other tests
whose names start `TestM` to `TestZ` and `remaining` for everything else.
Each retains race detection, uncached execution and a fifteen-minute process
timeout, and any failure fails the Application verification gate. Locally,
`make verify-app-core` runs the build target and then the lanes concurrently.

Each integration process owns a separate disposable PostgreSQL container, and
each test creates its own database and temporary resources. The database copies one
that the process migrates and checks once, and every fixture shares one password
hash and one loaded API specification, because those cost about 1.6 seconds per
fixture under the race detector. Preview recovery has a separate process budget from
coordinated placement; both are isolated from the remaining tests.

The Makefile selects tests by name. `-run` reaches only the packages on its own
command line, while `remaining` applies its `-skip` to every package, so a name
it skips must be selected in that package too; `operations` and `m-to-z` run
against `./...` for that reason. `make check-ci` runs `scripts/app-lanes.mjs`, which
fails when a test in the platform module is selected by no lane or by more than one,
when a selection matches no test, or when `verify-app-core` and the workflow's matrix
differ from the lanes. A new test needs no lane change: it lands in `remaining` or
`m-to-z` by its name's first letter, or in `operations` if its name has one of that
lane's prefixes.

The lanes run `go test -v`, so the job log has each test's time, in `--- PASS`
lines. One commit's package time has differed by 28% between hosted runners, so a
single run says little about a lane.

`lane-budget.yml` runs `make check-lane-budget` daily and on demand. It reads the
last five main runs of the current lanes and fails when a lane's package has a
median above 600 seconds, listing the times in its job summary. While that lasts it
keeps one issue open with the latest times, and closes it when the lanes recover. It
is not a required check, so runner variance cannot remove a pull request from the
queue. The integration package also adds a warning to any run past that budget, so
the pull request that crosses it shows it in its own checks. When a lane is past its
budget, first look for time every test shares, because the fixture is the only place
one change reaches every test. Then move the letter boundary and rename the lane to
match, move a test family to another lane, or add a lane by adding a variable, its
rule and the workflow's matrix. A lane of one test, such as `crosshost`, can only
come down by shortening or splitting that test. Moving tests between existing lanes
takes three main runs to show, because earlier runs stay in the sample; adding a lane
starts a new sample, and a lane with fewer than three runs neither fails the check nor
closes the issue. Locally, `GITHUB_TOKEN` must allow reading Actions, and nothing is
opened.

## Vulnerability scan

`make check-vulnerabilities` runs `govulncheck` (a `services/platform` Go tool) and
OSV Scanner (a release binary pinned by SHA-256 in `scripts/vulnerability-scan.mjs`)
against `pnpm-lock.yaml` and every Go module. The Application workflow calls
`vulnerability-scan.yml` when the suite is selected, and that gate requires it, so
it adds no required context. The same workflow runs daily on its own, so an advisory
published after the last code change fails a scheduled run without rerunning the
application suites; GitHub notifies whoever last changed the schedule. The target is
not part of `check-app` because it needs the Go and OSV vulnerability databases and
a GitHub release download. `make check-ci` runs its offline policy tests. The download
follows the rules in [Host downloads](#host-downloads): it repeats for at least four
minutes, a stall repeats while a slow download finishes, a file failing its hash stops
the scan, and a 404 also takes four minutes to report.

`go tool -n govulncheck` fetches modules on a cache miss, and `govulncheck` reads the Go
vulnerability database with one request per file, so one reset stops either. Each repeats
for at least four minutes when it exits non-zero, and a Go checksum failure stops at once.
A finding exits 0, so a completed scan never repeats, while any other failure repeats too,
so a package that does not compile takes four minutes to report. If the database stays
unreachable the scan fails, naming the scan and the URL: measured against a database that
resets or returns 404 or 502 on `index/modules.json` or an advisory, `govulncheck` always
exits 1. It tolerates only `index/db.json`, which holds no advisories, and never requests
`index/vulns.json`. A connection that stalls with its peer alive is not retried and uses up
the job's limit. OSV Scanner retries its own API calls.

A selected run can fail on an advisory the pull request did not introduce. That
holds up the merge group too, so land the bump or allowlist entry first.

Findings fail the same way wherever they are. The surface decides how urgent one is
and what an allowlist entry has to name:

| Surface | Finding |
| --- | --- |
| `image`, `agent`, `cli` | A vulnerable function is reachable from `cmd/server`, `cmd/agent` or `cmd/cli`, for the platforms that binary ships to |
| `dashboard` | A lockfile package in the production dependency closure of `@astilba/dashboard` |
| `tooling` | Any other lockfile package, or Go reachable only from other `services/platform` packages |
| `experiment` | A vulnerable function is reachable in `experiments/durable-execution`, `git-compose`, `fleet-capacity` or `platform-comparison/app` |

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

## Repository analyzers

`make check-app` runs `services/platform/internal/lint/astilba-lint` over non-test
code, and `jsonguard` and `jsontag` over tests too, because a test that reads JSON loosely can
pass where production would not. Each analyzer turns a defect that review kept catching into a failure with a
one-sentence explanation:

| Analyzer | Fails when | From |
| --- | --- | --- |
| `dberror` | A branch on the error from a value-returning sqlc method or a row scan never looks at it, so an outage reads as a refusal or a missing row. Use `store.IfMissing` or return the error. | #122, #147 |
| `wsclose` | Code outside `internal/terminal` calls `(*websocket.Conn).Close`, which a peer stalled inside a frame can hold forever. Use `terminal.Close`. | #117, #118, #131 |
| `jsonguard` | Code imports `encoding/json` outside the files [JSON encoding](json-encoding.md#remaining-v1-use) gives a reason for, or calls a plain `encoding/json/v2` writer outside a named third-party function. A stored or peer value written that way would move a digest when read back. Its two lists are the decisions recorded in [JSON encoding](json-encoding.md), not suppressions. | The `encoding/json/v2` adoption |
| `txnetwork` | A function holding a transaction calls a Git provider or GitHub, directly or through a function in its package, so a tenant's slow server holds the transaction's locks and connection. Ask between passes with `providerCall`. | Holding a server's row across provider calls, after #157 |
| `jsontag` | A struct field read from JSON, directly or through a package's own HTTP response helper, has no json tag naming its member. `encoding/json/v2` matches names exactly, so the field would stay zero with no error. | The JSON v2 shadow decoder, before #151 |

These catch the shapes the defects took, so a clean run does not rule a defect out.
`dberror` looks only at `err != nil` conditions, alone or as `||` terms, on an error set in
the same block. `jsontag` follows one level of helper, in the same package, that handles an
`*http.Response`. `txnetwork` follows direct calls within one package, so it misses a method
value and a call through an interface or a function value, which includes the fetch of a
`withProviderCalls` nested in a transaction. A transaction in a struct field, a `db.DBTX`
parameter and a pinned connection don't count as held. It treats a goroutine or a function
literal passed elsewhere that never names the transaction as running after it. A transaction
counts as held until the function returns, even past its own block, so a call after a commit
belongs in the caller. It does not cover Stripe, which still has these calls under the
workspace lock.

There is no suppression comment: each failure has a direct fix, and an analyzer
that needs exceptions should change or go. Add one only for a defect that recurs,
that it can detect without false alarms, and whose failure fits in a sentence.
Test it with `analysistest` against the shape of the original defect.

## Pin alignment

`make check-ci` runs `scripts/pin-alignment.mjs`, which reads the workspace file,
manifests, lockfile, workflows, local actions and Dockerfile without installing anything
and fails when pins that must move together differ. [Dependency pins](frontend-development.md#dependency-pins)
lists them.

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

Two things make a browser click or wait fail on a busy runner without anything being
wrong. If the page reflows during a click, its press and release can land on different
elements. The browser then sends the click to their common ancestor, and Playwright still
reports success. Wait for a section's parts to load before clicking below them, as
`openEnvironmentSection` does. A story's play starts at its first commit, so its waits
also span page loading; `.storybook/preview.tsx` gives them five seconds, and a wait that
has to beat a poll sets a shorter limit itself.

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

A separate step fetches the Go toolchain and modules before any test step, making
up to three attempts, so a briefly dropped module download does not fail the
check. Test steps are never retried.

Only `GOCACHE` and `GOMODCACHE` are forwarded through `sudo` to the disposable
native test commands. Runtime state, Docker storage, databases and credentials
are outside the cache. A cache miss still runs the complete
suite and builds the missing dependencies.

## Host downloads

Each Host job downloads pinned tools from GitHub releases, `download.docker.com` and
`go.dev`. Each download repeats for at least four minutes before the job fails, because
four attempts in seven seconds failed Host jobs in bursts of up to ten minutes on
5 November 2026, while the same step on the same architecture passed on other runners
in the same minutes.

- A file that fails its checksum stops the job. Only a failed download repeats.
- Every failure names the URL. The fixture scripts in `experiments/durable-execution/host`
  and `backup-tools.sh` use curl's retries, so a 404 such as a mistyped pin also takes
  about four minutes to report. The production bootstrap reports one at once.
- `build-tools.sh` installs the checksum-pinned `mise` where Railpack looks for it, as the
  production agent does, so `TestBuildArtifacts` does not download it during the test. The
  test fails if the file is missing.

These downloads are not cached between runs. `TestBootstrapOnDisposableHost` and the
production `mise` path must keep fetching from upstream to prove a fresh install works,
so a cache could not remove the wait for an outage. It would save under 30 seconds per
lane, and a pinned file removed upstream would go unnoticed until the cache expired.
An outage longer than the retries still fails the job, with the URL in its log.

`prepare-bootstrap-test.sh` replaces the runner image's `mirror+file` apt source with its
first two mirrors. With that source, `apt-get update` hangs without output when the first
mirror drops packets; with the mirrors listed directly it warns and uses the other. The bootstrap
limits its own `apt-get update` to 5 minutes for any source that hangs.

## Image downloads

`make verify-image` fetches these, and each was measured against a connection that resets:

| Fetch | From | Its own retry |
| --- | --- | --- |
| The three `FROM` images | Docker Hub | None for the metadata request: one reset stops the build |
| pnpm, in `scripts/install-pnpm.mjs` | npm registry | At least four minutes; a hash failure stops the build |
| `pnpm install --frozen-lockfile` | npm registry | About three and a half minutes |
| Go modules, in `pnpm app:build` | `proxy.golang.org` | None: one reset stops `go` |
| PostgreSQL, for the smoke test | Docker Hub | None for the manifest request: one reset stops the pull |

The script makes up to three attempts at the whole `docker build`, and repeats the
PostgreSQL pull as in [Image pulls](#image-pulls) within a 5-minute limit. One loop around the build covers the fetches without a retry of their own,
whichever `FROM` or `RUN` they are in, including any added later. A second attempt reuses
the completed layers, and the job has already built these sources. The attempts share the
build's 10-minute limit, and a build that reaches it stops, so a Go or registry connection
that stalls with its peer alive still fails the check; none has been seen in CI.

The loop also repeats what is not a fetch, such as a compile error, because these tools
exit alike for all of them. It reads the build's error output and stops on a failed
integrity check. Every attempt still enforces the digests, lockfile integrity and `go.sum`,
so an image that passes was verified, and each repeat leaves a warning on the run page to
diagnose. This differs from the Host and OSV Scanner downloads, which are single files
whose hash the script checks itself.

`.github/actions/install-pnpm` does not apply: it runs on the runner, and around
`make verify-image` it would repeat the smoke test, which is never retried. The smoke
test's HTTPS probe makes its own attempts.

## Image pulls

One reset on a registry's manifest request stops `docker pull` and an implicit pull in
`docker run`; the daemon's own retries cover only some layer downloads. A reset on
`mcr.microsoft.com` failed a required browser job on 6 November 2026 in exactly this way.
So every job that needs a pinned image pulls it first through
`.github/actions/pull-images`, which shares `pullImages` in `scripts/fetch-attempts.mjs`
with the image smoke test's PostgreSQL pull.

- Each image repeats for at least four minutes. The failure names it, and an image without
  a digest is refused.
- Bytes that fail their digest or a longer layer stop at once. These were measured with a
  registry serving changed manifests, configs and layers, and the wording differs by image
  store. Everything else repeats, because a reset can read as anything: on the containerd
  store, one on a pinned manifest reports `not found` and a short layer a short read. So a
  pin removed upstream also takes four minutes to report.
- The action runs on the runner's own Node, because Host jobs set up no Node of their own.

Not covered: Canvas and Workshop name their image in `container:`, which the runner pulls
with its own short retries before any step can run. Pulls made during a test, such as
BuildKit's in `TestNativePipeline`, are not retried either. A pull that stalls with its
peer alive uses up the job's limit, or 5 minutes in the smoke test.

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
