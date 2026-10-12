import {
  asciiCaseFold,
  isContractId,
  isLocalId,
  isRawSourceName,
} from "../core/identity.ts";
import {
  booleanCodec,
  ContractDefinitionError,
  defineContract,
  enumCodec,
  integerCodec,
  jsonCodec,
  opaqueCodec,
  originCodec,
  presentTogetherRule,
  safeIntegerCodec,
  stringCodec,
  stringListCodec,
  textCodec,
} from "../core/index.ts";
import { canonicalJson, isPortableString } from "../core/json.ts";
import type {
  OpaqueInputShapeDescriptor,
  OpaqueShapeDescriptor,
  PortableShapeDescriptor,
} from "../core/shapes.ts";
import type {
  CodecDescriptor,
  ContractDefinition,
  ContractRuleDefinition,
  Lifecycle,
  PublicCodecDescriptor,
  SafeIntegerCodecDescriptor,
  Visibility,
} from "../core/types.ts";
import type { ProviderBindingPlan } from "../provider/types.ts";

type ConsumerKind = "browser" | "server";

declare const entryBrand: unique symbol;
declare const consumerBrand: unique symbol;
declare const targetBrand: unique symbol;
declare const ruleBrand: unique symbol;
declare const environmentBrand: unique symbol;

type Entry<
  TOutput = unknown,
  TRequired extends boolean = boolean,
  TVisibility extends Visibility = Visibility,
  TLifecycle extends Lifecycle = Lifecycle,
  TKind extends string = string,
> = Readonly<{
  [entryBrand]: readonly [TOutput, TRequired, TVisibility, TLifecycle, TKind];
}>;

type EntryRecord = Readonly<Record<string, Entry>>;

type Consumer<
  TKind extends ConsumerKind,
  TEntries extends readonly string[] | null,
> = Readonly<{
  [consumerBrand]: readonly [TKind, TEntries];
}>;

type ProcessTarget<
  TConsumer extends string,
  TBindings extends Readonly<Record<string, string>>,
> = Readonly<{
  [targetBrand]: readonly [TConsumer, TBindings];
}>;

type PresentTogetherRule<TEntries extends readonly string[]> = Readonly<{
  [ruleBrand]: TEntries;
}>;

/**
 * Opaque result of a successfully validated environment declaration.
 *
 * Keep this value in the module that declares the contract, then pass it to
 * the compiler or CLI; its shape is intentionally not application data.
 */
export type EnvironmentDefinition = Readonly<{
  [environmentBrand]: true;
}>;

type ValidateTarget<TEntries extends EntryRecord, TConsumers, TTarget> =
  TTarget extends ProcessTarget<infer TConsumer, infer TBindings>
    ? TConsumer extends keyof TConsumers & string
      ? Exclude<keyof TBindings & string, keyof TEntries & string> extends never
        ? TTarget
        : never
      : never
    : never;

type ValidateTargets<TEntries extends EntryRecord, TConsumers, TTargets> = {
  readonly [TKey in keyof TTargets]: ValidateTarget<
    TEntries,
    TConsumers,
    TTargets[TKey]
  >;
};

type RequiredOptions<TRequired extends boolean> = Readonly<{
  /** Whether the source must be present. Defaults to `true`. */
  required?: TRequired;
}>;

type BooleanOptions<TRequired extends boolean> = Readonly<{
  /** How an empty source is handled. Defaults to `"missing"`. */
  blank?: "invalid" | "missing";
  /** Exact source text accepted as `false`. Defaults to `"false"`. */
  falseInput?: string;
  /** Whether the source must be present. Defaults to `true`. */
  required?: TRequired;
  /** Exact source text accepted as `true`. Defaults to `"true"`. */
  trueInput?: string;
}>;

type IntegerOptions<TRequired extends boolean> = Readonly<{
  /** How an empty source is handled. Defaults to `"missing"`. */
  blank?: "invalid" | "missing";
  /** Inclusive largest accepted integer. */
  maximum: number;
  /** Inclusive smallest accepted integer. */
  minimum: number;
  /** Whether the source must be present. Defaults to `true`. */
  required?: TRequired;
}>;

type JsonOptions<TRequired extends boolean> = Readonly<{
  /** How an empty source is handled. Defaults to `"missing"`. */
  blank?: "invalid" | "missing";
  /** Whether the source must be present. Defaults to `true`. */
  required?: TRequired;
}>;

type StringOptions<TRequired extends boolean> = Readonly<{
  /** Inclusive maximum Unicode code-point count. Defaults to `65_535`. */
  maximumCodePoints?: number;
  /** Inclusive minimum Unicode code-point count; omitted means zero. */
  minimumCodePoints?: number;
  /** Whether the source must be present. Defaults to `true`. */
  required?: TRequired;
}>;

type StringListOptions<TRequired extends boolean> = Readonly<{
  /** Whether empty comma-separated items are discarded or rejected. Defaults to `"drop"`. */
  emptyItems?: "drop" | "invalid";
  /** Inclusive maximum Unicode code-point count for each item. Defaults to `1_024`. */
  maximumItemCodePoints?: number;
  /** Inclusive maximum number of items. Defaults to `64`. */
  maximumItems?: number;
  /** Inclusive minimum Unicode code-point count for each non-empty item. Defaults to `1`. */
  minimumItemCodePoints?: number;
  /** Inclusive minimum number of items. Defaults to `0`. */
  minimumItems?: number;
  /** Whether the source must be present. Defaults to `true`. */
  required?: TRequired;
}>;

type TextOptions<TRequired extends boolean> = Readonly<{
  /** How an empty source is handled. Defaults to `"missing"`. */
  blank?: "invalid" | "missing";
  /** Inclusive maximum Unicode code-point count. Defaults to `65_535`. */
  maximumCodePoints?: number;
  /** Inclusive minimum Unicode code-point count. Defaults to `1`. */
  minimumCodePoints?: number;
  /** Preserve source whitespace or trim it before validation. Defaults to `"preserve"`. */
  normalise?: "preserve" | "trim";
  /** Whether the source must be present. Defaults to `true`. */
  required?: TRequired;
}>;

type SecretOptions<TRequired extends boolean> = Readonly<{
  /** How an empty source is handled. Defaults to `"missing"`. */
  blank?: "invalid" | "missing";
  /** Inclusive maximum Unicode code-point count. Defaults to `65_535`. */
  maximumCodePoints?: number;
  /** Inclusive minimum Unicode code-point count. Defaults to `1`. */
  minimumCodePoints?: number;
  /** Whether the source must be present. Defaults to `true`. */
  required?: TRequired;
}>;

/** Declarative portable-JSON shape accepted by an `env.*.*.json` builder. */
type PortableShape =
  | Readonly<{
      /** Discriminator for a boolean value. */
      kind: "boolean";
    }>
  | Readonly<{
      /** Discriminator for a null value. */
      kind: "null";
    }>
  | Readonly<{
      /** Discriminator for a decimal safe integer. */
      kind: "safe-integer";
      /** Inclusive largest accepted integer. */
      maximum: number;
      /** Inclusive smallest accepted integer. */
      minimum: number;
    }>
  | Readonly<{
      /** Discriminator for a portable Unicode string. */
      kind: "string";
    }>
  | Readonly<{
      /** Shape of every array element. */
      items: PortableShape;
      /** Discriminator for an array value. */
      kind: "array";
      /** Inclusive largest permitted array length. */
      maximumItems: number;
      /** Inclusive smallest permitted array length. */
      minimumItems: number;
    }>
  | Readonly<{
      /** Discriminator for an object value. */
      kind: "object";
      /** Object properties; each name must be unique. */
      properties: readonly Readonly<{
        /** Property name in the decoded object. */
        name: string;
        /** Whether this property must be present. */
        required: boolean;
        /** Shape used to validate this property's value. */
        shape: PortableShape;
      }>[];
    }>;

/** Portable output shape accepted by private opaque entry declarations. */
type OpaqueShape =
  | PortableShape
  | Readonly<{
      /** Discriminator for an optional output value. */
      kind: "optional";
      /** Shape of the value when it is present. */
      value: PortableShape;
    }>;

