import { definePlugin } from "@pandacss/dev";

const physical =
  /^(?:top|right|bottom|left|(?:min|max)?(?:Width|Height)|width|height|(?:margin|padding|scrollMargin|scrollPadding)(?:Top|Right|Bottom|Left)|border(?:Top|Right|Bottom|Left)(?:Width|Style|Color)?|border(?:Top|Bottom)(?:Left|Right)Radius|overflow[XY]|overscrollBehavior[XY])$/;

export function hasPhysicalStyle(property: string, value: unknown): boolean {
  if (physical.test(property)) return true;
  if (!["textAlign", "float", "clear", "resize"].includes(property))
    return false;
  if (value && typeof value === "object") {
    return Object.values(value).some((item) =>
      hasPhysicalStyle(property, item),
    );
  }
  const normalized = String(value)
    .toLowerCase()
    .replace(/\s*!(?:important)?$/i, "")
    .replace(/^\[(.*)\]$/, "$1")
    .trim();
  return ["left", "right", "horizontal", "vertical"].includes(normalized);
}

// Theme definitions are outside Panda's source inspection; check them on config load.
export const logicalProperties = definePlugin({
  name: "logical-properties",
  hooks: {
    "config:resolved"({ config, utils }) {
      const aliases = new Map<string, string>();
      for (const [name, utility] of Object.entries(config.utilities ?? {})) {
        if (!utility || !("shorthand" in utility)) continue;
        const names = utility.shorthand;
        for (const alias of typeof names === "string"
          ? [names]
          : Array.isArray(names)
            ? names
            : []) {
          aliases.set(alias, name);
        }
      }
      for (const styles of [
        config.globalCss,
        config.theme?.recipes,
        config.theme?.slotRecipes,
      ]) {
        utils.traverse(styles, ({ key, path, value }) => {
          if (hasPhysicalStyle(aliases.get(key) ?? key, value)) {
            throw new Error(
              `Use a logical CSS property or value in theme styles: ${path}`,
            );
          }
        });
      }
    },
  },
});
