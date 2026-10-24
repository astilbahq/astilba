import type { ComponentProps } from "react";
import { cx } from "../../styled-system/css";
import { select, type SelectVariantProps } from "../../styled-system/recipes";

export type SelectProps = Omit<ComponentProps<"select">, "size"> &
  SelectVariantProps;

export function Select({ variant, size, className, ...props }: SelectProps) {
  return (
    <select {...props} className={cx(select({ variant, size }), className)} />
  );
}
