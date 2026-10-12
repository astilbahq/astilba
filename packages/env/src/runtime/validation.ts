import type {
  CodecDescriptor,
  EntryIdentity,
  Lifecycle,
  NormalizedTarget,
  OpaqueInputShape,
  OpaqueShape,
  PortableShape,
  PresentTogetherRule,
  ProcessProjection,
  PublicProjectionEntry,
  ServerProjectionEntry,
} from "./model.ts";

const GENERATED_MODULE = "astilba.env.generated-module/v1" as const;
const MAXIMUM_ENTRIES = 4096;
const MAXIMUM_RULES = 512;
const MAXIMUM_RULE_ENTRY_REFERENCES = 8192;
const MAXIMUM_SHAPE_DEPTH = 8;
const MAXIMUM_SHAPE_NODES = 256;
const MAXIMUM_OBJECT_PROPERTIES = 256;
const MAXIMUM_ENUM_VALUES = 1024;
const MAXIMUM_ENUM_BYTES = 65_536;
const MAXIMUM_STRING_BYTES = 65_535;
const MAXIMUM_STRING_CODE_POINTS = 65_535;
const MAXIMUM_ARRAY_ITEMS = 1024;
const MAXIMUM_LIST_PRODUCT = 65_536;
const LOCAL_ID = /^[a-z][A-Za-z0-9]{0,63}$/u;
const RAW_SOURCE_NAME = /^[A-Z_][A-Z0-9_]{0,127}$/u;
const CONTRACT_LABEL = /^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/u;
const OPAQUE_LABEL = /^[A-Za-z0-9][A-Za-z0-9._/@:+-]{0,254}$/u;
const ASCII_GRAPHIC = /^[\u0021-\u007E]{1,32}$/u;
const UNSUPPORTED_FORMAT = new Error("Unsupported Astilba Env format.");
const INVALID_CONTRACT = new Error("Invalid Astilba Env contract.");
const TEXT_ENCODER = new TextEncoder();
const DANGEROUS_PROPERTY_NAMES = new Set([
  "__proto__",
  "constructor",
  "prototype",
]);
const CODEC_VERSION_PREFIXES = [
  "astilba.env.string-code-point/v",
  "astilba.env.origin-ascii/v",
  "astilba.env.enum/v",
  "astilba.env.boolean-exact/v",
  "astilba.env.safe-integer-decimal/v",
  "astilba.env.string-list-comma/v",
  "astilba.env.json-exact/v",
  "astilba.env.text/v",
  "astilba.env.integer/v",
  "astilba.env.opaque/v",
] as const;

type DataRecord = ReadonlyMap<string, unknown>;

type ValidationResult =
  | Readonly<{
      code: "ENV_CONTRACT_INVALID" | "ENV_FORMAT_UNSUPPORTED";
      ok: false;
    }>
  | Readonly<{
      ok: true;
      target: NormalizedTarget;
    }>;

const invalid = (): never => {
  throw INVALID_CONTRACT;
};

const unsupported = (): never => {
  throw UNSUPPORTED_FORMAT;
};

const compareStrings = (left: string, right: string): number => {
  if (left === right) {
    return 0;
  }
  return left < right ? -1 : 1;
};

const asciiCaseFold = (value: string): string => {
  let output = "";
  for (const character of value) {
    const code = character.codePointAt(0) ?? 0;
    output +=
      code >= 0x41 && code <= 0x5a
        ? String.fromCodePoint(code + 0x20)
        : character;
  }
  return output;
};

const isPortableString = (value: unknown): value is string => {
  if (typeof value !== "string") {
    return false;
  }
  for (let index = 0; index < value.length; index += 1) {
    // oxlint-disable-next-line unicorn/prefer-code-point -- Portable-string validation must inspect individual UTF-16 code units to reject lone surrogates.
    const code = value.charCodeAt(index);
    if (code >= 0xd8_00 && code <= 0xdb_ff) {
      // oxlint-disable-next-line unicorn/prefer-code-point -- The paired-surrogate check intentionally reads the next UTF-16 code unit.
      const next = value.charCodeAt(index + 1);
      if (next < 0xdc_00 || next > 0xdf_ff) {
        return false;
      }
      index += 1;
    } else if (code >= 0xdc_00 && code <= 0xdf_ff) {
      return false;
    }
  }
  return true;
};

const isCanonicalSafeInteger = (value: unknown): value is number =>
  typeof value === "number" &&
  Number.isSafeInteger(value) &&
  !Object.is(value, -0);

