import { ContractDefinitionError } from "./diagnostics.ts";
import { isPortableString } from "./json.ts";
import { INTEGER_CODEC_ABI, TEXT_CODEC_ABI } from "./types.ts";
import type {
  IntegerCodecDescriptor,
  JsonScalar,
  TextCodecDescriptor,
} from "./types.ts";

type ServerValueFailure = Readonly<{
  code: "ENV_INVALID_VALUE";
  ok: false;
}>;

type ServerValueMissing = Readonly<{
  ok: true;
  present: false;
}>;

type ServerValueSuccess = Readonly<{
  ok: true;
  present: true;
  value: JsonScalar;
}>;

export type ServerValueResult =
  | ServerValueFailure
  | ServerValueMissing
  | ServerValueSuccess;

const invalidValue = (): ServerValueFailure =>
  Object.freeze({
    code: "ENV_INVALID_VALUE" as const,
    ok: false as const,
  });

const missingValue = (): ServerValueMissing =>
  Object.freeze({
    ok: true as const,
    present: false as const,
  });

const validValue = (value: JsonScalar): ServerValueSuccess =>
  Object.freeze({
    ok: true as const,
    present: true as const,
    value,
  });

const isPlainDataRecord = (
  value: unknown,
  expectedKeys: readonly string[]
): value is Record<string, unknown> => {
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
};

const ownValue = (record: Record<string, unknown>, key: string): unknown => {
  const descriptor = Object.getOwnPropertyDescriptor(record, key);
  if (descriptor === undefined || !("value" in descriptor)) {
    throw new ContractDefinitionError();
  }
  return descriptor.value;
};

export const normalizeTextCodec = (
  descriptor: unknown
): TextCodecDescriptor => {
  if (
    !isPlainDataRecord(descriptor, [
      "abi",
      "blank",
      "kind",
      "maxCodePoints",
      "minCodePoints",
      "normalise",
    ])
  ) {
    throw new ContractDefinitionError();
  }

  const abi = ownValue(descriptor, "abi");
  const blank = ownValue(descriptor, "blank");
  const kind = ownValue(descriptor, "kind");
  const maxCodePoints = ownValue(descriptor, "maxCodePoints");
  const minCodePoints = ownValue(descriptor, "minCodePoints");
  const normalise = ownValue(descriptor, "normalise");
  if (
    abi !== TEXT_CODEC_ABI ||
    (blank !== "invalid" && blank !== "missing") ||
    kind !== "text" ||
    typeof maxCodePoints !== "number" ||
    typeof minCodePoints !== "number" ||
    !Number.isSafeInteger(maxCodePoints) ||
    !Number.isSafeInteger(minCodePoints) ||
    minCodePoints < 0 ||
    maxCodePoints < minCodePoints ||
    (normalise !== "preserve" && normalise !== "trim")
  ) {
    throw new ContractDefinitionError();
  }

  return Object.freeze({
    abi: TEXT_CODEC_ABI,
    blank,
    kind: "text" as const,
    maxCodePoints,
    minCodePoints,
    normalise,
  });
};

export const normalizeIntegerCodec = (
  descriptor: unknown
): IntegerCodecDescriptor => {
  if (
    !isPlainDataRecord(descriptor, [
      "abi",
      "blank",
      "default",
      "kind",
      "maximum",
      "minimum",
    ])
  ) {
    throw new ContractDefinitionError();
  }

  const abi = ownValue(descriptor, "abi");
  const blank = ownValue(descriptor, "blank");
  const defaultValue = ownValue(descriptor, "default");
  const kind = ownValue(descriptor, "kind");
  const maximum = ownValue(descriptor, "maximum");
  const minimum = ownValue(descriptor, "minimum");
  if (
    abi !== INTEGER_CODEC_ABI ||
    (blank !== "invalid" && blank !== "missing") ||
    kind !== "integer" ||
    typeof maximum !== "number" ||
    typeof minimum !== "number" ||
    !Number.isSafeInteger(maximum) ||
    !Number.isSafeInteger(minimum) ||
    maximum < minimum ||
    defaultValue !== null
  ) {
    throw new ContractDefinitionError();
  }

  return Object.freeze({
    abi: INTEGER_CODEC_ABI,
    blank,
    default: defaultValue,
    kind: "integer" as const,
    maximum,
    minimum,
  });
};

export const textCodec = (options: {
  blank: "invalid" | "missing";
  maxCodePoints: number;
  minCodePoints: number;
  normalise: "preserve" | "trim";
}): TextCodecDescriptor =>
  normalizeTextCodec({
    abi: TEXT_CODEC_ABI,
    blank: options.blank,
    kind: "text",
    maxCodePoints: options.maxCodePoints,
    minCodePoints: options.minCodePoints,
    normalise: options.normalise,
  });

export const integerCodec = (options: {
  blank: "invalid" | "missing";
  default: null;
  maximum: number;
  minimum: number;
}): IntegerCodecDescriptor =>
  normalizeIntegerCodec({
    abi: INTEGER_CODEC_ABI,
    blank: options.blank,
    default: options.default,
    kind: "integer",
    maximum: options.maximum,
    minimum: options.minimum,
  });

const blankResult = (
  blank: "invalid" | "missing"
): ServerValueFailure | ServerValueMissing =>
  blank === "missing" ? missingValue() : invalidValue();

const resolveText = (
  descriptor: TextCodecDescriptor,
  input: unknown
): ServerValueResult => {
  if (input === undefined) {
    return missingValue();
  }
  if (!isPortableString(input)) {
    return invalidValue();
  }
  if (input.trim().length === 0) {
    return blankResult(descriptor.blank);
  }

  const value = descriptor.normalise === "trim" ? input.trim() : input;
  if (value.length > descriptor.maxCodePoints * 2) {
    return invalidValue();
  }
  // oxlint-disable-next-line unicorn/prefer-spread -- Array.from avoids the type-aware string-spread hazard while retaining code-point counting.
  const codePoints = Array.from(value).length;
  if (
    codePoints < descriptor.minCodePoints ||
    codePoints > descriptor.maxCodePoints
  ) {
    return invalidValue();
  }
  return validValue(value);
};

const resolveInteger = (
  descriptor: IntegerCodecDescriptor,
  input: unknown
): ServerValueResult => {
  if (input === undefined) {
    return missingValue();
  }
  if (!isPortableString(input)) {
    return invalidValue();
  }
  const trimmed = input.trim();
  if (trimmed.length === 0) {
    if (descriptor.blank === "invalid") {
      return invalidValue();
    }
    return missingValue();
  }
  if (!/^[+-]?[0-9]+$/u.test(trimmed)) {
    return invalidValue();
  }
  const value = Number(trimmed);
  if (
    !Number.isSafeInteger(value) ||
    value < descriptor.minimum ||
    value > descriptor.maximum
  ) {
    return invalidValue();
  }
  return validValue(Object.is(value, -0) ? 0 : value);
};

export const resolveServerValue = (
  descriptor: IntegerCodecDescriptor | TextCodecDescriptor,
  input: unknown
): ServerValueResult => {
  switch (descriptor.kind) {
    case "integer": {
      return resolveInteger(descriptor, input);
    }
    case "text": {
      return resolveText(descriptor, input);
    }
  }
};
