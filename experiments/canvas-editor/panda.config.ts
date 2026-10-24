import { defineConfig } from "@pandacss/dev";
import base from "@pandacss/preset-base";
import panda from "@pandacss/preset-panda";

export default defineConfig({
  preflight: true,
  presets: [base, panda],
  include: ["./src/**/*.{ts,tsx}"],
  outdir: "styled-system",
  theme: {
    extend: {
      tokens: {
        colors: {
          ink: { value: "#18201e" },
          muted: { value: "#526158" },
          paper: { value: "#ffffff" },
          canvas: { value: "#f4f6f4" },
          line: { value: "#dce2dc" },
          control: { value: "#87938a" },
          accent: { value: "#6941c6" },
          tint: { value: "#f3effc" },
          danger: { value: "#a12332" },
        },
      },
    },
  },
  globalCss: {
    "html, body, #root": { minHeight: "100%", margin: "0" },
    body: {
      fontFamily: "system-ui, sans-serif",
      color: "ink",
      bg: "canvas",
      fontSize: "14px",
    },
    "button, input, select": { font: "inherit" },
    "button, summary, select": { cursor: "pointer" },
    "button:disabled": { cursor: "not-allowed", opacity: "0.55" },
    ":focus-visible": {
      outline: "2px solid token(colors.accent)",
      outlineOffset: "3px",
    },
    "@media (prefers-reduced-motion: reduce)": {
      "*, *::before, *::after": {
        animation: "none!",
        transition: "none!",
        scrollBehavior: "auto!",
      },
    },
    "@media (forced-colors: active)": {
      "button, input, select, .react-flow__node": {
        border: "1px solid ButtonText",
      },
    },
  },
});