const isLocalId = (value: unknown): value is string =>
  typeof value === "string" && LOCAL_ID.test(value);

const isRawSourceName = (value: unknown): value is string =>
  typeof value === "string" && RAW_SOURCE_NAME.test(value);

const isContractId = (value: unknown): value is string => {
  if (typeof value !== "string" || value.length < 3 || value.length > 255) {
    return false;
  }
  const labels = value.split(".");
  return (
    labels.length >= 2 &&
    labels.every((label) => label.length <= 63 && CONTRACT_LABEL.test(label))
  );
};

const readOwnStringKeys = (value: object): readonly string[] => {
  const output: string[] = [];
  for (const key of Reflect.ownKeys(value)) {
    if (typeof key !== "string") {
      return invalid();
    }
    output.push(key);
  }
  return output;
};

const readDataRecord = (value: unknown): DataRecord => {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    return invalid();
  }
  const prototype: unknown = Object.getPrototypeOf(value);
  if (prototype !== Object.prototype && prototype !== null) {
    return invalid();
  }
  const output = new Map<string, unknown>();
  const sorted = readOwnStringKeys(value).toSorted(compareStrings);
  for (const key of sorted) {
    const descriptor = Object.getOwnPropertyDescriptor(value, key);
    if (
      descriptor === undefined ||
      !("value" in descriptor) ||
      descriptor.enumerable !== true
    ) {
      return invalid();
    }
    output.set(key, descriptor.value);
  }
  return output;
};

const exactRecord = (
  value: unknown,
  expectedKeys: readonly string[]
): DataRecord => {
  const record = readDataRecord(value);
  if (
    record.size !== expectedKeys.length ||
    expectedKeys.some((key) => !record.has(key))
  ) {
    return invalid();
  }
  return record;
};

const recordValue = (record: DataRecord, key: string): unknown => {
  if (!record.has(key)) {
    return invalid();
  }
  return record.get(key);
};

const readDenseArray = (
  value: unknown,
  maximumLength: number
): readonly unknown[] => {
  if (
    !Array.isArray(value) ||
    Object.getPrototypeOf(value) !== Array.prototype
  ) {
    return invalid();
  }
  const sortedKeys = readOwnStringKeys(value).toSorted(compareStrings);
  const descriptors = new Map<string, PropertyDescriptor>();
  for (const key of sortedKeys) {
    const descriptor = Object.getOwnPropertyDescriptor(value, key);
    if (descriptor === undefined || !("value" in descriptor)) {
      return invalid();
    }
    descriptors.set(key, descriptor);
  }
  const lengthDescriptor = descriptors.get("length");
  if (
    lengthDescriptor === undefined ||
    lengthDescriptor.enumerable === true ||
    !isCanonicalSafeInteger(lengthDescriptor.value) ||
    lengthDescriptor.value < 0 ||
    lengthDescriptor.value > maximumLength
  ) {
    return invalid();
  }
  const length = lengthDescriptor.value;
  if (descriptors.size !== length + 1) {
    return invalid();
  }
  const output: unknown[] = [];
  for (let index = 0; index < length; index += 1) {
    const descriptor = descriptors.get(String(index));
    if (descriptor === undefined || descriptor.enumerable !== true) {
      return invalid();
    }
    output.push(descriptor.value);
  }
  return output;
};

const ownDataValue = (value: unknown, key: string): unknown => {
  if (
    (typeof value !== "object" || value === null) &&
    typeof value !== "function"
  ) {
    return undefined;
  }
  const descriptor = Object.getOwnPropertyDescriptor(value, key);
  return descriptor !== undefined && "value" in descriptor
    ? descriptor.value
    : undefined;
};

const scanVersionedString = (value: unknown, prefix: string): void => {
  if (typeof value !== "string" || !value.startsWith(prefix)) {
    return;
  }
  const suffix = value.slice(prefix.length);
  if (/^[1-9][0-9]*$/u.test(suffix) && suffix !== "1") {
    unsupported();
  }
};

const scanCodecVersion = (value: unknown): void => {
  const abi = ownDataValue(value, "abi");
  for (const prefix of CODEC_VERSION_PREFIXES) {
    scanVersionedString(abi, prefix);
  }
};

