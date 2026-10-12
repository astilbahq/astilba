import { bootstrapFailure } from "./failure.ts";
import type { BootstrapFailureCode } from "./failure.ts";

export type JsonValue =
  | boolean
  | null
  | number
  | string
  | JsonArray
  | JsonObject;

type JsonArray = readonly JsonValue[];

export interface JsonObject {
  readonly [key: string]: JsonValue;
}

const MAXIMUM_CONTAINER_ITEMS = 1024;
const MAXIMUM_DEPTH = 8;
const MAXIMUM_OBJECT_KEYS = 256;
const PRIMITIVE = /^(?:false|null|true|-?(?:0|[1-9][0-9]*))/u;
const UNPAIRED_SURROGATE = /[\uD800-\uDFFF]/u;

const failure = (code: BootstrapFailureCode): never => {
  throw bootstrapFailure(code);
};

const isDangerousKey = (key: string): boolean =>
  key === "__proto__" || key === "constructor" || key === "prototype";

// In Unicode mode the range matches isolated UTF-16 surrogates, not a valid
// pair consumed as one astral code point.
const isPortableString = (value: unknown): value is string =>
  typeof value === "string" && !UNPAIRED_SURROGATE.test(value);

class BootstrapJsonScanner {
  readonly #source: string;
  #containerItems = 0;
  #index = 0;
  #objectKeys = 0;

  constructor(source: string) {
    this.#source = source;
  }

  parse(): JsonValue {
    this.#whitespace();
    const value = this.#value(1);
    this.#whitespace();
    if (this.#index !== this.#source.length) {
      failure("BOOTSTRAP_INVALID_JSON");
    }
    return value;
  }

  #containerItem(): void {
    this.#containerItems += 1;
    if (this.#containerItems > MAXIMUM_CONTAINER_ITEMS) {
      failure("BOOTSTRAP_NON_PORTABLE_JSON");
    }
  }

  #container(depth: number, object: boolean): JsonArray | JsonObject {
    this.#index += 1;
    this.#whitespace();
    const output = object
      ? // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- Object mode constructs a null-prototype JsonObject one parsed JsonValue at a time.
        (Object.create(null) as Record<string, JsonValue>)
      : [];
    const end = object ? "}" : "]";
    if (this.#source[this.#index] === end) {
      this.#index += 1;
      return Object.freeze(output);
    }

    while (true) {
      let name: string | undefined;
      if (object) {
        name = this.#string();
        if (isDangerousKey(name)) {
          failure("BOOTSTRAP_NON_PORTABLE_JSON");
        }
        if (Object.hasOwn(output, name)) {
          failure("BOOTSTRAP_DUPLICATE_KEY");
        }
        this.#objectKeys += 1;
        if (this.#objectKeys > MAXIMUM_OBJECT_KEYS) {
          failure("BOOTSTRAP_JSON_TOO_MANY_KEYS");
        }
      }
      this.#containerItem();
      this.#whitespace();
      if (object) {
        if (this.#source[this.#index] !== ":") {
          failure("BOOTSTRAP_INVALID_JSON");
        }
        this.#index += 1;
        this.#whitespace();
      }
      const value = this.#value(depth + 1);
      if (object) {
        // oxlint-disable-next-line typescript/no-unsafe-type-assertion, typescript/non-nullable-type-assertion-style -- Object mode assigned name through #string before this guarded assignment; the assertion preserves frozen emitted bytes.
        (output as Record<string, JsonValue>)[name as string] = value;
      } else {
        // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- array mode output is the local JsonValue array branch above.
        (output as JsonValue[]).push(value);
      }
      this.#whitespace();
      const separator = this.#source[this.#index];
      if (separator === end) {
        this.#index += 1;
        return Object.freeze(output);
      }
      if (separator !== ",") {
        failure("BOOTSTRAP_INVALID_JSON");
      }
      this.#index += 1;
      this.#whitespace();
    }
  }

  #primitive(): boolean | null | number {
    const match = PRIMITIVE.exec(this.#source.slice(this.#index));
    if (match === null) {
      return failure("BOOTSTRAP_INVALID_JSON");
    }
    const [raw] = match;
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
    return Number.isSafeInteger(value) && !Object.is(value, -0)
      ? value
      : failure("BOOTSTRAP_INVALID_JSON");
  }

  #string(): string {
    if (this.#source[this.#index] !== '"') {
      return failure("BOOTSTRAP_INVALID_JSON");
    }
    const start = this.#index;
    this.#index += 1;
    while (this.#index < this.#source.length) {
      const character = this.#source[this.#index];
      if (character === "\\") {
        this.#index += 2;
        continue;
      }
      if (character === '"') {
        this.#index += 1;
        let value: unknown;
        try {
          value = JSON.parse(this.#source.slice(start, this.#index)) as unknown;
        } catch {
          return failure("BOOTSTRAP_INVALID_JSON");
        }
        return isPortableString(value)
          ? value
          : failure("BOOTSTRAP_NON_PORTABLE_JSON");
      }
      this.#index += 1;
    }
    return failure("BOOTSTRAP_INVALID_JSON");
  }

  #value(depth: number): JsonValue {
    const character = this.#source[this.#index];
    if ((character === "{" || character === "[") && depth > MAXIMUM_DEPTH) {
      failure("BOOTSTRAP_JSON_TOO_DEEP");
    }
    if (character === "{") {
      return this.#container(depth, true);
    }
    if (character === "[") {
      return this.#container(depth, false);
    }
    if (character === '"') {
      return this.#string();
    }
    return this.#primitive();
  }

  #whitespace(): void {
    while (" \n\r\t".includes(this.#source[this.#index] ?? "\0")) {
      this.#index += 1;
    }
  }
}

export const parseBootstrapJson = (source: string): JsonValue =>
  new BootstrapJsonScanner(source).parse();
