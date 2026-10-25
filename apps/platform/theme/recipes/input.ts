import { defineRecipe } from "@pandacss/dev";

export const inputRecipe = defineRecipe({
  className: "input",
  jsx: ["Input", "Field", "OTPInput"],
  base: {
    inlineSize: "full",
    minInlineSize: "0",
    font: "inherit",
    color: "fg",
    backgroundColor: "surface",
    caretColor: "fg",
    borderColor:
      "var(--input-hover-border-color, token(colors.control.border))",
    borderWidth: "1px",
    borderStyle: "solid",
    borderRadius: "control",
    paddingInline: "3",
    paddingBlock: "2",
    outline: "none",
    transitionProperty: "border-color",
    transitionDuration: "button",
    transitionTimingFunction: "out",
    _placeholder: { color: "subtle", opacity: 1 },
    _hover: {
      _enabled: {
        '&:not([aria-invalid="true"])': {
          borderColor: "control.interactiveBorder",
        },
      },
    },
    _focus: {
      borderColor: "control.interactiveBorder",
      outlineWidth: "1px",
      outlineStyle: "solid",
      outlineColor: "control.interactiveBorder",
      outlineOffset: "0",
    },
    '&[aria-invalid="true"]': {
      borderColor: "error",
      _hover: { borderColor: "error" },
      _focus: { borderColor: "error", outlineColor: "error" },
    },
    _disabled: { opacity: 0.55, cursor: "not-allowed" },
    "@media (forced-colors: active)": {
      borderColor: "ButtonText",
      _focus: { borderColor: "Highlight", outlineColor: "Highlight" },
    },
  },
  variants: {
    size: {
      sm: { minBlockSize: "control.sm", fontSize: "sm" },
      md: { minBlockSize: "control.md", fontSize: "md" },
    },
  },
  defaultVariants: { size: "md" },
});
