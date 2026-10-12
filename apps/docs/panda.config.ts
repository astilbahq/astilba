import { astilbaPreset } from "@astilba/tokens/panda";
import { defineConfig } from "@pandacss/dev";
import presetBase from "@pandacss/preset-base";

const themedColor = (dark: string, light: string) => ({
  value: { base: dark, _light: light },
});

export default defineConfig({
  preflight: false,
  presets: [presetBase, astilbaPreset],
  prefix: "astilba",
  hash: false,
  cssVarRoot: ":root",
  jsxFramework: "react",
  include: ["./src/**/*.{astro,js,jsx,ts,tsx}"],
  exclude: ["./dist/**", "./styled-system/**"],
  outdir: "styled-system",
  clean: true,
  validation: "error",
  layers: {
    reset: "astilba.reset",
    base: "astilba.base",
    tokens: "astilba.tokens",
    recipes: "astilba.recipes",
    utilities: "astilba.utilities",
  },
  conditions: {
    extend: {
      narrow: "@media (max-width: 30rem)",
      popupOpen: "&[data-popup-open]",
      panelOpen: "&[data-panel-open]",
      highlighted: "&[data-highlighted]",
      currentPage: '&[aria-current="page"]',
      currentTrue: '&[aria-current="true"]',
    },
  },
  theme: {
    extend: {
      breakpoints: {
        desktop: "50rem",
        wide: "72rem",
      },
      keyframes: {
        agentIconWave: {
          "0%, 100%": { transform: "translateY(0)" },
          "45%": { transform: "translateY(-3px)" },
        },
      },
      tokens: {
        durations: {
          agentIconWave: { value: "540ms" },
        },
      },
      semanticTokens: {
        colors: {
          callout: {
            note: {
              accent: themedColor("hsl(234, 100%, 60%)", "hsl(234, 90%, 60%)"),
              foreground: themedColor(
                "hsl(234, 100%, 87%)",
                "hsl(234, 80%, 30%)"
              ),
            },
            tip: {
              accent: themedColor("hsl(281, 82%, 63%)", "hsl(281, 90%, 60%)"),
              foreground: themedColor(
                "hsl(281, 82%, 89%)",
                "hsl(281, 90%, 30%)"
              ),
            },
            caution: {
              accent: themedColor("hsl(41, 82%, 63%)", "hsl(41, 90%, 60%)"),
              foreground: themedColor("hsl(41, 82%, 87%)", "hsl(41, 80%, 25%)"),
            },
            danger: {
              accent: themedColor("hsl(339, 82%, 63%)", "hsl(339, 90%, 60%)"),
              foreground: themedColor(
                "hsl(339, 82%, 87%)",
                "hsl(339, 80%, 30%)"
              ),
            },
          },
        },
      },
    },
  },
});
