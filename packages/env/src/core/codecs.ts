import { parseBoundedJsonValue, PORTABLE_JSON_LIMITS } from "./bounded-json.ts";
import { ContractDefinitionError } from "./diagnostics.ts";
import { isPortableString } from "./json.ts";
import {
  copyPortableShapeValue,
  normalizeOpaqueInputShape,
  normalizeOpaqueShape,
  normalizePortableShape,
} from "./shapes.ts";
import type {
  OpaqueInputShapeDescriptor,
  PortableShapeDescriptor,
  PortableShapeValue,
} from "./shapes.ts";
import {
  BOOLEAN_CODEC_ABI,
  ENUM_CODEC_ABI,
  JSON_CODEC_ABI,
  OPAQUE_CODEC_ABI,
  ORIGIN_CODEC_ABI,
  SAFE_INTEGER_CODEC_ABI,
  STRING_CODEC_ABI,
  STRING_LIST_CODEC_ABI,
} from "./types.ts";
import type {
  BooleanCodecDescriptor,
  CodecDescriptor,
  Compatibility,
  EnumCodecDescriptor,
  JsonValue,
  JsonCodecDescriptor,
  OpaqueCodecDescriptor,
  OpaqueShapeDescriptor,
  OriginCodecDescriptor,
  PortableCodecDescriptor,
  PublicCodecDescriptor,
  SafeIntegerCodecDescriptor,
  StringCodecDescriptor,
  StringListCodecDescriptor,
} from "./types.ts";

type PortableValueFailure = Readonly<{
  code: "ENV_INVALID_VALUE";
  ok: false;
}>;

type PortableValueSuccess<Value extends JsonValue = string> = Readonly<{
  ok: true;
  value: Value;
}>;

export type PortableValueResult<Value extends JsonValue = string> =
  | PortableValueFailure
  | PortableValueSuccess<Value>;

type PortableResolutionMissing = Readonly<{
  ok: true;
  present: false;
}>;

type PortableResolutionSuccess<Value extends JsonValue = JsonValue> = Readonly<{
  ok: true;
  present: true;
  value: Value;
}>;

export type PortableResolutionResult<Value extends JsonValue = JsonValue> =
  | PortableResolutionMissing
  | PortableResolutionSuccess<Value>
  | PortableValueFailure;

const DNS_LABEL = /^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/u;

function invalidValue(): PortableValueFailure {
  return Object.freeze({
    code: "ENV_INVALID_VALUE" as const,
    ok: false as const,
  });
}

function validValue<Value extends JsonValue>(
  value: Value
): PortableValueSuccess<Value> {
  return Object.freeze({ ok: true as const, value });
}

function missingValue(): PortableResolutionMissing {
  return Object.freeze({
    ok: true as const,
    present: false as const,
  });
}

function resolvedValue<Value extends JsonValue>(
  value: Value
): PortableResolutionSuccess<Value> {
  return Object.freeze({
    ok: true as const,
    present: true as const,
    value,
  });
}

function isPlainDataRecord(
  value: unknown,
  expectedKeys: readonly string[]
): value is Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    return false;
  }
  const prototype = Reflect.getPrototypeOf(value);
  if (prototype !== Object.prototype && prototype !== null) {
    return false;
  }

  const keys = Reflect.ownKeys(value);
  if (
    keys.length !== expectedKeys.length ||
    keys.some((key) => typeof key !== "string")
  ) {
    return false;
  }

  const expected = new Set(expectedKeys);
  for (const key of keys) {
    if (typeof key !== "string") {
      return false;
    }
    if (!expected.has(key)) {
      return false;
    }
    const descriptor = Object.getOwnPropertyDescriptor(value, key);
    if (
      descriptor === undefined ||
      !("value" in descriptor) ||
      descriptor.enumerable !== true
    ) {
      return false;
    }
  }
  return true;
}

function ownValue(record: Record<string, unknown>, key: string): unknown {
  const descriptor = Object.getOwnPropertyDescriptor(record, key);
  if (descriptor === undefined || !("value" in descriptor)) {
    throw new ContractDefinitionError();
  }
  return descriptor.value;
}

