import { z } from "zod";
import { validateWith } from "./form-validation";

export function namedValueValidation(
  required: string,
  invalid: string,
  tooLong: string,
) {
  return validateWith(
    z
      .string()
      .trim()
      .min(1, required)
      .refine((value) => Array.from(value).length <= 80, { error: tooLong })
      .refine(
        (value) =>
          !["\r", "\n", "\0"].some((character) => value.includes(character)),
        { error: invalid },
      ),
  );
}
