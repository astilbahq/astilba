# Continuous integration

Eight independent workflows run on pull requests, pushes to `main` and manual
dispatches. Each has its own workflow/ref concurrency group and can be retried
without waiting for another workflow's native tests. No workflow uses an event
path filter, so each required check is always reported.

| Workflow                   | Required check                   | Verification                                                                                                                      |
| -------------------------- | -------------------------------- | --------------------------------------------------------------------------------------------------------------------------------- |
| `go.yml`                   | Go verification                  | Durable-execution fixtures, including race detection                                                                              |
| `git-providers.yml`        | Git provider verification        | GitLab/Gitea and source-preserving Compose edits                                                                                  |
| `ci.yml`                   | Native verification              | Selected amd64 and arm64 host suites: fresh bootstrap, Docker, builds, PostgreSQL restore, systemd agent, deployment and recovery |
| `cloudflare.yml`           | Cloudflare packaging             | Optional hosting adapter packaging                                                                                                |
| `canvas.yml`               | Canvas browser verification      | Type/lint/build, domain tests and Chromium/WebKit journeys                                                                        |
| `application.yml`          | Application verification         | `make verify-app-core`, image smoke test and corresponding-source archive                                                         |
| `application-browsers.yml` | Application browser verification | Every Chromium/WebKit dashboard journey against a real API/PostgreSQL fixture                                                     |
| `workshop.yml`             | Workshop verification            | Standalone Storybook scenarios and accessibility audits in both browsers                                                          |

Matrix workflows have a final `always()` gate. A failed or cancelled shard cannot
satisfy it. Single-job workflows use their workload's check directly. All action
and image pins, assertions, race detection and uncached test execution remain
required.

## Native selection

Main pushes and manual runs always test both native architectures. A PR can skip
native checks only when every changed file is in the frontend/docs allowlist in
`scripts/ci-native-plan.mjs`: dashboard source, theme, public assets, browser tests,
Storybook configuration, Panda configuration, Markdown under `docs/`, or the root
README. `AGENTS.md` and `REVIEW.md` changes always require native checks.

Runtime, contracts, package/lock files, CI, scripts, toolchain and unknown paths
require both architectures. The planner compares the checked-out PR merge to its
actual first parent, including deleted paths and both sides of renames. Missing
merge metadata or a Git error fails the planner and required gate. An empty diff
also selects the full suite. Native checks skip only with a successful planner
and an explicit frontend/docs-only result; a missing, failed or cancelled native
matrix is otherwise a failure. `make check-ci` exercises these selection contracts
using temporary repositories.

Native tests run only on their owned disposable Ubuntu runners. They never reuse
an existing deployment host or application runtime state.

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
required-status-checks endpoint, with strict up-to-date checking and all eight
unique contexts above.

Before merging the workflow change:

1. Verify all eight new checks succeeded on the current PR merge commit, including
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