/** Limited input shape accepted before a private opaque validator runs. */
type OpaqueInputShape =
  | Readonly<{
      /** Discriminator for a required raw string input. */
      kind: "string";
    }>
  | Readonly<{
      /** Discriminator for an optional raw string input. */
      kind: "optional";
      /** Required-string shape when the raw input is present. */
      value: Extract<PortableShape, { kind: "string" }>;
    }>;

type Simplify<TValue> = {
  readonly [TKey in keyof TValue]: TValue[TKey];
};

type PortableObjectShapeValue<
  TProperties extends readonly Readonly<{
    name: string;
    required: boolean;
    shape: PortableShape;
  }>[],
> = Simplify<
  {
    readonly [TProperty in TProperties[number] as TProperty["required"] extends true
      ? TProperty["name"]
      : never]: PortableShapeValue<TProperty["shape"]>;
  } & {
    readonly [TProperty in TProperties[number] as TProperty["required"] extends false
      ? TProperty["name"]
      : never]?: PortableShapeValue<TProperty["shape"]>;
  }
>;

type PortableShapeValue<TShape extends PortableShape> =
  TShape extends Readonly<{ kind: "string" }>
    ? string
    : TShape extends Readonly<{ kind: "boolean" }>
      ? boolean
      : TShape extends Readonly<{ kind: "null" }>
        ? null
        : TShape extends Readonly<{ kind: "safe-integer" }>
          ? number
          : TShape extends Readonly<{
                items: infer TItems extends PortableShape;
                kind: "array";
              }>
            ? readonly PortableShapeValue<TItems>[]
            : TShape extends Readonly<{
                  kind: "object";
                  properties: infer TProperties extends readonly Readonly<{
                    name: string;
                    required: boolean;
                    shape: PortableShape;
                  }>[];
                }>
              ? PortableObjectShapeValue<TProperties>
              : never;

type OpaqueShapeValue<TShape extends OpaqueShape> =
  TShape extends Readonly<{
    kind: "optional";
    value: infer TValue extends PortableShape;
  }>
    ? PortableShapeValue<TValue> | undefined
    : TShape extends PortableShape
      ? PortableShapeValue<TShape>
      : never;

type OpaqueOptions<
  TInput extends OpaqueInputShape,
  TOutput extends OpaqueShape,
  TRequired extends boolean,
> = Readonly<{
  /** Portable input shape accepted before the private validator runs. */
  input: TInput;
  /** Portable output shape produced by the private validator. */
  output: TOutput;
  /** Whether the source must be present. Defaults to `true`. */
  required?: TRequired;
  /** Application-owned identifier for the validator implementation revision. */
  revision: string;
  /** Application-owned description of the validator semantics. */
  semantics: string;
}>;

type EntryBuilder<
  TVisibility extends Visibility,
  TLifecycle extends Lifecycle,
> = Readonly<{
  /** Declares an exact-text boolean entry. */
  boolean<const TRequired extends boolean = true>(
    options?: BooleanOptions<TRequired>
  ): Entry<boolean, TRequired, TVisibility, TLifecycle, "boolean">;
  /** Declares an entry constrained to one of the supplied literal strings. */
  enum<
    const TValues extends readonly string[],
    const TRequired extends boolean = true,
  >(
    values: TValues,
    options?: RequiredOptions<TRequired>
  ): Entry<TValues[number], TRequired, TVisibility, TLifecycle, "enum">;
  /** Declares an integer entry with inclusive bounds; browser consumers cannot select it. */
  integer<const TRequired extends boolean = true>(
    options: IntegerOptions<TRequired>
  ): Entry<number, TRequired, TVisibility, TLifecycle, "integer">;
  /** Declares an exact portable-JSON entry with a serialisable shape. */
  json<
    const TShape extends PortableShape,
    const TRequired extends boolean = true,
  >(
    shape: TShape,
    options?: JsonOptions<TRequired>
  ): Entry<
    PortableShapeValue<TShape>,
    TRequired,
    TVisibility,
    TLifecycle,
    "json"
  >;
  /** Declares an ASCII serialised-origin entry. */
  origin<const TRequired extends boolean = true>(
    options?: RequiredOptions<TRequired>
  ): Entry<string, TRequired, TVisibility, TLifecycle, "origin">;
  /** Declares a decimal safe-integer entry with inclusive bounds. */
  safeInteger<const TRequired extends boolean = true>(
    options: IntegerOptions<TRequired>
  ): Entry<number, TRequired, TVisibility, TLifecycle, "safe-integer">;
  /** Declares a Unicode string entry measured in code points. */
  string<const TRequired extends boolean = true>(
    options?: StringOptions<TRequired>
  ): Entry<string, TRequired, TVisibility, TLifecycle, "string">;
  /** Declares a comma-separated list of Unicode string items. */
  stringList<const TRequired extends boolean = true>(
    options?: StringListOptions<TRequired>
  ): Entry<
    readonly string[],
    TRequired,
    TVisibility,
    TLifecycle,
    "string-list"
  >;
  /** Declares a text entry, optionally normalising whitespace; browser consumers cannot select it. */
  text<const TRequired extends boolean = true>(
    options?: TextOptions<TRequired>
  ): Entry<string, TRequired, TVisibility, TLifecycle, "text">;
}>;

type PrivateEntryBuilder<TLifecycle extends "deployment" | "request"> =
  EntryBuilder<"private", TLifecycle> &
    Readonly<{
      /**
       * Declares a private entry validated by an application-owned Standard
       * Schema implementation. Opaque validation is never browser-safe.
       */
      /* oxlint-disable typescript/no-unnecessary-type-parameters -- This frozen public generic keeps the opaque input shape exact even though only the output shape is projected into the branded result. */
      opaque<
        const TInput extends OpaqueInputShape,
        const TOutput extends OpaqueShape,
        const TRequired extends boolean = true,
      >(
        options: OpaqueOptions<TInput, TOutput, TRequired>
      ): Entry<
        Exclude<OpaqueShapeValue<TOutput>, undefined>,
        TRequired,
        "private",
        TLifecycle,
        "opaque"
      >;
      /* oxlint-enable typescript/no-unnecessary-type-parameters */
      /** Declares a private text entry intended for confidential values. */
      secret<const TRequired extends boolean = true>(
        options?: SecretOptions<TRequired>
      ): Entry<string, TRequired, "private", TLifecycle, "text">;
    }>;

interface ConsumerBuilder<TKind extends ConsumerKind> {
  /** Selects every compatible entry for this consumer. */
  (): Consumer<TKind, null>;
  /** Selects exactly these entry names for this consumer. */
  <const TEntries extends readonly string[]>(
    entries: TEntries
  ): Consumer<TKind, TEntries>;
}

type EntryState = Readonly<{
  codec: CodecDescriptor;
  lifecycle: Lifecycle;
  required: boolean;
  visibility: Visibility;
}>;

type ConsumerState = Readonly<{
  entries: readonly string[] | null;
  kind: ConsumerKind;
}>;

type TargetState = Readonly<{
  bindings: readonly (readonly [string, string])[];
  consumer: string;
}>;

type RuleState = Readonly<{
  entries: readonly string[];
  id: string;
}>;

export type EnvironmentCompilerState = Readonly<{
  bindingPlans: Readonly<Record<string, ProviderBindingPlan>>;
  consumerEntries: Readonly<Record<string, readonly string[]>>;
  contract: ContractDefinition;
  targetLifecycles: Readonly<Record<string, Lifecycle>>;
  targets: Readonly<
    Record<string, Readonly<{ consumer: string; kind: "process" }>>
  >;
}>;

const entryStates = new WeakMap<object, EntryState>();
const consumerStates = new WeakMap<object, ConsumerState>();
const targetStates = new WeakMap<object, TargetState>();
const ruleStates = new WeakMap<object, RuleState>();
const environmentStates = new WeakMap<object, EnvironmentCompilerState>();

const failDefinition = (message: string): never => {
  throw new TypeError(message);
};

const compareText = (left: string, right: string): number =>
  left < right ? -1 : left === right ? 0 : 1;