function normaliseStringCodec(descriptor: unknown): StringCodecDescriptor {
  if (
    !isPlainDataRecord(descriptor, [
      "abi",
      "kind",
      "maxCodePoints",
      "minCodePoints",
    ])
  ) {
    throw new ContractDefinitionError();
  }

  const abi = ownValue(descriptor, "abi");
  const kind = ownValue(descriptor, "kind");
  const maxCodePoints = ownValue(descriptor, "maxCodePoints");
  const minCodePoints = ownValue(descriptor, "minCodePoints");

  if (
    abi !== STRING_CODEC_ABI ||
    kind !== "string" ||
    typeof maxCodePoints !== "number" ||
    typeof minCodePoints !== "number" ||
    !Number.isSafeInteger(maxCodePoints) ||
    !Number.isSafeInteger(minCodePoints) ||
    minCodePoints < 0 ||
    maxCodePoints < minCodePoints
  ) {
    throw new ContractDefinitionError();
  }

  return Object.freeze({
    abi: STRING_CODEC_ABI,
    kind: "string" as const,
    maxCodePoints,
    minCodePoints,
  });
}

function normaliseOriginCodec(descriptor: unknown): OriginCodecDescriptor {
  if (!isPlainDataRecord(descriptor, ["abi", "kind"])) {
    throw new ContractDefinitionError();
  }
  if (
    ownValue(descriptor, "abi") !== ORIGIN_CODEC_ABI ||
    ownValue(descriptor, "kind") !== "origin"
  ) {
    throw new ContractDefinitionError();
  }
  return Object.freeze({
    abi: ORIGIN_CODEC_ABI,
    kind: "origin" as const,
  });
}

function normaliseEnumCodec(descriptor: unknown): EnumCodecDescriptor {
  if (!isPlainDataRecord(descriptor, ["abi", "kind", "values"])) {
    throw new ContractDefinitionError();
  }
  const values = ownValue(descriptor, "values");
  if (
    ownValue(descriptor, "abi") !== ENUM_CODEC_ABI ||
    ownValue(descriptor, "kind") !== "enum" ||
    !Array.isArray(values) ||
    Object.getPrototypeOf(values) !== Array.prototype ||
    values.length === 0
  ) {
    throw new ContractDefinitionError();
  }
  const valueKeys = Reflect.ownKeys(values);
  if (
    valueKeys.length !== values.length + 1 ||
    valueKeys.some(
      (key) =>
        typeof key !== "string" ||
        (key !== "length" &&
          (!/^(?:0|[1-9][0-9]*)$/u.test(key) || Number(key) >= values.length))
    )
  ) {
    throw new ContractDefinitionError();
  }

  const normalisedValues: string[] = [];
  for (let index = 0; index < values.length; index += 1) {
    const item = Object.getOwnPropertyDescriptor(values, String(index));
    if (
      item === undefined ||
      !("value" in item) ||
      !isPortableString(item.value)
    ) {
      throw new ContractDefinitionError();
    }
    normalisedValues.push(item.value);
  }
  normalisedValues.sort();
  if (
    normalisedValues.some(
      (value, index) => index > 0 && value === normalisedValues[index - 1]
    )
  ) {
    throw new ContractDefinitionError();
  }

  return Object.freeze({
    abi: ENUM_CODEC_ABI,
    kind: "enum" as const,
    values: Object.freeze(normalisedValues),
  });
}

function isBooleanInput(value: unknown): value is string {
  return (
    isPortableString(value) &&
    value.length > 0 &&
    new TextEncoder().encode(value).byteLength <= 65_535 &&
    /^[\u0021-\u007E]+$/u.test(value)
  );
}