const scanArrayValues = (
  value: unknown,
  maximum: number
): readonly unknown[] => {
  if (!Array.isArray(value)) {
    return [];
  }
  const length = ownDataValue(value, "length");
  if (!isCanonicalSafeInteger(length) || length < 0 || length > maximum) {
    return [];
  }
  const output: unknown[] = [];
  for (let index = 0; index < length; index += 1) {
    output.push(ownDataValue(value, String(index)));
  }
  return output;
};

const detectUnsupportedFormat = (input: unknown): void => {
  scanVersionedString(
    ownDataValue(input, "generated"),
    "astilba.env.generated-module/v"
  );
  const projection = ownDataValue(input, "projection");
  const format = ownDataValue(projection, "format");
  const formatVersion = ownDataValue(projection, "formatVersion");
  const kind = ownDataValue(projection, "kind");
  if (
    format === "astilba.env.projection" &&
    isCanonicalSafeInteger(formatVersion)
  ) {
    const maximumVersion = kind === "public" ? 1 : 2;
    if (formatVersion > maximumVersion) {
      unsupported();
    }
  }
  scanVersionedString(
    ownDataValue(projection, "canonicalisation"),
    "astilba.jcs/v"
  );
  scanVersionedString(
    ownDataValue(projection, "codecAbi"),
    "astilba.env.codec/v"
  );
  scanVersionedString(
    ownDataValue(projection, "projectionAbi"),
    "astilba.env.projection/v"
  );
  const entries = scanArrayValues(
    ownDataValue(projection, "entries"),
    MAXIMUM_ENTRIES
  );
  for (const entry of entries) {
    scanCodecVersion(ownDataValue(entry, "codec"));
  }
  const rules = scanArrayValues(
    ownDataValue(projection, "rules"),
    MAXIMUM_RULES
  );
  for (const rule of rules) {
    scanVersionedString(
      ownDataValue(rule, "abi"),
      "astilba.env.present-together/v"
    );
  }
};

interface ShapeState {
  readonly ancestors: Set<object>;
  nodes: number;
}

const normalizePortableShape = (
  value: unknown,
  depth: number,
  state: ShapeState
): PortableShape => {
  if (
    typeof value !== "object" ||
    value === null ||
    state.ancestors.has(value) ||
    depth > MAXIMUM_SHAPE_DEPTH
  ) {
    return invalid();
  }
  state.nodes += 1;
  if (state.nodes > MAXIMUM_SHAPE_NODES) {
    return invalid();
  }
  state.ancestors.add(value);
  try {
    const record = readDataRecord(value);
    const kind = recordValue(record, "kind");
    if (kind === "boolean" || kind === "null" || kind === "string") {
      if (record.size !== 1) {
        return invalid();
      }
      return Object.freeze({ kind });
    }
    if (kind === "safe-integer") {
      if (
        record.size !== 3 ||
        !record.has("maximum") ||
        !record.has("minimum")
      ) {
        return invalid();
      }
      const maximum = recordValue(record, "maximum");
      const minimum = recordValue(record, "minimum");
      if (
        !isCanonicalSafeInteger(maximum) ||
        !isCanonicalSafeInteger(minimum) ||
        maximum < minimum
      ) {
        return invalid();
      }
      return Object.freeze({
        kind,
        maximum,
        minimum,
      });
    }
    if (kind === "array") {
      if (
        record.size !== 4 ||
        !record.has("items") ||
        !record.has("maximumItems") ||
        !record.has("minimumItems")
      ) {
        return invalid();
      }
      const maximumItems = recordValue(record, "maximumItems");
      const minimumItems = recordValue(record, "minimumItems");
      if (
        !isCanonicalSafeInteger(maximumItems) ||
        !isCanonicalSafeInteger(minimumItems) ||
        minimumItems < 0 ||
        maximumItems < minimumItems ||
        maximumItems > MAXIMUM_ARRAY_ITEMS
      ) {
        return invalid();
      }
      return Object.freeze({
        items: normalizePortableShape(
          recordValue(record, "items"),
          depth + 1,
          state
        ),
        kind,
        maximumItems,
        minimumItems,
      });
    }
    if (kind !== "object" || record.size !== 2 || !record.has("properties")) {
      return invalid();
    }
    const properties = readDenseArray(
      recordValue(record, "properties"),
      MAXIMUM_OBJECT_PROPERTIES
    );
    const output: {
      name: string;
      required: boolean;
      shape: PortableShape;
    }[] = [];
    let previousName: string | undefined;
    for (const property of properties) {
      const propertyRecord = exactRecord(property, [
        "name",
        "required",
        "shape",
      ]);
      const name = recordValue(propertyRecord, "name");
      const required = recordValue(propertyRecord, "required");
      if (
        !isPortableString(name) ||
        name.length === 0 ||
        TEXT_ENCODER.encode(name).byteLength > 255 ||
        DANGEROUS_PROPERTY_NAMES.has(name) ||
        typeof required !== "boolean" ||
        (previousName !== undefined && compareStrings(previousName, name) >= 0)
      ) {
        return invalid();
      }
      previousName = name;
      output.push(
        Object.freeze({
          name,
          required,
          shape: normalizePortableShape(
            recordValue(propertyRecord, "shape"),
            depth + 1,
            state
          ),
        })
      );
    }
    return Object.freeze({
      kind,
      properties: Object.freeze(output),
    });
  } finally {
    state.ancestors.delete(value);
  }
};