/* oxlint-disable typescript/no-unnecessary-type-parameters, typescript/no-unsafe-type-assertion -- Opaque public products carry compile-time-only brands; the WeakMap registration is their runtime authenticity boundary. */
const opaqueProduct = <TProduct>(
  registry: WeakMap<object, unknown>,
  state: unknown
): TProduct => {
  const product = Object.freeze({ __proto__: null });
  registry.set(product, state);
  return product as TProduct;
};
/* oxlint-enable typescript/no-unnecessary-type-parameters, typescript/no-unsafe-type-assertion */

type CapturedRecord = Readonly<{
  keys: readonly string[];
  values: ReadonlyMap<string, unknown>;
}>;

const captureRecord = (input: unknown, label: string): CapturedRecord => {
  try {
    if (typeof input !== "object" || input === null || Array.isArray(input)) {
      return failDefinition(`${label} must be a plain record.`);
    }
    const prototype = Reflect.getPrototypeOf(input);
    if (prototype !== Object.prototype && prototype !== null) {
      return failDefinition(`${label} must be a plain record.`);
    }
    const ownKeys = Reflect.ownKeys(input);
    if (!ownKeys.every((key): key is string => typeof key === "string")) {
      return failDefinition(`${label} cannot contain symbol keys.`);
    }
    const keys = ownKeys.toSorted(compareText);
    const values = new Map<string, unknown>();
    for (const key of keys) {
      const descriptor = Object.getOwnPropertyDescriptor(input, key);
      if (
        descriptor === undefined ||
        !("value" in descriptor) ||
        descriptor.enumerable !== true
      ) {
        return failDefinition(`${label} requires enumerable data properties.`);
      }
      values.set(key, descriptor.value);
    }
    return Object.freeze({
      keys: Object.freeze(keys),
      values,
    });
  } catch {
    return failDefinition(`${label} could not be observed safely.`);
  }
};

const requireRecordFields = (
  captured: CapturedRecord,
  required: readonly string[],
  optional: readonly string[],
  label: string
): void => {
  const allowed = new Set([...required, ...optional]);
  if (
    required.some((key) => !captured.values.has(key)) ||
    captured.keys.some((key) => !allowed.has(key))
  ) {
    failDefinition(`${label} has an invalid field set.`);
  }
};

const captureClosedRecord = (
  input: unknown,
  required: readonly string[],
  optional: readonly string[],
  label: string
): CapturedRecord => {
  const captured = captureRecord(input, label);
  requireRecordFields(captured, required, optional, label);
  return captured;
};

const captureDynamicRecord = (
  input: unknown,
  validKey: (key: string) => boolean,
  label: string
): CapturedRecord => {
  const captured = captureRecord(input, label);
  if (captured.keys.some((key) => !validKey(key))) {
    failDefinition(`${label} contains an invalid key.`);
  }
  return captured;
};

const captureArray = (input: unknown, label: string): readonly unknown[] => {
  try {
    if (!Array.isArray(input)) {
      return failDefinition(`${label} must be an array.`);
    }
    if (Object.getPrototypeOf(input) !== Array.prototype) {
      return failDefinition(`${label} must use the ordinary array prototype.`);
    }
    const ownKeys = Reflect.ownKeys(input);
    if (!ownKeys.every((key): key is string => typeof key === "string")) {
      return failDefinition(`${label} cannot contain symbol keys.`);
    }
    const keys = ownKeys.toSorted(compareText);
    const descriptors = new Map<string, PropertyDescriptor>();
    for (const key of keys) {
      const descriptor = Object.getOwnPropertyDescriptor(input, key);
      if (descriptor === undefined) {
        return failDefinition(`${label} has a missing property descriptor.`);
      }
      descriptors.set(key, descriptor);
    }
    const lengthDescriptor = descriptors.get("length");
    if (
      lengthDescriptor === undefined ||
      !("value" in lengthDescriptor) ||
      lengthDescriptor.enumerable === true
    ) {
      return failDefinition(`${label} has an invalid length.`);
    }
    const length: unknown = lengthDescriptor.value;
    if (
      typeof length !== "number" ||
      !Number.isSafeInteger(length) ||
      length < 0
    ) {
      return failDefinition(`${label} has an invalid length.`);
    }
    if (keys.length !== length + 1) {
      return failDefinition(`${label} must be dense.`);
    }
    const result: unknown[] = [];
    for (let index = 0; index < length; index += 1) {
      const descriptor = descriptors.get(String(index));
      if (
        descriptor === undefined ||
        !("value" in descriptor) ||
        descriptor.enumerable !== true
      ) {
        return failDefinition(`${label} must be dense.`);
      }
      result.push(descriptor.value);
    }
    if (
      keys.some(
        (key) =>
          key !== "length" &&
          (!/^(?:0|[1-9][0-9]*)$/u.test(key) || Number(key) >= length)
      )
    ) {
      return failDefinition(`${label} contains an extra property.`);
    }
    return Object.freeze(result);
  } catch {
    return failDefinition(`${label} could not be observed safely.`);
  }
};

const ownValue = (captured: CapturedRecord, key: string): unknown =>
  captured.values.get(key);

const invalidContractDefinition = (): never => {
  throw new ContractDefinitionError();
};

const optionalBlank = (
  captured: CapturedRecord,
  key: string
): "invalid" | "missing" | undefined => {
  const value = ownValue(captured, key);
  if (value === undefined || value === "invalid" || value === "missing") {
    return value;
  }
  return invalidContractDefinition();
};

const optionalEmptyItems = (
  captured: CapturedRecord,
  key: string
): "drop" | "invalid" | undefined => {
  const value = ownValue(captured, key);
  if (value === undefined || value === "drop" || value === "invalid") {
    return value;
  }
  return invalidContractDefinition();
};

const optionalNormalise = (
  captured: CapturedRecord,
  key: string
): "preserve" | "trim" | undefined => {
  const value = ownValue(captured, key);
  if (value === undefined || value === "preserve" || value === "trim") {
    return value;
  }
  return invalidContractDefinition();
};

const optionalNumber = (
  captured: CapturedRecord,
  key: string
): number | undefined => {
  const value = ownValue(captured, key);
  if (value === undefined || typeof value === "number") {
    return value;
  }
  return invalidContractDefinition();
};

const optionalString = (
  captured: CapturedRecord,
  key: string
): string | undefined => {
  const value = ownValue(captured, key);
  if (value === undefined || typeof value === "string") {
    return value;
  }
  return invalidContractDefinition();
};

const requiredBooleanValue = (
  captured: CapturedRecord,
  key: string
): boolean => {
  const value = ownValue(captured, key);
  if (typeof value === "boolean") {
    return value;
  }
  return invalidContractDefinition();
};

const requiredNumber = (captured: CapturedRecord, key: string): number => {
  const value = ownValue(captured, key);
  if (typeof value === "number") {
    return value;
  }
  return invalidContractDefinition();
};

const requiredString = (captured: CapturedRecord, key: string): string => {
  const value = ownValue(captured, key);
  if (typeof value === "string") {
    return value;
  }
  return invalidContractDefinition();
};

const requiredBoolean = <const TRequired extends boolean>(
  captured: CapturedRecord,
  _options: RequiredOptions<TRequired> | undefined
): TRequired => {
  const value = captured.values.has("required")
    ? ownValue(captured, "required")
    : true;
  if (typeof value !== "boolean") {
    return failDefinition("An entry required option must be boolean.");
  }
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- Descriptor capture preserves the value of the frozen generic option without observing the options object again.
  return value as TRequired;
};

const utf8Length = (value: string): number =>
  new TextEncoder().encode(value).byteLength;

const isEntryId = (value: string): boolean =>
  isLocalId(value) && value !== "constructor" && value !== "prototype";

const WINDOWS_DEVICE_NAMES = new Set([
  "aux",
  "con",
  "nul",
  "prn",
  ...Array.from({ length: 9 }, (_, index) => `com${index + 1}`),
  ...Array.from({ length: 9 }, (_, index) => `lpt${index + 1}`),
]);

const isConsumerOrTargetId = (value: string): boolean =>
  isLocalId(value) && !WINDOWS_DEVICE_NAMES.has(asciiCaseFold(value));

