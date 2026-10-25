import type { ComponentProps } from "react";
import { cx } from "../../styled-system/css";
import { checkbox } from "../../styled-system/recipes";

export function Checkbox({
  className,
  ...props
}: Omit<ComponentProps<"input">, "type">) {
  return (
    <input {...props} type="checkbox" className={cx(checkbox(), className)} />
  );
}
