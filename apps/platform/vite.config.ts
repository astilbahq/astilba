import pandacss from "@pandacss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite-plus";
const apiPort = process.env.ASTILBA_DEV_API_PORT ?? "54181";
export default defineConfig({
  plugins: [pandacss(), react()],
  server: {
    host: "127.0.0.1",
    port: Number(process.env.ASTILBA_DEV_UI_PORT ?? "54182"),
    strictPort: true,
    fs: {
      // Preserve Vite's default deny list and exclude private local runtime state.
      deny: [
        ".env",
        ".env.*",
        "*.{crt,pem,key,p12,pfx,cer,der}",
        ".npmrc",
        ".yarnrc.yml",
        "**/.git/**",
        "**/.artifacts/**",
        "**/.impeccable/**",
      ],
    },
    proxy: {
      "/api": { target: `http://127.0.0.1:${apiPort}` },
      "/hooks/git": { target: `http://127.0.0.1:${apiPort}` },
    },
  },
  preview: { host: "127.0.0.1", port: 54182, strictPort: true },
});