const requireUniqueFolded = (
  values: readonly string[],
  label: string
): void => {
  const folded = new Set<string>();
  for (const value of values) {
    const candidate = asciiCaseFold(value);
    if (folded.has(candidate)) {
      failDefinition(`${label} contains a case-folding collision.`);
    }
    folded.add(candidate);
  }
};

interface ShapeState {
  readonly ancestors: Set<object>;
  nodes: number;
}

const copyPortableShapeInner = (
  input: unknown,
  depth: number,
  state: ShapeState
): unknown => {
  if (
    typeof input !== "object" ||
    input === null ||
    state.ancestors.has(input)
  ) {
    return failDefinition("A portable shape is invalid.");
  }
  if (depth > 8) {
    return failDefinition("A portable shape exceeds the depth limit.");
  }
  state.nodes += 1;
  if (state.nodes > 256) {
    return failDefinition("A portable shape exceeds the node limit.");
  }
  state.ancestors.add(input);
  try {
    const captured = captureRecord(input, "A portable shape");
    const kind = ownValue(captured, "kind");
    switch (kind) {
      case "boolean":
      case "null":
      case "string": {
        requireRecordFields(captured, ["kind"], [], "A portable shape");
        return Object.freeze({ kind });
      }
      case "safe-integer": {
        requireRecordFields(
          captured,
          ["kind", "maximum", "minimum"],
          [],
          "A safe-integer shape"
        );
        return Object.freeze({
          kind,
          maximum: ownValue(captured, "maximum"),
          minimum: ownValue(captured, "minimum"),
        });
      }
      case "array": {
        requireRecordFields(
          captured,
          ["items", "kind", "maximumItems", "minimumItems"],
          [],
          "An array shape"
        );
        return Object.freeze({
          items: copyPortableShapeInner(
            ownValue(captured, "items"),
            depth + 1,
            state
          ),
          kind,
          maximumItems: ownValue(captured, "maximumItems"),
          minimumItems: ownValue(captured, "minimumItems"),
        });
      }
      case "object": {
        requireRecordFields(
          captured,
          ["kind", "properties"],
          [],
          "An object shape"
        );
        const propertyInputs = captureArray(
          ownValue(captured, "properties"),
          "Object shape properties"
        );
        if (propertyInputs.length > 256) {
          return failDefinition("An object shape has too many properties.");
        }
        const properties = propertyInputs.map((propertyInput) => {
          const property = captureClosedRecord(
            propertyInput,
            ["name", "required", "shape"],
            [],
            "An object shape property"
          );
          return Object.freeze({
            name: ownValue(property, "name"),
            required: ownValue(property, "required"),
            shape: copyPortableShapeInner(
              ownValue(property, "shape"),
              depth + 1,
              state
            ),
          });
        });
        return Object.freeze({
          kind,
          properties: Object.freeze(properties),
        });
      }
      default: {
        return failDefinition("A portable shape kind is invalid.");
      }
    }
  } finally {
    state.ancestors.delete(input);
  }
};

const validateCapturedPortableShape = (
  input: unknown
): PortableShapeDescriptor => {
  const captured = captureRecord(input, "A captured portable shape");
  const kind = ownValue(captured, "kind");
  switch (kind) {
    case "boolean":
    case "null":
    case "string": {
      requireRecordFields(captured, ["kind"], [], "A captured portable shape");
      return Object.freeze({ kind });
    }
    case "safe-integer": {
      requireRecordFields(
        captured,
        ["kind", "maximum", "minimum"],
        [],
        "A captured safe-integer shape"
      );
      return Object.freeze({
        kind,
        maximum: requiredNumber(captured, "maximum"),
        minimum: requiredNumber(captured, "minimum"),
      });
    }
    case "array": {
      requireRecordFields(
        captured,
        ["items", "kind", "maximumItems", "minimumItems"],
        [],
        "A captured array shape"
      );
      return Object.freeze({
        items: validateCapturedPortableShape(ownValue(captured, "items")),
        kind,
        maximumItems: requiredNumber(captured, "maximumItems"),
        minimumItems: requiredNumber(captured, "minimumItems"),
      });
    }
    case "object": {
      requireRecordFields(
        captured,
        ["kind", "properties"],
        [],
        "A captured object shape"
      );
      const propertyInputs = captureArray(
        ownValue(captured, "properties"),
        "Captured object shape properties"
      );
      const properties = propertyInputs.map((propertyInput) => {
        const property = captureClosedRecord(
          propertyInput,
          ["name", "required", "shape"],
          [],
          "A captured object shape property"
        );
        return Object.freeze({
          name: requiredString(property, "name"),
          required: requiredBooleanValue(property, "required"),
          shape: validateCapturedPortableShape(ownValue(property, "shape")),
        });
      });
      return Object.freeze({
        kind,
        properties: Object.freeze(properties),
      });
    }
    default: {
      return invalidContractDefinition();
    }
  }
};

const copyPortableShape = (input: unknown): PortableShapeDescriptor =>
  validateCapturedPortableShape(
    copyPortableShapeInner(input, 1, {
      ancestors: new Set(),
      nodes: 0,
    })
  );

const copyOpaqueShape = (input: unknown): OpaqueShapeDescriptor => {
  if (typeof input !== "object" || input === null) {
    return failDefinition("An opaque shape is invalid.");
  }
  const captured = captureRecord(input, "An opaque shape");
  if (ownValue(captured, "kind") !== "optional") {
    return copyPortableShape(input);
  }
  requireRecordFields(
    captured,
    ["kind", "value"],
    [],
    "An optional opaque shape"
  );
  return Object.freeze({
    kind: "optional",
    value: copyPortableShape(ownValue(captured, "value")),
  });
};

const copyOpaqueInputShape = (input: unknown): OpaqueInputShapeDescriptor => {
  const shape = copyOpaqueShape(input);
  if (shape.kind === "string") {
    return shape;
  }
  if (shape.kind === "optional" && shape.value.kind === "string") {
    return Object.freeze({
      kind: "optional",
      value: shape.value,
    });
  }
  return failDefinition(
    "An opaque input shape must be string or optional string."
  );
};

/* oxlint-disable typescript/no-unnecessary-type-parameters -- These return-only phantom parameters are the frozen entry brand projected by each public builder. */
const makeEntry = <
  TOutput,
  TRequired extends boolean,
  TVisibility extends Visibility,
  TLifecycle extends Lifecycle,
  TKind extends string,
>(
  state: EntryState
): Entry<TOutput, TRequired, TVisibility, TLifecycle, TKind> =>
  opaqueProduct(entryStates, Object.freeze(state));
/* oxlint-enable typescript/no-unnecessary-type-parameters */

const entryBuilder = <
  TVisibility extends Visibility,
  TLifecycle extends Lifecycle,
