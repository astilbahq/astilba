# @astilba/cache

A portable TypeScript caching library — tag invalidation (soft/hard), classified stale-if-error resilience, and private-by-default scopes. Targets Node, Bun, and Cloudflare Workers.

> **Public alpha (0.1).** The correctness kernel and the Cloudflare Workers and React Router paths ship and run under the test suite, but elapsed TTL and grace periods are not enforced yet, a few methods still throw, and there is no production support commitment. Expect breaking changes before 1.0. Check [implementation status](https://astilba.com/docs/cache/api-status/) before relying on a feature.

```sh
pnpm add @astilba/cache
```

Documentation lives at [astilba.com/docs/cache](https://astilba.com/docs/cache/).

## License

MIT © Rees Morris