function normaliseBooleanCodec(descriptor: unknown): BooleanCodecDescriptor {
  if (
    !isPlainDataRecord(descriptor, [
      "abi",
      "blank",
      "falseInput",
      "kind",
      "trueInput",
    ])
  ) {
    throw new ContractDefinitionError();
  }

  const blank = ownValue(descriptor, "blank");
  const falseInput = ownValue(descriptor, "falseInput");
  const trueInput = ownValue(descriptor, "trueInput");
  if (
    ownValue(descriptor, "abi") !== BOOLEAN_CODEC_ABI ||
    (blank !== "invalid" && blank !== "missing") ||
    !isBooleanInput(falseInput) ||
    ownValue(descriptor, "kind") !== "boolean" ||
    !isBooleanInput(trueInput) ||
    falseInput === trueInput
  ) {
    throw new ContractDefinitionError();
  }

  return Object.freeze({
    abi: BOOLEAN_CODEC_ABI,
    blank,
    falseInput,
    kind: "boolean" as const,
    trueInput,
  });
}

function isCanonicalSafeInteger(value: unknown): value is number {
  return (
    typeof value === "number" &&
    Number.isSafeInteger(value) &&
    !Object.is(value, -0)
  );
}

function normaliseSafeIntegerCodec(
  descriptor: unknown
): SafeIntegerCodecDescriptor {
  if (
    !isPlainDataRecord(descriptor, [
      "abi",
      "blank",
      "kind",
      "maximum",
      "minimum",
    ])
  ) {
    throw new ContractDefinitionError();
  }

  const blank = ownValue(descriptor, "blank");
  const maximum = ownValue(descriptor, "maximum");
  const minimum = ownValue(descriptor, "minimum");
  if (
    ownValue(descriptor, "abi") !== SAFE_INTEGER_CODEC_ABI ||
    (blank !== "invalid" && blank !== "missing") ||
    ownValue(descriptor, "kind") !== "safe-integer" ||
    !isCanonicalSafeInteger(maximum) ||
    !isCanonicalSafeInteger(minimum) ||
    maximum < minimum
  ) {
    throw new ContractDefinitionError();
  }

  return Object.freeze({
    abi: SAFE_INTEGER_CODEC_ABI,
    blank,
    kind: "safe-integer" as const,
    maximum,
    minimum,
  });
}

function normaliseStringListCodec(
  descriptor: unknown
): StringListCodecDescriptor {
  if (
    !isPlainDataRecord(descriptor, [
      "abi",
      "emptyItems",
      "kind",
      "maximumItemCodePoints",
      "maximumItems",
      "minimumItemCodePoints",
      "minimumItems",
      "separator",
    ])
  ) {
    throw new ContractDefinitionError();
  }

  const emptyItems = ownValue(descriptor, "emptyItems");
  const maximumItemCodePoints = ownValue(descriptor, "maximumItemCodePoints");
  const maximumItems = ownValue(descriptor, "maximumItems");
  const minimumItemCodePoints = ownValue(descriptor, "minimumItemCodePoints");
  const minimumItems = ownValue(descriptor, "minimumItems");
  if (
    ownValue(descriptor, "abi") !== STRING_LIST_CODEC_ABI ||
    (emptyItems !== "drop" && emptyItems !== "invalid") ||
    ownValue(descriptor, "kind") !== "string-list" ||
    !isCanonicalSafeInteger(maximumItemCodePoints) ||
    !isCanonicalSafeInteger(maximumItems) ||
    !isCanonicalSafeInteger(minimumItemCodePoints) ||
    !isCanonicalSafeInteger(minimumItems) ||
    maximumItemCodePoints < minimumItemCodePoints ||
    maximumItems < minimumItems ||
    minimumItemCodePoints < 1 ||
    minimumItems < 0 ||
    maximumItemCodePoints > 65_536 ||
    maximumItems > 1024 ||
    maximumItems * maximumItemCodePoints > 65_536 ||
    ownValue(descriptor, "separator") !== ","
  ) {
    throw new ContractDefinitionError();
  }

  return Object.freeze({
    abi: STRING_LIST_CODEC_ABI,
    emptyItems,
    kind: "string-list" as const,
    maximumItemCodePoints,
    maximumItems,
    minimumItemCodePoints,
    minimumItems,
    separator: "," as const,
  });
}

