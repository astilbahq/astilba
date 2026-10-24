import { defineConfig } from "@pandacss/dev";
import base from "@pandacss/preset-base";
import panda from "@pandacss/preset-panda";
import { globalStyles } from "./theme/global-styles";
import { semanticTokens, tokens } from "./theme/tokens";

export default defineConfig({
  preflight: true,
  presets: [base, panda],
  include: ["./src/**/*.{ts,tsx}"],
  outdir: "styled-system",
  strictPropertyValues: true,
  theme: { extend: { tokens, semanticTokens } },
  globalCss: globalStyles,
});
