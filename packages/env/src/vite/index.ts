import type { Plugin } from "vite";

import { createBrowserBoundaryPlugin, locatePackageRoots } from "./boundary.ts";

/**
 * Returns a Vite plugin that rejects imports of private Env modules from the
 * browser graph. Add it to the application Vite plugin list.
 */
export const astilbaEnvBrowserBoundary = (): Plugin =>
  createBrowserBoundaryPlugin(locatePackageRoots(import.meta.url));
