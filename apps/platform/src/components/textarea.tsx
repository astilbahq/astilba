import type { ComponentProps } from "react";
import { cx } from "../../styled-system/css";
import {
  textarea,
  type TextareaVariantProps,
} from "../../styled-system/recipes";

export type TextareaProps = ComponentProps<"textarea"> & TextareaVariantProps;

export function Textarea({
  variant,
  size,
  className,
  ...props
}: TextareaProps) {
  return (
    <textarea
      {...props}
      className={cx(textarea({ variant, size }), className)}
    />
  );
}
