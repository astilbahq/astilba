import envConfig from "../../../packages/env/oxlint.config.ts";

export default {
  ...envConfig,
  overrides: envConfig.overrides?.map((override) => ({
    ...override,
    files: override.files?.map((pattern) =>
      pattern.replace(/^examples\//u, "")
    ),
  })),
};
