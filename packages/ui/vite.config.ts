import { fileURLToPath } from "node:url";

import { defineConfig } from "vite";

export default defineConfig({
  build: {
    lib: {
      entry: fileURLToPath(new URL("src/index.ts", import.meta.url)),
      fileName: "index",
      formats: ["es"],
    },
    minify: false,
    rollupOptions: {
      external: [
        "@base-ui/react/button",
        "@base-ui/react/collapsible",
        "@base-ui/react/csp-provider",
        "@base-ui/react/dialog",
        "@base-ui/react/direction-provider",
        "@base-ui/react/menu",
        "@base-ui/react/scroll-area",
        "@base-ui/react/tabs",
        "@base-ui/react/tooltip",
        "react",
        "react/jsx-runtime",
        "react-dom",
      ],
      output: {
        entryFileNames: "[name].js",
        preserveModules: true,
        preserveModulesRoot: fileURLToPath(new URL("src", import.meta.url)),
      },
    },
    sourcemap: true,
  },
});
