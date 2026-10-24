import {
  type Configuration,
  type Fixture,
  fixtureData,
  type Layout,
  parseConfiguration,
  parseLayout,
  storageKey,
} from "./domain";

export function loadFixture(fixture: Fixture): {
  config: Configuration;
  layout: Layout;
  hasLayout: boolean;
  warnings: string[];
} {
  const fallback = fixtureData(fixture);
  let config = fallback.config;
  let layout = fallback.layout;
  let hasLayout = false;
  const warnings: string[] = [];
  for (const kind of ["draft", "layout"] as const) {
    try {
      const raw = localStorage.getItem(storageKey(fixture, kind));
      if (raw !== null) {
        if (kind === "draft") config = parseConfiguration(raw);
        else {
          layout = parseLayout(raw);
          hasLayout = true;
        }
      }
    } catch {
      warnings.push(
        "Saved " +
          kind +
          " could not be read. The fixture default is shown; stored data stays unchanged until you save.",
      );
    }
  }
  return { config, layout, hasLayout, warnings };
}

export function saveFixture(
  fixture: Fixture,
  kind: "draft",
  value: Configuration,
): void;
export function saveFixture(
  fixture: Fixture,
  kind: "layout",
  value: Layout,
): void;
export function saveFixture(
  fixture: Fixture,
  kind: "draft" | "layout",
  value: Configuration | Layout,
): void {
  localStorage.setItem(storageKey(fixture, kind), JSON.stringify(value));
}
