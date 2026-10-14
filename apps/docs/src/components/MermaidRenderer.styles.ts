import { css, cx } from "../../styled-system/css";
import { copyIconSwap, ghostControl, visuallyHidden } from "../styles/recipes";

export const mermaidRendererStyles = {
  enhancedDiagram: css({
    position: "relative",
  }),
  actions: css({
    position: "absolute",
    zIndex: 1,
    insetBlockStart: "0.5rem",
    insetInlineEnd: "0.5rem",
    display: "flex",
    gap: "0.25rem",
    lineHeight: 1,
  }),
  action: cx(
    ghostControl({ iconOnly: true }),
    css({
      background: "surface.copy",
      transitionProperty: "opacity, background-color",
      _hover: {
        background: "surface.copy.hover",
        color: "ink.control",
      },
      _focusVisible: {
        background: "surface.copy.hover",
        color: "ink.control",
      },
      _active: {
        background: "surface.copy.pressed",
      },
    })
  ),
  actionIcon: css({
    display: "block",
    inlineSize: "1rem",
    blockSize: "1rem",
  }),
  iconSwap: copyIconSwap,
  visuallyHidden,
};