function normaliseJsonCodec(descriptor: unknown): JsonCodecDescriptor {
  if (!isPlainDataRecord(descriptor, ["abi", "blank", "kind", "shape"])) {
    throw new ContractDefinitionError();
  }
  const blank = ownValue(descriptor, "blank");
  if (
    ownValue(descriptor, "abi") !== JSON_CODEC_ABI ||
    (blank !== "invalid" && blank !== "missing") ||
    ownValue(descriptor, "kind") !== "json"
  ) {
    throw new ContractDefinitionError();
  }
  return Object.freeze({
    abi: JSON_CODEC_ABI,
    blank,
    kind: "json" as const,
    shape: normalizePortableShape(
      // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- The enclosing data-record validation establishes shape's descriptor boundary.
      ownValue(descriptor, "shape") as PortableShapeDescriptor
    ),
  });
}

function isOpaqueLabel(value: unknown): value is string {
  return (
    isPortableString(value) &&
    value.length > 0 &&
    value.length <= 255 &&
    /^[\u0021-\u007E]+$/u.test(value)
  );
}

function normaliseOpaqueCodec(descriptor: unknown): OpaqueCodecDescriptor {
  if (
    !isPlainDataRecord(descriptor, [
      "abi",
      "input",
      "kind",
      "output",
      "revision",
      "semantics",
    ])
  ) {
    throw new ContractDefinitionError();
  }
  if (
    ownValue(descriptor, "abi") !== OPAQUE_CODEC_ABI ||
    ownValue(descriptor, "kind") !== "opaque"
  ) {
    throw new ContractDefinitionError();
  }

  const revision = ownValue(descriptor, "revision");
  const semantics = ownValue(descriptor, "semantics");
  if (!isOpaqueLabel(revision) || !isOpaqueLabel(semantics)) {
    throw new ContractDefinitionError();
  }

  return Object.freeze({
    abi: OPAQUE_CODEC_ABI,
    input: normalizeOpaqueInputShape(
      // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- The enclosing data-record validation establishes input's descriptor boundary.
      ownValue(descriptor, "input") as OpaqueInputShapeDescriptor
    ),
    kind: "opaque" as const,
    output: normalizeOpaqueShape(
      // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- The enclosing data-record validation establishes output's descriptor boundary.
      ownValue(descriptor, "output") as OpaqueShapeDescriptor
    ),
    revision,
    semantics,
  });
}

export function normalizePortableCodecDescriptor(
  descriptor: OpaqueCodecDescriptor | PortableCodecDescriptor
): OpaqueCodecDescriptor | PortableCodecDescriptor {
  if (typeof descriptor !== "object" || descriptor === null) {
    throw new ContractDefinitionError();
  }
  const kindDescriptor = Object.getOwnPropertyDescriptor(descriptor, "kind");
  if (kindDescriptor === undefined || !("value" in kindDescriptor)) {
    throw new ContractDefinitionError();
  }

  switch (kindDescriptor.value) {
    case "enum": {
      return normaliseEnumCodec(descriptor);
    }
    case "opaque": {
      return normaliseOpaqueCodec(descriptor);
    }
    case "origin": {
      return normaliseOriginCodec(descriptor);
    }
    case "string": {
      return normaliseStringCodec(descriptor);
    }
    default: {
      throw new ContractDefinitionError();
    }
  }
}

export function normalizePublicCodecDescriptor(
  descriptor: PublicCodecDescriptor
): PublicCodecDescriptor {
  if (typeof descriptor !== "object" || descriptor === null) {
    throw new ContractDefinitionError();
  }
  const kindDescriptor = Object.getOwnPropertyDescriptor(descriptor, "kind");
  if (kindDescriptor === undefined || !("value" in kindDescriptor)) {
    throw new ContractDefinitionError();
  }

  switch (kindDescriptor.value) {
    case "boolean": {
      return normaliseBooleanCodec(descriptor);
    }
    case "enum": {
      return normaliseEnumCodec(descriptor);
    }
    case "json": {
      return normaliseJsonCodec(descriptor);
    }
    case "origin": {
      return normaliseOriginCodec(descriptor);
    }
    case "safe-integer": {
      return normaliseSafeIntegerCodec(descriptor);
    }
    case "string": {
      return normaliseStringCodec(descriptor);
    }
    case "string-list": {
      return normaliseStringListCodec(descriptor);
    }
    default: {
      throw new ContractDefinitionError();
    }
  }
}

