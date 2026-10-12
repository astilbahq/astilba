import { ContractDefinitionError } from "./diagnostics.ts";
import { isPortableString } from "./json.ts";

type PortableStringShapeDescriptor = Readonly<{
  kind: "string";
}>;

type PortableBooleanShapeDescriptor = Readonly<{
  kind: "boolean";
}>;

type PortableNullShapeDescriptor = Readonly<{
  kind: "null";
}>;

type PortableSafeIntegerShapeDescriptor = Readonly<{
  kind: "safe-integer";
  maximum: number;
  minimum: number;
}>;

interface PortableArrayShapeDescriptor<
  TItems extends PortableShapeDescriptor = PortableShapeDescriptor,
> {
  readonly items: TItems;
  readonly kind: "array";
  readonly maximumItems: number;
  readonly minimumItems: number;
}

interface PortableObjectPropertyDescriptor<
  TName extends string = string,
  TShape extends PortableShapeDescriptor = PortableShapeDescriptor,
  TRequired extends boolean = boolean,
> {
  readonly name: TName;
  readonly required: TRequired;
  readonly shape: TShape;
}

interface PortableObjectShapeDescriptor<
  TProperties extends readonly PortableObjectPropertyDescriptor[] =
    readonly PortableObjectPropertyDescriptor[],
> {
  readonly kind: "object";
  readonly properties: TProperties;
}

export type PortableShapeDescriptor =
  | PortableArrayShapeDescriptor
  | PortableBooleanShapeDescriptor
  | PortableNullShapeDescriptor
  | PortableObjectShapeDescriptor
  | PortableSafeIntegerShapeDescriptor
  | PortableStringShapeDescriptor;

interface OptionalShapeDescriptor<
  TValue extends PortableShapeDescriptor = PortableShapeDescriptor,
> {
  readonly kind: "optional";
  readonly value: TValue;
}

export type OpaqueShapeDescriptor =
  | OptionalShapeDescriptor
  | PortableShapeDescriptor;

export type OpaqueInputShapeDescriptor =
  | OptionalShapeDescriptor<PortableStringShapeDescriptor>
  | PortableStringShapeDescriptor;

type Simplify<TValue> = {
  readonly [TKey in keyof TValue]: TValue[TKey];
};

type PortableObjectShapeValue<
  TProperties extends readonly PortableObjectPropertyDescriptor[],
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

export type PortableShapeValue<TShape extends PortableShapeDescriptor> =
  TShape extends PortableStringShapeDescriptor
    ? string
    : TShape extends PortableBooleanShapeDescriptor
      ? boolean
      : TShape extends PortableNullShapeDescriptor
        ? null
        : TShape extends PortableSafeIntegerShapeDescriptor
          ? number
          : TShape extends PortableArrayShapeDescriptor<infer TItems>
            ? readonly PortableShapeValue<TItems>[]
            : TShape extends PortableObjectShapeDescriptor<infer TProperties>
              ? PortableObjectShapeValue<TProperties>
              : never;

export type OpaqueShapeValue<TShape extends OpaqueShapeDescriptor> =
  TShape extends OptionalShapeDescriptor<infer TValue>
    ? PortableShapeValue<TValue> | undefined
    : TShape extends PortableShapeDescriptor
      ? PortableShapeValue<TShape>
      : never;

export interface PortableValueLimits {
  readonly maximumContainerItems: number;
  readonly maximumDepth: number;
  readonly maximumObjectKeys: number;
  readonly maximumStringBytes: number;
}

const PORTABLE_VALUE_LIMITS: PortableValueLimits = Object.freeze({
  maximumContainerItems: 1024,
  maximumDepth: 8,
  maximumObjectKeys: 256,
  maximumStringBytes: 64 * 1024,
});

const DANGEROUS_KEYS = new Set(["__proto__", "constructor", "prototype"]);
const MAXIMUM_SHAPE_NODES = 256;

export class PortableShapeValueError extends TypeError {
  constructor() {
    super("Value does not match its declared portable shape.");
    this.name = "PortableShapeValueError";
  }
}

const failDefinition = (): never => {
  throw new ContractDefinitionError();
};

const failValue = (): never => {
  throw new PortableShapeValueError();
};

const plainRecord = (
  value: unknown,
  expectedKeys: readonly string[]
): Record<string, unknown> => {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    return failDefinition();
  }
  const prototype = Reflect.getPrototypeOf(value);
  if (prototype !== Object.prototype && prototype !== null) {
    return failDefinition();
  }
  const keys = Reflect.ownKeys(value);
  if (
    keys.length !== expectedKeys.length ||
    keys.some((key) => typeof key !== "string" || !expectedKeys.includes(key))
  ) {
    return failDefinition();
  }
  for (const key of keys) {
    if (typeof key !== "string") {
      return failDefinition();
    }
    const descriptor = Object.getOwnPropertyDescriptor(value, key);
    if (
      descriptor === undefined ||
      !("value" in descriptor) ||
      descriptor.enumerable !== true
    ) {
      return failDefinition();
    }
  }
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- The preceding own-key and data-descriptor walk establishes this record shape.
  return value as Record<string, unknown>;
};

