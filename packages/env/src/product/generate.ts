import { lstat, mkdir, readFile, readdir, writeFile } from "node:fs/promises";
import { dirname, relative, resolve, sep } from "node:path";

import {
  inspectGeneratedDirectory,
  replaceGeneratedDirectory,
} from "../artifacts/output.ts";
import type { EnvironmentDefinition } from "../authoring/index.ts";
import { getEnvironmentCompilerState } from "../authoring/internal.ts";
import {
  canonicalJson,
  compileContract,
  findProjection,
  isLocalId,
  resolvePublicLifecycle,
} from "../core/index.ts";
import type {
  BrowserProjectionEntry,
  BrowserProjectionManifest,
  CompiledContract,
  ConsumerProjectionManifest,
  JsonValue,
  Lifecycle,
  OpaqueShapeDescriptor,
  PortableShapeDescriptor,
  ResolvedConfiguration,
  ResolutionBinding,
  ServerProjectionEntry,
  Sha256Digest,
} from "../core/index.ts";
import { createPlanningSnapshot } from "../planning/index.ts";
import type { PlanningSnapshot } from "../planning/index.ts";
import { encodeCliCompilationV1 } from "./compilation.ts";
import type { ProductCompilation } from "./compilation.ts";

export const GENERATED_FORMAT = "astilba.env.generated/v1" as const;

export type GeneratedProduct = Readonly<{
  compiled: CompiledContract;
  files: ReadonlyMap<string, string>;
  snapshot: PlanningSnapshot;
}>;

export type GenerateOptions = Readonly<{
  check?: boolean;
  outputDirectory?: string;
  projectRoot: string;
  source?: Readonly<Record<string, unknown>>;
}>;

export type GenerateResult = Readonly<{
  files: readonly string[];
  mode: "checked" | "written";
  outputDirectory: string;
  snapshot: PlanningSnapshot;
}>;

export type PreparedGeneratedOutput = Readonly<{
  outputRoot: string;
  previous: Awaited<ReturnType<typeof inspectGeneratedDirectory>>;
  projectRoot: string;
}>;

export class GeneratedOutputStaleError extends Error {
  readonly paths: readonly string[];

  constructor(paths: readonly string[]) {
    super("Generated Astilba Env output is stale.");
    this.name = "GeneratedOutputStaleError";
    this.paths = Object.freeze([...paths]);
  }
}

type GeneratedEntry = BrowserProjectionEntry | ServerProjectionEntry;

const compareText = (left: string, right: string): number =>
  left < right ? -1 : left === right ? 0 : 1;

const MAXIMUM_CONSUMERS = 256;
const MAXIMUM_DECLARATION_ENTRIES = 4096;
const MAXIMUM_GENERATED_FILES = 2048;
const MAXIMUM_GENERATED_FILE_BYTES = 8_388_608;
const MAXIMUM_GENERATED_TREE_BYTES = 67_108_864;
const MAXIMUM_PHYSICAL_BINDINGS = 2048;
const MAXIMUM_PROJECTION_ENTRY_REFERENCES = 65_536;
const MAXIMUM_RULE_ENTRY_REFERENCES = 8192;
const MAXIMUM_RULES = 512;
const MAXIMUM_TARGET_BINDINGS = 65_536;
const MAXIMUM_TARGETS = 512;
const TEXT_ENCODER = new TextEncoder();

const jsonLine = (value: JsonValue): string => `${canonicalJson(value)}\n`;

const sourceString = (value: string): string =>
  JSON.stringify(value)
    .replaceAll("<", "\\u003c")
    .replaceAll(">", "\\u003e")
    .replaceAll("&", "\\u0026")
    .replaceAll("\u2028", "\\u2028")
    .replaceAll("\u2029", "\\u2029");

const renderScalarValue = (value: unknown): string => {
  if (value === null) {
    return "null";
  }
  if (typeof value === "string") {
    return sourceString(value);
  }
  if (typeof value === "boolean") {
    return value ? "true" : "false";
  }
  if (
    typeof value === "number" &&
    Number.isSafeInteger(value) &&
    !Object.is(value, -0)
  ) {
    return String(value);
  }
  throw new TypeError("A generated source value is not portable.");
};

const renderOwnedValue = (value: JsonValue): string => {
  if (value === null || typeof value !== "object") {
    return renderScalarValue(value);
  }
  if (Array.isArray(value)) {
    return `Object.freeze([${value
      // oxlint-disable-next-line typescript/no-unsafe-argument -- The JsonValue array branch guarantees each item is a JsonValue.
      .map((item) => renderOwnedValue(item))
      .join(",")}])`;
  }
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- Non-array JsonValue is the frozen object form produced by the contract compiler.
  const record = value as Readonly<Record<string, JsonValue>>;
  const definitions = Object.keys(record)
    .toSorted(compareText)
    .map((key) => {
      const item = record[key];
      if (item === undefined) {
        throw new TypeError("A generated source value is not portable.");
      }
      return `  Object.defineProperty(output, ${sourceString(key)}, { enumerable: true, value: ${renderOwnedValue(item)} });`;
    });
  return [
    "(() => {",
    "  const output = Object.create(null) as Record<string, unknown>;",
    ...definitions,
    "  return Object.freeze(output);",
    "})()",
  ].join("\n");
};

const renderGeneratedDataProperty = (
  target: string,
  key: string,
  value: JsonValue
): string =>
  `Object.defineProperty(${target}, ${sourceString(key)}, { enumerable: true, value: ${renderOwnedValue(value)} });`;

