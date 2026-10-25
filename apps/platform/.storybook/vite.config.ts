import appConfig from "../vite.config";

export default {
  ...appConfig,
  server: { ...appConfig.server, proxy: {}, host: "127.0.0.1" },
  preview: { host: "127.0.0.1", port: 54201, strictPort: true },
};