export function booleanCodec(options: {
  blank: "invalid" | "missing";
  falseInput: string;
  trueInput: string;
}): BooleanCodecDescriptor {
  return normaliseBooleanCodec({
    abi: BOOLEAN_CODEC_ABI,
    blank: options.blank,
    falseInput: options.falseInput,
    kind: "boolean",
    trueInput: options.trueInput,
  });
}

export function safeIntegerCodec(options: {
  blank: "invalid" | "missing";
  maximum: number;
  minimum: number;
}): SafeIntegerCodecDescriptor {
  return normaliseSafeIntegerCodec({
    abi: SAFE_INTEGER_CODEC_ABI,
    blank: options.blank,
    kind: "safe-integer",
    maximum: options.maximum,
    minimum: options.minimum,
  });
}

export function stringListCodec(options: {
  emptyItems: "drop" | "invalid";
  maximumItemCodePoints: number;
  maximumItems: number;
  minimumItemCodePoints: number;
  minimumItems: number;
}): StringListCodecDescriptor {
  return normaliseStringListCodec({
    abi: STRING_LIST_CODEC_ABI,
    emptyItems: options.emptyItems,
    kind: "string-list",
    maximumItemCodePoints: options.maximumItemCodePoints,
    maximumItems: options.maximumItems,
    minimumItemCodePoints: options.minimumItemCodePoints,
    minimumItems: options.minimumItems,
    separator: ",",
  });
}

export function jsonCodec<
  const TShape extends PortableShapeDescriptor,
>(options: {
  blank: "invalid" | "missing";
  shape: TShape;
}): JsonCodecDescriptor<TShape> {
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- Normalization preserves the generic shape supplied to this constructor.
  return normaliseJsonCodec({
    abi: JSON_CODEC_ABI,
    blank: options.blank,
    kind: "json",
    shape: options.shape,
  }) as JsonCodecDescriptor<TShape>;
}

export function stringCodec(options: {
  maxCodePoints: number;
  minCodePoints: number;
}): StringCodecDescriptor {
  return normaliseStringCodec({
    abi: STRING_CODEC_ABI,
    kind: "string",
    maxCodePoints: options.maxCodePoints,
    minCodePoints: options.minCodePoints,
  });
}

export function originCodec(): OriginCodecDescriptor {
  return normaliseOriginCodec({
    abi: ORIGIN_CODEC_ABI,
    kind: "origin",
  });
}

export function enumCodec(values: readonly string[]): EnumCodecDescriptor {
  return normaliseEnumCodec({
    abi: ENUM_CODEC_ABI,
    kind: "enum",
    values: [...values],
  });
}

export function opaqueCodec<
  const TInput extends OpaqueInputShapeDescriptor,
  const TOutput extends OpaqueShapeDescriptor,
>(options: {
  input: TInput;
  output: TOutput;
  revision: string;
  semantics: string;
}): OpaqueCodecDescriptor<TInput, TOutput> {
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- Normalization preserves both generic opaque shapes supplied to this constructor.
  return normaliseOpaqueCodec({
    abi: OPAQUE_CODEC_ABI,
    input: options.input,
    kind: "opaque",
    output: options.output,
    revision: options.revision,
    semantics: options.semantics,
  }) as OpaqueCodecDescriptor<TInput, TOutput>;
}

function validateString(
  descriptor: StringCodecDescriptor,
  input: unknown
): PortableValueResult {
  if (
    typeof input !== "string" ||
    input.length > descriptor.maxCodePoints * 2 ||
    !isPortableString(input)
  ) {
    return invalidValue();
  }
  // oxlint-disable-next-line unicorn/prefer-spread -- Array.from avoids the type-aware string-spread hazard while retaining code-point counting.
  const codePoints = Array.from(input).length;
  if (
    codePoints < descriptor.minCodePoints ||
    codePoints > descriptor.maxCodePoints
  ) {
    return invalidValue();
  }
  return validValue(input);
}