const portableShapeType = (shape: PortableShapeDescriptor): string => {
  switch (shape.kind) {
    case "array": {
      return `readonly (${portableShapeType(shape.items)})[]`;
    }
    case "boolean": {
      return "boolean";
    }
    case "null": {
      return "null";
    }
    case "object": {
      return `{ ${shape.properties
        .map(
          (property) =>
            `readonly ${sourceString(property.name)}${property.required ? "" : "?"}: ${portableShapeType(property.shape)};`
        )
        .join(" ")} }`;
    }
    case "safe-integer": {
      return "number";
    }
    case "string": {
      return "string";
    }
  }
};

const opaqueShapeType = (shape: OpaqueShapeDescriptor): string =>
  shape.kind === "optional"
    ? portableShapeType(shape.value)
    : portableShapeType(shape);

const declaredOpaqueShapeType = (shape: OpaqueShapeDescriptor): string =>
  shape.kind === "optional"
    ? `${portableShapeType(shape.value)} | undefined`
    : portableShapeType(shape);

const outputType = (entry: GeneratedEntry): string => {
  switch (entry.codec.kind) {
    case "boolean": {
      return "boolean";
    }
    case "enum": {
      return entry.codec.values.map((value) => sourceString(value)).join(" | ");
    }
    case "integer":
    case "safe-integer": {
      return "number";
    }
    case "json": {
      return portableShapeType(entry.codec.shape);
    }
    case "opaque": {
      return opaqueShapeType(entry.codec.output);
    }
    case "origin":
    case "string":
    case "text": {
      return "string";
    }
    case "string-list": {
      return "readonly string[]";
    }
  }
};

const renderConfigurationType = (
  entries: readonly GeneratedEntry[]
): string => {
  const properties = [...entries]
    .toSorted((left, right) => compareText(left.name, right.name))
    .map(
      (entry) =>
        `  readonly ${sourceString(entry.name)}${entry.required ? "" : "?"}: ${outputType(entry)};`
    )
    .join("\n");
  return `export interface Configuration {\n${properties}\n}`;
};

const selectedOpaqueEntries = (
  entries: readonly GeneratedEntry[]
): readonly (ServerProjectionEntry & {
  codec: Extract<ServerProjectionEntry["codec"], { kind: "opaque" }>;
})[] =>
  entries.filter(
    (
      entry
    ): entry is ServerProjectionEntry & {
      codec: Extract<ServerProjectionEntry["codec"], { kind: "opaque" }>;
    } => entry.codec.kind === "opaque"
  );

const renderSchemasType = (entries: readonly GeneratedEntry[]): string => {
  const properties = [...selectedOpaqueEntries(entries)]
    .toSorted((left, right) => compareText(left.name, right.name))
    .map(
      (entry) =>
        `  readonly ${sourceString(entry.name)}: readonly [${declaredOpaqueShapeType(entry.codec.input)}, ${declaredOpaqueShapeType(entry.codec.output)}];`
    )
    .join("\n");
  return [
    "type __AstilbaExpectedSchemaPairs = Readonly<{",
    properties,
    "}>;",
    "type __AstilbaSchemaCandidates = Readonly<{",
    "  readonly [TName in keyof __AstilbaExpectedSchemaPairs]: StandardSchemaV1;",
    "}>;",
    "type __AstilbaSchemaTypes<TSchema extends StandardSchemaV1> =",
    '  TSchema["~standard"] extends { readonly types?: infer TTypes }',
    "    ? NonNullable<TTypes>",
    "    : never;",
    "type __AstilbaSchemaInput<TSchema extends StandardSchemaV1> =",
    "  __AstilbaSchemaTypes<TSchema> extends { readonly input: infer TInput }",
    "    ? TInput",
    "    : never;",
    "type __AstilbaSchemaOutput<TSchema extends StandardSchemaV1> =",
    "  __AstilbaSchemaTypes<TSchema> extends { readonly output: infer TOutput }",
    "    ? TOutput",
    "    : never;",
    "type __AstilbaIsExactly<TLeft, TRight> =",
    "  (<TValue>() => TValue extends TLeft ? 1 : 2) extends",
    "  (<TValue>() => TValue extends TRight ? 1 : 2)",
    "    ? (<TValue>() => TValue extends TRight ? 1 : 2) extends",
    "      (<TValue>() => TValue extends TLeft ? 1 : 2)",
    "      ? true",
    "      : false",
    "    : false;",
    "type __AstilbaSchemaChecks<TSchemas extends __AstilbaSchemaCandidates> = {",
    "  readonly [TName in keyof __AstilbaExpectedSchemaPairs]:",
    "    __AstilbaIsExactly<",
    "      __AstilbaSchemaInput<TSchemas[TName]>,",
    "      __AstilbaExpectedSchemaPairs[TName][0]",
    "    > extends true",
    "      ? __AstilbaIsExactly<",
    "          __AstilbaSchemaOutput<TSchemas[TName]>,",
    "          __AstilbaExpectedSchemaPairs[TName][1]",
    "        >",
    "      : false;",
    "}[keyof __AstilbaExpectedSchemaPairs];",
    "type __AstilbaSchemaGate<TSchemas extends __AstilbaSchemaCandidates> =",
    "  Exclude<keyof TSchemas, keyof __AstilbaSchemaCandidates> extends never",
    "    ? __AstilbaSchemaChecks<TSchemas> extends true",
    "      ? unknown",
    "      : { readonly __astilbaSchemaTypeMismatch: never }",
    "    : { readonly __astilbaSchemaMapMismatch: never };",
    "export type Schemas<TSchemas extends __AstilbaSchemaCandidates> =",
    "  TSchemas & __AstilbaSchemaGate<TSchemas>;",
  ].join("\n");
};

