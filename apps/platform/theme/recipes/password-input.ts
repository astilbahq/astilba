import { defineSlotRecipe } from "@pandacss/dev";

export const passwordInputRecipe = defineSlotRecipe({
  className: "password-input",
  slots: ["root", "input", "toggle"],
  base: {
    root: {
      position: "relative",
      minInlineSize: "0",
      _hover: {
        "& > input:enabled": {
          "--input-hover-border-color":
            "token(colors.control.interactiveBorder)",
        },
      },
    },
    input: { paddingInlineEnd: "12" },
    toggle: {
      "--icon-size": "token(sizes.4)",
      position: "absolute",
      insetInlineEnd: "0.5",
      insetBlock: "0",
      marginBlock: "auto",
      blockSize: "control.sm",
    },
  },
});
