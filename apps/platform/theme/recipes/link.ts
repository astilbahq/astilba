import { defineRecipe } from "@pandacss/dev";

export const linkRecipe = defineRecipe({
  className: "link",
  jsx: ["Link", "Anchor", "LinkButton"],
  base: {
    color: "link.fg",
    cursor: "pointer",
    borderRadius: "xs",
    textUnderlineOffset: "4px",
    _disabled: { opacity: 0.55, cursor: "not-allowed" },
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
      subtle: {
        color: "subtle",
        textDecoration: "none",
        _hover: { color: "link.fg", textDecoration: "underline" },
      },
      navigation: {
        paddingBlock: "2",
        paddingInline: "2.5",
        borderRadius: "control",
        textDecoration: "none",
        color: "fg",
        transitionProperty: "background-color",
        transitionDuration: "button",
        transitionTimingFunction: "out",
        _hover: { backgroundColor: "action.ghostHover" },
        '&[data-status="active"]': {
          bg: "navigation.active",
          color: "fg",
          fontWeight: "semibold",
        },
      },
    },
  },
  defaultVariants: { variant: "underline" },
});