const renderTargetModule = (input: {
  bindings: readonly Readonly<{ entry: string; source: string }>[];
  lifecycle: Lifecycle;
  projection: ConsumerProjectionManifest;
}): string => {
  const selected = input.projection.entries.filter(
    (entry) => entry.lifecycle === input.lifecycle
  );
  const opaqueEntries = selectedOpaqueEntries(selected);
  const hasOpaque = opaqueEntries.length > 0;
  const definition = {
    bindings: input.bindings,
    generated: "astilba.env.generated-module/v1",
    lifecycle: input.lifecycle,
    projection: input.projection,
  };
  const imports = hasOpaque
    ? [
        "  checkProcessTargetWithSchemas,",
        "  loadProcessTargetWithSchemas,",
        "  type ProcessSource,",
        "  type ProcessTargetDefinition,",
        "  type StandardSchemaV1,",
      ]
    : [
        "  checkProcessTarget,",
        "  loadProcessTarget,",
        "  type ProcessSource,",
        "  type ProcessTargetDefinition,",
      ];
  const operations = hasOpaque
    ? [
        "export const check = <const TSchemas extends __AstilbaSchemaCandidates>(",
        "  source: ProcessSource,",
        "  schemas: Schemas<TSchemas>,",
        ") => checkProcessTargetWithSchemas<Configuration>(definition, source, schemas);",
        "",
        "export const load = <const TSchemas extends __AstilbaSchemaCandidates>(",
        "  source: ProcessSource,",
        "  schemas: Schemas<TSchemas>,",
        "): Promise<Configuration> =>",
        "  loadProcessTargetWithSchemas<Configuration>(definition, source, schemas);",
      ]
    : [
        "export const check = (source: ProcessSource) =>",
        "  checkProcessTarget<Configuration>(definition, source);",
        "",
        "export const load = (source: ProcessSource): Configuration =>",
        "  loadProcessTarget<Configuration>(definition, source);",
      ];
  return [
    "/* Generated by Astilba Env. Do not edit. */",
    "import {",
    ...imports,
    '} from "@astilba/env/runtime";',
    "",
    renderConfigurationType(selected),
    ...(hasOpaque ? ["", renderSchemasType(opaqueEntries)] : []),
    "",
    // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- The internal definition is assembled from the validated projection model before source rendering.
    `const definition = ${renderOwnedValue(definition as JsonValue)} as ProcessTargetDefinition;`,
    "",
    ...operations,
    "",
  ].join("\n");
};

class BrowserShapeCopiers {
  readonly #names = new Map<PortableShapeDescriptor, string>();
  readonly #sources = new Map<string, string>();

  copy(shape: PortableShapeDescriptor, value: string): string {
    return `${this.#name(shape)}(${value})`;
  }

  render(): string {
    return [...this.#sources.entries()]
      .toSorted(([left], [right]) => compareText(left, right))
      .map(([, source]) => source)
      .join("\n\n");
  }

  #name(shape: PortableShapeDescriptor): string {
    const existing = this.#names.get(shape);
    if (existing !== undefined) {
      return existing;
    }
    const name = `copyShape${this.#names.size}`;
    this.#names.set(shape, name);
    this.#sources.set(name, this.#render(name, shape));
    return name;
  }

  #render(name: string, shape: PortableShapeDescriptor): string {
    switch (shape.kind) {
      case "boolean": {
        return `const ${name} = (value: unknown): unknown | undefined =>\n  typeof value === "boolean" ? value : undefined;`;
      }
      case "null": {
        return `const ${name} = (value: unknown): unknown | undefined =>\n  value === null ? null : undefined;`;
      }
      case "safe-integer": {
        return [
          `const ${name} = (value: unknown): unknown | undefined => {`,
          '  if (typeof value !== "number") {',
          "    return undefined;",
          "  }",
          `  if (!Number.isSafeInteger(value) || value < ${renderOwnedValue(shape.minimum)} || value > ${renderOwnedValue(shape.maximum)}) {`,
          "    return undefined;",
          "  }",
          "  return value;",
          "};",
        ].join("\n");
      }
      case "string": {
        return `const ${name} = (value: unknown): unknown | undefined =>\n  typeof value === "string" ? value : undefined;`;
      }
      case "array": {
        const child = this.#name(shape.items);
        return [
          `const ${name} = (value: unknown): unknown | undefined => {`,
          "  if (",
          "    !Array.isArray(value) ||",
          `    value.length < ${renderOwnedValue(shape.minimumItems)} ||`,
          `    value.length > ${renderOwnedValue(shape.maximumItems)}`,
          "  ) {",
          "    return undefined;",
          "  }",
          "  const output: unknown[] = [];",
          "  for (const item of value) {",
          `    const copied = ${child}(item);`,
          "    if (copied === undefined) {",
          "      return undefined;",
          "    }",
          "    output.push(copied);",
          "  }",
          "  return Object.freeze(output);",
          "};",
        ].join("\n");
      }
      case "object": {
        const expectedKeys = renderOwnedValue(
          shape.properties.map((property) => property.name)
        );
        const statements = shape.properties.flatMap((property) => {
          const key = sourceString(property.name);
          const child = this.#name(property.shape);
          const copied = `copied${shape.properties.indexOf(property)}`;
          if (property.required) {
            return [
              `  if (!Object.hasOwn(value, ${key})) {`,
              "    return undefined;",
              "  }",
              `  const ${copied} = ${child}(value[${key}]);`,
              `  if (${copied} === undefined) {`,
              "    return undefined;",
              "  }",
              `  Object.defineProperty(output, ${key}, { enumerable: true, value: ${copied} });`,
            ];
          }
          return [
            `  if (Object.hasOwn(value, ${key})) {`,
            `    const ${copied} = ${child}(value[${key}]);`,
            `    if (${copied} === undefined) {`,
            "      return undefined;",
            "    }",
            `    Object.defineProperty(output, ${key}, { enumerable: true, value: ${copied} });`,
            "  }",
          ];
        });
        return [
          `const ${name} = (input: unknown): unknown | undefined => {`,
          "  if (",
          '    typeof input !== "object" ||',
          "    input === null ||",
          "    Array.isArray(input)",
          "  ) {",
          "    return undefined;",
          "  }",
          "  const value = input as Readonly<Record<string, unknown>>;",
          `  const allowed = new Set(${expectedKeys});`,
          "  const keys = Reflect.ownKeys(value);",
          "  if (",
          '    keys.some((key) => typeof key !== "string" || !allowed.has(key)) ||',
          "    keys.length > allowed.size",
          "  ) {",
          "    return undefined;",
          "  }",
          "  const output = Object.create(null) as Record<string, unknown>;",
          ...statements,
          "  return Object.freeze(output);",
          "};",
        ].join("\n");
      }
    }
  }
}

