import { CacheTimeoutError, HttpError } from "./errors";

// The classifier . `httpError(res)` yields a typed status error;
// `isRetriableHttp` is the default `staleIfError` predicate. Default retriable set
//: {408,425,429,500,502,503,504,520,521,522,523,524,525,526,527,530} + network
// errors + cache timeouts. `404/410/403` are facts, never masked. `Retry-After` is
// parsed, capped, and feeds `graceBackoff`.

/** The default retriable HTTP status set (Cloudflare 52x joins it). */
const RETRIABLE_STATUSES: ReadonlySet<number> = new Set([
  408, 425, 429, 500, 502, 503, 504, 520, 521, 522, 523, 524, 525, 526, 527,
  530,
]);

/** Wrap a non-ok `Response` into a typed `HttpError`. */
export function httpError(response: Response): HttpError {
  return new HttpError(response);
}

/**
 * The default `staleIfError` predicate — retriable: the default
 * retriable set, network errors, and cache-originated timeouts. `404/410/403` are
 * facts, never masked (never retriable) — a caller-abort `CacheTimeoutError` with
 * `source:'caller'` is likewise not evidence of origin health.
 */
export function isRetriableHttp(err: unknown): boolean {
  if (err instanceof HttpError) {
    return RETRIABLE_STATUSES.has(err.status);
  }
  if (err instanceof CacheTimeoutError) {
    return err.source === "cache";
  }
  // A network-level failure (fetch rejection, DNS, connection reset, etc.) has no
  // typed status — it is not a `404/410/403` fact, so it is retriable by default.
  return err instanceof TypeError;
}
