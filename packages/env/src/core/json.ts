import type { JsonValue } from "./types.ts";

const DANGEROUS_KEYS = new Set(["__proto__", "constructor", "prototype"]);

function hasLoneSurrogate(value: string): boolean {
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index);
    if (code >= 0xd8_00 && code <= 0xdb_ff) {
      const next = value.charCodeAt(index + 1);
      if (index + 1 >= value.length || next < 0xdc_00 || next > 0xdf_ff) {
        return true;
      }
      index += 1;
    } else if (code >= 0xdc_00 && code <= 0xdf_ff) {
      return true;
    }
  }
  return false;
}

export function isPortableString(value: unknown): value is string {
  return typeof value === "string" && !hasLoneSurrogate(value);
}

function assertPortableNumber(value: number): void {
  if (!Number.isSafeInteger(value) || Object.is(value, -0)) {
    throw new TypeError("Value is outside the portable JSON domain.");
  }
}

function assertArrayShape(value: readonly unknown[]): void {
  if (Object.getPrototypeOf(value) !== Array.prototype) {
    throw new TypeError("Value is outside the portable JSON domain.");
  }

  const keys = Reflect.ownKeys(value);
  if (
    keys.length !== value.length + 1 ||
    keys.some(
      (key) =>
        typeof key !== "string" ||
        (key !== "length" &&
          (!/^(?:0|[1-9][0-9]*)$/u.test(key) || Number(key) >= value.length))
    )
  ) {
    throw new TypeError("Value is outside the portable JSON domain.");
  }

  for (let index = 0; index < value.length; index += 1) {
    const descriptor = Object.getOwnPropertyDescriptor(value, String(index));
    if (descriptor === undefined || !("value" in descriptor)) {
      throw new TypeError("Value is outside the portable JSON domain.");
    }
  }
}

function assertRecordShape(value: object): readonly string[] {
  const prototype = Reflect.getPrototypeOf(value);
  if (prototype !== Object.prototype && prototype !== null) {
    throw new TypeError("Value is outside the portable JSON domain.");
  }

  const keys = Reflect.ownKeys(value);
  if (keys.some((key) => typeof key !== "string")) {
    throw new TypeError("Value is outside the portable JSON domain.");
  }

  const stringKeys = keys.filter(
    (key): key is string => typeof key === "string"
  );
  for (const key of stringKeys) {
    if (!isPortableString(key) || DANGEROUS_KEYS.has(key)) {
      throw new TypeError("Value is outside the portable JSON domain.");
    }
    const descriptor = Object.getOwnPropertyDescriptor(value, key);
    if (
      descriptor === undefined ||
      !("value" in descriptor) ||
      descriptor.enumerable !== true
    ) {
      throw new TypeError("Value is outside the portable JSON domain.");
    }
  }

  return stringKeys;
}

function copyJson(value: unknown, ancestors: Set<object>): JsonValue {
  if (value === null || typeof value === "boolean") {
    return value;
  }
  if (typeof value === "string") {
    if (!isPortableString(value)) {
      throw new TypeError("Value is outside the portable JSON domain.");
    }
    return value;
  }
  if (typeof value === "number") {
    assertPortableNumber(value);
    return value;
  }
  if (typeof value !== "object") {
    throw new TypeError("Value is outside the portable JSON domain.");
  }
  if (ancestors.has(value)) {
    throw new TypeError("Value is outside the portable JSON domain.");
  }

  ancestors.add(value);
  try {
    if (Array.isArray(value)) {
      assertArrayShape(value);
      const copy = value.map((item) => copyJson(item, ancestors));
      return Object.freeze(copy);
    }

    const keys = assertRecordShape(value);
    // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- A null-prototype record is built exclusively with recursively validated JsonValue fields below.
    const copy = Object.create(null) as Record<string, JsonValue>;
    for (const key of keys) {
      const descriptor = Object.getOwnPropertyDescriptor(value, key);
      if (descriptor === undefined || !("value" in descriptor)) {
        throw new TypeError("Value is outside the portable JSON domain.");
      }
      Object.defineProperty(copy, key, {
        configurable: true,
        enumerable: true,
        value: copyJson(descriptor.value, ancestors),
        writable: true,
      });
    }
    return Object.freeze(copy);
  } finally {
    ancestors.delete(value);
  }
}

export function deepFreezeJson<T extends JsonValue>(value: T): T {
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- copyJson preserves the input JSON structure while removing mutability.
  return copyJson(value, new Set()) as T;
}

function serializeCanonical(value: unknown, ancestors: Set<object>): string {
  if (value === null) {
    return "null";
  }
  if (typeof value === "boolean") {
    return value ? "true" : "false";
  }
  if (typeof value === "string") {
    if (!isPortableString(value)) {
      throw new TypeError("Value is outside the portable JSON domain.");
    }
    return JSON.stringify(value);
  }
  if (typeof value === "number") {
    assertPortableNumber(value);
    return JSON.stringify(value);
  }
  if (typeof value !== "object" || value === null) {
    throw new TypeError("Value is outside the portable JSON domain.");
  }
  if (ancestors.has(value)) {
    throw new TypeError("Value is outside the portable JSON domain.");
  }

  ancestors.add(value);
  try {
    if (Array.isArray(value)) {
      assertArrayShape(value);
      return `[${value
        .map((item) => serializeCanonical(item, ancestors))
        .join(",")}]`;
    }

    const keys = [...assertRecordShape(value)].toSorted();
    const properties = keys.map((key) => {
      const descriptor = Object.getOwnPropertyDescriptor(value, key);
      if (descriptor === undefined || !("value" in descriptor)) {
        throw new TypeError("Value is outside the portable JSON domain.");
      }
      return `${JSON.stringify(key)}:${serializeCanonical(
        descriptor.value,
        ancestors
      )}`;
    });
    return `{${properties.join(",")}}`;
  } finally {
    ancestors.delete(value);
  }
}

export function canonicalJson(value: unknown): string {
  return serializeCanonical(value, new Set());
}

export function canonicalJsonBytes(value: unknown): Uint8Array {
  return new TextEncoder().encode(canonicalJson(value));
}
