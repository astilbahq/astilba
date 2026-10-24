import type { ComponentProps } from "react";
import { cx } from "../../styled-system/css";
import { field } from "../../styled-system/recipes";

export type LabelProps = ComponentProps<"label">;

export function Label({ htmlFor, children, className, ...props }: LabelProps) {
  return (
    <label
      {...props}
      htmlFor={htmlFor}
      className={cx(field().label, className)}
    >
      {children}
    </label>
  );
}