const ownValue = (record: Record<string, unknown>, key: string): unknown => {
  const descriptor = Object.getOwnPropertyDescriptor(record, key);
  if (descriptor === undefined || !("value" in descriptor)) {
    return failDefinition();
  }
  return descriptor.value;
};

const denseArrayItems = (value: unknown): readonly unknown[] => {
  if (
    !Array.isArray(value) ||
    Object.getPrototypeOf(value) !== Array.prototype
  ) {
    return failDefinition();
  }
  const lengthDescriptor = Object.getOwnPropertyDescriptor(value, "length");
  if (
    lengthDescriptor === undefined ||
    !("value" in lengthDescriptor) ||
    typeof lengthDescriptor.value !== "number"
  ) {
    return failDefinition();
  }
  const length = lengthDescriptor.value;
  const keys = Reflect.ownKeys(value);
  if (
    keys.length !== length + 1 ||
    keys.some(
      (key) =>
        typeof key !== "string" ||
        (key !== "length" &&
          (!/^(?:0|[1-9][0-9]*)$/u.test(key) || Number(key) >= length))
    )
  ) {
    return failDefinition();
  }
  const items: unknown[] = [];
  for (let index = 0; index < length; index += 1) {
    const descriptor = Object.getOwnPropertyDescriptor(value, String(index));
    if (
      descriptor === undefined ||
      !("value" in descriptor) ||
      descriptor.enumerable !== true
    ) {
      return failDefinition();
    }
    items.push(descriptor.value);
  }
  return items;
};

interface ShapeNormalizationState {
  readonly ancestors: Set<object>;
  nodes: number;
}

const normalizePortableShapeInner = (
  input: unknown,
  depth: number,
  state: ShapeNormalizationState
): PortableShapeDescriptor => {
  if (typeof input !== "object" || input === null) {
    return failDefinition();
  }
  if (state.ancestors.has(input)) {
    return failDefinition();
  }
  if (depth > PORTABLE_VALUE_LIMITS.maximumDepth) {
    return failDefinition();
  }
  state.nodes += 1;
  if (state.nodes > MAXIMUM_SHAPE_NODES) {
    return failDefinition();
  }
  state.ancestors.add(input);
  try {
    const kindDescriptor = Object.getOwnPropertyDescriptor(input, "kind");
    if (kindDescriptor === undefined || !("value" in kindDescriptor)) {
      return failDefinition();
    }
    switch (kindDescriptor.value) {
      case "boolean": {
        plainRecord(input, ["kind"]);
        return Object.freeze({ kind: "boolean" as const });
      }
      case "null": {
        plainRecord(input, ["kind"]);
        return Object.freeze({ kind: "null" as const });
      }
      case "safe-integer": {
        const record = plainRecord(input, ["kind", "maximum", "minimum"]);
        const maximum = ownValue(record, "maximum");
        const minimum = ownValue(record, "minimum");
        if (
          typeof maximum !== "number" ||
          typeof minimum !== "number" ||
          !Number.isSafeInteger(maximum) ||
          !Number.isSafeInteger(minimum) ||
          Object.is(maximum, -0) ||
          Object.is(minimum, -0) ||
          maximum < minimum
        ) {
          return failDefinition();
        }
        return Object.freeze({
          kind: "safe-integer" as const,
          maximum,
          minimum,
        });
      }
      case "string": {
        plainRecord(input, ["kind"]);
        return Object.freeze({ kind: "string" as const });
      }
      case "array": {
        const record = plainRecord(input, [
          "items",
          "kind",
          "maximumItems",
          "minimumItems",
        ]);
        const maximumItems = ownValue(record, "maximumItems");
        const minimumItems = ownValue(record, "minimumItems");
        if (
          typeof maximumItems !== "number" ||
          typeof minimumItems !== "number" ||
          !Number.isSafeInteger(maximumItems) ||
          !Number.isSafeInteger(minimumItems) ||
          minimumItems < 0 ||
          maximumItems < minimumItems ||
          maximumItems > PORTABLE_VALUE_LIMITS.maximumContainerItems
        ) {
          return failDefinition();
        }
        return Object.freeze({
          items: normalizePortableShapeInner(
            ownValue(record, "items"),
            depth + 1,
            state
          ),
          kind: "array" as const,
          maximumItems,
          minimumItems,
        });
      }
      case "object": {
        const record = plainRecord(input, ["kind", "properties"]);
        const properties = denseArrayItems(ownValue(record, "properties"));
        if (properties.length > PORTABLE_VALUE_LIMITS.maximumObjectKeys) {
          return failDefinition();
        }
        const normalized: PortableObjectPropertyDescriptor[] = [];
        const names = new Set<string>();
        for (const property of properties) {
          const propertyRecord = plainRecord(property, [
            "name",
            "required",
            "shape",
          ]);
          const name = ownValue(propertyRecord, "name");
          const required = ownValue(propertyRecord, "required");
          if (
            !isPortableString(name) ||
            name.length === 0 ||
            new TextEncoder().encode(name).byteLength > 255 ||
            DANGEROUS_KEYS.has(name) ||
            typeof required !== "boolean" ||
            names.has(name)
          ) {
            return failDefinition();
          }
          names.add(name);
          normalized.push(
            Object.freeze({
              name,
              required,
              shape: normalizePortableShapeInner(
                ownValue(propertyRecord, "shape"),
                depth + 1,
                state
              ),
            })
          );
        }
        normalized.sort((left, right) =>
          left.name < right.name ? -1 : left.name === right.name ? 0 : 1
        );
        return Object.freeze({
          kind: "object" as const,
          properties: Object.freeze(normalized),
        });
      }
      default: {
        return failDefinition();
      }
    }
  } finally {
    state.ancestors.delete(input);
  }
};

