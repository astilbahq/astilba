import { defineRecipe } from "@pandacss/dev";
import { inputRecipe } from "./input";

export const textareaRecipe = defineRecipe({
  ...inputRecipe,
  className: "textarea",
  jsx: ["Textarea"],
  base: { ...inputRecipe.base, resize: "block" },
});