const normalizeShape = (value: unknown): PortableShape =>
  normalizePortableShape(value, 1, {
    ancestors: new Set(),
    nodes: 0,
  });

const normalizeOpaqueShape = (value: unknown): OpaqueShape => {
  if (ownDataValue(value, "kind") !== "optional") {
    return normalizeShape(value);
  }
  const record = exactRecord(value, ["kind", "value"]);
  return Object.freeze({
    kind: "optional" as const,
    value: normalizeShape(recordValue(record, "value")),
  });
};

const normalizeOpaqueInputShape = (value: unknown): OpaqueInputShape => {
  if (ownDataValue(value, "kind") === "optional") {
    const record = exactRecord(value, ["kind", "value"]);
    const inner = exactRecord(recordValue(record, "value"), ["kind"]);
    if (recordValue(inner, "kind") !== "string") {
      return invalid();
    }
    return Object.freeze({
      kind: "optional" as const,
      value: Object.freeze({ kind: "string" as const }),
    });
  }
  const record = exactRecord(value, ["kind"]);
  if (recordValue(record, "kind") !== "string") {
    return invalid();
  }
  return Object.freeze({ kind: "string" as const });
};

const normalizeCodec = (value: unknown): CodecDescriptor => {
  const record = readDataRecord(value);
  const kind = recordValue(record, "kind");
  const abi = recordValue(record, "abi");
  if (kind === "string") {
    const exact = exactRecord(value, [
      "abi",
      "kind",
      "maxCodePoints",
      "minCodePoints",
    ]);
    const maxCodePoints = recordValue(exact, "maxCodePoints");
    const minCodePoints = recordValue(exact, "minCodePoints");
    if (
      abi !== "astilba.env.string-code-point/v1" ||
      !isCanonicalSafeInteger(maxCodePoints) ||
      !isCanonicalSafeInteger(minCodePoints) ||
      minCodePoints < 0 ||
      maxCodePoints < minCodePoints ||
      maxCodePoints > MAXIMUM_STRING_CODE_POINTS
    ) {
      return invalid();
    }
    return Object.freeze({
      abi,
      kind,
      maxCodePoints,
      minCodePoints,
    });
  }
  if (kind === "origin") {
    exactRecord(value, ["abi", "kind"]);
    if (abi !== "astilba.env.origin-ascii/v1") {
      return invalid();
    }
    return Object.freeze({ abi, kind });
  }
  if (kind === "enum") {
    const exact = exactRecord(value, ["abi", "kind", "values"]);
    if (abi !== "astilba.env.enum/v1") {
      return invalid();
    }
    const inputValues = readDenseArray(
      recordValue(exact, "values"),
      MAXIMUM_ENUM_VALUES
    );
    if (inputValues.length === 0) {
      return invalid();
    }
    const values: string[] = [];
    let totalBytes = 0;
    let previous: string | undefined;
    for (const item of inputValues) {
      if (
        !isPortableString(item) ||
        (previous !== undefined && compareStrings(previous, item) >= 0)
      ) {
        return invalid();
      }
      const bytes = TEXT_ENCODER.encode(item).byteLength;
      totalBytes += bytes;
      if (bytes > MAXIMUM_STRING_BYTES || totalBytes > MAXIMUM_ENUM_BYTES) {
        return invalid();
      }
      previous = item;
      values.push(item);
    }
    return Object.freeze({
      abi,
      kind,
      values: Object.freeze(values),
    });
  }
  if (kind === "boolean") {
    const exact = exactRecord(value, [
      "abi",
      "blank",
      "falseInput",
      "kind",
      "trueInput",
    ]);
    const blank = recordValue(exact, "blank");
    const falseInput = recordValue(exact, "falseInput");
    const trueInput = recordValue(exact, "trueInput");
    if (
      abi !== "astilba.env.boolean-exact/v1" ||
      (blank !== "invalid" && blank !== "missing") ||
      typeof falseInput !== "string" ||
      !ASCII_GRAPHIC.test(falseInput) ||
      typeof trueInput !== "string" ||
      !ASCII_GRAPHIC.test(trueInput) ||
      falseInput === trueInput
    ) {
      return invalid();
    }
    return Object.freeze({
      abi,
      blank,
      falseInput,
      kind,
      trueInput,
    });
  }
  if (kind === "safe-integer") {
    const exact = exactRecord(value, [
      "abi",
      "blank",
      "kind",
      "maximum",
      "minimum",
    ]);
    const blank = recordValue(exact, "blank");
    const maximum = recordValue(exact, "maximum");
    const minimum = recordValue(exact, "minimum");
    if (
      abi !== "astilba.env.safe-integer-decimal/v1" ||
      (blank !== "invalid" && blank !== "missing") ||
      !isCanonicalSafeInteger(maximum) ||
      !isCanonicalSafeInteger(minimum) ||
      maximum < minimum
    ) {
      return invalid();
    }
    return Object.freeze({
      abi,
      blank,
      kind,
      maximum,
      minimum,
    });
  }
  if (kind === "string-list") {
    const exact = exactRecord(value, [
      "abi",
      "emptyItems",
      "kind",
      "maximumItemCodePoints",
      "maximumItems",
      "minimumItemCodePoints",
      "minimumItems",
      "separator",
    ]);
    const emptyItems = recordValue(exact, "emptyItems");
    const maximumItemCodePoints = recordValue(exact, "maximumItemCodePoints");
    const maximumItems = recordValue(exact, "maximumItems");
    const minimumItemCodePoints = recordValue(exact, "minimumItemCodePoints");
    const minimumItems = recordValue(exact, "minimumItems");
    if (
      abi !== "astilba.env.string-list-comma/v1" ||
      (emptyItems !== "drop" && emptyItems !== "invalid") ||
      !isCanonicalSafeInteger(maximumItemCodePoints) ||
      !isCanonicalSafeInteger(maximumItems) ||
      !isCanonicalSafeInteger(minimumItemCodePoints) ||
      !isCanonicalSafeInteger(minimumItems) ||
      maximumItemCodePoints < minimumItemCodePoints ||
      maximumItems < minimumItems ||
      minimumItemCodePoints < 1 ||
      minimumItems < 0 ||
      maximumItemCodePoints > 65_536 ||
      maximumItems > MAXIMUM_ARRAY_ITEMS ||
      maximumItems * maximumItemCodePoints > MAXIMUM_LIST_PRODUCT ||
      recordValue(exact, "separator") !== ","
    ) {
      return invalid();
    }
    return Object.freeze({
      abi,
      emptyItems,
      kind,
      maximumItemCodePoints,
      maximumItems,
      minimumItemCodePoints,
      minimumItems,
      separator: "," as const,
    });
  }
  if (kind === "json") {
    const exact = exactRecord(value, ["abi", "blank", "kind", "shape"]);
    const blank = recordValue(exact, "blank");
    if (
      abi !== "astilba.env.json-exact/v1" ||
      (blank !== "invalid" && blank !== "missing")
    ) {
      return invalid();
    }
    return Object.freeze({
      abi,
      blank,
      kind,
      shape: normalizeShape(recordValue(exact, "shape")),
    });
  }
  if (kind === "text") {
    const exact = exactRecord(value, [
      "abi",
      "blank",
      "kind",
      "maxCodePoints",
      "minCodePoints",
      "normalise",
    ]);
    const blank = recordValue(exact, "blank");
    const maxCodePoints = recordValue(exact, "maxCodePoints");
    const minCodePoints = recordValue(exact, "minCodePoints");
    const normalise = recordValue(exact, "normalise");
    if (
      abi !== "astilba.env.text/v1" ||
      (blank !== "invalid" && blank !== "missing") ||
      !isCanonicalSafeInteger(maxCodePoints) ||
      !isCanonicalSafeInteger(minCodePoints) ||
      minCodePoints < 0 ||
      maxCodePoints < minCodePoints ||
      maxCodePoints > MAXIMUM_STRING_CODE_POINTS ||
      (normalise !== "preserve" && normalise !== "trim")
    ) {
      return invalid();
    }
    return Object.freeze({
      abi,
      blank,
      kind,
      maxCodePoints,
      minCodePoints,
      normalise,
    });
  }
  if (kind === "integer") {
    const exact = exactRecord(value, [
      "abi",
      "blank",
      "default",
      "kind",
      "maximum",
      "minimum",
    ]);
    const blank = recordValue(exact, "blank");
    const maximum = recordValue(exact, "maximum");
    const minimum = recordValue(exact, "minimum");
    if (
      abi !== "astilba.env.integer/v1" ||
      (blank !== "invalid" && blank !== "missing") ||
      recordValue(exact, "default") !== null ||
      !isCanonicalSafeInteger(maximum) ||
      !isCanonicalSafeInteger(minimum) ||
      maximum < minimum
    ) {
      return invalid();
    }
    return Object.freeze({
      abi,
      blank,
      default: null,
      kind,
      maximum,
      minimum,
    });
  }
  if (kind !== "opaque") {
    return invalid();
  }
  const exact = exactRecord(value, [
    "abi",
    "input",
    "kind",
    "output",
    "revision",
    "semantics",
  ]);
  const revision = recordValue(exact, "revision");
  const semantics = recordValue(exact, "semantics");
  if (
    abi !== "astilba.env.opaque/v1" ||
    typeof revision !== "string" ||
    !OPAQUE_LABEL.test(revision) ||
    typeof semantics !== "string" ||
    !OPAQUE_LABEL.test(semantics)
  ) {
    return invalid();
  }
  return Object.freeze({
    abi,
    input: normalizeOpaqueInputShape(recordValue(exact, "input")),
    kind,
    output: normalizeOpaqueShape(recordValue(exact, "output")),
    revision,
    semantics,
  });
};

