import type { RouteConfig } from "@react-router/dev/routes";
import { index, route } from "@react-router/dev/routes";

// Three routes, one of them parameterised — which is the point. `react-router typegen` turns this
// file into the `Register` interface that makes `href("/scene/:sceneId", { sceneId })` a checked
// call rather than a string, and into the `+types` modules each route imports its own arg types
// from. Neither is decoration: delete the generated overlay and this app stops compiling.
//
// The third is a RESOURCE route — a loader and no component, so it answers with its own `Response`
// rather than a rendered page. It is the machine-readable half of the console: `scripts/
// demo-boot-witness.mjs` reads the reader's channel from it instead of scraping the table, so a
// change to the page's markup can never quietly blind that gate.
export default [
  index("routes/home.tsx"),
  route("scene/:sceneId", "routes/scene.tsx"),
  route("status.json", "routes/status.ts"),
] satisfies RouteConfig;
