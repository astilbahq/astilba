import { defineSemanticTokens, defineTokens } from "@pandacss/dev";

export const tokens = defineTokens({
  spacing: { "18": { value: "72px" }, hairline: { value: "1px" } },
  fontWeights: { brand: { value: 650 } },
  lineHeights: {
    body: { value: 1.6 },
    heading: { value: 1.3 },
    editor: { value: 1.7 },
  },
  letterSpacings: {
    heading: { value: "-0.025em" },
    brand: { value: "-0.05em" },
  },
  fonts: {
    body: { value: "Inter, ui-sans-serif, system-ui, sans-serif" },
    ui: { value: "ui-sans-serif, system-ui, -apple-system, sans-serif" },
    mono: { value: "ui-monospace, SFMono-Regular, monospace" },
  },
  fontSizes: {
    heading: { value: "28px" },
    secondary: { value: "13px" },
    brand: { value: "22px" },
  },
  sizes: {
    hairline: { value: "1px" },
    viewport: { value: "100dvh" },
    control: { sm: { value: "40px" }, md: { value: "46px" } },
    auth: { content: { value: "360px" } },
    shell: { value: "1440px" },
    editor: { value: "360px" },
    log: { value: "280px" },
    chart: { value: "100px" },
    table: { value: "400px" },
  },
  radii: { control: { value: "6px" }, panel: { value: "10px" } },
  durations: {
    fast: { value: "120ms" },
    button: { value: "200ms" },
    theme: { value: "250ms" },
    menu: { open: { value: "250ms" }, close: { value: "150ms" } },
    tooltip: { open: { value: "150ms" }, close: { value: "50ms" } },
  },
  easings: {
    menu: { value: "cubic-bezier(0.22, 1, 0.36, 1)" },
    theme: { value: "ease-in-out" },
  },
  colors: {
    ink: { value: "#0d0d0d" },
    muted: { value: "#6c6c6c" },
    paper: { value: "#fdfdfd" },
    canvas: { value: "#fdfdfd" },
    line: { value: "#d9dfd9" },
    accent: { value: "#275e49" },
    tint: { value: "#e8efe8" },
    danger: { value: "#a72b35" },
  },
});

export const semanticTokens = defineSemanticTokens({
  shadows: {
    menu: {
      value: {
        base: "0 0 0 1px rgba(0, 0, 0, 0.06), 0 2px 6px 0 rgba(0, 0, 0, 0.05), 0 4px 42px 0 rgba(0, 0, 0, 0.06)",
        _dark:
          "0 4px 42px 0 rgba(0, 0, 0, 0.5), 0 2px 6px 0 rgba(0, 0, 0, 0.4), 0 0 0 1px rgba(255, 255, 255, 0.08), inset 0 0 0 1px rgba(255, 255, 255, 0.04), inset 0 1px 0 0 rgba(255, 255, 255, 0.03)",
      },
    },
  },
  colors: {
    menu: {
      itemHover: {
        value: { base: "#f4f4f5", _dark: "rgba(255, 255, 255, 0.04)" },
      },
    },
    fg: { value: { base: "{colors.ink}", _dark: "#ffffff" } },
    subtle: {
      value: { base: "{colors.muted}", _dark: "rgba(202, 202, 202, 0.7)" },
    },
    surface: { value: { base: "{colors.paper}", _dark: "#121212" } },
    page: { value: { base: "{colors.canvas}", _dark: "#121212" } },
    border: { value: { base: "{colors.line}", _dark: "#435448" } },
    brand: { value: { base: "{colors.accent}", _dark: "#9ad8b4" } },
    soft: { value: { base: "{colors.tint}", _dark: "#2b3c30" } },
    error: { value: { base: "#982332", _dark: "#ff7b7b" } },
    link: {
      fg: { value: "{colors.brand}" },
      focus: { value: "{colors.control.interactiveBorder}" },
    },
    control: {
      border: { value: { base: "#898989", _dark: "#858585" } },
      quietBorder: { value: { base: "#dedede", _dark: "#3a3a3a" } },
      interactiveBorder: { value: { base: "#8c8c8c", _dark: "#707070" } },
    },
    action: {
      ghostHover: {
        value: { base: "#f4f4f5", _dark: "rgba(255, 255, 255, 0.04)" },
      },
      solid: { value: { base: "#0d0d0d", _dark: "#ffffff" } },
      contrast: { value: { base: "#ffffff", _dark: "#0d0d0d" } },
      hover: { value: { base: "#2b2b2b", _dark: "#e8e8e8" } },
    },
    destructive: {
      solid: { value: { base: "#a72b35", _dark: "#ffadb5" } },
      contrast: { value: { base: "#ffffff", _dark: "#401219" } },
      hover: { value: { base: "#8c202c", _dark: "#ffc3c9" } },
    },
    auth: {
      fg: { value: "{colors.fg}" },
      subtle: { value: "{colors.subtle}" },
      surface: { value: "{colors.surface}" },
      page: { value: "{colors.page}" },
      border: { value: "{colors.control.quietBorder}" },
      controlBorder: { value: "{colors.control.border}" },
      action: { value: "{colors.action.solid}" },
      onAction: { value: "{colors.action.contrast}" },
      actionHover: { value: "{colors.action.hover}" },
      soft: { value: { base: "#edf1f5", _dark: "#263442" } },
      focus: { value: "{colors.control.interactiveBorder}" },
      selection: { value: { base: "#d6e4f3", _dark: "#334d67" } },
      danger: { value: { base: "#a72b35", _dark: "#ff7b7b" } },
    },
  },
});