export const normalizePortableShape = <
  const TShape extends PortableShapeDescriptor,
>(
  input: TShape
): TShape => {
  try {
    // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- Normalization preserves a portable shape's structural subtype.
    return normalizePortableShapeInner(input, 1, {
      ancestors: new Set(),
      nodes: 0,
    }) as TShape;
  } catch (error) {
    if (error instanceof ContractDefinitionError) {
      throw error;
    }
    return failDefinition();
  }
};

export const normalizeOpaqueShape = <
  const TShape extends OpaqueShapeDescriptor,
>(
  input: TShape
): TShape => {
  try {
    if (
      typeof input === "object" &&
      input !== null &&
      Object.getOwnPropertyDescriptor(input, "kind")?.value === "optional"
    ) {
      const record = plainRecord(input, ["kind", "value"]);
      // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- The optional branch preserves the caller's opaque-shape subtype.
      return Object.freeze({
        kind: "optional" as const,
        value: normalizePortableShape(
          // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- plainRecord and ownValue above establish the optional payload is a portable descriptor.
          ownValue(record, "value") as PortableShapeDescriptor
        ),
      }) as TShape;
    }
    // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- An opaque non-optional descriptor is a portable descriptor by the discriminated type contract.
    return normalizePortableShape(input as PortableShapeDescriptor) as TShape;
  } catch (error) {
    if (error instanceof ContractDefinitionError) {
      throw error;
    }
    return failDefinition();
  }
};

export const normalizeOpaqueInputShape = <
  const TShape extends OpaqueInputShapeDescriptor,
>(
  input: TShape
): TShape => {
  const normalized = normalizeOpaqueShape(input);
  if (
    normalized.kind !== "string" &&
    (normalized.kind !== "optional" || normalized.value.kind !== "string")
  ) {
    return failDefinition();
  }
  return normalized;
};

interface ValueCopyState {
  readonly ancestors: Set<object>;
  readonly limits: PortableValueLimits;
  containerItems: number;
  objectKeys: number;
  stringBytes: number;
}

const countString = (value: string, state: ValueCopyState): void => {
  state.stringBytes += new TextEncoder().encode(value).byteLength;
  if (state.stringBytes > state.limits.maximumStringBytes) {
    failValue();
  }
};

const countContainerItem = (state: ValueCopyState): void => {
  state.containerItems += 1;
  if (state.containerItems > state.limits.maximumContainerItems) {
    failValue();
  }
};