>(
  visibility: TVisibility,
  lifecycle: TLifecycle
): EntryBuilder<TVisibility, TLifecycle> => {
  const boolean = <const TRequired extends boolean = true>(
    options?: BooleanOptions<TRequired>
  ): Entry<boolean, TRequired, TVisibility, TLifecycle, "boolean"> => {
    const captured = captureClosedRecord(
      options ?? {},
      [],
      ["blank", "falseInput", "required", "trueInput"],
      "Boolean options"
    );
    const required = requiredBoolean(captured, options);
    return makeEntry({
      codec: booleanCodec({
        blank: optionalBlank(captured, "blank") ?? "missing",
        falseInput: optionalString(captured, "falseInput") ?? "false",
        trueInput: optionalString(captured, "trueInput") ?? "true",
      }),
      lifecycle,
      required,
      visibility,
    });
  };

  const enumBuilder = <
    const TValues extends readonly string[],
    const TRequired extends boolean = true,
  >(
    values: TValues,
    options?: RequiredOptions<TRequired>
  ): Entry<TValues[number], TRequired, TVisibility, TLifecycle, "enum"> => {
    const capturedValues = captureArray(values, "Enum values");
    if (capturedValues.length < 1 || capturedValues.length > 1024) {
      failDefinition("An enum must have between 1 and 1,024 choices.");
    }
    let totalBytes = 0;
    const copiedValues = capturedValues.map((value) => {
      if (!isPortableString(value)) {
        return failDefinition("An enum choice must be a portable string.");
      }
      const bytes = utf8Length(value);
      totalBytes += bytes;
      if (bytes > 65_535 || totalBytes > 65_536) {
        return failDefinition("Enum choices exceed their byte limits.");
      }
      return value;
    });
    const capturedOptions = captureClosedRecord(
      options ?? {},
      [],
      ["required"],
      "Enum options"
    );
    const required = requiredBoolean(capturedOptions, options);
    return makeEntry({
      codec: enumCodec(copiedValues),
      lifecycle,
      required,
      visibility,
    });
  };

  const integer = <const TRequired extends boolean = true>(
    options: IntegerOptions<TRequired>
  ): Entry<number, TRequired, TVisibility, TLifecycle, "integer"> => {
    const captured = captureClosedRecord(
      options,
      ["maximum", "minimum"],
      ["blank", "required"],
      "Integer options"
    );
    const required = requiredBoolean(captured, options);
    return makeEntry({
      codec: integerCodec({
        blank: optionalBlank(captured, "blank") ?? "missing",
        default: null,
        maximum: requiredNumber(captured, "maximum"),
        minimum: requiredNumber(captured, "minimum"),
      }),
      lifecycle,
      required,
      visibility,
    });
  };

  const json = <
    const TShape extends PortableShape,
    const TRequired extends boolean = true,
  >(
    shape: TShape,
    options?: JsonOptions<TRequired>
  ): Entry<
    PortableShapeValue<TShape>,
    TRequired,
    TVisibility,
    TLifecycle,
    "json"
  > => {
    const captured = captureClosedRecord(
      options ?? {},
      [],
      ["blank", "required"],
      "JSON options"
    );
    const required = requiredBoolean(captured, options);
    return makeEntry({
      codec: jsonCodec({
        blank: optionalBlank(captured, "blank") ?? "missing",
        shape: copyPortableShape(shape),
      }),
      lifecycle,
      required,
      visibility,
    });
  };

  const origin = <const TRequired extends boolean = true>(
    options?: RequiredOptions<TRequired>
  ): Entry<string, TRequired, TVisibility, TLifecycle, "origin"> => {
    const captured = captureClosedRecord(
      options ?? {},
      [],
      ["required"],
      "Origin options"
    );
    const required = requiredBoolean(captured, options);
    return makeEntry({
      codec: originCodec(),
      lifecycle,
      required,
      visibility,
    });
  };

  const safeInteger = <const TRequired extends boolean = true>(
    options: IntegerOptions<TRequired>
  ): Entry<number, TRequired, TVisibility, TLifecycle, "safe-integer"> => {
    const captured = captureClosedRecord(
      options,
      ["maximum", "minimum"],
      ["blank", "required"],
      "Safe-integer options"
    );
    const required = requiredBoolean(captured, options);
    return makeEntry({
      codec: safeIntegerCodec({
        blank: optionalBlank(captured, "blank") ?? "missing",
        maximum: requiredNumber(captured, "maximum"),
        minimum: requiredNumber(captured, "minimum"),
      }),
      lifecycle,
      required,
      visibility,
    });
  };

  const string = <const TRequired extends boolean = true>(
    options?: StringOptions<TRequired>
  ): Entry<string, TRequired, TVisibility, TLifecycle, "string"> => {
    const captured = captureClosedRecord(
      options ?? {},
      [],
      ["maximumCodePoints", "minimumCodePoints", "required"],
      "String options"
    );
    const required = requiredBoolean(captured, options);
    return makeEntry({
      codec: stringCodec({
        maxCodePoints: optionalNumber(captured, "maximumCodePoints") ?? 65_535,
        minCodePoints: optionalNumber(captured, "minimumCodePoints") ?? 0,
      }),
      lifecycle,
      required,
      visibility,
    });
  };

  const stringList = <const TRequired extends boolean = true>(
    options?: StringListOptions<TRequired>
  ): Entry<
    readonly string[],
    TRequired,
    TVisibility,
    TLifecycle,
    "string-list"
  > => {
    const captured = captureClosedRecord(
      options ?? {},
      [],
      [
        "emptyItems",
        "maximumItemCodePoints",
        "maximumItems",
        "minimumItemCodePoints",
        "minimumItems",
        "required",
      ],
      "String-list options"
    );
    const required = requiredBoolean(captured, options);
    return makeEntry({
      codec: stringListCodec({
        emptyItems: optionalEmptyItems(captured, "emptyItems") ?? "drop",
        maximumItemCodePoints:
          optionalNumber(captured, "maximumItemCodePoints") ?? 1024,
        maximumItems: optionalNumber(captured, "maximumItems") ?? 64,
        minimumItemCodePoints:
          optionalNumber(captured, "minimumItemCodePoints") ?? 1,
        minimumItems: optionalNumber(captured, "minimumItems") ?? 0,
      }),
      lifecycle,
      required,
      visibility,
    });
  };

  const text = <const TRequired extends boolean = true>(
    options?: TextOptions<TRequired>
  ): Entry<string, TRequired, TVisibility, TLifecycle, "text"> => {
    const captured = captureClosedRecord(
      options ?? {},
      [],
      [
        "blank",
        "maximumCodePoints",
        "minimumCodePoints",
        "normalise",
        "required",
      ],
      "Text options"
    );
    const required = requiredBoolean(captured, options);
    return makeEntry({
      codec: textCodec({
        blank: optionalBlank(captured, "blank") ?? "missing",
        maxCodePoints: optionalNumber(captured, "maximumCodePoints") ?? 65_535,
        minCodePoints: optionalNumber(captured, "minimumCodePoints") ?? 1,
        normalise: optionalNormalise(captured, "normalise") ?? "preserve",
      }),
      lifecycle,
      required,
      visibility,
    });
  };

  return Object.freeze({
    boolean,
    enum: enumBuilder,
    integer,
    json,
    origin,
    safeInteger,
    string,
    stringList,
    text,
  });
};

const privateEntryBuilder = <TLifecycle extends "deployment" | "request">(
  lifecycle: TLifecycle
): PrivateEntryBuilder<TLifecycle> => {
  const portable = entryBuilder("private", lifecycle);
  const opaque = <
    const TOutput extends OpaqueShape,
    const TRequired extends boolean = true,
  >(
    options: OpaqueOptions<OpaqueInputShape, TOutput, TRequired>
  ): Entry<
    Exclude<OpaqueShapeValue<TOutput>, undefined>,
    TRequired,
    "private",
    TLifecycle,
    "opaque"
  > => {
    const captured = captureClosedRecord(
      options,
      ["input", "output", "revision", "semantics"],
      ["required"],
      "Opaque options"
    );
    const required = requiredBoolean(captured, options);
    return makeEntry({
      codec: opaqueCodec({
        input: copyOpaqueInputShape(ownValue(captured, "input")),
        output: copyOpaqueShape(ownValue(captured, "output")),
        revision: requiredString(captured, "revision"),
        semantics: requiredString(captured, "semantics"),
      }),
      lifecycle,
      required,
      visibility: "private",
    });
  };

  const secret = <const TRequired extends boolean = true>(
    options?: SecretOptions<TRequired>
  ): Entry<string, TRequired, "private", TLifecycle, "text"> => {
    const captured = captureClosedRecord(
      options ?? {},
      [],
      ["blank", "maximumCodePoints", "minimumCodePoints", "required"],
      "Secret options"
    );
    const required = requiredBoolean(captured, options);
    return makeEntry({
      codec: textCodec({
        blank: optionalBlank(captured, "blank") ?? "missing",
        maxCodePoints: optionalNumber(captured, "maximumCodePoints") ?? 65_535,
        minCodePoints: optionalNumber(captured, "minimumCodePoints") ?? 1,
        normalise: "preserve",
      }),
      lifecycle,
      required,
      visibility: "private",
    });
  };

  return Object.freeze({
    ...portable,
    opaque,
    secret,
  });
};

const makeConsumer = <
  TKind extends ConsumerKind,
  const TEntries extends readonly string[] | null,
>(
  kind: TKind,
  entries: TEntries
): Consumer<TKind, TEntries> =>
  opaqueProduct(
    consumerStates,
    Object.freeze({
      entries,
      kind,
    })
  );

