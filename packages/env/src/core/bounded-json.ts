import { isPortableString } from "./json.ts";
import type { JsonArray, JsonObject, JsonValue } from "./types.ts";

export type BoundedJsonFailureCode =
  | "DUPLICATE_KEY"
  | "INVALID_JSON"
  | "NON_PORTABLE_JSON"
  | "TOO_DEEP"
  | "TOO_MANY_BYTES"
  | "TOO_MANY_ITEMS"
  | "TOO_MANY_KEYS";

export class BoundedJsonFailure extends Error {
  readonly code: BoundedJsonFailureCode;

  constructor(code: BoundedJsonFailureCode) {
    super(code);
    this.code = code;
  }
}

export interface BoundedJsonLimits {
  readonly maximumArrayItems: number;
  readonly maximumBytes: number;
  readonly maximumContainerItems: number;
  readonly maximumDepth: number;
  readonly maximumObjectKeys: number;
  readonly maximumStringBytes: number;
}

export const PORTABLE_JSON_LIMITS: BoundedJsonLimits = Object.freeze({
  maximumArrayItems: 1024,
  maximumBytes: 64 * 1024,
  maximumContainerItems: 1024,
  maximumDepth: 8,
  maximumObjectKeys: 256,
  maximumStringBytes: 64 * 1024,
});

const PRIMITIVE = /^(?:false|null|true|-?(?:0|[1-9]\d*))/u;
const TEXT_ENCODER = new TextEncoder();

function failure(code: BoundedJsonFailureCode): never {
  throw new BoundedJsonFailure(code);
}

function isDangerousKey(key: string): boolean {
  return key === "__proto__" || key === "constructor" || key === "prototype";
}

class BoundedJsonScanner {
  readonly #input: string;
  readonly #limits: BoundedJsonLimits;
  #containerItems = 0;
  #index = 0;
  #objectKeys = 0;

  constructor(input: string, limits: BoundedJsonLimits) {
    this.#input = input;
    this.#limits = limits;
  }

  scan(): JsonValue {
    if (
      TEXT_ENCODER.encode(this.#input).byteLength > this.#limits.maximumBytes
    ) {
      failure("TOO_MANY_BYTES");
    }

    this.#whitespace();
    const value = this.#value(1);
    this.#whitespace();
    if (this.#index !== this.#input.length) {
      failure("INVALID_JSON");
    }
    return value;
  }

  #value(depth: number): JsonValue {
    const character = this.#input[this.#index];
    if (
      (character === "{" || character === "[") &&
      depth > this.#limits.maximumDepth
    ) {
      failure("TOO_DEEP");
    }
    if (character === "{") {
      return this.#object(depth);
    }
    if (character === "[") {
      return this.#array(depth);
    }
    if (character === '"') {
      return this.#string();
    }
    return this.#primitive();
  }

  #array(depth: number): JsonArray {
    this.#index += 1;
    this.#whitespace();

    const values: JsonValue[] = [];
    if (this.#input[this.#index] === "]") {
      this.#index += 1;
      return Object.freeze(values);
    }

    while (true) {
      if (values.length >= this.#limits.maximumArrayItems) {
        failure("TOO_MANY_ITEMS");
      }
      this.#containerItem();
      values.push(this.#value(depth + 1));
      this.#whitespace();

      const separator = this.#input[this.#index];
      if (separator === "]") {
        this.#index += 1;
        return Object.freeze(values);
      }
      if (separator !== ",") {
        failure("INVALID_JSON");
      }
      this.#index += 1;
      this.#whitespace();
    }
  }

  #object(depth: number): JsonObject {
    this.#index += 1;
    this.#whitespace();

    const value: Record<string, JsonValue> = {};
    if (this.#input[this.#index] === "}") {
      this.#index += 1;
      return Object.freeze(value);
    }

    while (true) {
      const name = this.#string();
      if (Object.hasOwn(value, name)) {
        failure("DUPLICATE_KEY");
      }
      if (isDangerousKey(name)) {
        failure("NON_PORTABLE_JSON");
      }
      this.#containerItem();
      this.#objectKeys += 1;
      if (this.#objectKeys > this.#limits.maximumObjectKeys) {
        failure("TOO_MANY_KEYS");
      }
      this.#whitespace();
      if (this.#input[this.#index] !== ":") {
        failure("INVALID_JSON");
      }
      this.#index += 1;
      this.#whitespace();
      value[name] = this.#value(depth + 1);
      this.#whitespace();

      const separator = this.#input[this.#index];
      if (separator === "}") {
        this.#index += 1;
        return Object.freeze(value);
      }
      if (separator !== ",") {
        failure("INVALID_JSON");
      }
      this.#index += 1;
      this.#whitespace();
    }
  }

  #containerItem(): void {
    this.#containerItems += 1;
    if (this.#containerItems > this.#limits.maximumContainerItems) {
      failure("TOO_MANY_ITEMS");
    }
  }

  #primitive(): boolean | null | number {
    const match = PRIMITIVE.exec(this.#input.slice(this.#index));
    if (match === null) {
      failure("INVALID_JSON");
    }
    const raw = match[0];
    this.#index += raw.length;
    if (raw === "false") {
      return false;
    }
    if (raw === "null") {
      return null;
    }
    if (raw === "true") {
      return true;
    }

    const value = Number(raw);
    if (!Number.isSafeInteger(value) || Object.is(value, -0)) {
      failure("INVALID_JSON");
    }
    return value;
  }

  #string(): string {
    if (this.#input[this.#index] !== '"') {
      failure("INVALID_JSON");
    }
    const start = this.#index;
    this.#index += 1;

    while (this.#index < this.#input.length) {
      const character = this.#input[this.#index];
      if (character === "\\") {
        this.#index += 2;
        continue;
      }
      if (character === '"') {
        this.#index += 1;
        let value: unknown;
        try {
          value = JSON.parse(this.#input.slice(start, this.#index)) as unknown;
        } catch {
          failure("INVALID_JSON");
        }
        if (!isPortableString(value)) {
          failure("NON_PORTABLE_JSON");
        }
        if (
          TEXT_ENCODER.encode(value).byteLength >
          this.#limits.maximumStringBytes
        ) {
          failure("TOO_MANY_BYTES");
        }
        return value;
      }
      this.#index += 1;
    }

    failure("INVALID_JSON");
  }

  #whitespace(): void {
    while (" \n\r\t".includes(this.#input[this.#index] ?? "\0")) {
      this.#index += 1;
    }
  }
}

export function parseBoundedJsonValue(
  input: string,
  limits: BoundedJsonLimits = PORTABLE_JSON_LIMITS
): JsonValue {
  try {
    return new BoundedJsonScanner(input, limits).scan();
  } catch (error) {
    if (error instanceof BoundedJsonFailure) {
      throw error;
    }
    failure("INVALID_JSON");
  }
}
