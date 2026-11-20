import type { ComponentProps } from "react";
import { cx } from "../../styled-system/css";
import { button, type ButtonVariantProps } from "../../styled-system/recipes";

// focusableWhenDisabled keeps focus on a button that disables itself while it works.
export type ButtonProps = ComponentProps<"button"> &
  ButtonVariantProps &
  Pick<BaseButton.Props, "focusableWhenDisabled">;

export function Button({
  variant,
  size,
  shape,
  className,
  ...props
}: ButtonProps) {
  return (
    <BaseButton
      {...props}
      className={cx(button({ variant, size, shape }), className)}
    />
  );
}
import { Button as BaseButton } from "@base-ui/react/button";
