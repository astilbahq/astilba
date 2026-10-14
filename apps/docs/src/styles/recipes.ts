import { css, cva } from "../../styled-system/css";

export const visuallyHidden = css({
  position: "absolute",
  inlineSize: "1px",
  blockSize: "1px",
  padding: 0,
  margin: "-1px",
  overflow: "hidden",
  clip: "rect(0, 0, 0, 0)",
  whiteSpace: "nowrap",
  border: 0,
});

export const copyIconSwap = css({
  position: "relative",
  display: "inline-grid",
  flex: "0 0 auto",
  placeItems: "center",
  "& [data-copy-icon]": {
    gridArea: "1 / 1",
    display: "inline-grid",
    placeItems: "center",
    opacity: 0,
    filter: "blur(var(--astilba-blurs-icon-swap))",
    transform: "scale(0.25)",
    transitionProperty: "opacity, filter, transform",
    transitionDuration: "iconSwap",
    transitionTimingFunction: "inOut",
    pointerEvents: "none",
  },
  '&[data-state="idle"] [data-copy-icon="idle"], &[data-state="copying"] [data-copy-icon="idle"], &[data-state="copied"] [data-copy-icon="copied"], &[data-state="error"] [data-copy-icon="error"]':
    {
      opacity: 1,
      filter: "blur(0)",
      transform: "scale(1)",
    },
  _reducedMotion: {
    "& [data-copy-icon]": {
      transition: "none",
    },
  },
});

export const ghostControl = cva({
  base: {
    position: "relative",
    display: "inline-flex",
    minInlineSize: "2rem",
    blockSize: "2rem",
    alignItems: "center",
    justifyContent: "center",
    gap: "0.4375rem",
    border: 0,
    borderRadius: 0,
    paddingInline: "0.625rem",
    background: "transparent",
    color: "ink.muted",
    cursor: "pointer",
    fontSize: "0.8125rem",
    fontWeight: "medium",
    lineHeight: 1,
    textDecoration: "none",
    whiteSpace: "nowrap",
    _hover: {
      background: "surface.hover",
      color: "ink.strong",
    },
    _focusVisible: {
      background: "surface.hover",
      color: "ink.strong",
      outlineColor: "signal",
      outlineOffset: "-0.125rem",
      outlineStyle: "solid",
      outlineWidth: "0.125rem",
    },
    _active: {
      background: "surface.pressed",
    },
    _disabled: {
      background: "transparent",
      color: "ink.muted",
      cursor: "default",
    },
  },
  variants: {
    appearance: {
      ghost: {},
      outline: {
        borderWidth: "1px",
        borderStyle: "solid",
        borderColor: "border.control",
      },
      primary: {
        borderWidth: "1px",
        borderStyle: "solid",
        borderColor: "surface.action.primary",
        background: "surface.action.primary",
        color: "ink.onPrimary",
        "&::selection, & *::selection": {
          background: "ink.onPrimary",
          color: "surface.action.primary",
        },
        _hover: {
          borderColor: "surface.action.primaryHover",
          background: "surface.action.primaryHover",
          color: "ink.onPrimary",
        },
        _focusVisible: {
          borderColor: "surface.action.primaryHover",
          background: "surface.action.primaryHover",
          color: "ink.onPrimary",
        },
        _active: {
          borderColor: "surface.action.primaryHover",
          background: "surface.action.primaryHover",
          color: "ink.onPrimary",
          opacity: 0.88,
        },
      },
    },
    iconOnly: {
      true: {
        inlineSize: "2rem",
        paddingInline: 0,
      },
    },
    size: {
      default: {},
      large: {
        blockSize: "2.5rem",
        paddingInline: "0.875rem",
      },
    },
  },
  defaultVariants: {
    appearance: "ghost",
    size: "default",
  },
});