const renderBrowserEntry = (
  entry: BrowserProjectionEntry,
  index: number,
  copiers: BrowserShapeCopiers
): string => {
  const name = sourceString(entry.name);
  const raw = `raw${index}`;
  const value = `value${index}`;
  const lines = [`const ${raw} = input[${name}];`];
  switch (entry.codec.kind) {
    case "boolean": {
      lines.push(
        `if (typeof ${raw} !== "boolean") {`,
        "  invalid();",
        "}",
        `const ${value} = ${raw};`
      );
      break;
    }
    case "enum": {
      lines.push(
        `if (typeof ${raw} !== "string" || !${renderOwnedValue(entry.codec.values)}.includes(${raw})) {`,
        "  invalid();",
        "}",
        `const ${value} = ${raw};`
      );
      break;
    }
    case "json": {
      lines.push(
        `const ${value} = ${copiers.copy(entry.codec.shape, raw)};`,
        `if (${value} === undefined) {`,
        "  invalid();",
        "}"
      );
      break;
    }
    case "origin": {
      lines.push(
        `const ${value} = normaliseOrigin(${raw});`,
        `if (${value} === undefined) {`,
        "  invalid();",
        "}"
      );
      break;
    }
    case "safe-integer": {
      lines.push(
        `if (typeof ${raw} !== "number") {`,
        "  return invalid();",
        "}",
        `if (!Number.isSafeInteger(${raw}) || ${raw} < ${renderOwnedValue(entry.codec.minimum)} || ${raw} > ${renderOwnedValue(entry.codec.maximum)}) {`,
        "  return invalid();",
        "}",
        `const ${value} = ${raw};`
      );
      break;
    }
    case "string": {
      lines.push(
        `if (!validText(${raw}, ${renderOwnedValue(entry.codec.minCodePoints)}, ${renderOwnedValue(entry.codec.maxCodePoints)})) {`,
        "  invalid();",
        "}",
        `const ${value} = ${raw};`
      );
      break;
    }
    case "string-list": {
      lines.push(
        `if (!Array.isArray(${raw})) {`,
        "  return invalid();",
        "}",
        `if (${raw}.length < ${renderOwnedValue(entry.codec.minimumItems)} || ${raw}.length > ${renderOwnedValue(entry.codec.maximumItems)}) {`,
        "  return invalid();",
        "}",
        `for (let index = 0; index < ${raw}.length; index += 1) {`,
        `  if (!validText(${raw}[index], ${renderOwnedValue(entry.codec.minimumItemCodePoints)}, ${renderOwnedValue(entry.codec.maximumItemCodePoints)})) {`,
        "    return invalid();",
        "  }",
        "}",
        `const ${value} = Object.freeze([...${raw}]);`
      );
      break;
    }
  }
  lines.push(
    `Object.defineProperty(values, ${name}, { enumerable: true, value: ${value} });`
  );
  if (entry.required) {
    return [
      `if (!Object.hasOwn(input, ${name})) {`,
      "  missing();",
      "}",
      ...lines,
    ].join("\n");
  }
  return [
    `if (Object.hasOwn(input, ${name})) {`,
    ...lines.map((line) => `  ${line}`),
    "}",
  ].join("\n");
};

