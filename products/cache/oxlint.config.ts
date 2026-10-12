import cache from "./vite.config.ts";

// The CLI still runs the type-aware and compiler lanes explicitly.
export default cache.lint;
