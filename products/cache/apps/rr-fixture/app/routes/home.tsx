import type { LoaderFunctionArgs } from "react-router";

import { cacheContext, currentRequest } from "@astilba/cache/react-router";

// A loader that exercises the FULL published adapter surface end-to-end (compiled by the build):
// read the cache off RR context, and pass the ALS-carried principal `RequestContext` straight into
// `getOrSet`'s `request` — the mechanism the kernel derives `usr:<hash>` scope from.
export async function loader({ context }: LoaderFunctionArgs) {
  const cache = context.get(cacheContext);
  const greeting = await cache.getOrSet({
    key: "home:greeting",
    request: currentRequest(),
    factory: async () => "hello from the @astilba/cache RR fixture",
  });
  return { greeting };
}

export default function Home() {
  return <h1>@astilba/cache — React Router v8 build-smoke fixture</h1>;
}
