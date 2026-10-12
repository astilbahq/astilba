import {
  asciiCaseFold,
  findProjection,
  isRawSourceName,
} from "../core/index.ts";
import type { ConsumerProjectionManifest } from "../core/index.ts";
import type { ProductCompilation } from "../product/index.ts";

const MAXIMUM_INVENTORY_ENTRIES = 2048;
const CONTRACT_INVENTORY_FORMAT = "astilba.env.contract-inventory/v1" as const;
const OBSERVED_INVENTORY_FORMAT =
  "astilba.env.observed-name-inventory/v1" as const;

export type InventoryOwnership = "closed" | "open";

type ContractInventoryEntry = Readonly<{
  entry: string;
  lifecycle: "build" | "deployment" | "request";
  name: string;
  required: boolean;
  visibility: "private" | "public";
}>;

type ContractInventory = Readonly<{
  entries: readonly ContractInventoryEntry[];
  format: typeof CONTRACT_INVENTORY_FORMAT;
  target: string;
}>;

type ObservedInventoryEntry = Readonly<{ name: string }>;

type ObservedInventory = Readonly<{
  entries: readonly ObservedInventoryEntry[];
  format: typeof OBSERVED_INVENTORY_FORMAT;
}>;

type InventoryIssueCode =
  | "OPTIONAL_MISSING"
  | "REQUIRED_MISSING"
  | "UNEXPECTED_ENTRY";

type InventoryIssue = Readonly<{
  code: InventoryIssueCode;
  entry: string | null;
  name: string;
}>;

type InventoryCheckReport = Readonly<{
  format: "astilba.env.inventory-check/v1";
  issues: readonly InventoryIssue[];
  ownership: InventoryOwnership;
  pass: boolean;
  target: string;
}>;

export type InventoryFailureCode =
  | "ENV_INVENTORY_INVALID"
  | "ENV_INVENTORY_TARGET_UNKNOWN"
  | "ENV_INVENTORY_TARGET_UNSUPPORTED"
  | "ENV_OBSERVED_INVALID"
  | "ENV_OBSERVED_UNSUPPORTED";

export class InventoryFailure extends Error {
  readonly code: InventoryFailureCode;

  constructor(code: InventoryFailureCode) {
    super(code);
    this.code = code;
    this.name = "InventoryFailure";
  }
}

const compareText = (left: string, right: string): number => {
  if (left < right) {
    return -1;
  }
  if (left === right) {
    return 0;
  }
  return 1;
};

const fail = (code: InventoryFailureCode): never => {
  throw new InventoryFailure(code);
};

const isDataRecord = (
  value: unknown
): value is Readonly<Record<string, unknown>> => {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    return false;
  }
  const prototype: unknown = Object.getPrototypeOf(value);
  return prototype === Object.prototype || prototype === null;
};

const exactRecord = <const TKey extends string>(
  value: unknown,
  expected: readonly TKey[],
  code: InventoryFailureCode
): Readonly<Record<TKey, unknown>> => {
  if (!isDataRecord(value)) {
    return fail(code);
  }
  const keys = Reflect.ownKeys(value);
  if (
    keys.length !== expected.length ||
    keys.some(
      (key) =>
        typeof key !== "string" ||
        !expected.some((candidate) => candidate === key)
    )
  ) {
    return fail(code);
  }
  for (const key of expected) {
    const descriptor = Object.getOwnPropertyDescriptor(value, key);
    if (
      descriptor === undefined ||
      !("value" in descriptor) ||
      descriptor.enumerable !== true
    ) {
      return fail(code);
    }
  }
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- Every requested own enumerable data property was verified above.
  return value;
};

const ownEnumerableDataValue = (value: unknown, key: string): unknown => {
  if (!isDataRecord(value)) {
    return undefined;
  }
  const descriptor = Object.getOwnPropertyDescriptor(value, key);
  return descriptor !== undefined &&
    "value" in descriptor &&
    descriptor.enumerable === true
    ? descriptor.value
    : undefined;
};

const projectionEntryByName = (
  projection: ConsumerProjectionManifest
): ReadonlyMap<string, ConsumerProjectionManifest["entries"][number]> => {
  const entries = new Map<
    string,
    ConsumerProjectionManifest["entries"][number]
  >();
  for (const entry of projection.entries) {
    if (entries.has(entry.name)) {
      return fail("ENV_INVENTORY_INVALID");
    }
    entries.set(entry.name, entry);
  }
  return entries;
};