function validateOrigin(input: unknown): PortableValueResult {
  if (!isPortableString(input)) {
    return invalidValue();
  }
  for (let index = 0; index < input.length; index += 1) {
    const code = input.codePointAt(index);
    if (code === undefined || code > 0x7f) {
      return invalidValue();
    }
  }
  if (!input.startsWith("https://")) {
    return invalidValue();
  }

  let authority = input.slice("https://".length);
  if (authority.endsWith("/")) {
    authority = authority.slice(0, -1);
  }
  if (
    authority.length === 0 ||
    authority.includes("/") ||
    authority.includes("?") ||
    authority.includes("#") ||
    authority.includes("@")
  ) {
    return invalidValue();
  }

  const colon = authority.indexOf(":");
  if (colon !== authority.lastIndexOf(":")) {
    return invalidValue();
  }
  const host = colon === -1 ? authority : authority.slice(0, colon);
  const port = colon === -1 ? undefined : authority.slice(colon + 1);

  if (
    host.length === 0 ||
    host.length > 253 ||
    host.endsWith(".") ||
    host === "localhost"
  ) {
    return invalidValue();
  }
  const labels = host.split(".");
  if (
    labels.length < 2 ||
    /^(?:(?:0x[\da-f]+|\d+)\.){1,3}0x[\da-f]+$/u.test(host) ||
    !/[a-z][a-z0-9-]*$/u.test(host) ||
    labels.some(
      (label) =>
        label.length === 0 || label.length > 63 || !DNS_LABEL.test(label)
    )
  ) {
    return invalidValue();
  }

  if (port !== undefined) {
    if (
      !/^[1-9][0-9]*$/u.test(port) ||
      port.length > 5 ||
      Number(port) > 65_535
    ) {
      return invalidValue();
    }
  }

  return validValue(
    `https://${host}${port === undefined || port === "443" ? "" : `:${port}`}`
  );
}

function validateBoolean(input: unknown): PortableValueResult<boolean> {
  return typeof input === "boolean" ? validValue(input) : invalidValue();
}

function validateSafeInteger(
  descriptor: SafeIntegerCodecDescriptor,
  input: unknown
): PortableValueResult<number> {
  return isCanonicalSafeInteger(input) &&
    input >= descriptor.minimum &&
    input <= descriptor.maximum
    ? validValue(input)
    : invalidValue();
}

function validateStringList(
  descriptor: StringListCodecDescriptor,
  input: unknown
): PortableValueResult<readonly string[]> {
  try {
    if (
      !Array.isArray(input) ||
      Object.getPrototypeOf(input) !== Array.prototype ||
      input.length < descriptor.minimumItems ||
      input.length > descriptor.maximumItems
    ) {
      return invalidValue();
    }

    const keys = Reflect.ownKeys(input);
    if (
      keys.length !== input.length + 1 ||
      keys.some((key) => typeof key !== "string")
    ) {
      return invalidValue();
    }

    const copy: string[] = [];
    for (let index = 0; index < input.length; index += 1) {
      const item = Object.getOwnPropertyDescriptor(input, String(index));
      if (
        item === undefined ||
        !("value" in item) ||
        item.enumerable !== true ||
        typeof item.value !== "string" ||
        item.value.length > descriptor.maximumItemCodePoints * 2 ||
        !isPortableString(item.value)
      ) {
        return invalidValue();
      }
      // oxlint-disable-next-line unicorn/prefer-spread -- Array.from avoids the type-aware string-spread hazard while retaining code-point counting.
      const codePoints = Array.from(item.value).length;
      if (
        codePoints < descriptor.minimumItemCodePoints ||
        codePoints > descriptor.maximumItemCodePoints
      ) {
        return invalidValue();
      }
      copy.push(item.value);
    }
    return validValue(Object.freeze(copy));
  } catch {
    return invalidValue();
  }
}

function validateJsonShape<TShape extends PortableShapeDescriptor>(
  descriptor: JsonCodecDescriptor<TShape>,
  input: unknown
): PortableValueResult<PortableShapeValue<TShape>> {
  try {
    return validValue(copyPortableShapeValue(descriptor.shape, input));
  } catch {
    return invalidValue();
  }
}

