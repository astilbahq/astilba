// @ts-check
/// <reference types="node" />

import { readFile } from "node:fs/promises";
import { resolve } from "node:path";
import { fileURLToPath } from "node:url";

import ts from "typescript";

const root = fileURLToPath(new URL("../", import.meta.url));

/** @param {string} message @returns {never} */
const fail = (message) => {
  throw new Error(message);
};

/** @param {unknown} value @returns {value is Record<string, unknown>} */
const isRecord = (value) =>
  typeof value === "object" && value !== null && !Array.isArray(value);

/** @param {readonly string[]} values */
const sorted = (values) => {
  /** @type {string[]} */
  const output = [];
  for (const value of values) {
    const index = output.findIndex((current) => value < current);
    if (index === -1) {
      output.push(value);
    } else {
      output.splice(index, 0, value);
    }
  }
  return output;
};

const expectedEntrypoints = Object.freeze([
  Object.freeze({
    exportPath: ".",
    id: "root",
    symbols: Object.freeze([
      "EnvironmentDefinition",
      "defineEnvironment",
      "env",
    ]),
  }),
  Object.freeze({
    exportPath: "./browser",
    id: "browser",
    symbols: Object.freeze([
      "BOOTSTRAP_PROTOCOL",
      "BootstrapFailure",
      "BootstrapFailureCode",
      "BrowserApplicationModule",
      "BrowserAudience",
      "BrowserProjection",
      "BrowserValues",
      "LoadBootstrapOptions",
      "MAXIMUM_BOOTSTRAP_BYTES",
      "ParseBootstrapOptions",
      "StartBrowserApplicationOptions",
      "ValidatedBootstrap",
      "loadBrowserBootstrap",
      "parseBrowserBootstrap",
      "startBrowserApplication",
    ]),
  }),
  Object.freeze({
    exportPath: "./runtime",
    id: "runtime",
    symbols: Object.freeze([
      "EnvironmentConfigurationError",
      "ProcessSource",
      "ProcessTargetDefinition",
      "ProcessTargetSchemas",
      "StandardSchemaResult",
      "StandardSchemaV1",
      "checkProcessTarget",
      "checkProcessTargetWithSchemas",
      "loadProcessTarget",
      "loadProcessTargetWithSchemas",
    ]),
  }),
  Object.freeze({
    exportPath: "./vite",
    id: "vite",
    symbols: Object.freeze(["astilbaEnvBrowserBoundary"]),
  }),
]);

const selectedAuthoringShapes = Object.freeze([
  Object.freeze({
    members: Object.freeze(["required"]),
    name: "RequiredOptions",
  }),
  Object.freeze({
    members: Object.freeze(["blank", "falseInput", "required", "trueInput"]),
    name: "BooleanOptions",
  }),
  Object.freeze({
    members: Object.freeze(["blank", "maximum", "minimum", "required"]),
    name: "IntegerOptions",
  }),
  Object.freeze({
    members: Object.freeze(["blank", "required"]),
    name: "JsonOptions",
  }),
  Object.freeze({
    members: Object.freeze([
      "maximumCodePoints",
      "minimumCodePoints",
      "required",
    ]),
    name: "StringOptions",
  }),
  Object.freeze({
    members: Object.freeze([
      "emptyItems",
      "maximumItemCodePoints",
      "maximumItems",
      "minimumItemCodePoints",
      "minimumItems",
      "required",
    ]),
    name: "StringListOptions",
  }),
  Object.freeze({
    members: Object.freeze([
      "blank",
      "maximumCodePoints",
      "minimumCodePoints",
      "normalise",
      "required",
    ]),
    name: "TextOptions",
  }),
  Object.freeze({
    members: Object.freeze([
      "blank",
      "maximumCodePoints",
      "minimumCodePoints",
      "required",
    ]),
    name: "SecretOptions",
  }),
  Object.freeze({
    members: Object.freeze([
      "input",
      "output",
      "required",
      "revision",
      "semantics",
    ]),
    name: "OpaqueOptions",
  }),
]);

