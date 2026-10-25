import pandacss from "@pandacss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite-plus";

export default defineConfig({
  plugins: [pandacss(), react()],
  server: {
    host: "127.0.0.1",
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
      ],
    },
  },
  preview: { host: "127.0.0.1", port: 54171, strictPort: true },
});
