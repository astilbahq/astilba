import { fileURLToPath } from "node:url";
import { bindRules, loadPandaProject } from "@pandacss/eslint-plugin";
import { hasPhysicalStyle } from "./theme/logical-properties.ts";

const { linter, project } = await loadPandaProject({
  cwd: fileURLToPath(new URL(".", import.meta.url)),
  configPath: fileURLToPath(new URL("./panda.config.ts", import.meta.url)),
});
const rules = bindRules(linter, project);
const aliases = project.compiler.spec().utilities.shorthands;

// Extend the official rule's limited property list using Panda's own style inspection.
rules["no-physical-properties"] = {
  meta: {
    type: "suggestion",
    schema: [],
    messages: {
      physical:
        'Use logical CSS instead of "{{property}}" and its physical values.',
    },
  },
  create(context) {
    return {
      Program() {
        const inspection = linter.inspectProject(
          project,
          context.filename,
          context.sourceCode.text,
        );
        for (const entry of inspection.styleEntries) {
          if (entry.kind !== "utility" && entry.kind !== "unknown") continue;
          const property =
            entry.canonicalName ?? aliases[entry.name] ?? entry.name;
          if (!hasPhysicalStyle(property, entry.sourceValue)) continue;
          context.report({
            messageId: "physical",
            data: { property },
            loc: {
              start: {
                line: entry.range.start.line,
                column: entry.range.start.column - 1,
              },
              end: {
                line: entry.range.end.line,
                column: entry.range.end.column - 1,
              },
            },
          });
        }
      },
    };
  },
};

export default { meta: { name: "@pandacss" }, rules };
