import { cloudflare } from "@cloudflare/vite-plugin";
import { reactRouter } from "@react-router/dev/vite";
import { defineConfig } from "vite";

// REAL Vite 8 (8.1.4, carved through the Flag-1 pnpm overrides) — NOT the vp-core alias the kernel
// builds under. `react-router build` running this config to success is the dual-Vite proof.
export default defineConfig({
  plugins: [cloudflare({ viteEnvironment: { name: "ssr" } }), reactRouter()],
});
