# Continuous integration

Seven independent workflows run on pull requests, pushes to `main` and manual
dispatches; the native, durable-execution, Git provider and canvas workflows
also run nightly. Each has its own concurrency group per workflow and ref (and,
for scheduled workflows, event) and can be retried without waiting for another
workflow's native tests. No workflow uses an event path filter: a planning job
selects the suites a pull request can affect, and each required check is a
final `always()` gate, so it is reported on every run.

| Workflow                   | Required check                   | Verification                                                                                                                      |
| -------------------------- | -------------------------------- | --------------------------------------------------------------------------------------------------------------------------------- |
| `go.yml`                   | Go verification                  | Durable-execution fixtures, including race detection                                                                              |
| `git-providers.yml`        | Git provider verification        | GitLab/Gitea and source-preserving Compose edits                                                                                  |
| `ci.yml`                   | Native verification              | Selected amd64 and arm64 host suites: fresh bootstrap, Docker, builds, PostgreSQL restore, systemd agent, deployment and recovery |
| `canvas.yml`               | Canvas browser verification      | Type/lint/build, domain tests and Chromium/WebKit journeys                                                                        |
| `application.yml`          | Application verification         | `make verify-app-build` and three parallel Go race lanes, image smoke test and corresponding-source archive                                                         |
| `application-browsers.yml` | Application browser verification | Every Chromium/WebKit dashboard journey against a real API/PostgreSQL fixture                                                     |
| `workshop.yml`             | Workshop verification            | Standalone Storybook scenarios and accessibility audits in both browsers                                                          |

Each gate requires a successful planner and then either a successful selected
workload or a skipped unselected one. A failed or cancelled job cannot satisfy
it. All action and image pins, assertions, race detection and uncached test
execution remain required.

## Suite selection

Every suite runs for main pushes, manual and scheduled runs. For a pull request,
the plan jobs run the base branch's `scripts/ci-plan.mjs`, so editing the planner
cannot select its own checks. A suite the base planner does not report, including
every suite when the base has no planner, is selected. Pull requests run their
own workflow files, so workflow changes still rely on review. It compares the checked-out merge to its actual first
parent, including deleted paths and both sides of renames, and skips a suite only
when every changed path is known not to affect it:

| Suite | Skipped when every changed path is |
| --- | --- |
| Native hosts | Markdown under `docs/` or the root README, dashboard source, theme, public assets, browser tests, Storybook or Panda configuration, or the server-only `auth`, `alerts`, `mailer`, `billing` and `automationclient` packages or the CLI |
| Application Go race lanes | documentation or dashboard paths above |
| Application and workshop browsers | documentation |
| Durable execution, Git providers, canvas | outside that experiment, its workflow and the Makefile (canvas also follows the root JavaScript manifests and lockfile) |

`AGENTS.md`, `REVIEW.md`, runtime, contracts, migrations, package/lock files, CI,
scripts, toolchain and unknown paths select the native suite. A change to the
planner or the native workflow selects every suite. Missing merge metadata or a
Git error in the planner fails every gate; an unreadable base planner or an empty
diff selects every suite.
Application checks, builds, the image smoke test and the corresponding-source
archive always run. `make check-ci` exercises these selection contracts using
temporary repositories.

Native tests run only on their owned disposable Ubuntu runners. They never reuse
an existing deployment host or application runtime state.

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
PR merge's actual first parent, the previous commit from a push event, or the
manual run's first parent. CI and explicit `--base <ref>` checks require available
Git history. Local Git checks otherwise use the common ancestor with
`origin/main`, falling back to local `main` only when that remote ref is absent.
Fetch the missing history or supply an available base when resolution fails;
the checker never fetches automatically.

A Git-free corresponding-source archive can check its embedded sizes and
allowances, with an explicit notice that historical comparison is unavailable.
Archives cannot regenerate allowances or satisfy a CI history check.

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

## Required-check migration

The previous branch protection requires `Verify`. This PR intentionally does not
emit that check: it remains blocked until an administrator migrates protection.
`.github/required-status-checks.json` is the exact replacement payload for the
required-status-checks endpoint, with strict up-to-date checking and all seven
unique contexts above.

Before merging the workflow change:

1. Verify all seven new checks succeeded on the current PR merge commit, including
   both native architectures and every browser shard. Update the branch if its
   base has changed and wait for those checks again.
2. Back up current branch protection, then atomically replace only its required
   status checks with the manifest. Preserve review, admin, linear-history and
   conversation-resolution settings. For this repository, the status-check update
   is `gh api --method PATCH repos/astilbahq/astilba/branches/main/protection/required_status_checks --input .github/required-status-checks.json`.
3. Read protection back and confirm strict checking and exactly the manifest's
   contexts before merging. Do not temporarily remove protection or retain a
   missing `Verify` requirement alongside the new contexts.

Opening this PR does not apply that administrator change. If the workflow change
is abandoned before merge, leave the existing `Verify` requirement in place. To
roll back after merge, restore the old workflow and its `Verify` requirement
together once that check succeeds on the rollback PR.

## Native Go cache

The host jobs cache downloaded Go modules and compilation outputs under the
runner's temporary directory. The key includes the operating system,
architecture, pinned bootstrap/toolchain configuration, module files and Go
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
