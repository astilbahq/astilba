import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import ts from "typescript";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const manifest = JSON.parse(
  readFileSync(resolve(root, "SOURCE-SNAPSHOT.json"), "utf8")
);
const printer = ts.createPrinter({
  newLine: ts.NewLineKind.LineFeed,
  removeComments: true,
});

function normalized(name, text) {
  const file = ts.createSourceFile(
    name,
    text,
    ts.ScriptTarget.Latest,
    true,
    name.endsWith(".jsonc") ? ts.ScriptKind.JSON : undefined
  );
  assert.equal(file.parseDiagnostics.length, 0, `Invalid source: ${name}`);
  return printer.printFile(file);
}

function digest(value) {
  return createHash("sha256").update(value).digest("hex");
}

// Controls prove that comment edits pass while a changed literal fails.
const control = normalized("control.ts", "export const value = 1;");
assert.equal(
  normalized("control.ts", "// documentation\nexport const value = 1;"),
  control
);
assert.notEqual(normalized("control.ts", "export const value = 2;"), control);
assert.equal(ts.version, manifest.parserVersion);
assert.ok(manifest.files.length > 200);

for (const { file, executableSha256 } of manifest.files) {
  assert.ok(!file.includes("..") && !file.startsWith("/"), "Invalid manifest path");
  const current = normalized(file, readFileSync(resolve(root, file), "utf8"));
  assert.equal(digest(current), executableSha256, `Source changed: ${file}`);
}

console.log(`Preserved executable source in ${manifest.files.length} files.`);
