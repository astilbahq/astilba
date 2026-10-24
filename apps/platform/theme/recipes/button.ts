import { defineRecipe } from "@pandacss/dev";

export const buttonRecipe = defineRecipe({
  className: "button",
  jsx: ["Button", "BusyButton", "IconButton"],
  base: {
    display: "inline-flex",
    alignItems: "center",
    justifyContent: "center",
    gap: "2",
    font: "inherit",
    fontWeight: "semibold",
    borderWidth: "1px",
    borderStyle: "solid",
    borderRadius: "control",
    paddingInline: "3",
    paddingBlock: "2",
    cursor: "pointer",
    transitionProperty: "background-color, border-color, color, outline-color",
    transitionDuration: "button",
    transitionTimingFunction: "out",
    _focusVisible: {
      outline: "2px solid var(--button-focus-color)",
      outlineOffset: "2px",
    },
    _disabled: { opacity: 0.55, cursor: "not-allowed" },
    "@media (forced-colors: active)": {
      borderColor: "ButtonText",
      _focusVisible: { outlineColor: "Highlight" },
    },
  },
  variants: {
    variant: {
      ghost: {
        "--button-focus-color": "token(colors.fg)",
        backgroundColor: "transparent",
        borderColor: "transparent",
        color: "fg",
        _hover: { _enabled: { backgroundColor: "action.ghostHover" } },
        "&[aria-expanded=true]": { backgroundColor: "action.ghostHover" },
      },
      primary: {
        "--button-focus-color": "token(colors.action.solid)",
        backgroundColor: "action.solid",
        borderColor: "action.solid",
        color: "action.contrast",
        _hover: {
          _enabled: {
            "--button-focus-color": "token(colors.action.hover)",
            backgroundColor: "action.hover",
            borderColor: "action.hover",
          },
        },
      },
      secondary: {
        "--button-focus-color": "token(colors.fg)",
        backgroundColor: "surface",
        borderColor: "control.border",
        color: "fg",
        _hover: { _enabled: { backgroundColor: "soft" } },
      },
      danger: {
        "--button-focus-color": "token(colors.destructive.solid)",
        backgroundColor: "destructive.solid",
        borderColor: "destructive.solid",
        color: "destructive.contrast",
        _hover: {
          _enabled: {
            "--button-focus-color": "token(colors.destructive.hover)",
            backgroundColor: "destructive.hover",
            borderColor: "destructive.hover",
          },
        },
      },
    },
    size: {
      sm: {
        "--button-size": "token(sizes.control.sm)",
        minBlockSize: "control.sm",
        fontSize: "sm",
      },
      md: {
        "--button-size": "token(sizes.control.md)",
        minBlockSize: "control.md",
        fontSize: "sm",
      },
    },
    shape: {
      rectangle: {},
      square: { inlineSize: "var(--button-size)", padding: "0", flexShrink: 0 },
    },
  },
  defaultVariants: { variant: "secondary", size: "md", shape: "rectangle" },
});
