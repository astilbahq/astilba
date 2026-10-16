import { mkdtemp, readFile, readdir, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";
import { gzipSync } from "node:zlib";

import { build } from "vite";
import { describe, expect, it } from "vitest";

const distDirectory = path.resolve(import.meta.dirname, "../dist");
const readOutput = (file: string) =>
  readFile(path.resolve(distDirectory, file), "utf-8");

describe("published JavaScript output", () => {
  it("keeps the root entry as a small, runtime-free barrel", async () => {
    const rootEntry = await readOutput("index.js");

    expect(rootEntry).not.toContain("@base-ui/react");
    expect(rootEntry).not.toContain("styled-system");
    expect(gzipSync(rootEntry).byteLength).toBeLessThan(500);
  });

  it("marks only the component stylesheet as side-effectful", async () => {
    const manifest = JSON.parse(
      await readFile(
        path.resolve(import.meta.dirname, "../package.json"),
        "utf-8"
      )
    ) as { sideEffects?: unknown };

    expect(manifest.sideEffects).toEqual(["./dist/styles.css"]);
  });

  it("isolates Base UI primitives by component", async () => {
    const [
      button,
      collapsible,
      dialog,
      field,
      input,
      linkButton,
      menu,
      scrollArea,
      tabs,
      textarea,
      tooltip,
    ] = await Promise.all([
      readOutput("button.js"),
      readOutput("collapsible.js"),
      readOutput("dialog.js"),
      readOutput("field.js"),
      readOutput("input.js"),
      readOutput("link-button.js"),
      readOutput("menu.js"),
      readOutput("scroll-area.js"),
      readOutput("tabs.js"),
      readOutput("textarea.js"),
      readOutput("tooltip.js"),
    ]);

    expect(button).toContain("@base-ui/react/button");
    expect(button).not.toContain("@base-ui/react/tooltip");
    expect(collapsible).toContain("@base-ui/react/collapsible");
    expect(collapsible).not.toContain("@base-ui/react/button");
    expect(collapsible).not.toContain("@base-ui/react/menu");
    expect(collapsible).not.toContain("@base-ui/react/tooltip");
    expect(dialog).toContain("@base-ui/react/dialog");
    expect(dialog).not.toContain("@base-ui/react/button");
    expect(dialog).not.toContain("@base-ui/react/menu");
    expect(dialog).not.toContain("@base-ui/react/tooltip");
    expect(field).not.toContain("@base-ui/react");
    expect(input).not.toContain("@base-ui/react");
    expect(linkButton).not.toContain("@base-ui/react");
    expect(menu).toContain("@base-ui/react/menu");
    expect(menu).not.toContain("@base-ui/react/button");
    expect(menu).not.toContain("@base-ui/react/scroll-area");
    expect(menu).not.toContain("@base-ui/react/tooltip");
    expect(scrollArea).toContain("@base-ui/react/scroll-area");
    expect(scrollArea).toContain("@base-ui/react/csp-provider");
    expect(scrollArea).toContain("@base-ui/react/direction-provider");
    expect(scrollArea).not.toContain("@base-ui/react/button");
    expect(scrollArea).not.toContain("@base-ui/react/menu");
    expect(scrollArea).not.toContain("@base-ui/react/tooltip");
    expect(tabs).toContain("@base-ui/react/tabs");
    expect(tabs).not.toContain("@base-ui/react/button");
    expect(tabs).not.toContain("@base-ui/react/menu");
    expect(tabs).not.toContain("@base-ui/react/tooltip");
    expect(textarea).not.toContain("@base-ui/react");
    expect(tooltip).toContain("@base-ui/react/tooltip");
    expect(tooltip).not.toContain("@base-ui/react/button");
    expect(tooltip).not.toContain("@base-ui/react/scroll-area");
  });

  it("ships no Panda runtime in component modules", async () => {
    const files = await readdir(distDirectory);
    const javaScriptFiles = files.filter((file) => file.endsWith(".js"));
    const outputs = await Promise.all(javaScriptFiles.map(readOutput));

    expect(outputs.join("\n")).not.toContain("styled-system");
    expect(outputs.join("\n")).not.toContain("createRecipe");
  });

  it("tree-shakes a root LinkButton import to the static component", async () => {
    const fixtureDirectory = await mkdtemp(
      path.join(tmpdir(), "astilba-ui-consumer-")
    );
    const entry = path.resolve(fixtureDirectory, "entry.js");
    await writeFile(
      entry,
      `export { LinkButton } from ${JSON.stringify(
        pathToFileURL(path.resolve(distDirectory, "index.js")).href
      )};`,
      "utf-8"
    );

    try {
      const result = await build({
        build: {
          lib: {
            entry,
            formats: ["es"],
          },
          minify: false,
          rollupOptions: {
            external: ["react", "react/jsx-runtime", "react-dom"],
          },
          write: false,
        },
        configFile: false,
        logLevel: "silent",
        root: fixtureDirectory,
      });
      if (!Array.isArray(result) && !("output" in result)) {
        throw new TypeError("Expected a completed Vite build");
      }

      const buildOutputs = Array.isArray(result) ? result : [result];
      const outputs = buildOutputs.flatMap((output) => output.output);
      const javaScript = outputs
        .filter((output) => output.type === "chunk")
        .map((output) => output.code)
        .join("\n");

      expect(javaScript).toContain("LinkButton");
      expect(javaScript).not.toContain("@base-ui/react");
      expect(javaScript).not.toContain("styled-system");
      expect(javaScript).not.toContain("createRecipe");
      expect(javaScript).not.toContain("Tooltip");
      expect(gzipSync(javaScript).byteLength).toBeLessThan(1000);
    } finally {
      await rm(fixtureDirectory, { force: true, recursive: true });
    }
  });
});