export const compileContractInventory = (
  compilation: ProductCompilation,
  targetId: string
): ContractInventory => {
  const target = compilation.targets.find(
    (candidate) => candidate.bindingPlan.target === targetId
  );
  if (target === undefined) {
    return fail("ENV_INVENTORY_TARGET_UNKNOWN");
  }
  if (
    target.bindingPlan.adapterAbi !== "astilba.env.adapter.process-record/v1" ||
    target.bindingPlan.bindings.length === 0 ||
    target.bindingPlan.bindings.length > MAXIMUM_INVENTORY_ENTRIES
  ) {
    return fail("ENV_INVENTORY_TARGET_UNSUPPORTED");
  }
  const projection = findProjection(compilation.compiled, target.consumer);
  if (projection === undefined) {
    return fail("ENV_INVENTORY_INVALID");
  }

  const projectedEntries = projectionEntryByName(projection.manifest);
  const contractEntries = new Map(
    compilation.compiled.full.manifest.entries.map((entry) => [
      entry.name,
      entry,
    ])
  );
  const entryIds = new Set<string>();
  const foldedEntryIds = new Set<string>();
  const names = new Set<string>();
  const foldedNames = new Set<string>();
  const entries = target.bindingPlan.bindings.map((binding) => {
    const projected = projectedEntries.get(binding.entry);
    const contractEntry = contractEntries.get(binding.entry);
    if (
      projected === undefined ||
      contractEntry === undefined ||
      !isRawSourceName(binding.rawName)
    ) {
      return fail("ENV_INVENTORY_INVALID");
    }
    const foldedEntryId = asciiCaseFold(binding.entry);
    const foldedName = asciiCaseFold(binding.rawName);
    if (
      entryIds.has(binding.entry) ||
      foldedEntryIds.has(foldedEntryId) ||
      names.has(binding.rawName) ||
      foldedNames.has(foldedName)
    ) {
      return fail("ENV_INVENTORY_INVALID");
    }
    entryIds.add(binding.entry);
    foldedEntryIds.add(foldedEntryId);
    names.add(binding.rawName);
    foldedNames.add(foldedName);
    const visibility = contractEntry.visibility;
    const expectedClass =
      visibility === "private" ? "confidential" : "non-confidential";
    const expectedKind =
      visibility === "private" ? "private_text" : "public_text";
    if (
      binding.class !== expectedClass ||
      binding.kind !== expectedKind ||
      binding.channel !== contractEntry.lifecycle ||
      projected.identity[0] !== contractEntry.identity[0] ||
      projected.identity[1] !== contractEntry.identity[1]
    ) {
      return fail("ENV_INVENTORY_INVALID");
    }
    return Object.freeze({
      entry: binding.entry,
      lifecycle: binding.channel,
      name: binding.rawName,
      required: contractEntry.required,
      visibility,
    });
  });

  entries.sort(
    (left, right) =>
      compareText(left.name, right.name) || compareText(left.entry, right.entry)
  );
  return Object.freeze({
    entries: Object.freeze(entries),
    format: CONTRACT_INVENTORY_FORMAT,
    target: targetId,
  });
};

const isGreaterObservedVersion = (value: unknown): boolean => {
  if (
    typeof value !== "string" ||
    !value.startsWith("astilba.env.observed-name-inventory/v")
  ) {
    return false;
  }
  const version = value.slice("astilba.env.observed-name-inventory/v".length);
  return /^[1-9][0-9]*$/u.test(version) && version !== "1";
};

export const parseObservedInventory = (input: unknown): ObservedInventory => {
  if (isGreaterObservedVersion(ownEnumerableDataValue(input, "format"))) {
    return fail("ENV_OBSERVED_UNSUPPORTED");
  }
  const record = exactRecord(
    input,
    ["entries", "format"],
    "ENV_OBSERVED_INVALID"
  );
  if (
    record.format !== OBSERVED_INVENTORY_FORMAT ||
    !Array.isArray(record.entries) ||
    record.entries.length > MAXIMUM_INVENTORY_ENTRIES
  ) {
    return fail("ENV_OBSERVED_INVALID");
  }
  for (let index = 0; index < record.entries.length; index += 1) {
    if (!Object.hasOwn(record.entries, index)) {
      return fail("ENV_OBSERVED_INVALID");
    }
  }

  const names = new Set<string>();
  const foldedNames = new Set<string>();
  const entries = record.entries.map((value) => {
    const entry = exactRecord(value, ["name"], "ENV_OBSERVED_INVALID");
    if (typeof entry.name !== "string" || !isRawSourceName(entry.name)) {
      return fail("ENV_OBSERVED_INVALID");
    }
    const foldedName = asciiCaseFold(entry.name);
    if (names.has(entry.name) || foldedNames.has(foldedName)) {
      return fail("ENV_OBSERVED_INVALID");
    }
    names.add(entry.name);
    foldedNames.add(foldedName);
    return Object.freeze({ name: entry.name });
  });

  entries.sort((left, right) => compareText(left.name, right.name));
  return Object.freeze({
    entries: Object.freeze(entries),
    format: OBSERVED_INVENTORY_FORMAT,
  });
};

export const checkContractInventory = (
  expected: ContractInventory,
  observedInput: unknown,
  ownership: InventoryOwnership
): InventoryCheckReport => {
  const observed = parseObservedInventory(observedInput);
  const observedByName = new Map(
    observed.entries.map((entry) => [entry.name, entry])
  );
  const expectedNames = new Set(expected.entries.map((entry) => entry.name));
  const issues: InventoryIssue[] = [];
  let pass = true;

  for (const entry of expected.entries) {
    if (!observedByName.has(entry.name)) {
      const code = entry.required ? "REQUIRED_MISSING" : "OPTIONAL_MISSING";
      issues.push(
        Object.freeze({ code, entry: entry.entry, name: entry.name })
      );
      if (entry.required) {
        pass = false;
      }
    }
  }

  for (const entry of observed.entries) {
    if (!expectedNames.has(entry.name)) {
      issues.push(
        Object.freeze({
          code: "UNEXPECTED_ENTRY" as const,
          entry: null,
          name: entry.name,
        })
      );
      if (ownership === "closed") {
        pass = false;
      }
    }
  }

  issues.sort(
    (left, right) =>
      compareText(left.name, right.name) || compareText(left.code, right.code)
  );
  return Object.freeze({
    format: "astilba.env.inventory-check/v1",
    issues: Object.freeze(issues),
    ownership,
    pass,
    target: expected.target,
  });
};