const selectedPortableShapeMembers = Object.freeze([
  Object.freeze({ members: Object.freeze(["kind"]), name: "scalar" }),
  Object.freeze({
    members: Object.freeze(["kind", "maximum", "minimum"]),
    name: "safe-integer",
  }),
  Object.freeze({
    members: Object.freeze(["items", "kind", "maximumItems", "minimumItems"]),
    name: "array",
  }),
  Object.freeze({
    members: Object.freeze(["kind", "properties"]),
    name: "object",
  }),
  Object.freeze({
    members: Object.freeze(["name", "required", "shape"]),
    name: "object property",
  }),
]);

/** @param {ts.SourceFile} sourceFile @param {ts.Node} node */
const hasTSDoc = (sourceFile, node) =>
  ts
    .getLeadingCommentRanges(sourceFile.text, node.getFullStart())
    ?.some((range) =>
      sourceFile.text.slice(range.pos, range.end).startsWith("/**")
    ) ?? false;

/** @param {ts.TypeElement | ts.ClassElement} member */
const memberName = (member) => {
  if (ts.isCallSignatureDeclaration(member)) {
    return `call/${member.parameters.length}`;
  }
  if (
    !ts.isMethodSignature(member) &&
    !ts.isPropertyDeclaration(member) &&
    !ts.isPropertySignature(member)
  ) {
    return undefined;
  }
  if (ts.isComputedPropertyName(member.name)) {
    return undefined;
  }
  return ts.isStringLiteral(member.name)
    ? member.name.text
    : member.name.getText();
};

/** @param {ts.Node} node */
const nestedTypeLiterals = (node) => {
  /** @type {ts.TypeLiteralNode[]} */
  const literals = [];
  /** @param {ts.Node} current */
  const visit = (current) => {
    if (ts.isTypeLiteralNode(current)) {
      literals.push(current);
    }
    ts.forEachChild(current, visit);
  };
  visit(node);
  return literals;
};

/** @param {ts.Node} declaration */
const memberContainers = (declaration) => {
  if (ts.isClassDeclaration(declaration)) {
    return [declaration.members];
  }
  if (ts.isInterfaceDeclaration(declaration)) {
    return [
      declaration.members,
      ...nestedTypeLiterals(declaration).map((literal) => literal.members),
    ];
  }
  return nestedTypeLiterals(declaration).map((literal) => literal.members);
};

/** @param {ts.SourceFile} sourceFile @param {string} name */
const namedDeclaration = (sourceFile, name) => {
  for (const statement of sourceFile.statements) {
    if (
      (ts.isClassDeclaration(statement) ||
        ts.isFunctionDeclaration(statement) ||
        ts.isInterfaceDeclaration(statement) ||
        ts.isTypeAliasDeclaration(statement)) &&
      statement.name?.text === name
    ) {
      return statement;
    }
    if (!ts.isVariableStatement(statement)) {
      continue;
    }
    const declaration = statement.declarationList.declarations.find(
      (candidate) => candidate.name.getText(sourceFile) === name
    );
    if (declaration !== undefined) {
      return declaration;
    }
  }
  return undefined;
};

/**
 * @param {ts.SourceFile} sourceFile
 * @param {ts.Node} declaration
 * @param {readonly string[]} memberNames
 * @param {string} label
 * @param {string[]} failures
 * @param {boolean} [allMatches]
 */
const assertDocumentedMembers = (
  sourceFile,
  declaration,
  memberNames,
  label,
  failures,
  allMatches = false
) => {
  const expected = sorted(memberNames);
  const matching = memberContainers(declaration).filter(
    (members) =>
      JSON.stringify(
        sorted(members.map(memberName).filter((name) => name !== undefined))
      ) === JSON.stringify(expected)
  );
  if (matching.length === 0) {
    failures.push(`${label} has no exact emitted member shape.`);
    return;
  }
  for (const members of allMatches ? matching : matching.slice(0, 1)) {
    for (const member of members) {
      const name = memberName(member);
      if (name !== undefined && !hasTSDoc(sourceFile, member)) {
        failures.push(`${label}.${name} is missing TSDoc.`);
      }
    }
  }
};

