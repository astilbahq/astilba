import { defineGlobalStyles } from "@pandacss/dev";

export const globalStyles = defineGlobalStyles({
  "html,body,#root": { minHeight: "100%", margin: 0 },
  body: {
    bg: "page",
    color: "fg",
    fontFamily: "Inter, ui-sans-serif, system-ui, sans-serif",
    fontSize: "14px",
    lineHeight: 1.6,
  },
  "*": { boxSizing: "border-box" },
  html: { colorScheme: "light" },
  "html.dark": { colorScheme: "dark" },
  ".skip-link": {
    position: "absolute",
    top: "-100px",
    left: "12px",
    zIndex: 100,
    bg: "surface",
    padding: "12px",
  },
  ".skip-link:focus": { top: "12px" },
  "button,a,select,summary": { cursor: "pointer" },
  "button,input,select,textarea": {
    font: "inherit",
    border: "1px solid token(colors.border)",
    borderRadius: "6px",
    bg: "surface",
    color: "fg",
    padding: "9px 12px",
  },
  button: { fontWeight: 600 },
  "button:hover:not(:disabled)": { bg: "soft" },
  "button:disabled": { opacity: 0.55, cursor: "not-allowed" },
  "input,select,textarea": { width: "100%", minWidth: 0 },
  textarea: { resize: "vertical" },
  label: { display: "grid", gap: "6px", fontWeight: 500 },
  a: {
    color: "brand",
    textDecoration: "underline",
    textUnderlineOffset: "4px",
  },
  "h1,h2,h3,p": { margin: 0 },
  h1: { fontSize: "28px", fontWeight: 600, letterSpacing: "-0.04em" },
  h2: { fontSize: "18px", fontWeight: 600, letterSpacing: "-0.02em" },
  h3: { fontSize: "14px", fontWeight: 600 },
  small: { color: "subtle" },
  "[aria-invalid=true]": { borderColor: "danger" },
  ":focus-visible": {
    outline: "2px solid token(colors.brand)",
    outlineOffset: "3px",
  },
  "@media (prefers-reduced-motion: reduce)": {
    "*,*::before,*::after": {
      animation: "none!",
      transition: "none!",
      scrollBehavior: "auto!",
    },
  },
  "@media (forced-colors:active)": {
    "button,input,select,textarea": { border: "1px solid ButtonText" },
  },
  'label input[type="checkbox"]': {
    width: "18px",
    height: "18px",
    flex: "0 0 auto",
  },
});
