import { defineSlotRecipe } from "@pandacss/dev";

export const fieldRecipe = defineSlotRecipe({
  className: "field",
  jsx: ["Field", "Label"],
  slots: ["root", "header", "control", "action", "label", "error"],
  base: {
    root: { display: "grid", gap: "2", minInlineSize: "0" },
    control: { display: "grid", minInlineSize: "0" },
    label: { display: "grid", gap: "1.5", fontWeight: "medium" },
    error: { gridColumn: "1 / -1", color: "error", fontSize: "secondary" },
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
