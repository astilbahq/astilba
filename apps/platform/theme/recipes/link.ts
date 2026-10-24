import { defineRecipe } from "@pandacss/dev";

export const linkRecipe = defineRecipe({
  className: "link",
  jsx: ["Link", "Anchor"],
  base: {
    color: "link.fg",
    cursor: "pointer",
    borderRadius: "xs",
    textUnderlineOffset: "4px",
    _focusVisible: {
      outline: "2px solid token(colors.link.focus)",
      outlineOffset: "3px",
    },
  },
  variants: {
    variant: {
      underline: { textDecoration: "underline" },
      plain: {
        textDecoration: "none",
        _hover: { textDecoration: "underline" },
      },
      navigation: {
        paddingBlock: "2",
        paddingInline: "2.5",
        borderRadius: "control",
        textDecoration: "none",
        color: "subtle",
        '&[data-status="active"]': {
          bg: "soft",
          color: "link.fg",
          fontWeight: "semibold",
        },
      },
    },
  },
  defaultVariants: { variant: "underline" },
});
