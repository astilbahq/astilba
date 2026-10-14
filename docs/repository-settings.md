# Repository settings

`astilbahq/astilba` has an active [main ruleset](https://github.com/astilbahq/astilba/rules/25021109). GitHub settings are external state; administrators must verify the live rules before changing checks or repository policy.

- `main` requires a pull request, resolved review threads, linear history and squash merging. Deletion and force pushes are blocked. There are no bypass actors.
- All 51 verified per-PR GitHub Actions contexts are required, bound to the GitHub Actions app. Branches must be tested against current `main`. Cache `integration` is required independently of `CI / required`.
- CodeQL must supply results without analysis errors or high-or-higher security alerts. The scheduled/manual mutation ratchet retains its 55% floor; it is not a per-PR status requirement.
- Secret scanning, secret-scanning push protection, Dependabot alerts and private vulnerability reporting are enabled. The default workflow token is read-only and cannot approve PRs.
- Merge commits and rebase merges are disabled. Auto-merge and automatic branch deletion remain disabled; agents require explicit merge and branch-deletion authority.

The required contexts cover UI runtime/browser checks, Docs/apex verification, both registry-example platforms, every Env archive/runtime/Next consumer lane, every Cache correctness/integration/boot lane, actionlint, dependency review, OSV, PR title and Zizmor, plus both CodeQL language jobs. Renaming a job requires a coordinated ruleset update; do not remove a required check to conceal a failure.

Codex and CodeRabbit review policy remains risk- and availability-aware rather than a permanently required bot status. Dependency-bot PRs are not signed off merely by a green CI board. Assess supported peer ranges and shared toolchain coherence before merging.

Existing repositories, live npm publisher identities, protected production environments and deployment routes are unchanged. Env's workflow is installed but disabled by the absent `ENV_PUBLISHER_ENABLED` variable; UI remains an inactive template. The monorepo has no enabled publisher or production environment. See [Publisher cutover](publisher-cutover.md) and [development cutover](architecture/development-cutover.md).

The Env release controls were configured and read back on 14 October 2026. `npm-env` requires manual approval from ReesMorris, allows self-approval by the sole maintainer, forbids administrator bypass and permits only tags matching `env/v*`. [Immutable Env tags](https://github.com/astilbahq/astilba/rules/25070624) block updates and deletions with no bypass actors. A separate [creation ruleset](https://github.com/astilbahq/astilba/rules/25070627) restricts creation to ReesMorris; this creation exception cannot bypass immutability. Recheck all external settings before an approved release window; no test tag or publication established live OIDC acceptance.
