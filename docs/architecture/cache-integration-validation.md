# Cache integration validation

The sanitized Cache snapshot is integrated into the local monorepo pilot. All available local source, browser, archive, consumer, and security checks passed. Releases, production deployments, repository visibility, and registry application package pins remain unchanged. GitHub-hosted acceptance is still required before cutover.

The implementation commit is `1b9958239db0b568dd448304db68474912fb4a88`. Cache was imported as files from approved snapshot `6f09d653a4f12e477ed9652c5432ff2d306a861d`, without private Git ancestry. Handbook remains excluded; the E31 pin remains an ancestor of its canonical main and has an equal scoped specification tree.

## Integration

Cache's five workspaces live under `products/cache`, bringing the root install to thirteen projects. The product has no independent active lock or workspace file. Its original standalone lock/policy and workflow copies are inactive provenance under `tooling/migration`.

Root, Env, and Cache use Vite+ 0.3.0 with the matching Vitest/coverage 4.1.11 family. The common formatter/linter family is Oxfmt 0.64.0, Oxlint 1.79.0, and oxlint-tsgolint 7.0.2001. Product-specific Ultracite rules remain intact; Docs/apex retain their own scoped tooling. Root Node 24.18.1, pnpm 11.17.0, dependency age 4320 minutes, build permissions, and the independent registry fixture's pins are unchanged.

Native formatter/linter adapters prevent nested discovery from evaluating neighboring app build configs. Cache's type-aware and compiler checks remain explicit. Two Env files changed only in formatting, with equal normalized executable source. The UI accessibility test's named AxeBuilder import is the same constructor as the previous default import; assertions and screenshots are unchanged.

Cache's scoped typecheck and Knip commands select only its workspaces. Integration type generation uses an explicit Wrangler 4.109.0 dependency in the Cache package, preventing fallback to root Wrangler. Both committed Worker type artifacts retain their original bytes.

The active Cache workflow and local census are equal. Boundary tests independently require all thirteen original lanes, the complete aggregate dependency list, and successful planted-job detection. Integration retains its separately adjudicated aggregate status; the local root verification runs its tests and blocks on failures. The scheduled mutation floor remains 55.

## Results

| Verification | Result |
| --- | --- |
| Root frozen install | Passed across 13 workspaces |
| Root `pnpm verify` | Passed with UI, Env, Docs/apex, and Cache gates |
| UI minimum runtime | Node 22.18.0 base verification passed |
| Browser contracts | UI 19, Docs 26, apex 12 passed; no baseline update |
| Cache correctness | 723 tests passed in 108 files; unit coverage lane 300 passed |
| Cache minimum runtime | 553 invariant/unit/conformance tests passed under Node 22.18.0 |
| Cache Bun | Six smoke tests passed under Bun 1.3.14 |
| Cache preservation | 210 executable fingerprints match; existing sacred files byte-identical |
| Autofixer canary | Rewrote the seeded equality assertion and left sacred files untouched |
| Packaging and builds | UI packages, Env, Cache import graph, both Cache apps, and web builds passed |
| Cache runtime witnesses | Three local Worker boot witnesses and typed-route deletion/regeneration controls passed |
| Generated types | Both Cache Worker type files match their baseline |
| Analysis and workflow checks | Both Knip modes, compiler/type-aware lint, boundary tests, NUL tripwire, Actionlint passed |
| OSV | Zero advisories across 1,817 package records: root 1,648 and registry fixture 169 |
| Private-corpus screening | Zero comment/document prose candidates in integrated Cache at the 12-word threshold |
| Tracked-source secret scan | Only the unchanged fake Cache test key; no suppression added |
| Fresh Env archive | 61 entries passed source, hash, entry, mode, and package verification |
| Archive package managers | npm 11.16.0, pnpm 11.10.0, Bun 1.3.14 passed |
| Archive runtimes | Workers admission, Bun/workerd portable equivalence passed |
| Node and TypeScript consumers | All eight original runtime/compiler combinations passed |
| Framework consumers | Generation, Vite 8.1.5, and all eight Next 15.5.22/16.2.12 modes passed |
| Registry adoption | Independent `verify:all` passed with unchanged package pins and lock |

The consumer-tested Env archive has SHA-256 `efcd1e7fc2e43ef885bae84978585fc41cf915f592e87e19fdc87237514ce856`, bound to implementation commit `1b9958239db0b568dd448304db68474912fb4a88`. A documentation-only follow-up requires a fresh source-bound manifest; identical archive bytes permit reuse of the consumer results, not reuse of the old source identity.

CodeRabbit completed two substantive integration reviews. Its independent-job-list finding was fixed and verified with the boundary tests. Its remaining major lockfile finding is contradicted by the reviewed, regenerated, committed lockfile and successful frozen installs. It is documented as a verified disposition, not relabeled or reported as a zero-finding review.

The source-export Gitleaks scan exits 1 on the known static fake key. A broader directory scan also found generated local Next encryption/preview material; those build outputs are ignored and absent from the tracked-source export. No secret values were included in the report or public source import.

Registry Next builds refresh a tracked ambient declaration. It was restored to its original bytes before completing source-bound consumer checks. The environment's Bun package shim lacked its postinstall artifact; consumer checks used the already-installed, pinned native Bun executable without enabling installation scripts or changing policy.

## Remaining acceptance

The destination repository has not been created and no code has been pushed. GitHub-hosted Linux/Windows, CodeQL, dependency review, Zizmor, the enforced two-CPU integration shape, and the full scheduled mutation ratchet remain hosted acceptance work. Existing release/deployment authorities and source repository visibility are unchanged. No production credentials are used by the active pilot workflows.