const copySelection = (input: unknown): readonly string[] => {
  const values = captureArray(input, "A consumer selection");
  if (values.length === 0) {
    failDefinition("A consumer selection cannot be empty.");
  }
  const entries = values.map((value) => {
    if (typeof value !== "string" || !isEntryId(value)) {
      return failDefinition("A consumer selection has an invalid entry.");
    }
    return value;
  });
  if (new Set(entries).size !== entries.length) {
    failDefinition("A consumer selection cannot contain duplicates.");
  }
  return Object.freeze(entries);
};

function server(): Consumer<"server", null>;
function server<const TEntries extends readonly string[]>(
  entries: TEntries
): Consumer<"server", TEntries>;
function server(
  entries?: readonly string[]
): Consumer<"server", readonly string[] | null> {
  return entries === undefined
    ? makeConsumer("server", null)
    : makeConsumer("server", copySelection(entries));
}

function browser(): Consumer<"browser", null>;
function browser<const TEntries extends readonly string[]>(
  entries: TEntries
): Consumer<"browser", TEntries>;
function browser(
  entries?: readonly string[]
): Consumer<"browser", readonly string[] | null> {
  return entries === undefined
    ? makeConsumer("browser", null)
    : makeConsumer("browser", copySelection(entries));
}

const processTarget = <
  const TConsumer extends string,
  const TBindings extends Readonly<Record<string, string>>,
>(
  consumer: TConsumer,
  bindings: TBindings
): ProcessTarget<TConsumer, TBindings> => {
  if (!isConsumerOrTargetId(consumer)) {
    failDefinition("A process target has an invalid consumer ID.");
  }
  const captured = captureDynamicRecord(
    bindings,
    isEntryId,
    "Process bindings"
  );
  if (captured.keys.length === 0) {
    failDefinition("A process target must bind at least one entry.");
  }
  const pairs = captured.keys.map((entry) => {
    const rawName = ownValue(captured, entry);
    if (!isRawSourceName(rawName)) {
      return failDefinition("A process binding has an invalid raw name.");
    }
    return Object.freeze([entry, rawName] as const);
  });
  requireUniqueFolded(
    pairs.map(([, rawName]) => rawName),
    "Process bindings"
  );
  return opaqueProduct(
    targetStates,
    Object.freeze({
      bindings: Object.freeze(pairs),
      consumer,
    })
  );
};

const together = <const TEntries extends readonly string[]>(
  id: string,
  entries: TEntries
): PresentTogetherRule<TEntries> => {
  if (!isLocalId(id)) {
    failDefinition("A present-together rule has an invalid ID.");
  }
  const copied = captureArray(entries, "Present-together entries").map(
    (value) => {
      if (typeof value !== "string" || !isEntryId(value)) {
        return failDefinition(
          "A present-together rule has an invalid entry ID."
        );
      }
      return value;
    }
  );
  if (copied.length < 2 || new Set(copied).size !== copied.length) {
    failDefinition(
      "A present-together rule requires at least two unique entries."
    );
  }
  return opaqueProduct(
    ruleStates,
    Object.freeze({
      entries: Object.freeze(copied),
      id,
    })
  );
};

/**
 * Declarative builders for environment entries, consumers, process targets,
 * and cross-entry rules. Builders are inert until passed to
 * {@link defineEnvironment}; they never read configuration values.
 */
export const env: Readonly<{
  /** Declares a browser consumer, which can select only public entries. */
  browser: ConsumerBuilder<"browser">;
  /** Private deployment- and request-lifecycle entry builders. */
  private: Readonly<{
    /** Declares private values resolved once for a deployment or process. */
    deployment: PrivateEntryBuilder<"deployment">;
    /** Declares private values resolved from explicit request context. */
    request: PrivateEntryBuilder<"request">;
  }>;
  /**
   * Maps selected consumer entry names to raw process-source names. A target
   * must bind at least one entry; bindings are checked against its consumer
   * when the environment is defined.
   */
  process: <
    const TConsumer extends string,
    const TBindings extends Readonly<Record<string, string>>,
  >(
    consumer: TConsumer,
    bindings: TBindings
  ) => ProcessTarget<TConsumer, TBindings>;
  /** Public entry builders grouped by the lifecycle in which values resolve. */
  public: Readonly<{
    /** Public values deliberately embedded while creating an artifact. */
    build: EntryBuilder<"public", "build">;
    /** Public values delivered after build, without changing artifact bytes. */
    deployment: EntryBuilder<"public", "deployment">;
    /** Public values resolved from explicit request context. */
    request: EntryBuilder<"public", "request">;
  }>;
  /** Declares a server consumer, which may select public and private entries. */
  server: ConsumerBuilder<"server">;
  /** Requires two or more entries to be present or absent together. */
  together: <const TEntries extends readonly string[]>(
    id: string,
    entries: TEntries
  ) => PresentTogetherRule<TEntries>;
}> = Object.freeze({
  browser,
  private: Object.freeze({
    deployment: privateEntryBuilder("deployment"),
    request: privateEntryBuilder("request"),
  }),
  process: processTarget,
  public: Object.freeze({
    build: entryBuilder("public", "build"),
    deployment: entryBuilder("public", "deployment"),
    request: entryBuilder("public", "request"),
  }),
  server,
  together,
});

type WitnessMetrics = Readonly<{
  containerItems: number;
  depth: number;
  objectKeys: number;
  sourceBytes: number;
  stringBytes: number;
}>;

const METRIC_LIMIT = 65_537;

const saturatingAdd = (...values: readonly number[]): number => {
  let result = 0;
  for (const value of values) {
    result += value;
    if (!Number.isSafeInteger(result) || result >= METRIC_LIMIT) {
      return METRIC_LIMIT;
    }
  }
  return result;
};

const saturatingMultiply = (left: number, right: number): number => {
  const result = left * right;
  return !Number.isSafeInteger(result) || result >= METRIC_LIMIT
    ? METRIC_LIMIT
    : result;
};

const jsonStringBytes = (value: string): number =>
  utf8Length(canonicalJson(value));

const stringMetrics = (value: string): WitnessMetrics =>
  Object.freeze({
    containerItems: 0,
    depth: 0,
    objectKeys: 0,
    sourceBytes: jsonStringBytes(value),
    stringBytes: utf8Length(value),
  });

const repeatedAsciiStringMetrics = (codePoints: number): WitnessMetrics =>
  Object.freeze({
    containerItems: 0,
    depth: 0,
    objectKeys: 0,
    sourceBytes: saturatingAdd(codePoints, 2),
    stringBytes: Math.min(codePoints, METRIC_LIMIT),
  });

const scalarMetrics = (sourceBytes: number): WitnessMetrics =>
  Object.freeze({
    containerItems: 0,
    depth: 0,
    objectKeys: 0,
    sourceBytes,
    stringBytes: 0,
  });

const arrayMetrics = (item: WitnessMetrics, count: number): WitnessMetrics =>
  Object.freeze({
    containerItems: saturatingAdd(
      count,
      saturatingMultiply(item.containerItems, count)
    ),
    depth: saturatingAdd(1, item.depth),
    objectKeys: saturatingMultiply(item.objectKeys, count),
    sourceBytes: saturatingAdd(
      2,
      saturatingMultiply(item.sourceBytes, count),
      count === 0 ? 0 : count - 1
    ),
    stringBytes: saturatingMultiply(item.stringBytes, count),
  });

const recordMetrics = (
  properties: readonly (readonly [string, WitnessMetrics])[]
): WitnessMetrics => {
  let containerItems = properties.length;
  let depth = 1;
  let objectKeys = properties.length;
  let sourceBytes = properties.length === 0 ? 2 : 1;
  let stringBytes = 0;
  for (const [key, value] of properties) {
    containerItems = saturatingAdd(containerItems, value.containerItems);
    depth = Math.max(depth, saturatingAdd(1, value.depth));
    objectKeys = saturatingAdd(objectKeys, value.objectKeys);
    sourceBytes = saturatingAdd(
      sourceBytes,
      jsonStringBytes(key),
      1,
      value.sourceBytes,
      1
    );
    stringBytes = saturatingAdd(
      stringBytes,
      utf8Length(key),
      value.stringBytes
    );
  }
  if (properties.length > 0) {
    sourceBytes -= 1;
    sourceBytes = saturatingAdd(sourceBytes, 1);
  }
  return Object.freeze({
    containerItems,
    depth,
    objectKeys,
    sourceBytes,
    stringBytes,
  });
};

