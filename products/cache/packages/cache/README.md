# @astilba/cache

A portable TypeScript caching library — tag invalidation (soft/hard), classified stale-if-error resilience, and private-by-default scopes. Targets Node, Bun, and Cloudflare Workers.

> 🚧 **Pre-release.** The full public API and a suite of correctness invariants were pinned before implementation, so every behavior is anchored by a test that must stay green. The correctness kernel is complete; real Cloudflare Workers drivers are in progress.

```sh
pnpm add @astilba/cache
```

Documentation lives at [astilba.com/docs/cache](https://astilba.com/docs/cache/).

## License

MIT © Rees Morris