/**
 * @param {ts.SourceFile} sourceFile
 * @param {string} name
 * @param {readonly string[]} members
 * @param {string[]} failures
 * @param {boolean} [allMatches]
 */
const assertNamedMembers = (
  sourceFile,
  name,
  members,
  failures,
  allMatches = false
) => {
  const declaration = namedDeclaration(sourceFile, name);
  if (declaration === undefined) {
    failures.push(`${name} is missing from emitted declarations.`);
    return;
  }
  assertDocumentedMembers(
    sourceFile,
    declaration,
    members,
    name,
    failures,
    allMatches
  );
};

/** @type {unknown} */
const parsedPackageJson = JSON.parse(
  await readFile(resolve(root, "package.json"), "utf-8")
);
if (!isRecord(parsedPackageJson)) {
  throw new TypeError("Package exports are invalid.");
}
const packageJson = parsedPackageJson;
const packageExports = packageJson.exports;
if (!isRecord(packageExports)) {
  throw new TypeError("Package exports are invalid.");
}
const actualExportPaths = sorted(Object.keys(packageExports));
const expectedExportPaths = sorted([
  ...new Set(expectedEntrypoints.map((entrypoint) => entrypoint.exportPath)),
]);
if (JSON.stringify(actualExportPaths) !== JSON.stringify(expectedExportPaths)) {
  fail(
    `Package export paths differ from the public TSDoc manifest: expected ${JSON.stringify(expectedExportPaths)}, received ${JSON.stringify(actualExportPaths)}.`
  );
}

/** @type {Map<string, string>} */
const declarationPaths = new Map();
for (const entrypoint of expectedEntrypoints) {
  const entry = packageExports[entrypoint.exportPath];
  if (!isRecord(entry) || typeof entry.types !== "string") {
    throw new TypeError(
      `Package export ${entrypoint.exportPath} has no declaration target.`
    );
  }
  declarationPaths.set(entrypoint.id, resolve(root, entry.types));
}

const program = ts.createProgram({
  options: {
    module: ts.ModuleKind.NodeNext,
    moduleResolution: ts.ModuleResolutionKind.NodeNext,
    skipLibCheck: false,
    target: ts.ScriptTarget.ES2024,
  },
  rootNames: [...declarationPaths.values()],
});
const diagnostics = ts.getPreEmitDiagnostics(program);
if (diagnostics.length > 0) {
  fail(
    `Emitted declarations do not type-check:\n${ts.formatDiagnosticsWithColorAndContext(
      diagnostics,
      {
        getCanonicalFileName: (path) => path,
        getCurrentDirectory: () => root,
        getNewLine: () => "\n",
      }
    )}`
  );
}
const checker = program.getTypeChecker();
const failures = [];
/** @type {Set<ts.Symbol>} */
const documentedSymbols = new Set();

for (const entrypoint of expectedEntrypoints) {
  const declarationPath = declarationPaths.get(entrypoint.id);
  if (declarationPath === undefined) {
    failures.push(`${entrypoint.id} has no resolved declaration target.`);
    continue;
  }
  const sourceFile = program.getSourceFile(declarationPath);
  const moduleSymbol = sourceFile && checker.getSymbolAtLocation(sourceFile);
  if (sourceFile === undefined || moduleSymbol === undefined) {
    failures.push(`${entrypoint.id} declaration target could not be loaded.`);
    continue;
  }
  const exports = checker.getExportsOfModule(moduleSymbol);
  const actual = sorted(exports.map((symbol) => symbol.name));
  const expected = sorted(entrypoint.symbols);
  if (JSON.stringify(actual) !== JSON.stringify(expected)) {
    failures.push(
      `${entrypoint.id} exports differ from the documented public manifest: expected ${JSON.stringify(expected)}, received ${JSON.stringify(actual)}.`
    );
  }
  for (const exported of exports) {
    const isAlias =
      exported.declarations?.some((declaration) =>
        ts.isExportSpecifier(declaration)
      ) ?? false;
    const symbol = isAlias ? checker.getAliasedSymbol(exported) : exported;
    if (documentedSymbols.has(symbol)) {
      continue;
    }
    documentedSymbols.add(symbol);
    const documentation = ts
      .displayPartsToString(symbol.getDocumentationComment(checker))
      .trim();
    if (documentation.length === 0) {
      failures.push(
        `${entrypoint.id} export ${exported.name} has no emitted TSDoc on its canonical declaration.`
      );
    }
  }
}