export function validatePortableValue(
  descriptor: PortableCodecDescriptor,
  input: unknown
): PortableValueResult;
export function validatePortableValue(
  descriptor: BooleanCodecDescriptor,
  input: unknown
): PortableValueResult<boolean>;
export function validatePortableValue(
  descriptor: SafeIntegerCodecDescriptor,
  input: unknown
): PortableValueResult<number>;
export function validatePortableValue(
  descriptor: StringListCodecDescriptor,
  input: unknown
): PortableValueResult<readonly string[]>;
export function validatePortableValue<TShape extends PortableShapeDescriptor>(
  descriptor: JsonCodecDescriptor<TShape>,
  input: unknown
): PortableValueResult<PortableShapeValue<TShape>>;
export function validatePortableValue(
  descriptor: PublicCodecDescriptor,
  input: unknown
): PortableValueResult<JsonValue>;
export function validatePortableValue(
  descriptor: PublicCodecDescriptor,
  input: unknown
): PortableValueResult<JsonValue> {
  switch (descriptor.kind) {
    case "boolean": {
      return validateBoolean(input);
    }
    case "enum": {
      return isPortableString(input) && descriptor.values.includes(input)
        ? validValue(input)
        : invalidValue();
    }
    case "json": {
      return validateJsonShape(descriptor, input);
    }
    case "origin": {
      return validateOrigin(input);
    }
    case "safe-integer": {
      return validateSafeInteger(descriptor, input);
    }
    case "string": {
      return validateString(descriptor, input);
    }
    case "string-list": {
      return validateStringList(descriptor, input);
    }
  }
}

export function resolvePortableValue(
  descriptor: PortableCodecDescriptor,
  input: unknown
): PortableResolutionResult<string>;
export function resolvePortableValue(
  descriptor: BooleanCodecDescriptor,
  input: unknown
): PortableResolutionResult<boolean>;
export function resolvePortableValue(
  descriptor: SafeIntegerCodecDescriptor,
  input: unknown
): PortableResolutionResult<number>;
export function resolvePortableValue(
  descriptor: StringListCodecDescriptor,
  input: unknown
): PortableResolutionResult<readonly string[]>;
export function resolvePortableValue<TShape extends PortableShapeDescriptor>(
  descriptor: JsonCodecDescriptor<TShape>,
  input: unknown
): PortableResolutionResult<PortableShapeValue<TShape>>;
export function resolvePortableValue(
  descriptor: PublicCodecDescriptor,
  input: unknown
): PortableResolutionResult;
export function resolvePortableValue(
  descriptor: PublicCodecDescriptor,
  input: unknown
): PortableResolutionResult {
  if (input === undefined) {
    return missingValue();
  }
  if (typeof input !== "string") {
    return invalidValue();
  }
  if (
    (descriptor.kind === "string" &&
      input.length > descriptor.maxCodePoints * 2) ||
    (descriptor.kind === "string-list" &&
      input.length >
        descriptor.maximumItems * descriptor.maximumItemCodePoints * 2 +
          PORTABLE_JSON_LIMITS.maximumContainerItems)
  ) {
    return invalidValue();
  }
  if (!isPortableString(input)) {
    return invalidValue();
  }

  if (
    input.length === 0 &&
    (descriptor.kind === "boolean" ||
      descriptor.kind === "json" ||
      descriptor.kind === "safe-integer")
  ) {
    return descriptor.blank === "missing" ? missingValue() : invalidValue();
  }

  switch (descriptor.kind) {
    case "boolean": {
      if (input === descriptor.falseInput) {
        return resolvedValue(false);
      }
      return input === descriptor.trueInput
        ? resolvedValue(true)
        : invalidValue();
    }
    case "json": {
      let parsed: unknown;
      try {
        parsed = parseBoundedJsonValue(input);
      } catch {
        return invalidValue();
      }
      const result = validateJsonShape(descriptor, parsed);
      return result.ok ? resolvedValue(result.value) : invalidValue();
    }
    case "safe-integer": {
      if (input.length > 17 || !/^-?(?:0|[1-9][0-9]*)$/u.test(input)) {
        return invalidValue();
      }
      const result = validateSafeInteger(descriptor, Number(input));
      return result.ok ? resolvedValue(result.value) : invalidValue();
    }
    case "string-list": {
      const selected: string[] = [];
      let rawItems = 0;
      let start = 0;
      for (let index = 0; index <= input.length; index += 1) {
        if (index < input.length && input[index] !== descriptor.separator) {
          if (index - start > descriptor.maximumItemCodePoints * 2) {
            return invalidValue();
          }
          continue;
        }

        rawItems += 1;
        if (rawItems > PORTABLE_JSON_LIMITS.maximumContainerItems) {
          return invalidValue();
        }
        const item = input.slice(start, index);
        start = index + 1;
        if (item.length === 0) {
          if (descriptor.emptyItems === "invalid") {
            return invalidValue();
          }
          continue;
        }
        if (selected.length >= descriptor.maximumItems) {
          return invalidValue();
        }
        selected.push(item);
      }
      const result = validateStringList(descriptor, selected);
      return result.ok ? resolvedValue(result.value) : invalidValue();
    }
    case "enum":
    case "origin":
    case "string": {
      const result = validatePortableValue(descriptor, input);
      return result.ok ? resolvedValue(result.value) : invalidValue();
    }
  }
}

