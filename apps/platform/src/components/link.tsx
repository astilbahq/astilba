import { createLink } from "@tanstack/react-router";
import type { ComponentProps } from "react";
import { cx } from "../../styled-system/css";
import { link, type LinkVariantProps } from "../../styled-system/recipes";

export type AnchorProps = ComponentProps<"a"> & LinkVariantProps;

export function Anchor({
  variant,
  children,
  className,
  ...props
}: AnchorProps) {
  return (
    <a {...props} className={cx(link({ variant }), className)}>
      {children}
    </a>
  );
}

export const Link = createLink(Anchor);
import { Button as BaseButton } from "@base-ui/react/button";

export function LinkButton({
  variant,
  className,
  type = "button",
  ...props
}: ComponentProps<"button"> & LinkVariantProps) {
  return (
    <BaseButton
      {...props}
      type={type}
      className={cx(link({ variant }), className)}
    />
  );
}