const renderBrowserProjectionModule = (input: {
  digest: Sha256Digest;
  lifecycle: "deployment" | "request";
  projection: BrowserProjectionManifest;
}): string => {
  const entries = input.projection.entries
    .filter((entry) => entry.lifecycle === input.lifecycle)
    .toSorted((left, right) => compareText(left.name, right.name));
  const copiers = new BrowserShapeCopiers();
  const decoder = entries
    .map((entry, index) => renderBrowserEntry(entry, index, copiers))
    .join("\n\n");
  const copyHelpers = copiers.render();
  const usesText = entries.some(
    (entry) =>
      entry.codec.kind === "string" || entry.codec.kind === "string-list"
  );
  const usesOrigin = entries.some((entry) => entry.codec.kind === "origin");
  const selectedNames = renderOwnedValue(entries.map((entry) => entry.name));
  return [
    "/* Generated by Astilba Env. Do not edit. */",
    'import type { BrowserProjection } from "@astilba/env/browser";',
    "",
    renderConfigurationType(entries),
    "",
    ...(usesText
      ? [
          "const validText = (value: unknown, minimum: number, maximum: number): value is string => {",
          '  if (typeof value !== "string" || value.length > maximum * 2) {',
          "    return false;",
          "  }",
          "  const length = [...value].length;",
          "  return length >= minimum && length <= maximum;",
          "};",
          "",
        ]
      : []),
    ...(usesOrigin
      ? [
          "const normaliseOrigin = (input: unknown): string | undefined => {",
          '  if (typeof input !== "string") {',
          "    return undefined;",
          "  }",
          "  for (let index = 0; index < input.length; index += 1) {",
          "    const code = input.codePointAt(index);",
          "    if (code === undefined || code > 0x7f) {",
          "      return undefined;",
          "    }",
          "  }",
          '  if (!input.startsWith("https://")) {',
          "    return undefined;",
          "  }",
          "  let authority = input.slice(8);",
          '  if (authority.endsWith("/")) {',
          "    authority = authority.slice(0, -1);",
          "  }",
          "  if (",
          "    authority.length === 0 ||",
          '    authority.includes("/") ||',
          '    authority.includes("?") ||',
          '    authority.includes("#") ||',
          '    authority.includes("@")',
          "  ) {",
          "    return undefined;",
          "  }",
          '  const colon = authority.indexOf(":");',
          '  if (colon !== authority.lastIndexOf(":")) {',
          "    return undefined;",
          "  }",
          "  const host = colon < 0 ? authority : authority.slice(0, colon);",
          "  const port = colon < 0 ? undefined : authority.slice(colon + 1);",
          "  if (",
          "    host.length === 0 ||",
          "    host.length > 253 ||",
          '    host.endsWith(".") ||',
          '    host === "localhost"',
          "  ) {",
          "    return undefined;",
          "  }",
          '  const labels = host.split(".");',
          "  if (",
          "    labels.length < 2 ||",
          "    /^(?:(?:0x[\\da-f]+|\\d+)\\.){1,3}0x[\\da-f]+$/u.test(host) ||",
          "    !/[a-z][a-z0-9-]*$/u.test(host) ||",
          "    labels.some((label) =>",
          "      label.length === 0 ||",
          "      label.length > 63 ||",
          "      !/^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/u.test(label),",
          "    ) ||",
          "    (port !== undefined &&",
          "      (!/^[1-9][0-9]*$/u.test(port) ||",
          "        port.length > 5 ||",
          "        Number(port) > 65_535))",
          "  ) {",
          "    return undefined;",
          "  }",
          '  return `https://${host}${port === undefined || port === "443" ? "" : `:${port}`}`;',
          "};",
          "",
        ]
      : []),
    ...(copyHelpers.length > 0 ? [copyHelpers, ""] : []),
    "const decode = (",
    "  input: Readonly<Record<string, unknown>>,",
    "  failure: (",
    '    code: "BOOTSTRAP_UNKNOWN_FIELD" | "BOOTSTRAP_VALUE_INVALID" | "BOOTSTRAP_VALUE_MISSING",',
    "  ) => never,",
    "): Readonly<Configuration> => {",
    `  const selected = new Set(${selectedNames});`,
    '  if (Reflect.ownKeys(input).some((key) => typeof key !== "string" || !selected.has(key))) {',
    '    failure("BOOTSTRAP_UNKNOWN_FIELD");',
    "  }",
    '  const invalid = (): never => failure("BOOTSTRAP_VALUE_INVALID");',
    '  const missing = (): never => failure("BOOTSTRAP_VALUE_MISSING");',
    "  const values = Object.create(null) as Record<string, unknown>;",
    "",
    ...decoder
      .split("\n")
      .map((line) => (line.length === 0 ? line : `  ${line}`)),
    "",
    "  return Object.freeze(values) as Readonly<Configuration>;",
    "};",
    "",
    "const generatedProjection = Object.create(null) as Record<string, unknown>;",
    renderGeneratedDataProperty(
      "generatedProjection",
      "codecAbi",
      "astilba.env.codec/v1"
    ),
    renderGeneratedDataProperty(
      "generatedProjection",
      "consumer",
      input.projection.consumer
    ),
    renderGeneratedDataProperty(
      "generatedProjection",
      "contract",
      input.projection.contract
    ),
    'Object.defineProperty(generatedProjection, "decode", { enumerable: true, value: decode });',
    renderGeneratedDataProperty("generatedProjection", "digest", input.digest),
    renderGeneratedDataProperty(
      "generatedProjection",
      "format",
      "astilba.env.projection"
    ),
    renderGeneratedDataProperty("generatedProjection", "formatVersion", 1),
    renderGeneratedDataProperty(
      "generatedProjection",
      "generated",
      "astilba.env.generated-module/v1"
    ),
    renderGeneratedDataProperty("generatedProjection", "kind", "public"),
    renderGeneratedDataProperty(
      "generatedProjection",
      "lifecycle",
      input.lifecycle
    ),
    renderGeneratedDataProperty(
      "generatedProjection",
      "projectionAbi",
      "astilba.env.projection/v1"
    ),
    "export const projection = Object.freeze(generatedProjection) as unknown as BrowserProjection<Configuration>;",
    "",
  ].join("\n");
};

