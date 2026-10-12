import envConfig from "../../../packages/env/oxfmt.config.ts";

export default {
  ...envConfig,
  ignorePatterns: envConfig.ignorePatterns?.map((pattern) =>
    pattern.replace(/^examples\//u, "")
  ),
};
