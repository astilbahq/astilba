import { defineSlotRecipe } from "@pandacss/dev";

export const otpInputRecipe = defineSlotRecipe({
  className: "otp-input",
  slots: ["root", "input"],
  base: {
    root: { display: "flex", gap: "2", minInlineSize: "0" },
    input: {
      flex: "1",
      inlineSize: "0",
      paddingInline: "0",
      textAlign: "center",
      fontVariantNumeric: "tabular-nums",
    },
  },
});