const renderBrowserBuildModule = (
  entries: readonly BrowserProjectionEntry[],
  configuration: ResolvedConfiguration
): string =>
  [
    "/* Generated by Astilba Env. Do not edit. */",
    renderConfigurationType(entries),
    "",
    `export const configuration: Readonly<Configuration> = ${renderOwnedValue(configuration)} as Readonly<Configuration>;`,
    "",
  ].join("\n");

const resolutionBindings = (
  bindings: readonly Readonly<{ entry: string; rawName: string }>[]
): readonly ResolutionBinding[] =>
  Object.freeze(
    bindings
      .map((binding) =>
        Object.freeze({
          entry: binding.entry,
          source: binding.rawName,
        })
      )
      .toSorted((left, right) => compareText(left.entry, right.entry))
  );

const failCompilationLimit = (limit: string): never => {
  throw new TypeError(`Astilba Env compiled declaration exceeds its ${limit}.`);
};

const addBoundedCount = (
  current: number,
  added: number,
  maximum: number,
  limit: string
): number => {
  if (added > maximum - current) {
    return failCompilationLimit(limit);
  }
  return current + added;
};

const assertCompilationCardinality = (input: ProductCompilation): void => {
  const manifest = input.compiled.full.manifest;
  if (manifest.entries.length > MAXIMUM_DECLARATION_ENTRIES) {
    failCompilationLimit("entry limit");
  }
  if (
    manifest.consumers.length > MAXIMUM_CONSUMERS ||
    input.compiled.projections.length > MAXIMUM_CONSUMERS
  ) {
    failCompilationLimit("consumer limit");
  }
  if (input.targets.length > MAXIMUM_TARGETS) {
    failCompilationLimit("target limit");
  }
  if ("rules" in manifest && manifest.rules.length > MAXIMUM_RULES) {
    failCompilationLimit("rule limit");
  }

  let selectionReferences = 0;
  for (const consumer of manifest.consumers) {
    selectionReferences = addBoundedCount(
      selectionReferences,
      consumer.entries.length,
      MAXIMUM_PROJECTION_ENTRY_REFERENCES,
      "consumer-selection limit"
    );
  }

  let projectionReferences = 0;
  for (const projection of input.compiled.projections) {
    if (projection.manifest.entries.length > MAXIMUM_DECLARATION_ENTRIES) {
      failCompilationLimit("projection-entry limit");
    }
    projectionReferences = addBoundedCount(
      projectionReferences,
      projection.manifest.entries.length,
      MAXIMUM_PROJECTION_ENTRY_REFERENCES,
      "projection-entry-reference limit"
    );
  }

  let ruleReferences = 0;
  if ("rules" in manifest) {
    for (const rule of manifest.rules) {
      ruleReferences = addBoundedCount(
        ruleReferences,
        rule.entries.length,
        MAXIMUM_RULE_ENTRY_REFERENCES,
        "rule-entry-reference limit"
      );
    }
  }

  let targetBindings = 0;
  for (const target of input.targets) {
    const bindingCount = target.bindingPlan.bindings.length;
    if (bindingCount > MAXIMUM_PHYSICAL_BINDINGS) {
      failCompilationLimit("physical-binding limit");
    }
    targetBindings = addBoundedCount(
      targetBindings,
      bindingCount,
      MAXIMUM_TARGET_BINDINGS,
      "target-binding limit"
    );
  }
};

const assertCompilationBundleLimit = (input: ProductCompilation): void => {
  void encodeCliCompilationV1(input);
};

const assertGeneratedFileCount = (input: ProductCompilation): void => {
  let count = 3 + input.compiled.projections.length + input.targets.length;
  for (const projection of input.compiled.projections) {
    if (projection.manifest.kind !== "public") {
      continue;
    }
    for (const lifecycle of ["build", "deployment", "request"] as const) {
      if (
        projection.manifest.entries.some(
          (entry) => entry.lifecycle === lifecycle
        )
      ) {
        count += 1;
      }
    }
  }
  if (count > MAXIMUM_GENERATED_FILES) {
    throw new TypeError("Astilba Env generated output exceeds its file limit.");
  }
};

const matchesGeneratedLocalIdPath = (
  path: string,
  prefix: string,
  suffix: string
): boolean =>
  path.startsWith(prefix) &&
  path.endsWith(suffix) &&
  isLocalId(path.slice(prefix.length, -suffix.length));

const isGeneratedProductPath = (path: string): boolean =>
  path === "contract.json" ||
  path === "manifest.json" ||
  path === "snapshot.json" ||
  matchesGeneratedLocalIdPath(path, "consumers/", ".public.json") ||
  matchesGeneratedLocalIdPath(path, "consumers/", ".server.json") ||
  matchesGeneratedLocalIdPath(path, "browser/", ".build.ts") ||
  matchesGeneratedLocalIdPath(path, "browser/", ".deployment.ts") ||
  matchesGeneratedLocalIdPath(path, "browser/", ".request.ts") ||
  matchesGeneratedLocalIdPath(path, "", ".server.ts");

type EncodedGeneratedFile = Readonly<{
  bytes: Uint8Array;
  path: string;
  text: string;
}>;