export function compareCodecCompatibility(
  left: CodecDescriptor | PublicCodecDescriptor,
  right: CodecDescriptor | PublicCodecDescriptor
): Compatibility {
  if (left.kind === "opaque" || right.kind === "opaque") {
    return "UNKNOWN";
  }
  if (left.kind !== right.kind || left.abi !== right.abi) {
    return "UNEQUAL";
  }

  switch (left.kind) {
    case "boolean": {
      return right.kind === "boolean" &&
        left.blank === right.blank &&
        left.falseInput === right.falseInput &&
        left.trueInput === right.trueInput
        ? "EQUAL"
        : "UNEQUAL";
    }
    case "enum": {
      return right.kind === "enum" &&
        left.values.length === right.values.length &&
        left.values.every((value, index) => value === right.values[index])
        ? "EQUAL"
        : "UNEQUAL";
    }
    case "integer": {
      return right.kind === "integer" &&
        left.blank === right.blank &&
        left.default === right.default &&
        left.maximum === right.maximum &&
        left.minimum === right.minimum
        ? "EQUAL"
        : "UNEQUAL";
    }
    case "json": {
      return right.kind === "json" &&
        left.blank === right.blank &&
        JSON.stringify(left.shape) === JSON.stringify(right.shape)
        ? "EQUAL"
        : "UNEQUAL";
    }
    case "origin": {
      return right.kind === "origin" ? "EQUAL" : "UNEQUAL";
    }
    case "safe-integer": {
      return right.kind === "safe-integer" &&
        left.blank === right.blank &&
        left.maximum === right.maximum &&
        left.minimum === right.minimum
        ? "EQUAL"
        : "UNEQUAL";
    }
    case "string": {
      return right.kind === "string" &&
        left.minCodePoints === right.minCodePoints &&
        left.maxCodePoints === right.maxCodePoints
        ? "EQUAL"
        : "UNEQUAL";
    }
    case "text": {
      return right.kind === "text" &&
        left.blank === right.blank &&
        left.maxCodePoints === right.maxCodePoints &&
        left.minCodePoints === right.minCodePoints &&
        left.normalise === right.normalise
        ? "EQUAL"
        : "UNEQUAL";
    }
    case "string-list": {
      return right.kind === "string-list" &&
        left.emptyItems === right.emptyItems &&
        left.maximumItemCodePoints === right.maximumItemCodePoints &&
        left.maximumItems === right.maximumItems &&
        left.minimumItemCodePoints === right.minimumItemCodePoints &&
        left.minimumItems === right.minimumItems &&
        left.separator === right.separator
        ? "EQUAL"
        : "UNEQUAL";
    }
  }
}
