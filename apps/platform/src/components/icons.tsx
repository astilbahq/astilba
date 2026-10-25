import type { ReactNode } from "react";
import { css } from "../../styled-system/css";

export function Icon({ children }: { children: ReactNode }) {
  return (
    <svg
      className={css({
        inlineSize: "var(--icon-size, token(sizes.5))",
        blockSize: "var(--icon-size, token(sizes.5))",
        flexShrink: 0,
      })}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.75"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      focusable="false"
    >
      {children}
    </svg>
  );
}

export function CheckIcon() {
  return (
    <Icon>
      <path d="m5 12 4 4L19 6" />
    </Icon>
  );
}