const copyPortableValueInner = (
  shape: PortableShapeDescriptor,
  input: unknown,
  depth: number,
  state: ValueCopyState
): unknown => {
  switch (shape.kind) {
    case "boolean": {
      return typeof input === "boolean" ? input : failValue();
    }
    case "null": {
      return input === null ? null : failValue();
    }
    case "safe-integer": {
      return typeof input === "number" &&
        Number.isSafeInteger(input) &&
        !Object.is(input, -0) &&
        input >= shape.minimum &&
        input <= shape.maximum
        ? input
        : failValue();
    }
    case "string": {
      if (!isPortableString(input)) {
        return failValue();
      }
      countString(input, state);
      return input;
    }
    case "array": {
      if (depth > state.limits.maximumDepth || !Array.isArray(input)) {
        return failValue();
      }
      if (state.ancestors.has(input)) {
        return failValue();
      }
      const prototype = Reflect.getPrototypeOf(input);
      const lengthDescriptor = Object.getOwnPropertyDescriptor(input, "length");
      if (
        prototype !== Array.prototype ||
        lengthDescriptor === undefined ||
        !("value" in lengthDescriptor) ||
        typeof lengthDescriptor.value !== "number"
      ) {
        return failValue();
      }
      const length = lengthDescriptor.value;
      const keys = Reflect.ownKeys(input);
      if (
        length < shape.minimumItems ||
        length > shape.maximumItems ||
        keys.length !== length + 1 ||
        keys.some(
          (key) =>
            typeof key !== "string" ||
            (key !== "length" &&
              (!/^(?:0|[1-9][0-9]*)$/u.test(key) || Number(key) >= length))
        )
      ) {
        return failValue();
      }
      state.ancestors.add(input);
      try {
        const copy: unknown[] = [];
        for (let index = 0; index < length; index += 1) {
          const descriptor = Object.getOwnPropertyDescriptor(
            input,
            String(index)
          );
          if (
            descriptor === undefined ||
            !("value" in descriptor) ||
            descriptor.enumerable !== true
          ) {
            return failValue();
          }
          countContainerItem(state);
          copy.push(
            copyPortableValueInner(
              shape.items,
              descriptor.value,
              depth + 1,
              state
            )
          );
        }
        return Object.freeze(copy);
      } finally {
        state.ancestors.delete(input);
      }
    }
    case "object": {
      if (
        depth > state.limits.maximumDepth ||
        typeof input !== "object" ||
        input === null ||
        Array.isArray(input) ||
        state.ancestors.has(input)
      ) {
        return failValue();
      }
      const prototype = Reflect.getPrototypeOf(input);
      if (prototype !== Object.prototype && prototype !== null) {
        return failValue();
      }
      const keys = Reflect.ownKeys(input);
      if (
        keys.some((key) => typeof key !== "string") ||
        keys.length > state.limits.maximumObjectKeys
      ) {
        return failValue();
      }
      const properties = new Map(
        shape.properties.map((property) => [property.name, property])
      );
      for (const key of keys) {
        if (typeof key !== "string") {
          return failValue();
        }
        if (!properties.has(key) || DANGEROUS_KEYS.has(key)) {
          return failValue();
        }
        const descriptor = Object.getOwnPropertyDescriptor(input, key);
        if (
          descriptor === undefined ||
          !("value" in descriptor) ||
          descriptor.enumerable !== true
        ) {
          return failValue();
        }
      }
      for (const property of shape.properties) {
        if (property.required && !Object.hasOwn(input, property.name)) {
          return failValue();
        }
      }

      state.objectKeys += keys.length;
      if (state.objectKeys > state.limits.maximumObjectKeys) {
        return failValue();
      }
      state.ancestors.add(input);
      try {
        // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- The null-prototype record receives only schema-selected validated values below.
        const copy = Object.create(null) as Record<string, unknown>;
        for (const property of shape.properties) {
          const descriptor = Object.getOwnPropertyDescriptor(
            input,
            property.name
          );
          if (descriptor === undefined) {
            continue;
          }
          if (!("value" in descriptor)) {
            return failValue();
          }
          countContainerItem(state);
          countString(property.name, state);
          Object.defineProperty(copy, property.name, {
            configurable: true,
            enumerable: true,
            value: copyPortableValueInner(
              property.shape,
              descriptor.value,
              depth + 1,
              state
            ),
            writable: true,
          });
        }
        return Object.freeze(copy);
      } finally {
        state.ancestors.delete(input);
      }
    }
  }
};

export const copyPortableShapeValue = <
  const TShape extends PortableShapeDescriptor,
>(
  shape: TShape,
  input: unknown,
  limits: PortableValueLimits = PORTABLE_VALUE_LIMITS
): PortableShapeValue<TShape> => {
  try {
    // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- The recursive copier is driven by this exact generic shape.
    return copyPortableValueInner(shape, input, 1, {
      ancestors: new Set(),
      containerItems: 0,
      limits,
      objectKeys: 0,
      stringBytes: 0,
    }) as PortableShapeValue<TShape>;
  } catch (error) {
    if (error instanceof PortableShapeValueError) {
      throw error;
    }
    return failValue();
  }
};

export const copyOpaqueShapeValue = <
  const TShape extends OpaqueShapeDescriptor,
>(
  shape: TShape,
  input: unknown,
  limits: PortableValueLimits = PORTABLE_VALUE_LIMITS
): OpaqueShapeValue<TShape> => {
  if (shape.kind === "optional") {
    // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- The optional branch exactly implements OpaqueShapeValue for an optional descriptor.
    return (
      input === undefined
        ? undefined
        : copyPortableShapeValue(shape.value, input, limits)
    ) as OpaqueShapeValue<TShape>;
  }
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- A non-optional opaque descriptor is portable and its copied value is the opaque value.
  return copyPortableShapeValue(
    shape,
    input,
    limits
  ) as OpaqueShapeValue<TShape>;
};