const smallestSafeInteger = (
  descriptor: SafeIntegerCodecDescriptor
): number => {
  if (descriptor.minimum <= 0 && descriptor.maximum >= 0) {
    return 0;
  }
  if (descriptor.minimum > 0) {
    return descriptor.minimum;
  }
  const digitCount = String(Math.abs(descriptor.maximum)).length;
  if (digitCount >= 16) {
    return descriptor.minimum;
  }
  return Math.max(descriptor.minimum, -(10 ** digitCount - 1));
};

const shapeWitnessMetrics = (
  shape: PortableShapeDescriptor
): WitnessMetrics => {
  switch (shape.kind) {
    case "boolean": {
      return scalarMetrics(4);
    }
    case "null": {
      return scalarMetrics(4);
    }
    case "safe-integer": {
      return scalarMetrics(
        String(
          smallestSafeInteger({
            abi: "astilba.env.safe-integer-decimal/v1",
            blank: "missing",
            kind: "safe-integer",
            maximum: shape.maximum,
            minimum: shape.minimum,
          })
        ).length
      );
    }
    case "string": {
      return stringMetrics("");
    }
    case "array": {
      return arrayMetrics(shapeWitnessMetrics(shape.items), shape.minimumItems);
    }
    case "object": {
      return recordMetrics(
        shape.properties
          .filter((property) => property.required)
          .map(
            (property) =>
              [property.name, shapeWitnessMetrics(property.shape)] as const
          )
      );
    }
  }
};

const codecWitnessMetrics = (codec: PublicCodecDescriptor): WitnessMetrics => {
  switch (codec.kind) {
    case "boolean": {
      return scalarMetrics(4);
    }
    case "enum": {
      const selected = [...codec.values].toSorted((left, right) => {
        const byteDifference = jsonStringBytes(left) - jsonStringBytes(right);
        return byteDifference === 0 ? compareText(left, right) : byteDifference;
      })[0];
      if (selected === undefined) {
        return failDefinition("A browser enum cannot be empty.");
      }
      return stringMetrics(selected);
    }
    case "json": {
      return shapeWitnessMetrics(codec.shape);
    }
    case "origin": {
      return stringMetrics("https://a.a");
    }
    case "safe-integer": {
      return scalarMetrics(String(smallestSafeInteger(codec)).length);
    }
    case "string": {
      return repeatedAsciiStringMetrics(codec.minCodePoints);
    }
    case "string-list": {
      return arrayMetrics(
        repeatedAsciiStringMetrics(Math.max(1, codec.minimumItemCodePoints)),
        codec.minimumItems
      );
    }
  }
};

const assertBrowserFeasible = (
  contractId: string,
  consumerId: string,
  lifecycle: "deployment" | "request",
  entries: readonly (readonly [string, EntryState])[]
): void => {
  const values = recordMetrics(
    entries
      .filter(([, entry]) => entry.required)
      .map(([name, entry]) => {
        const { codec } = entry;
        if (
          codec.kind === "integer" ||
          codec.kind === "opaque" ||
          codec.kind === "text"
        ) {
          return failDefinition(
            "A browser consumer selected a server-only codec."
          );
        }
        return [name, codecWitnessMetrics(codec)] as const;
      })
  );
  const envelope = recordMetrics([
    ["protocol", stringMetrics("astilba.env.bootstrap/v1")],
    ["contract", stringMetrics(contractId)],
    ["consumer", stringMetrics(consumerId)],
    ["lifecycle", stringMetrics(lifecycle)],
    ["projection", stringMetrics(`sha256-${"A".repeat(43)}`)],
    ["audience", recordMetrics([["origin", stringMetrics("http://a")]])],
    ["values", values],
  ]);
  if (
    envelope.depth > 8 ||
    envelope.objectKeys > 256 ||
    envelope.containerItems > 1024 ||
    envelope.stringBytes > 65_536 ||
    envelope.sourceBytes > 65_536
  ) {
    failDefinition(
      "A browser lifecycle has no feasible minimum bootstrap envelope."
    );
  }
};

const freezeRecord = <TValue>(
  entries: readonly (readonly [string, TValue])[]
): Readonly<Record<string, TValue>> => {
  const result: Record<string, TValue> = {};
  Object.setPrototypeOf(result, null);
  for (const [key, value] of entries) {
    Object.defineProperty(result, key, {
      configurable: false,
      enumerable: true,
      value,
      writable: false,
    });
  }
  return Object.freeze(result);
};

/**
 * Validates and brands one complete declarative environment contract.
 *
 * This throws `TypeError` for invalid identifiers, unknown selections,
 * inconsistent lifecycles, or malformed builder products. It reads no
 * environment sources and does not resolve secrets.
 */
export function defineEnvironment<
  const TEntries extends EntryRecord,
  const TConsumers extends Readonly<
    Record<
      string,
      Consumer<ConsumerKind, readonly (keyof TEntries & string)[] | null>
    >
  >,
  const TTargets extends Readonly<
    Record<
      string,
      ProcessTarget<keyof TConsumers & string, Readonly<Record<string, string>>>
    >
  >,
