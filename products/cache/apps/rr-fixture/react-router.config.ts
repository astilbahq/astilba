import type { Config } from "@react-router/dev/config";

// Minimal RR-v8 framework config for the build-smoke fixture. SSR on (the Workers profile);
// no prerender, so loaders never run at build — `react-router build` only compiles.
export default {
  ssr: true,
} satisfies Config;