const authoringPath = resolve(root, "dist/authoring/environment.d.ts");
const authoring = program.getSourceFile(authoringPath);
if (authoring === undefined) {
  failures.push("Authoring declarations could not be loaded.");
} else {
  for (const shape of selectedAuthoringShapes) {
    const declaration = namedDeclaration(authoring, shape.name);
    if (declaration === undefined) {
      failures.push(
        `${shape.name} is missing from emitted authoring declarations.`
      );
      continue;
    }
    assertDocumentedMembers(
      authoring,
      declaration,
      shape.members,
      shape.name,
      failures
    );
  }
  const portableShape = namedDeclaration(authoring, "PortableShape");
  if (portableShape === undefined) {
    failures.push(
      "PortableShape is missing from emitted authoring declarations."
    );
  } else {
    for (const shape of selectedPortableShapeMembers) {
      assertDocumentedMembers(
        authoring,
        portableShape,
        shape.members,
        `PortableShape ${shape.name}`,
        failures,
        true
      );
    }
  }
  const opaqueShape = namedDeclaration(authoring, "OpaqueShape");
  if (opaqueShape === undefined) {
    failures.push(
      "OpaqueShape is missing from emitted authoring declarations."
    );
  } else {
    assertDocumentedMembers(
      authoring,
      opaqueShape,
      ["kind", "value"],
      "OpaqueShape optional",
      failures
    );
  }
  const opaqueInputShape = namedDeclaration(authoring, "OpaqueInputShape");
  if (opaqueInputShape === undefined) {
    failures.push(
      "OpaqueInputShape is missing from emitted authoring declarations."
    );
  } else {
    assertDocumentedMembers(
      authoring,
      opaqueInputShape,
      ["kind", "value"],
      "OpaqueInputShape optional",
      failures
    );
  }
  const defineEnvironment = namedDeclaration(authoring, "defineEnvironment");
  if (defineEnvironment === undefined) {
    failures.push(
      "defineEnvironment is missing from emitted authoring declarations."
    );
  } else {
    assertDocumentedMembers(
      authoring,
      defineEnvironment,
      ["consumers", "entries", "id", "rules", "targets"],
      "defineEnvironment declaration",
      failures
    );
  }
  assertNamedMembers(
    authoring,
    "EntryBuilder",
    [
      "boolean",
      "enum",
      "integer",
      "json",
      "origin",
      "safeInteger",
      "string",
      "stringList",
      "text",
    ],
    failures
  );
  assertNamedMembers(
    authoring,
    "PrivateEntryBuilder",
    ["opaque", "secret"],
    failures
  );
  assertNamedMembers(
    authoring,
    "ConsumerBuilder",
    ["call/0", "call/1"],
    failures
  );
  const env = namedDeclaration(authoring, "env");
  if (env === undefined) {
    failures.push("env is missing from emitted authoring declarations.");
  } else {
    assertDocumentedMembers(
      authoring,
      env,
      ["browser", "private", "process", "public", "server", "together"],
      "env",
      failures
    );
    assertDocumentedMembers(
      authoring,
      env,
      ["deployment", "request"],
      "env.private",
      failures
    );
    assertDocumentedMembers(
      authoring,
      env,
      ["build", "deployment", "request"],
      "env.public",
      failures
    );
  }
}

