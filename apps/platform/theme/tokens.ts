import { defineSemanticTokens, defineTokens } from "@pandacss/dev";

export const tokens = defineTokens({
  colors: {
    ink: { value: "#222e2a" },
    muted: { value: "#5a6862" },
    paper: { value: "#ffffff" },
    canvas: { value: "#f5f6f2" },
    line: { value: "#d9dfd9" },
    accent: { value: "#275e49" },
    tint: { value: "#e8efe8" },
    danger: { value: "#a72b35" },
  },
});

export const semanticTokens = defineSemanticTokens({
  colors: {
    fg: { value: { base: "{colors.ink}", _dark: "#e8eee8" } },
    subtle: { value: { base: "{colors.muted}", _dark: "#b1beb5" } },
    surface: { value: { base: "{colors.paper}", _dark: "#1f2924" } },
    page: { value: { base: "{colors.canvas}", _dark: "#151e19" } },
    border: { value: { base: "{colors.line}", _dark: "#435448" } },
    brand: { value: { base: "{colors.accent}", _dark: "#9ad8b4" } },
    soft: { value: { base: "{colors.tint}", _dark: "#2b3c30" } },
  },
});