const encodeGeneratedProductFiles = (
  files: ReadonlyMap<string, string>
): readonly EncodedGeneratedFile[] => {
  if (files.size > MAXIMUM_GENERATED_FILES) {
    throw new TypeError("Astilba Env generated output exceeds its file limit.");
  }
  const encoded: EncodedGeneratedFile[] = [];
  const paths = new Set<string>();
  let totalBytes = 0;
  for (const [path, text] of files) {
    if (
      typeof path !== "string" ||
      typeof text !== "string" ||
      paths.has(path) ||
      !isGeneratedProductPath(path)
    ) {
      throw new TypeError("Astilba Env generated output is invalid.");
    }
    paths.add(path);
    const bytes = TEXT_ENCODER.encode(text);
    if (bytes.byteLength > MAXIMUM_GENERATED_FILE_BYTES) {
      throw new TypeError(
        "Astilba Env generated output exceeds its per-file limit."
      );
    }
    totalBytes += bytes.byteLength;
    if (totalBytes > MAXIMUM_GENERATED_TREE_BYTES) {
      throw new TypeError(
        "Astilba Env generated output exceeds its tree limit."
      );
    }
    encoded.push(
      Object.freeze({
        bytes,
        path,
        text,
      })
    );
  }
  if (
    encoded.length !== files.size ||
    !paths.has("contract.json") ||
    !paths.has("manifest.json") ||
    !paths.has("snapshot.json")
  ) {
    throw new TypeError("Astilba Env generated output is invalid.");
  }
  const listed = [...paths]
    .filter((path) => path !== "manifest.json")
    .toSorted(compareText);
  const expectedManifest = jsonLine({
    files: Object.freeze(listed),
    format: GENERATED_FORMAT,
  });
  if (
    encoded.find((file) => file.path === "manifest.json")?.text !==
    expectedManifest
  ) {
    throw new TypeError("Astilba Env generated output is invalid.");
  }
  return Object.freeze(
    encoded.toSorted((left, right) => compareText(left.path, right.path))
  );
};

export const compileProduct = async (
  environment: EnvironmentDefinition,
  source?: Readonly<Record<string, unknown>>
): Promise<GeneratedProduct> => {
  const state = getEnvironmentCompilerState(environment);
  const compiled = await compileContract(state.contract);
  const targetNames = Object.keys(state.bindingPlans).toSorted(compareText);
  return compileProductFromCompilation(
    {
      compiled,
      targets: targetNames.map((target) => {
        const bindingPlan = state.bindingPlans[target];
        if (bindingPlan === undefined) {
          throw new TypeError(
            "The environment target binding plan is invalid."
          );
        }
        return {
          bindingPlan,
          consumer: state.targets[target]?.consumer ?? "",
        };
      }),
    },
    source
  );
};

export const compileProductFromCompilation = (
  input: ProductCompilation,
  source?: Readonly<Record<string, unknown>>
): GeneratedProduct => {
  assertCompilationCardinality(input);
  assertCompilationBundleLimit(input);
  assertGeneratedFileCount(input);

  const { compiled } = input;
  const projections = [...compiled.projections].toSorted((left, right) =>
    compareText(left.manifest.consumer, right.manifest.consumer)
  );
  const targets = [...input.targets].toSorted((left, right) =>
    compareText(left.bindingPlan.target, right.bindingPlan.target)
  );
  const targetByName = new Map(
    targets.map((target) => [target.bindingPlan.target, target])
  );
  const targetNames = [...targetByName.keys()];
  if (targetNames.length !== targets.length) {
    throw new TypeError("Astilba Env process target IDs must be unique.");
  }
  const snapshot = createPlanningSnapshot({
    compiled,
    targets,
  });

  const files = new Map<string, string>([
    ["contract.json", `${compiled.full.text}\n`],
  ]);
  for (const projection of projections) {
    files.set(
      `consumers/${projection.manifest.consumer}.${projection.manifest.kind}.json`,
      `${projection.text}\n`
    );
    if (projection.manifest.kind !== "public") {
      continue;
    }
    for (const lifecycle of ["deployment", "request"] as const) {
      if (
        projection.manifest.entries.some(
          (entry) => entry.lifecycle === lifecycle
        )
      ) {
        files.set(
          `browser/${projection.manifest.consumer}.${lifecycle}.ts`,
          renderBrowserProjectionModule({
            digest: projection.digest,
            lifecycle,
            projection: projection.manifest,
          })
        );
      }
    }
    const buildEntries = projection.manifest.entries.filter(
      (entry) => entry.lifecycle === "build"
    );
    if (buildEntries.length === 0) {
      continue;
    }
    const buildTarget = targetNames.find(
      (target) =>
        targetByName.get(target)?.consumer === projection.manifest.consumer &&
        targetByName
          .get(target)
          ?.bindingPlan.bindings.every(
            (binding) => binding.channel === "build"
          ) === true
    );
    if (buildTarget === undefined || source === undefined) {
      throw new TypeError(
        "A public build configuration requires an explicit build source."
      );
    }
    const plan = targetByName.get(buildTarget)?.bindingPlan;
    if (plan === undefined) {
      throw new TypeError("A public build target is incomplete.");
    }
    const result = resolvePublicLifecycle(
      projection.manifest,
      "build",
      resolutionBindings(plan.bindings),
      source
    );
    if (!result.ok) {
      throw new TypeError("A public build configuration is invalid.");
    }
    files.set(
      `browser/${projection.manifest.consumer}.build.ts`,
      renderBrowserBuildModule(buildEntries, result.value)
    );
  }

  for (const target of targetNames) {
    const targetDefinition = targetByName.get(target);
    const bindingPlan = targetDefinition?.bindingPlan;
    const lifecycle = bindingPlan?.bindings[0]?.channel;
    if (targetDefinition === undefined || bindingPlan === undefined) {
      throw new TypeError("Astilba Env target metadata is incomplete.");
    }
    if (
      lifecycle === undefined ||
      bindingPlan.bindings.some((binding) => binding.channel !== lifecycle)
    ) {
      throw new TypeError(
        "Astilba Env process target must bind one lifecycle."
      );
    }
    const projection = findProjection(compiled, targetDefinition.consumer);
    if (projection === undefined) {
      throw new TypeError("Astilba Env process target projection is missing.");
    }
    files.set(
      `${target}.server.ts`,
      renderTargetModule({
        bindings: resolutionBindings(bindingPlan.bindings),
        lifecycle,
        projection: projection.manifest,
      })
    );
  }
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- The snapshot encoder constructs this value solely from generated JSON model data.
  files.set("snapshot.json", jsonLine(snapshot as unknown as JsonValue));
  files.set(
    "manifest.json",
    jsonLine({
      files: Object.freeze([...files.keys()].toSorted(compareText)),
      format: GENERATED_FORMAT,
    })
  );
  void encodeGeneratedProductFiles(files);

  return Object.freeze({
    compiled,
    files,
    snapshot,
  });
};