const normalizeIdentity = (value: unknown): EntryIdentity => {
  const tuple = readDenseArray(value, 2);
  if (tuple.length !== 2 || !isContractId(tuple[0]) || !isLocalId(tuple[1])) {
    return invalid();
  }
  return Object.freeze([tuple[0], tuple[1]] as const);
};

const identityKey = (identity: EntryIdentity): string =>
  `${identity[0]}\u0000${identity[1]}`;

function normalizeEntry(value: unknown, kind: "public"): PublicProjectionEntry;
function normalizeEntry(value: unknown, kind: "server"): ServerProjectionEntry;
function normalizeEntry(
  value: unknown,
  kind: "public" | "server"
): PublicProjectionEntry | ServerProjectionEntry {
  const expected =
    kind === "public"
      ? ["codec", "identity", "lifecycle", "name", "required"]
      : ["codec", "identity", "lifecycle", "name", "required", "visibility"];
  const record = exactRecord(value, expected);
  const codec = normalizeCodec(recordValue(record, "codec"));
  const identity = normalizeIdentity(recordValue(record, "identity"));
  const lifecycle = recordValue(record, "lifecycle");
  const name = recordValue(record, "name");
  const required = recordValue(record, "required");
  if (
    (lifecycle !== "build" &&
      lifecycle !== "deployment" &&
      lifecycle !== "request") ||
    !isLocalId(name) ||
    typeof required !== "boolean"
  ) {
    return invalid();
  }
  if (kind === "public") {
    if (
      codec.kind === "integer" ||
      codec.kind === "opaque" ||
      codec.kind === "text"
    ) {
      return invalid();
    }
    return Object.freeze({
      codec,
      identity,
      lifecycle,
      name,
      required,
    });
  }
  const visibility = recordValue(record, "visibility");
  if (
    (visibility !== "private" && visibility !== "public") ||
    (visibility === "private" && lifecycle === "build") ||
    (codec.kind === "opaque" &&
      (visibility !== "private" || lifecycle === "build"))
  ) {
    return invalid();
  }
  return Object.freeze({
    codec,
    identity,
    lifecycle,
    name,
    required,
    visibility,
  });
}

