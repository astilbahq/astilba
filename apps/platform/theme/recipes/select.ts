import { defineRecipe } from "@pandacss/dev";
import { inputRecipe } from "./input";

export const selectRecipe = defineRecipe({
  ...inputRecipe,
  className: "select",
  jsx: ["Select"],
  base: { ...inputRecipe.base, cursor: "pointer" },
});
