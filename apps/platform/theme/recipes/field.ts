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
          gridTemplateColumns: "minmax(0, 1fr) auto",
          alignItems: "baseline",
          columnGap: "3",
        },
        header: { gridColumn: "1", gridRow: "1" },
        control: { gridColumn: "1 / -1", gridRow: "2" },
        action: { gridColumn: "2", gridRow: "1" },
      },
    },
  },
});