const normalizeProjectionEntries = <
  TEntry extends PublicProjectionEntry | ServerProjectionEntry,
>(
  inputEntries: readonly unknown[],
  normalize: (value: unknown) => TEntry
): readonly TEntry[] => {
  const entries: TEntry[] = [];
  const identities = new Set<string>();
  const foldedNames = new Set<string>();
  let previousName: string | undefined;
  for (const inputEntry of inputEntries) {
    const entry = normalize(inputEntry);
    const identity = identityKey(entry.identity);
    const foldedName = asciiCaseFold(entry.name);
    if (
      identities.has(identity) ||
      foldedNames.has(foldedName) ||
      (previousName !== undefined &&
        compareStrings(previousName, entry.name) >= 0)
    ) {
      return invalid();
    }
    identities.add(identity);
    foldedNames.add(foldedName);
    previousName = entry.name;
    entries.push(entry);
  }
  return Object.freeze(entries);
};

const normalizeRule = (
  value: unknown,
  entries: ReadonlyMap<string, ServerProjectionEntry>
): PresentTogetherRule => {
  const record = exactRecord(value, ["abi", "entries", "id", "kind"]);
  const id = recordValue(record, "id");
  if (
    recordValue(record, "abi") !== "astilba.env.present-together/v1" ||
    !isLocalId(id) ||
    recordValue(record, "kind") !== "present-together"
  ) {
    return invalid();
  }
  const inputEntries = readDenseArray(
    recordValue(record, "entries"),
    MAXIMUM_RULE_ENTRY_REFERENCES
  );
  if (inputEntries.length < 2) {
    return invalid();
  }
  const identities: EntryIdentity[] = [];
  let previous: string | undefined;
  let lifecycle: Lifecycle | undefined;
  for (const input of inputEntries) {
    const identity = normalizeIdentity(input);
    const key = identityKey(identity);
    const entry = entries.get(key);
    if (
      entry === undefined ||
      (previous !== undefined && compareStrings(previous, key) >= 0) ||
      (lifecycle !== undefined && lifecycle !== entry.lifecycle)
    ) {
      return invalid();
    }
    previous = key;
    lifecycle = entry.lifecycle;
    identities.push(identity);
  }
  return Object.freeze({
    abi: "astilba.env.present-together/v1" as const,
    entries: Object.freeze(identities),
    id,
    kind: "present-together" as const,
  });
};

