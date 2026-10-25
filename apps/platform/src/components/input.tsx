import type { ComponentProps } from "react";
import { cx } from "../../styled-system/css";
import { input, type InputVariantProps } from "../../styled-system/recipes";

export type InputProps = Omit<ComponentProps<"input">, "size"> &
  InputVariantProps;

export function Input({ size, className, ...props }: InputProps) {
  return <BaseInput {...props} className={cx(input({ size }), className)} />;
}
import { Input as BaseInput } from "@base-ui/react/input";
