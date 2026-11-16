import { defineSlotRecipe } from "@pandacss/dev";

export const fieldRecipe = defineSlotRecipe({
  className: "field",
  jsx: ["Field", "Label"],
  slots: ["root", "header", "control", "action", "label", "error"],
  base: {
    root: { display: "grid", gap: "2", minInlineSize: "0" },
    control: { display: "grid", minInlineSize: "0" },
    label: { display: "grid", gap: "1.5", fontWeight: "medium" },
    error: {
      gridColumn: "1 / -1",
      color: "error",
      fontSize: "secondary",
      // Base UI keeps a resolved error in the page until the next animation frame, for an exit
      // animation it doesn't have, so what follows would move a frame after the field is valid.
      "&[data-ending-style]": { display: "none" },
    },
  },
  variants: {
    hasAction: {
      true: {
        root: {
          display: "flex",
          flexWrap: "wrap",
          alignItems: "baseline",
          columnGap: "3",
        },
        header: { maxInlineSize: "full" },
        control: { order: 2, flexBasis: "full" },
        action: {
          order: 1,
          maxInlineSize: "full",
          marginInlineStart: "auto",
        },
        error: { order: 3, flexBasis: "full" },
      },
    },
  },
});