const normalizeProjection = (value: unknown): ProcessProjection => {
  const discriminator = readDataRecord(value);
  const kind = recordValue(discriminator, "kind");
  const formatVersion = recordValue(discriminator, "formatVersion");
  const expectedKeys =
    kind === "server" && formatVersion === 2
      ? [
          "canonicalisation",
          "codecAbi",
          "consumer",
          "contract",
          "entries",
          "format",
          "formatVersion",
          "kind",
          "projectionAbi",
          "rules",
        ]
      : [
          "canonicalisation",
          "codecAbi",
          "consumer",
          "contract",
          "entries",
          "format",
          "formatVersion",
          "kind",
          "projectionAbi",
        ];
  const record = exactRecord(value, expectedKeys);
  const consumer = recordValue(record, "consumer");
  const contract = recordValue(record, "contract");
  if (
    recordValue(record, "canonicalisation") !== "astilba.jcs/v1" ||
    recordValue(record, "codecAbi") !== "astilba.env.codec/v1" ||
    !isLocalId(consumer) ||
    !isContractId(contract) ||
    recordValue(record, "format") !== "astilba.env.projection" ||
    recordValue(record, "projectionAbi") !== "astilba.env.projection/v1" ||
    (kind !== "public" && kind !== "server") ||
    (formatVersion !== 1 && !(kind === "server" && formatVersion === 2))
  ) {
    return invalid();
  }
  const inputEntries = readDenseArray(
    recordValue(record, "entries"),
    MAXIMUM_ENTRIES
  );
  if (inputEntries.length === 0) {
    return invalid();
  }
  const base = {
    canonicalisation: "astilba.jcs/v1" as const,
    codecAbi: "astilba.env.codec/v1" as const,
    consumer,
    contract,
    format: "astilba.env.projection" as const,
    projectionAbi: "astilba.env.projection/v1" as const,
  };
  if (kind === "public") {
    const entries = normalizeProjectionEntries(inputEntries, (entry) =>
      normalizeEntry(entry, "public")
    );
    return Object.freeze({
      ...base,
      entries,
      formatVersion: 1 as const,
      kind,
    });
  }
  const serverEntries = normalizeProjectionEntries(inputEntries, (entry) =>
    normalizeEntry(entry, "server")
  );
  if (formatVersion === 1) {
    return Object.freeze({
      ...base,
      entries: serverEntries,
      formatVersion,
      kind,
    });
  }
  const byIdentity = new Map(
    serverEntries.map((entry) => [identityKey(entry.identity), entry])
  );
  const inputRules = readDenseArray(
    recordValue(record, "rules"),
    MAXIMUM_RULES
  );
  if (inputRules.length === 0) {
    return invalid();
  }
  const rules: PresentTogetherRule[] = [];
  const foldedRuleIds = new Set<string>();
  let previousRuleId: string | undefined;
  let references = 0;
  for (const inputRule of inputRules) {
    const rule = normalizeRule(inputRule, byIdentity);
    references += rule.entries.length;
    const foldedId = asciiCaseFold(rule.id);
    if (
      references > MAXIMUM_RULE_ENTRY_REFERENCES ||
      foldedRuleIds.has(foldedId) ||
      (previousRuleId !== undefined &&
        compareStrings(previousRuleId, rule.id) >= 0)
    ) {
      return invalid();
    }
    foldedRuleIds.add(foldedId);
    previousRuleId = rule.id;
    rules.push(rule);
  }
  return Object.freeze({
    ...base,
    entries: serverEntries,
    formatVersion,
    kind,
    rules: Object.freeze(rules),
  });
};

