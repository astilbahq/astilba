import { defineSlotRecipe } from "@pandacss/dev";

export const tabsRecipe = defineSlotRecipe({
  className: "tabs",
  slots: ["root", "list", "tab", "panel"],
  base: {
    root: { minInlineSize: "0" },
    list: {
      display: { base: "grid", md: "flex" },
      gridTemplateColumns: "repeat(2, minmax(0, 1fr))",
      flexWrap: "wrap",
      gap: "1",
      borderBlockEndWidth: "1px",
      borderBlockEndStyle: "solid",
      borderBlockEndColor: "separator",
    },
    tab: {
      position: "relative",
      minBlockSize: "11",
      paddingInline: "3",
      paddingBlock: "3",
      backgroundColor: "transparent",
      color: "subtle",
      fontSize: "sm",
      fontWeight: "medium",
      cursor: "pointer",
      borderRadius: "control",
      transitionProperty: "background-color, color",
      transitionDuration: "button",
      transitionTimingFunction: "out",
      _hover: { backgroundColor: "action.ghostHover", color: "fg" },
      "&[data-active]": {
        color: "fg",
        _after: {
          content: '""',
          position: "absolute",
          insetInline: "3",
          insetBlockEnd: "0",
          blockSize: "0.5",
          backgroundColor: "fg",
        },
      },
      _focusVisible: {
        outline: "2px solid token(colors.control.interactiveBorder)",
        outlineOffset: "-2px",
      },
      _disabled: { opacity: 0.5, cursor: "not-allowed" },
      _motionReduce: { transitionProperty: "none" },
      "@media (forced-colors: active)": {
        "&[data-active]": { _after: { backgroundColor: "CanvasText" } },
      },
    },
    panel: {
      minInlineSize: "0",
      paddingBlockStart: "7",
      _focusVisible: {
        outline: "2px solid token(colors.control.interactiveBorder)",
        outlineOffset: "2px",
        borderRadius: "control",
      },
    },
  },
});