>(
  declaration: Readonly<{
    /** Named consumer declarations built with {@link env.browser} or {@link env.server}. */
    consumers: TConsumers;
    /** Named entry declarations built from one lifecycle and visibility builder. */
    entries: TEntries;
    /** Reverse-DNS identifier for the complete environment contract. */
    id: string;
    /** Optional present-together rules over declared entry names. */
    rules?: readonly PresentTogetherRule<
      readonly (keyof TEntries & string)[]
    >[];
    /** Named process-source bindings for declared consumers. */
    targets: TTargets & ValidateTargets<TEntries, TConsumers, TTargets>;
  }>
): EnvironmentDefinition {
  const outer = captureClosedRecord(
    declaration,
    ["consumers", "entries", "id", "targets"],
    ["rules"],
    "An environment declaration"
  );
  const candidateId = ownValue(outer, "id");
  if (!isContractId(candidateId)) {
    return failDefinition("An environment requires a valid reverse-DNS ID.");
  }
  const id = candidateId;

  const capturedEntries = captureDynamicRecord(
    ownValue(outer, "entries"),
    isEntryId,
    "Environment entries"
  );
  if (capturedEntries.keys.length === 0 || capturedEntries.keys.length > 4096) {
    failDefinition("An environment must have between 1 and 4,096 entries.");
  }
  requireUniqueFolded(capturedEntries.keys, "Environment entries");
  const entries = capturedEntries.keys.map((name) => {
    const product = ownValue(capturedEntries, name);
    if (typeof product !== "object" || product === null) {
      return failDefinition("An entry must come from this env instance.");
    }
    const state = entryStates.get(product);
    if (state === undefined) {
      return failDefinition("An entry must come from this env instance.");
    }
    return Object.freeze([name, state] as const);
  });
  const entryByName = new Map(entries);

  const capturedConsumers = captureDynamicRecord(
    ownValue(outer, "consumers"),
    isConsumerOrTargetId,
    "Environment consumers"
  );
  if (
    capturedConsumers.keys.length === 0 ||
    capturedConsumers.keys.length > 256
  ) {
    failDefinition("An environment must have between 1 and 256 consumers.");
  }
  requireUniqueFolded(capturedConsumers.keys, "Environment consumers");
  const consumers = capturedConsumers.keys.map((consumerId) => {
    const product = ownValue(capturedConsumers, consumerId);
    if (typeof product !== "object" || product === null) {
      return failDefinition("A consumer must come from this env instance.");
    }
    const state = consumerStates.get(product);
    if (state === undefined) {
      return failDefinition("A consumer must come from this env instance.");
    }
    const selected = (state.entries ?? capturedEntries.keys)
      .map((entryName) => {
        if (!entryByName.has(entryName)) {
          return failDefinition(
            "A consumer selects an unknown environment entry."
          );
        }
        return entryName;
      })
      .toSorted(compareText);
    if (selected.length === 0) {
      failDefinition("A consumer must select at least one entry.");
    }
    if (state.kind === "browser") {
      for (const entryName of selected) {
        const entry = entryByName.get(entryName);
        if (
          entry === undefined ||
          entry.visibility !== "public" ||
          entry.codec.kind === "integer" ||
          entry.codec.kind === "opaque" ||
          entry.codec.kind === "text"
        ) {
          failDefinition(
            "A browser consumer may select only public browser-portable entries."
          );
        }
      }
    }
    return Object.freeze({
      id: consumerId,
      selected: Object.freeze(selected),
      state,
    });
  });
  const totalConsumerReferences = consumers.reduce(
    (total, consumer) => total + consumer.selected.length,
    0
  );
  if (totalConsumerReferences > 65_536) {
    failDefinition("Environment consumer selections exceed their limit.");
  }
  const consumerById = new Map(
    consumers.map((consumer) => [consumer.id, consumer] as const)
  );

  const ruleProducts = outer.values.has("rules")
    ? captureArray(ownValue(outer, "rules"), "Environment rules")
    : Object.freeze([]);
  if (ruleProducts.length > 512) {
    failDefinition("An environment cannot have more than 512 rules.");
  }
  const rules = ruleProducts.map((product) => {
    if (typeof product !== "object" || product === null) {
      return failDefinition("A rule must come from this env instance.");
    }
    const state = ruleStates.get(product);
    if (state === undefined) {
      return failDefinition("A rule must come from this env instance.");
    }
    if (state.entries.some((entryName) => !entryByName.has(entryName))) {
      return failDefinition("A rule selects an unknown environment entry.");
    }
    return state;
  });
  if (rules.reduce((total, rule) => total + rule.entries.length, 0) > 8192) {
    failDefinition("Environment rule entries exceed their limit.");
  }
  requireUniqueFolded(
    rules.map((rule) => rule.id),
    "Environment rules"
  );
  const contractRules: ContractRuleDefinition[] = rules.map((rule) =>
    presentTogetherRule(
      rule.id,
      rule.entries.map((entryName) => [id, entryName] as const)
    )
  );

  const contract: ContractDefinition = {
    consumers: consumers.map((consumer) => ({
      entries: consumer.selected.map((entryName) => [id, entryName] as const),
      id: consumer.id,
      kind: consumer.state.kind,
    })),
    entries: entries.map(([name, entry]) => ({
      codec: entry.codec,
      fragment: id,
      id: name,
      lifecycle: entry.lifecycle,
      required: entry.required,
      visibility: entry.visibility,
    })),
    id,
    ...(contractRules.length === 0
      ? {}
      : { rules: Object.freeze(contractRules) }),
  };
  defineContract(contract);

  const capturedTargets = captureDynamicRecord(
    ownValue(outer, "targets"),
    isConsumerOrTargetId,
    "Environment targets"
  );
  if (capturedTargets.keys.length === 0 || capturedTargets.keys.length > 512) {
    failDefinition("An environment must have between 1 and 512 targets.");
  }
  requireUniqueFolded(capturedTargets.keys, "Environment targets");

  const bindingPlans: (readonly [string, ProviderBindingPlan])[] = [];
  const targetLifecycles: (readonly [string, Lifecycle])[] = [];
  const compilerTargets: (readonly [
    string,
    Readonly<{ consumer: string; kind: "process" }>,
  ])[] = [];
  let totalBindings = 0;
  for (const targetId of capturedTargets.keys) {
    const product = ownValue(capturedTargets, targetId);
    if (typeof product !== "object" || product === null) {
      return failDefinition("A target must come from this env instance.");
    }
    const target = targetStates.get(product);
    if (target === undefined) {
      return failDefinition("A target must come from this env instance.");
    }
    const consumer = consumerById.get(target.consumer);
    if (consumer === undefined) {
      return failDefinition("A process target names an unknown consumer.");
    }
    const boundNames = target.bindings.map(([entryName]) => entryName);
    if (
      boundNames.some((entryName) => !consumer.selected.includes(entryName))
    ) {
      failDefinition(
        "A process target binds an entry not selected by its consumer."
      );
    }
    const lifecycles = new Set(
      boundNames.map((entryName) => entryByName.get(entryName)?.lifecycle)
    );
    if (lifecycles.size !== 1 || lifecycles.has(undefined)) {
      failDefinition(
        "A process target must bind exactly one complete lifecycle."
      );
    }
    const lifecycle = [...lifecycles][0];
    if (lifecycle === undefined) {
      return failDefinition("A process target lifecycle is missing.");
    }
    const expected = consumer.selected
      .filter(
        (entryName) => entryByName.get(entryName)?.lifecycle === lifecycle
      )
      .toSorted(compareText);
    const actual = [...boundNames].toSorted(compareText);
    if (
      expected.length !== actual.length ||
      expected.some((entryName, index) => actual[index] !== entryName)
    ) {
      failDefinition(
        "A process target must bind every selected entry in one lifecycle exactly once."
      );
    }
    totalBindings += target.bindings.length;
    const planBindings = target.bindings.map(([entryName, rawName]) => {
      const entry = entryByName.get(entryName);
      if (entry === undefined) {
        return failDefinition("A process target binds an unknown entry.");
      }
      return Object.freeze({
        channel: entry.lifecycle,
        class:
          entry.visibility === "private"
            ? ("confidential" as const)
            : ("non-confidential" as const),
        entry: entryName,
        kind:
          entry.visibility === "private"
            ? ("private_text" as const)
            : ("public_text" as const),
        rawName,
      });
    });
    bindingPlans.push([
      targetId,
      Object.freeze({
        adapterAbi: "astilba.env.adapter.process-record/v1",
        bindings: Object.freeze(planBindings),
        format: "astilba.env.binding-plan/v1",
        target: targetId,
      }),
    ]);
    targetLifecycles.push([targetId, lifecycle]);
    compilerTargets.push([
      targetId,
      Object.freeze({
        consumer: target.consumer,
        kind: "process",
      }),
    ]);
  }
  if (totalBindings > 65_536) {
    failDefinition("Environment target bindings exceed their limit.");
  }

  for (const consumer of consumers) {
    if (consumer.state.kind !== "browser") {
      continue;
    }
    const selectedEntries = consumer.selected.map((entryName) => {
      const entry = entryByName.get(entryName);
      if (entry === undefined) {
        return failDefinition("A browser consumer entry is missing.");
      }
      return [entryName, entry] as const;
    });
    const buildEntries = selectedEntries.filter(
      ([, entry]) => entry.lifecycle === "build"
    );
    if (buildEntries.length > 0) {
      const buildTargetCount = compilerTargets.filter(
        ([targetId, target]) =>
          target.consumer === consumer.id &&
          targetLifecycles.find(
            ([lifecycleTargetId]) => lifecycleTargetId === targetId
          )?.[1] === "build"
      ).length;
      if (buildTargetCount !== 1) {
        failDefinition(
          "A browser consumer with build entries requires exactly one build target."
        );
      }
    }
    for (const lifecycle of ["deployment", "request"] as const) {
      const lifecycleEntries = selectedEntries.filter(
        ([, entry]) => entry.lifecycle === lifecycle
      );
      if (lifecycleEntries.length > 0) {
        assertBrowserFeasible(id, consumer.id, lifecycle, lifecycleEntries);
      }
    }
  }

  const compilerState = Object.freeze({
    bindingPlans: freezeRecord(bindingPlans),
    consumerEntries: freezeRecord(
      consumers.map((consumer) => [consumer.id, consumer.selected] as const)
    ),
    contract: Object.freeze(contract),
    targetLifecycles: freezeRecord(targetLifecycles),
    targets: freezeRecord(compilerTargets),
  });
  return opaqueProduct<EnvironmentDefinition>(environmentStates, compilerState);
}

export const getEnvironmentCompilerState = (
  definition: unknown
): EnvironmentCompilerState => {
  if (typeof definition !== "object" || definition === null) {
    return failDefinition(
      "An environment definition must come from this env instance."
    );
  }
  const state = environmentStates.get(definition);
  if (state === undefined) {
    return failDefinition(
      "An environment definition must come from this env instance."
    );
  }
  return state;
};
