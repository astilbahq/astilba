---
name: upgrade-dependencies
description: >-
  Routine dependency upgrades for this repository: the pnpm workspace and every Go
  module, behind the 24-hour release-age gate, with release-note and supply-chain
  review, OSV-Scanner and govulncheck, peer review and one pull request per coherent
  group. Use when asked to update, bump or refresh dependencies, to work through
  open Dependabot pull requests, or when a scheduled dependency run starts.
---

# Upgrade dependencies

Covers `pnpm-workspace.yaml` with its packages, and the Go modules found by `git ls-files '*go.mod'`. Docker base images and GitHub Actions are left to Dependabot; Go and Node toolchains are reported, not bumped (see [Toolchains](#toolchains)).

## Rules

1. **Nothing younger than 24 hours.** pnpm enforces `minimumReleaseAge: 1440` in `pnpm-workspace.yaml`. Never lower it, never bypass it per command, and add a `minimumReleaseAgeExclude` entry only for a security fix you name in the PR, scoped to `name@version` and removed in the next run. Go has no such setting: `node scripts/go-release-age.mjs origin/main` applies the same gate and must pass.
2. **Read before you land.** Every version moved gets its changelog or release notes read across the whole range, not just the newest entry. A held-back or dropped bump is a good outcome; a bump nobody read is not.
3. **One pull request per coherent group** ([Groups](#groups)). Majors never ride in a routine group.
4. **Exact pins.** `saveExact` is on; Go requires are exact by nature. Keep lockfiles and `go.sum` in the same commit as the manifests.
5. **No global CLIs and no install scripts by default.** Use the repository's pnpm (`packageManager`), Go and `fnm exec --using=<engines.node> --` for Node. A new `allowBuilds` entry is a decision for the PR description, not a default.
6. **Peer review before each pull request.** Run the `pd-iterative-peer-review` skill, as `AGENTS.md` requires. Do not merge or enable auto-merge unless whoever started you said to.

If other sessions share the machine, give pnpm a store of your own (`--store-dir`) and use a disposable Docker daemon of your own for `make verify-app`.

## 1. Survey

Start from a fresh branch off `origin/main`.

1. **Open Dependabot pull requests:** `gh pr list --author app/dependabot`. Each one is either a security update (land its fix in this run, first) or a version update this run replaces. Dependabot closes its own pull request once main has the version, so do not close them by hand; say in your pull request which ones it supersedes.
2. **Known advisories:** `gh api 'repos/{owner}/{repo}/dependabot/alerts?state=open'` and `make check-vulnerabilities`. Anything either reports is the first group.
3. **pnpm candidates:** after `pnpm install --frozen-lockfile`, run `pnpm outdated -r --format json`. Its `latest` already skips versions younger than the gate, but it is the newest release of any major. For the newest release within the current major, read the package's versions from the registry (`curl -s https://registry.npmjs.org/<name>`, `versions` and `time`).
4. **Go candidates**, in each module directory:

   ```bash
   go list -m -u -f '{{if and .Update (not .Indirect)}}{{.Path}} {{.Version}} {{.Update.Version}} {{.Update.Time}}{{end}}' all
   # Tools declared with `tool` are marked indirect, so list their modules separately.
   go list -m -u -f '{{if .Update}}{{.Path}} {{.Version}} {{.Update.Version}} {{.Update.Time}}{{end}}' $(go list -f '{{.Module.Path}}' tool | sort -u)
   ```

   Other indirect modules move only when a direct bump or an advisory needs them. Dependabot does not see tools at all, so their security releases arrive only this way.

Write the candidate list, with each version's type (patch, minor, major), into your notes before changing anything.

## Groups

Each group below is its own pull request. Skip a group with nothing to move.

| Group | Contents |
| --- | --- |
| Security | Every version that fixes a reported advisory, any ecosystem, even a major. Also any release whose notes announce a security fix the scanners do not know yet (advisories can sit unreviewed in GitHub's database for days). |
| Go: platform | Patch and minor updates of `services/platform`'s direct requirements and tools. |
| Go: experiments | The same for every module under `experiments/`. |
| pnpm: routine | Patch and minor updates of packages outside the coupled families below. |
| pnpm: coupled | One pull request per family that moved, with the edits [Coupled pins](#coupled-pins) lists. |
| Majors | One pull request per major, and only when the person running you agreed to it. Otherwise list them in your report with what they would need. |

## Coupled pins

`make check-ci` runs `scripts/pin-alignment.mjs`, which fails on most of these. [Dependency pins](../../../docs/frontend-development.md#dependency-pins) explains why each exists.

- **pnpm:** catalog `pnpm` and `packageManager` in `package.json` move together; `scripts/install-pnpm.mjs` reads the hash from the lockfile. Once both have moved, install the new pnpm with that script (point `npm_config_prefix` at a directory of your own and put its `bin` first on `PATH`) and run `pnpm install --frozen-lockfile` with it, as CI will. pnpm 12 and later is a wrapper with optional dependencies that the install script refuses, so it is a major needing that script changed first. Update the version named in `AGENTS.md`, `README.md`, `services/platform/README.md`, `docs/platform-architecture.md` and `experiments/canvas-editor/README.md` (`git grep -F <old version>`).
- **Vite+:** catalog `vite-plus` and the `vite` alias, the `vitest@*` override and both `peerDependencyRules.allowedVersions` entries.
- **Playwright:** catalog `@playwright/test` with the image tag and digest in the Canvas, Workshop and Application browsers workflows. Take the digest from the registry for that tag.
- **Panda, Storybook:** every package in a family at one version. A Panda release can change generated CSS; compare `styled-system` output before and after.
- **i18next:** `i18next`, `react-i18next` and `i18next-cli` follow one major. A newer `i18next-cli` can depend on the next major of the other two, so hold it at the newest release whose dependencies match the app's.
- **`allowBuilds`:** each key names an exact version. When a package with install scripts moves, read what the new scripts do, then move the key and keep or change its decision.
- **`overrides`:** each has a reason in a comment. Remove one when the reason no longer holds; keep `vite@*` and `vitest@*`, which belong to Vite+.
- **`@types/node`:** its major follows `engines.node`.
- **Contract TypeScript:** `contracts/platform` aliases TypeScript 5 for `openapi-typescript`. Bump the alias by hand, and run `pnpm generate:check`.

## 2. Bump

Work on one branch per group, each from `origin/main`.

pnpm, naming each target version exactly. A range such as `<name>@^<current>` leaves exact pins in place and only re-resolves what is below them.

```bash
pnpm update -r <name>@<version> [<name>@<version> …]
```

A package with `catalog:` in its manifests moves by editing its line in the `catalog` of `pnpm-workspace.yaml` and running `pnpm install`; `pnpm update` can replace `catalog:` with a plain pin. Do not run `pnpm dedupe` inside a group: its churn hides the bump.

After a bump, `git grep -F <old version>` finds the documents that name it (READMEs, `AGENTS.md`, `docs/platform-architecture.md`). Update those that describe the current state; leave dated records of earlier runs.

Go, per module directory:

```bash
go get <module>@<version>    # a tool: go get -tool <module>@<version>
go mod tidy
```

Pick the newest version the gate allows: `go list -m -versions <module>` lists them, and `scripts/go-release-age.mjs` says which are too young. `go get` can raise indirect modules too, sometimes to pseudo-versions from the last day; when the gate rejects one, find which bump pulled it in (`go mod graph`) and take that bump's previous release or leave it for the next run.

## 3. Review what moved

For each group, against `origin/main`:

1. `node scripts/npm-release-changes.mjs origin/main` lists every package version the lockfile gained, transitive ones included, with any new install script, publisher, maintainer, lost provenance or deprecation. Explain or reject each flagged line in the pull request. A publisher changing to `GitHub Actions` usually means the project moved to npm trusted publishing; confirm it from the project's own release or commit, by a maintainer you already trust.
2. `node scripts/go-release-age.mjs origin/main` must pass. It also catches a version whose commit date was backdated, from when the Go proxy first saw it.
3. Read the release notes for the full range of each direct bump. Note breaking changes, behaviour this repository relies on, security fixes and anything that touches our configuration (`pnpm-workspace.yaml` settings, build tags, database drivers).
4. Check what the code actually uses where a change matters, with `git grep`, and say what you found.

## 4. Verify

- `make check-app` for every group.
- `make check-vulnerabilities` for every group. Scans can fail on an advisory published since main last passed; that belongs in the Security group.
- `make verify-app` when a group touches the dashboard's runtime, build or browser tooling (Vite+, Panda, Storybook, Playwright, React, router, i18next). Needs a disposable `PLAT_APP_DOCKER_HOST`.
- `make verify` or the experiment's own target when an experiment's module moved.

Fix what a bump breaks within the group when the fix is small and in scope. Otherwise drop that bump and report why.

## 5. Ship

1. Branch `chore/deps-<group>-<YYYYMMDD>`, Conventional Commit `chore(deps): …`.
2. Run `pd-iterative-peer-review` on the group, with the release notes and the two scripts' output as evidence.
3. Pull request into `main`. Its description lists each version moved with the reason it is safe, the flagged lines and what you made of them, the Dependabot pull requests it supersedes, the checks run, and anything held back by the gate or left for later.

## Toolchains

Go (`go` lines, Go images in the Dockerfiles, host bootstrap pins and `verify.sh` checks) and Node (`engines`, `.claude/hooks/node.sh`, `node-version` in workflows and the Node image) are pinned in many places. A routine run reports newer patch releases. A `govulncheck` finding in the standard library makes the Go patch the Security group; otherwise a toolchain bump is its own pull request when asked for. Find every pin with `git grep -F <old version>`.

## Report

End the run with one summary: groups opened (links), Dependabot pull requests each supersedes, versions held back by the gate with when they become eligible, majors and toolchain releases waiting for a decision, and any check you could not run.
