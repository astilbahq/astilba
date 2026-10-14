import { css, cx } from "../../styled-system/css";
import { copyIconSwap, ghostControl, visuallyHidden } from "../styles/recipes";

export const pageActionsStyles = {
  root: css({
    display: "flex",
    flexWrap: "wrap",
    alignItems: "center",
    marginBlockStart: "1.25rem",
    "@media print": {
      display: "none",
    },
  }),
  splitControl: css({
    position: "relative",
    display: "inline-flex",
    alignItems: "stretch",
    "&::after": {
      position: "absolute",
      inset: 0,
      borderWidth: "1px",
      borderStyle: "solid",
      borderColor: "border.control",
      content: '""',
      pointerEvents: "none",
    },
  }),
  copyControl: css({
    minInlineSize: "8.5rem",
    justifyContent: "flex-start",
    _disabled: {
      opacity: 1,
    },
    '&:disabled[data-copy-state="copying"]': {
      cursor: "default",
    },
  }),
  menuTrigger: cx(
    ghostControl({ iconOnly: true }),
    css({
      borderInlineStartWidth: "1px",
      borderInlineStartStyle: "solid",
      borderInlineStartColor: "border.control",
      "&[data-popup-open]": {
        background: "surface.hover",
        color: "ink.strong",
      },
    })
  ),
  controlIcon: css({
    display: "block",
    flex: "0 0 auto",
  }),
  iconSwap: copyIconSwap,
  textSwap: css({
    display: "inline-grid",
    minInlineSize: 0,
    textAlign: "start",
    "& [data-copy-label]": {
      gridArea: "1 / 1",
      opacity: 1,
      visibility: "visible",
    },
    '& [data-copy-label="copied"]': {
      opacity: 0,
      visibility: "hidden",
    },
    '&[data-state="copied"] [data-copy-label="idle"]': {
      opacity: 0,
      visibility: "hidden",
    },
    '&[data-state="copied"] [data-copy-label="copied"]': {
      opacity: 1,
      visibility: "visible",
    },
  }),
  chevron: css({
    flex: "0 0 auto",
    color: "ink.muted",
    transform: "rotate(0deg)",
    transformOrigin: "center",
    transitionProperty: "transform",
    transitionDuration: "control",
    transitionTimingFunction: "outExpo",
    "[data-popup-open] &": {
      transform: "rotate(180deg)",
    },
    "& path": {
      vectorEffect: "non-scaling-stroke",
    },
    _reducedMotion: {
      transitionDuration: "instant",
    },
  }),
  positioner: css({
    "--astilba-menu-z-index": "var(--sl-z-index-menu)",
    inlineSize: "15rem",
  }),
  menu: css({
    inlineSize: "100%",
    "& [data-page-actions-item]": {
      minBlockSize: "2.75rem",
      gap: "0.625rem",
      paddingBlock: "0.5rem",
      paddingInline: "0.75rem",
      fontSize: "0.875rem",
      lineHeight: 1.35,
      desktop: {
        minBlockSize: "2rem",
        paddingBlock: "0.35rem",
      },
    },
  }),
  menuItem: css({
    inlineSize: "100%",
  }),
  menuIcon: css({
    display: "inline-grid",
    flex: "0 0 auto",
    inlineSize: "1rem",
    blockSize: "1rem",
    placeItems: "center",
    color: "ink.muted",
  }),
  menuTrailing: css({
    color: "ink.faint",
  }),
  status: visuallyHidden,
};
