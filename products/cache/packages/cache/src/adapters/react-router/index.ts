// @astilba/cache/react-router — the PUBLIC React-Router-v8 adapter surface (phase 2).
//
// The `./react-router` subpath barrel: the `cacheMiddleware` factory (the born-private root
// middleware), the `cacheContext` key a loader reads the cache off (`context.get(cacheContext)`),
// the `currentRequest()` accessor a `getOrSet` call site passes as `request`, and the config/arg
// types (`CacheMiddlewareOptions`, `CacheMiddlewareArgs`). From also the two names the
// request-piggyback poll driver makes observable: `POLL_TICK_FAILED` (the swallowed-tick telemetry
// counter an app routes to its sink) and `TICK_MIN_INTERVAL_MS` (the staleness gate's floor — what a
// burst of requests actually costs). Also the two names the header algorithm makes
// observable: `L3_INELIGIBLE` (the single telemetry event a demoted response emits) and
// `L3_BUDGET_DEFAULT` (the header budget the middleware commits against). The two
// `AsyncLocalStorage` frames, `bindRenderCollector`, and `tickIfStale` itself are deep internals —
// never re-exported (the middleware IS their entry point). `react` + `react-router` are
// peerDependencies (optional), so `.`/`./cloudflare` consumers never need them.

export {
  cacheContext,
  cacheMiddleware,
  type CacheMiddlewareArgs,
  type CacheMiddlewareOptions,
  currentRequest,
  L3_BUDGET_DEFAULT,
  L3_INELIGIBLE,
} from "./middleware";
export { POLL_TICK_FAILED, TICK_MIN_INTERVAL_MS } from "./poll-driver";
