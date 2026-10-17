/// <reference types="vite/client" />

// The RR framework build injects this virtual module (the compiled server build). Declared here —
// with ServerBuild's shape — so a standalone `tsc --noEmit` (which does NOT run `react-router
// typegen`) resolves the worker entry's dynamic import; the real `react-router build` supplies it.
declare module "virtual:react-router/server-build" {
  import type { ServerBuild } from "react-router";

  export const entry: ServerBuild["entry"];
  export const routes: ServerBuild["routes"];
  export const assets: ServerBuild["assets"];
  export const basename: ServerBuild["basename"];
  export const publicPath: ServerBuild["publicPath"];
  export const assetsBuildDirectory: ServerBuild["assetsBuildDirectory"];
  export const future: ServerBuild["future"];
  export const ssr: ServerBuild["ssr"];
  export const isSpaMode: ServerBuild["isSpaMode"];
  export const prerender: ServerBuild["prerender"];
  export const routeDiscovery: ServerBuild["routeDiscovery"];
}
