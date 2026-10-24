import pandacss from "@pandacss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite-plus";
export default defineConfig({
  plugins: [pandacss(), react()],
  server: {
    host: "127.0.0.1",
    port: 54182,
    strictPort: true,
    proxy: {
      "/api": { target: "http://127.0.0.1:54181" },
      "/hooks/git": { target: "http://127.0.0.1:54181" },
    },
  },
  preview: { host: "127.0.0.1", port: 54182, strictPort: true },
});
