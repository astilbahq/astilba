import { cloudflare } from "@cloudflare/vite-plugin";
import { reactRouter } from "@react-router/dev/vite";
import { defineConfig } from "vite";

// REAL Vite 8 (8.1.4, carved through the Flag-1 pnpm overrides), not the vp-core alias the kernel
// builds under — the same dual-Vite arrangement the build-smoke fixture proves, with one more
// consumer and no second copy of anything.
export default defineConfig({
  plugins: [cloudflare({ viteEnvironment: { name: "ssr" } }), reactRouter()],
});
