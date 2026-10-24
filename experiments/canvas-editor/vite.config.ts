import pandacss from "@pandacss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

export default defineConfig({
  plugins: [pandacss(), react()],
  server: { host: "127.0.0.1", strictPort: true },
  preview: { host: "127.0.0.1", port: 54171, strictPort: true },
});
