import { defineConfig } from "@pandacss/dev";
import base from "@pandacss/preset-base";
import panda from "@pandacss/preset-panda";
import { globalStyles } from "./theme/global-styles";
import { logicalProperties } from "./theme/logical-properties";
import { buttonRecipe } from "./theme/recipes/button";
import { checkboxRecipe } from "./theme/recipes/checkbox";
import { fieldRecipe } from "./theme/recipes/field";
import { inputRecipe } from "./theme/recipes/input";
import { linkRecipe } from "./theme/recipes/link";
import { menuRecipe } from "./theme/recipes/menu";
import { otpInputRecipe } from "./theme/recipes/otp-input";
import { passwordInputRecipe } from "./theme/recipes/password-input";
import { selectRecipe } from "./theme/recipes/select";
import { textareaRecipe } from "./theme/recipes/textarea";
import { tooltipRecipe } from "./theme/recipes/tooltip";
import { semanticTokens, tokens } from "./theme/tokens";

export default defineConfig({
  preflight: true,
  jsxFramework: "react",
  presets: [base, panda],
  include: ["./src/**/*.{ts,tsx}"],
  outdir: "styled-system",
  strictPropertyValues: true,
  strictTokens: true,
  plugins: [logicalProperties],
  theme: {
    extend: {
      tokens,
      semanticTokens,
      recipes: {
        button: buttonRecipe,
        input: inputRecipe,
        select: selectRecipe,
        textarea: textareaRecipe,
        checkbox: checkboxRecipe,
        link: linkRecipe,
      },
      slotRecipes: {
        field: fieldRecipe,
        menu: menuRecipe,
        otpInput: otpInputRecipe,
        passwordInput: passwordInputRecipe,
        tooltip: tooltipRecipe,
      },
    },
  },
  globalCss: globalStyles,
});
