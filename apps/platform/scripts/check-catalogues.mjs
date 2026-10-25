import assert from "node:assert/strict";
import { readdir, readFile } from "node:fs/promises";

// The CLI checks absent keys but permits empty primary-language placeholders.
// English is our fallback, so every leaf must contain displayable copy.
function check(value, path) {
  if (typeof value === "string") {
    assert(value.trim(), `Empty translation: ${path}`);
  } else {
    assert(value && typeof value === "object", `Invalid translation: ${path}`);
    for (const [key, child] of Object.entries(value)) {
      check(child, `${path}.${key}`);
    }
  }
}

const directory = new URL("../src/locales/en/", import.meta.url);
for (const file of await readdir(directory)) {
  if (file.endsWith(".json")) {
    check(JSON.parse(await readFile(new URL(file, directory), "utf8")), file);
  }
}
