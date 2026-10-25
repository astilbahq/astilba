import { defineRecipe } from "@pandacss/dev";

export const checkboxRecipe = defineRecipe({
  className: "checkbox",
  jsx: ["Checkbox"],
  base: {
    inlineSize: "4.5",
    blockSize: "4.5",
    flex: "0 0 auto",
    accentColor: "brand",
    cursor: "pointer",
    _disabled: { opacity: 0.55, cursor: "not-allowed" },
  },
});
