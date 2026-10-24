import { defineSlotRecipe } from "@pandacss/dev";

export const tooltipRecipe = defineSlotRecipe({
  className: "tooltip",
  slots: ["positioner", "content"],
  base: {
    positioner: { zIndex: "tooltip" },
    content: {
      backgroundColor: "surface",
      color: "fg",
      paddingInline: "2.5",
      paddingBlock: "1.5",
      borderRadius: "full",
      boxShadow: "menu",
      fontFamily: "inherit",
      fontSize: "secondary",
      lineHeight: "body",
      maxInlineSize: "min(20rem, var(--available-width))",
      overflowWrap: "anywhere",
      transformOrigin: "var(--transform-origin)",
      transform: "scale(1)",
      opacity: 1,
      transitionProperty: "transform, opacity",
      transitionDuration: "tooltip.open",
      transitionTimingFunction: "out",
      "&[data-starting-style]": { transform: "scale(0.98)", opacity: 0 },
      "&[data-ending-style]": {
        transform: "scale(0.98)",
        opacity: 0,
        transitionDuration: "tooltip.close",
      },
      "&[data-closed]": { pointerEvents: "none" },
      "&[data-instant]": { transition: "none" },
      "@media (forced-colors: active)": {
        borderWidth: "1px",
        borderStyle: "solid",
        borderColor: "CanvasText",
      },
    },
  },
});
