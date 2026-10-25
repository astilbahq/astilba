import { defineGlobalStyles } from "@pandacss/dev";

export const globalStyles = defineGlobalStyles({
  "html,body,#root": { minBlockSize: "full", margin: "0" },
  body: {
    bg: "page",
    color: "fg",
    fontFamily: "body",
    fontSize: "sm",
    lineHeight: "body",
  },
  "*": { boxSizing: "border-box" },
  html: { colorScheme: "light" },
  "html.dark": { colorScheme: "dark" },
  "html[data-theme-transition], html[data-theme-transition] *, html[data-theme-transition] *::before, html[data-theme-transition] *::after, html[data-theme-transition] *::placeholder":
    {
      transition: "none!",
    },
  "html[data-theme-transition]::view-transition-group(root), html[data-theme-transition]::view-transition-old(root), html[data-theme-transition]::view-transition-new(root)":
    {
      animationDuration: "theme",
      animationTimingFunction: "theme",
    },
  ".skip-link": {
    "--colors-link-fg": "token(colors.fg)",
    position: "absolute",
    insetBlockStart: "3",
    transform: "translateY(-200%)",
    insetInlineStart: "3",
    zIndex: 100,
    bg: "surface",
    padding: "3",
  },
  ".skip-link:focus": { transform: "none" },
  summary: { cursor: "pointer" },
  "h1,h2,h3,p": { margin: "0" },
  h1: { fontSize: "heading", fontWeight: "semibold", letterSpacing: "tighter" },
  h2: { fontSize: "lg", fontWeight: "semibold", letterSpacing: "tight" },
  h3: { fontSize: "sm", fontWeight: "semibold" },
  small: { color: "subtle" },
  ":focus-visible": {
    outline: "2px solid token(colors.brand)",
    outlineOffset: "3px",
  },
  "@media (prefers-reduced-motion: reduce)": {
    "*,*::before,*::after,::view-transition-group(root),::view-transition-old(root),::view-transition-new(root)":
      {
        animation: "none!",
        transition: "none!",
        scrollBehavior: "auto!",
      },
  },
});