const isMissing = (error: unknown): boolean =>
  error instanceof Error && "code" in error && error.code === "ENOENT";

const listOutputFiles = async (
  root: string,
  current: string = root
): Promise<readonly string[]> => {
  let names;
  try {
    names = await readdir(current, { withFileTypes: true });
  } catch (error) {
    if (isMissing(error)) {
      return [];
    }
    throw error;
  }
  const output: string[] = [];
  for (const item of names) {
    const path = resolve(current, item.name);
    if (item.isSymbolicLink()) {
      throw new TypeError(
        "Generated Astilba Env output must not contain symbolic links."
      );
    }
    if (item.isDirectory()) {
      output.push(...(await listOutputFiles(root, path)));
      continue;
    }
    if (!item.isFile()) {
      throw new TypeError(
        "Generated Astilba Env output contains an unsupported file."
      );
    }
    output.push(relative(root, path).split(sep).join("/"));
  }
  return output.toSorted(compareText);
};

const stalePaths = async (
  outputRoot: string,
  expected: ReadonlyMap<string, string>
): Promise<readonly string[]> => {
  const actualPaths = await listOutputFiles(outputRoot);
  const allPaths = new Set([...actualPaths, ...expected.keys()]);
  const stale: string[] = [];
  for (const path of [...allPaths].toSorted(compareText)) {
    const expectedText = expected.get(path);
    if (expectedText === undefined) {
      stale.push(path);
      continue;
    }
    try {
      const metadata = await lstat(resolve(outputRoot, path));
      if (!metadata.isFile() || metadata.isSymbolicLink()) {
        stale.push(path);
        continue;
      }
      if (
        (await readFile(resolve(outputRoot, path), "utf-8")) !== expectedText
      ) {
        stale.push(path);
      }
    } catch (error) {
      if (isMissing(error)) {
        stale.push(path);
        continue;
      }
      throw error;
    }
  }
  return Object.freeze(stale);
};

export const prepareGeneratedOutput = async (
  options: GenerateOptions
): Promise<PreparedGeneratedOutput> => {
  const projectRoot = resolve(options.projectRoot);
  const outputRoot = resolve(
    projectRoot,
    options.outputDirectory ?? ".astilba/env"
  );
  const relativeOutput = relative(projectRoot, outputRoot);
  if (
    relativeOutput === "" ||
    relativeOutput === ".." ||
    relativeOutput.startsWith(`..${sep}`)
  ) {
    throw new TypeError(
      "Generated Astilba Env output must stay below the project root."
    );
  }

  const previous = await inspectGeneratedDirectory(projectRoot, outputRoot);
  return Object.freeze({
    outputRoot,
    previous,
    projectRoot,
  });
};

export const writeGeneratedProduct = async (
  product: GeneratedProduct,
  prepared: PreparedGeneratedOutput,
  check: boolean
): Promise<GenerateResult> => {
  const encodedFiles = encodeGeneratedProductFiles(product.files);
  const expected = new Map(
    encodedFiles.map((file) => [file.path, file.text] as const)
  );
  const paths = Object.freeze(encodedFiles.map((file) => file.path));
  const snapshot = product.snapshot;
  if (check) {
    const stale = await stalePaths(prepared.outputRoot, expected);
    if (stale.length > 0) {
      throw new GeneratedOutputStaleError(stale);
    }
    return Object.freeze({
      files: paths,
      mode: "checked" as const,
      outputDirectory: prepared.outputRoot,
      snapshot,
    });
  }

  await replaceGeneratedDirectory(
    prepared.projectRoot,
    prepared.outputRoot,
    prepared.previous
  );
  for (const file of encodedFiles) {
    const outputPath = resolve(prepared.outputRoot, file.path);
    await mkdir(dirname(outputPath), { recursive: true });
    await writeFile(outputPath, file.bytes);
  }
  return Object.freeze({
    files: paths,
    mode: "written" as const,
    outputDirectory: prepared.outputRoot,
    snapshot,
  });
};

export const generateEnvironment = async (
  environment: EnvironmentDefinition,
  options: GenerateOptions
): Promise<GenerateResult> => {
  const prepared = await prepareGeneratedOutput(options);
  const product = await compileProduct(environment, options.source);
  return await writeGeneratedProduct(product, prepared, options.check === true);
};
