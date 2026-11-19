import type { ComponentProps } from "react";
import { cx } from "../../styled-system/css";
import { radio } from "../../styled-system/recipes";

export function Radio({
  className,
  ...props
}: Omit<ComponentProps<"input">, "type">) {
  return <input {...props} type="radio" className={cx(radio(), className)} />;
}