const browserPath = declarationPaths.get("browser");
const browser =
  browserPath === undefined ? undefined : program.getSourceFile(browserPath);
if (browser === undefined) {
  failures.push("Browser declarations could not be loaded.");
} else {
  const browserLoader = program.getSourceFile(
    resolve(root, "dist/browser/loader.d.ts")
  );
  const browserFailure = program.getSourceFile(
    resolve(root, "dist/browser/failure.d.ts")
  );
  if (browserLoader === undefined) {
    failures.push("Browser loader declarations could not be loaded.");
  } else {
    assertNamedMembers(browserLoader, "BrowserAudience", ["origin"], failures);
    assertNamedMembers(
      browserLoader,
      "BrowserProjection",
      ["consumer", "contract", "digest", "lifecycle"],
      failures
    );
    assertNamedMembers(
      browserLoader,
      "ValidatedBootstrap",
      ["audience", "values"],
      failures
    );
    assertNamedMembers(
      browserLoader,
      "LoadBootstrapOptions",
      ["endpoint", "expectedAudience", "fetch", "projection", "requestBaseUrl"],
      failures
    );
    assertNamedMembers(
      browserLoader,
      "ParseBootstrapOptions",
      ["expectedAudience", "projection", "source"],
      failures
    );
    assertNamedMembers(
      browserLoader,
      "BrowserApplicationModule",
      ["start"],
      failures
    );
    assertNamedMembers(
      browserLoader,
      "StartBrowserApplicationOptions",
      ["importApplication"],
      failures
    );
  }
  if (browserFailure === undefined) {
    failures.push("Browser failure declarations could not be loaded.");
  } else {
    assertNamedMembers(browserFailure, "BootstrapFailure", ["code"], failures);
  }
}

const runtimePath = declarationPaths.get("runtime");
const runtimeDeclarations =
  runtimePath === undefined ? undefined : program.getSourceFile(runtimePath);
if (runtimeDeclarations === undefined) {
  failures.push("Runtime declarations could not be loaded.");
} else {
  const processDeclarations = program.getSourceFile(
    resolve(root, "dist/runtime/process.d.ts")
  );
  const schemaDeclarations = program.getSourceFile(
    resolve(root, "dist/runtime/standard-schema.d.ts")
  );
  if (processDeclarations === undefined) {
    failures.push("Process runtime declarations could not be loaded.");
  } else {
    assertNamedMembers(
      processDeclarations,
      "ProcessTargetDefinition",
      ["bindings", "generated", "lifecycle", "projection"],
      failures
    );
    assertNamedMembers(
      processDeclarations,
      "ProcessTargetDefinition",
      ["entry", "source"],
      failures
    );
    assertNamedMembers(
      processDeclarations,
      "EnvironmentConfigurationError",
      ["diagnostics"],
      failures
    );
  }
  if (schemaDeclarations === undefined) {
    failures.push("Standard Schema declarations could not be loaded.");
  } else {
    assertNamedMembers(
      schemaDeclarations,
      "StandardSchemaResult",
      ["issues", "value"],
      failures,
      true
    );
    assertNamedMembers(
      schemaDeclarations,
      "StandardSchemaV1",
      ["~standard"],
      failures
    );
    assertNamedMembers(
      schemaDeclarations,
      "StandardSchemaV1",
      ["types", "validate", "vendor", "version"],
      failures
    );
    assertNamedMembers(
      schemaDeclarations,
      "StandardSchemaV1",
      ["input", "output"],
      failures
    );
  }
}

if (failures.length > 0) {
  fail(`Public TSDoc verification failed:\n- ${failures.join("\n- ")}`);
}

process.stdout.write(
  `${JSON.stringify({ entrypoints: expectedEntrypoints.map(({ id }) => id), passed: true })}\n`
);
