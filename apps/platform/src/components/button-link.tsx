import { createLink } from "@tanstack/react-router";
import type { ComponentProps } from "react";
import { cx } from "../../styled-system/css";
import { button, type ButtonVariantProps } from "../../styled-system/recipes";

function ButtonAnchor({
  variant,
  size,
  shape,
  className,
  children,
  ...props
}: ComponentProps<"a"> & ButtonVariantProps) {
  return (
    <a {...props} className={cx(button({ variant, size, shape }), className)}>
      {children}
    </a>
  );
}

export const ButtonLink = createLink(ButtonAnchor);
