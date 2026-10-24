# Continuous integration

`.github/workflows/ci.yml` runs every verification group on pull requests,
pushes to `main` and manual dispatches. The required `Verify` job succeeds only
when every group succeeds, including both native architectures and both
application browsers. There are no path filters or cache-hit conditions that
skip verification jobs.

| Group | Verification |
| --- | --- |
| Go | Durable-execution fixture, including race detection |
| Git | GitLab/Gitea workflows and source-preserving Compose edits |
| Host, amd64 and arm64 | Native Docker, builds, PostgreSQL restore, systemd agent, deployment and recovery |
| Cloudflare | Optional hosting adapter packaging |
| Canvas | Type/lint/build checks, domain tests and Chromium/WebKit journeys |
| Application | `make verify-app-core`, image smoke test and corresponding-source archive |
| Application browser | All Chromium and WebKit dashboard journeys against a real API/PostgreSQL fixture |

`make verify-app` remains the complete local application check. The CI-only
split runs its core checks and browser journeys on separate runners. Both
browsers retain the original single worker and shared fixture, so tests still
run serially within the browser job.
The core target explicitly builds dashboard assets before Worker packaging.
No application server, database, ports or build outputs are shared between the
core and browser jobs. The browser fixture still builds the dashboard and
embedded Go server from the current checkout. Keeping both browsers in one job
avoids repeating their fixture setup while the longer native host suites run.

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