/**
 * Exact projection normalization shared with provider conformance.
 *
 * @internal
 */
export const normalizeProjectionForProvider = (
  value: unknown
): ProcessProjection => normalizeProjection(value);

const normalizeBindings = (
  value: unknown,
  projection: ProcessProjection,
  lifecycle: Lifecycle
): Readonly<{
  bindings: readonly Readonly<{
    entry: string;
    source: string;
  }>[];
  selected: readonly (PublicProjectionEntry | ServerProjectionEntry)[];
}> => {
  const selected = projection.entries.filter(
    (entry) => entry.lifecycle === lifecycle
  );
  if (selected.length === 0) {
    return invalid();
  }
  const inputBindings = readDenseArray(value, MAXIMUM_ENTRIES);
  if (inputBindings.length !== selected.length) {
    return invalid();
  }
  const bindings: {
    entry: string;
    source: string;
  }[] = [];
  const foldedSources = new Set<string>();
  for (const [index, inputBinding] of inputBindings.entries()) {
    const record = exactRecord(inputBinding, ["entry", "source"]);
    const entry = recordValue(record, "entry");
    const source = recordValue(record, "source");
    if (
      !isLocalId(entry) ||
      !isRawSourceName(source) ||
      entry !== selected[index]?.name ||
      foldedSources.has(asciiCaseFold(source))
    ) {
      return invalid();
    }
    foldedSources.add(asciiCaseFold(source));
    bindings.push(Object.freeze({ entry, source }));
  }
  return Object.freeze({
    bindings: Object.freeze(bindings),
    selected: Object.freeze([...selected]),
  });
};

export const validateProcessTarget = (input: unknown): ValidationResult => {
  try {
    detectUnsupportedFormat(input);
    const record = exactRecord(input, [
      "bindings",
      "generated",
      "lifecycle",
      "projection",
    ]);
    const generated = recordValue(record, "generated");
    const lifecycle = recordValue(record, "lifecycle");
    if (
      generated !== GENERATED_MODULE ||
      (lifecycle !== "build" &&
        lifecycle !== "deployment" &&
        lifecycle !== "request")
    ) {
      return invalid();
    }
    const projection = normalizeProjection(recordValue(record, "projection"));
    const { bindings, selected } = normalizeBindings(
      recordValue(record, "bindings"),
      projection,
      lifecycle
    );
    return Object.freeze({
      ok: true as const,
      target: Object.freeze({
        bindings,
        generated,
        lifecycle,
        projection,
        selected,
      }),
    });
  } catch (error) {
    return Object.freeze({
      code:
        error === UNSUPPORTED_FORMAT
          ? "ENV_FORMAT_UNSUPPORTED"
          : "ENV_CONTRACT_INVALID",
      ok: false as const,
    });
  }
};

export const validateSchemaMap = (
  value: unknown,
  expectedNames: readonly string[]
):
  | Readonly<{
      ok: false;
    }>
  | Readonly<{
      ok: true;
      schemas: ReadonlyMap<string, unknown>;
    }> => {
  try {
    const record = readDataRecord(value);
    if (
      record.size !== expectedNames.length ||
      expectedNames.some((name) => !record.has(name))
    ) {
      return Object.freeze({ ok: false as const });
    }
    const schemas = new Map<string, unknown>();
    for (const name of expectedNames) {
      schemas.set(name, recordValue(record, name));
    }
    return Object.freeze({
      ok: true as const,
      schemas,
    });
  } catch {
    return Object.freeze({ ok: false as const });
  }
};
