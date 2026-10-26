import type { z } from "zod";

export type FieldValidator = (value: unknown) => string | null;

export type FormValidation = {
  fields: Readonly<Record<string, FieldValidator>>;
  // Field-specific API error codes render inline instead of in the form alert.
  errorFields?: Readonly<Record<string, readonly string[]>>;
  shouldToastError?: (error: unknown) => boolean;
  message: (error: unknown, field?: string) => string;
};

// Synchronous schemas let Base UI block submission and focus the invalid field.
export function validateWith(schema: z.ZodType): FieldValidator {
  return (value) => {
    const result = schema.safeParse(value);
    return result.success ? null : result.error.issues[0].message;
  };
}
