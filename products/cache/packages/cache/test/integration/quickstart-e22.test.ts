// the quickstart as an EXECUTABLE CI twin. The two construction blocks below are the
// documented hero form (i) + explicit composition (ii), CHARACTER-FOR-CHARACTER; only the surrounding
// imports/exercise are the test's own. This tooth is ALSO the import-path check: the imports
// (`@astilba/cache/cloudflare`, `@astilba/cache`) must resolve against the new `./cloudflare` exports
// map — if a line did not resolve, this file would fail to compile/run (a FINDING, never a silent
// rewrite). The two blocks must produce EQUIVALENT working caches: each serves a `getOrSet`, each is
// invalidated by `expire`.
//
// Lane law: the invalidation is observed via a STRONG read — a live registry check that
// catches the soft-purge deterministically, without depending on bus-frame / poller convergence
// timing. The verbatim requirement is on the CONSTRUCTION lines; the exercise is the test's to design.

// ─── Quickstart (i) hero — import line, character-for-character ───
import { createWorkersCache, Coordinator } from '@astilba/cache/cloudflare'
export { Coordinator }                    // the DO class your Worker registers

// ─── Quickstart (ii) explicit composition — import lines, character-for-character ───
import { createCache, memory } from '@astilba/cache'
import { cloudflareKV, doRegistry, doBus } from '@astilba/cache/cloudflare'

// ─── test scaffolding (not part of the verbatim blocks) ───
import type { Cache, Tag } from '@astilba/cache'
import { t } from '@astilba/cache'
import { env } from 'cloudflare:workers'
import { describe, expect, it } from 'vitest'
import type { ClientSocket } from '../../src/adapters/cloudflare/bus-client'

// `clock` / `rng` / `openBusSocket` are the illustrative (UNSIGNED) boundary plumbing, supplied here
// so the explicit-composition block is verbatim. In a TEST file (not `src/`), ambient time/rng need no
// `// PLATFORM:` note — rule 5 scopes the kernel core, and the grep-guard scans `src/` only.
const clock = { now: (): number => Date.now() }
const rng = { next: (): number => Math.random() }
async function openBusSocket(
  stub: DurableObjectStub<Coordinator>
): Promise<ClientSocket> {
  const response = await stub.fetch('https://coordinator/bus', {
    headers: { Upgrade: 'websocket' },
  })
  const socket = response.webSocket
  if (!socket) {
    throw new Error(`expected a 101 webSocket, got status ${response.status}`)
  }
  socket.accept()
  return socket
}

/**
 * Exercise a cache end-to-end: a plain `getOrSet` SERVES the first value, then `expire` INVALIDATES —
 * observed by a subsequent STRONG read that live-checks the bumped watermark and refills. Distinct
 * `bookId` per block partitions the shared `journals` registry / KV so the two blocks don't collide.
 */
async function serveThenExpire(cache: Cache, bookId: string): Promise<void> {
  const key = `book/${bookId}`
  const tag: Tag = t`book:${bookId}`
  let version = 0
  const factory = async (): Promise<string> => {
    version += 1
    return `v${version}`
  }
  // SERVES — a cold eventual fill (the hero's plain getOrSet).
  expect(await cache.getOrSet({ key, tags: [tag], factory })).toBe('v1')
  // INVALIDATES — expire the tag; a strong read live-checks the registry, finds the soft-purge, and
  // refills. (A plain eventual re-read can serve the value it already recorded known-clean; the
  // strong read re-confirms against the authoritative registry, so it catches the purge at once.)
  await cache.expire({ tag })
  expect(
    await cache.getOrSet({ key, tags: [tag], consistency: 'strong', factory })
  ).toBe('v2')
}

describe('§2/E22 quickstart is an executable twin (both blocks equivalent)', () => {
  it('(i) hero — the createWorkersCache five-liner serves + expire invalidates', async () => {
    // ┌── Quickstart (i), character-for-character ──────────────────────────────────────
    const cache = createWorkersCache({
      name: 'journals',                       // one name ⇒ DO address, registry id, key namespace
      kv: env.CACHE_KV,
      coordinator: env.COORDINATOR,
    })
    // └──────────────────────────────────────────────────────────────────────────
    await serveThenExpire(cache, 'hero')
  })

  it('(ii) explicit composition — the sugar unfolded produces the same working cache', async () => {
    // ┌── Quickstart (ii), character-for-character ─────────────────────────────────────
    const stub = env.COORDINATOR.get(env.COORDINATOR.idFromName('journals'))
    const cache = createCache({
      namespace: 'journals',
      clock, rng,                             // injected at the platform boundary
      l1: memory({ maxEntries: 512, maxBytes: 5_000_000 }),
      l2: cloudflareKV(env.CACHE_KV),
      registry: doRegistry(stub, 'journals'),          // (stub, regId) — never (namespace)
      bus: doBus(() => openBusSocket(stub), { regId: 'journals' }),  // (dial, options)
      defaults: { ttl: '5m', grace: '24h', consistency: 'eventual', unknownPolicy: 'registry-check' },
    })
    // └──────────────────────────────────────────────────────────────────────────
    await serveThenExpire(cache, 'explicit')
  })
})
