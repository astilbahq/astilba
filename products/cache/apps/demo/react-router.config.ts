import type { Config } from "@react-router/dev/config";

// RR-v8 framework config. SSR on (the Workers profile); no prerender — every loader on this app
// reads a live cache against live bindings, and a page rendered at build time would be a page that
// never touched the machinery the demo is about.
export default {
  ssr: true,
} satisfies Config;
