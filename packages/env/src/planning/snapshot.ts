import { parseBoundedJsonValue } from "../core/bounded-json.ts";
import type { BoundedJsonLimits } from "../core/bounded-json.ts";
import {
  asciiCaseFold,
  canonicalJson,
  compileContract,
  isLocalId,
  isRawSourceName,
} from "../core/index.ts";
import type { JsonArray, JsonObject, JsonValue } from "../core/index.ts";
import type {
  ProviderBindingPlan,
  ProviderBindingPlanEntry,
} from "../provider/types.ts";
import { createPlanningSnapshot } from "./plan.ts";
import type { PlanningSnapshotTarget } from "./plan.ts";
import type {
  EntryIdentity,
  EntryLifecycle,
  PlanningSnapshot,
  ProviderBindingClass,
} from "./types.ts";

const MAXIMUM_SERIAL_BYTES = 8_388_608;
const MAXIMUM_BINDING_PLAN_ROWS = 2048;
const SERIAL_LIMITS: BoundedJsonLimits = Object.freeze({
  maximumArrayItems: 65_536,
  maximumBytes: MAXIMUM_SERIAL_BYTES,
  maximumContainerItems: 262_144,
  maximumDepth: 64,
  maximumObjectKeys: 262_144,
  maximumStringBytes: 1_048_576,
});
const METADATA_IDENTIFIER = /^[A-Za-z0-9][A-Za-z0-9._/@:+-]{0,254}$/u;
const VERSION_SUFFIX = /^[1-9][0-9]*$/u;
const TEXT_DECODER = new TextDecoder("utf-8", {
  fatal: true,
  ignoreBOM: true,
});

const CURRENT_ABI_PREFIXES = Object.freeze([
  "astilba.env.boolean-exact/v",
  "astilba.env.enum/v",
  "astilba.env.integer/v",
  "astilba.env.json-exact/v",
  "astilba.env.opaque/v",
  "astilba.env.origin-ascii/v",
  "astilba.env.present-together/v",
  "astilba.env.safe-integer-decimal/v",
  "astilba.env.string-code-point/v",
  "astilba.env.string-list-comma/v",
  "astilba.env.text/v",
]);
const CURRENT_ADAPTER_PREFIXES = Object.freeze([
  "astilba.env.adapter.cloudflare-workers/v",
  "astilba.env.adapter.process-record/v",
]);
const PLANNING_FORMAT_PREFIX = "astilba.env.planning-snapshot/v";

export type PlanningSnapshotDecodeCode =
  | "ENV_PLANNING_FORMAT_UNSUPPORTED"
  | "ENV_PLANNING_INVALID";

export class PlanningSnapshotDecodeError extends Error {
  readonly code: PlanningSnapshotDecodeCode;

  constructor(code: PlanningSnapshotDecodeCode) {
    super(code);
    this.name = "PlanningSnapshotDecodeError";
    this.code = code;
  }
}

const invalid = (): never => {
  throw new PlanningSnapshotDecodeError("ENV_PLANNING_INVALID");
};

const unsupported = (): never => {
  throw new PlanningSnapshotDecodeError("ENV_PLANNING_FORMAT_UNSUPPORTED");
};

const hasExactFields = <TKey extends string>(
  value: JsonValue,
  keys: readonly TKey[]
): value is JsonObject & Readonly<Record<TKey, JsonValue>> => {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    return false;
  }
  const actual = Object.keys(value);
  return !(
    actual.length !== keys.length ||
    actual.some((key) => !keys.some((expected) => expected === key))
  );
};

const exactRecord = <const TKey extends string>(
  value: JsonValue,
  keys: readonly TKey[]
): JsonObject & Readonly<Record<TKey, JsonValue>> =>
  hasExactFields(value, keys) ? value : invalid();

const boundedArray = (
  value: JsonValue,
  options: Readonly<{ maximum?: number; nonEmpty?: boolean }> = {}
): JsonArray => {
  if (
    !Array.isArray(value) ||
    (options.nonEmpty === true && value.length === 0) ||
    value.length > (options.maximum ?? 65_536)
  ) {
    return invalid();
  }
  // oxlint-disable-next-line typescript/no-unsafe-return -- Array.isArray establishes the JSON-array branch while the caller validates each element.
  return value;
};

const requiredString = (value: JsonValue): string =>
  typeof value === "string" ? value : invalid();

const identity = (value: JsonValue): EntryIdentity => {
  const tuple = boundedArray(value, { maximum: 2, nonEmpty: true });
  if (
    tuple.length !== 2 ||
    typeof tuple[0] !== "string" ||
    typeof tuple[1] !== "string"
  ) {
    return invalid();
  }
  return Object.freeze([tuple[0], tuple[1]]);
};

const lifecycle = (value: JsonValue): EntryLifecycle =>
  value === "build" || value === "deployment" || value === "request"
    ? value
    : invalid();

const providerClass = (value: JsonValue): ProviderBindingClass =>
  value === "capability" ||
  value === "confidential" ||
  value === "non-confidential" ||
  value === "unknown"
    ? value
    : invalid();

const metadataIdentifier = (value: JsonValue): string => {
  const text = requiredString(value);
  return METADATA_IDENTIFIER.test(text) ? text : invalid();
};

const recognisedNewerVersion = (
  value: JsonValue,
  prefixes: readonly string[]
): boolean => {
  if (typeof value !== "string") {
    return false;
  }
  for (const prefix of prefixes) {
    if (!value.startsWith(prefix)) {
      continue;
    }
    const suffix = value.slice(prefix.length);
    return VERSION_SUFFIX.test(suffix) && suffix !== "1";
  }
  return false;
};

const refuseRecognisedNewerFormats = (value: JsonValue): void => {
  if (typeof value !== "object" || value === null) {
    return;
  }
  if (Array.isArray(value)) {
    for (const item of value) {
      // oxlint-disable-next-line typescript/no-unsafe-argument -- JSON array elements are the recursive JsonValue domain after the array branch check.
      refuseRecognisedNewerFormats(item);
    }
    return;
  }
  for (const [key, field] of Object.entries(value)) {
    if (
      (key === "format" &&
        recognisedNewerVersion(field, [PLANNING_FORMAT_PREFIX])) ||
      (key === "abi" && recognisedNewerVersion(field, CURRENT_ABI_PREFIXES)) ||
      (key === "adapterAbi" &&
        recognisedNewerVersion(field, CURRENT_ADAPTER_PREFIXES))
    ) {
      unsupported();
    }
    refuseRecognisedNewerFormats(field);
  }
};

const logicalEntryDefinition = (value: JsonValue): unknown => {
  const record = exactRecord(value, [
    "codec",
    "identity",
    "lifecycle",
    "outputName",
    "required",
    "visibility",
  ]);
  const stableIdentity = identity(record.identity);
  if (
    typeof record.required !== "boolean" ||
    (record.visibility !== "private" && record.visibility !== "public")
  ) {
    return invalid();
  }
  return Object.freeze({
    codec: record.codec,
    fragment: stableIdentity[0],
    id: stableIdentity[1],
    lifecycle: lifecycle(record.lifecycle),
    output: requiredString(record.outputName),
    required: record.required,
    visibility: record.visibility,
  });
};

const consumerDefinition = (
  value: JsonValue
): Readonly<{ contract: string; definition: unknown }> => {
  const record = exactRecord(value, [
    "contract",
    "entries",
    "id",
    "projectionDigest",
    "projectionKind",
  ]);
  const projectionKind = record.projectionKind;
  if (projectionKind !== "public" && projectionKind !== "server") {
    return invalid();
  }
  requiredString(record.projectionDigest);
  return Object.freeze({
    contract: requiredString(record.contract),
    definition: Object.freeze({
      entries: Object.freeze(
        boundedArray(record.entries, { nonEmpty: true }).map(identity)
      ),
      id: requiredString(record.id),
      kind: projectionKind === "public" ? "browser" : "server",
    }),
  });
};

const reverseBindingPlan = (
  value: JsonValue
): Readonly<{ consumer: string; plan: ProviderBindingPlan }> => {
  const target = exactRecord(value, [
    "adapterAbi",
    "bindings",
    "consumer",
    "id",
  ]);
  const adapterAbi = requiredString(target.adapterAbi);
  if (
    adapterAbi !== "astilba.env.adapter.process-record/v1" &&
    adapterAbi !== "astilba.env.adapter.cloudflare-workers/v1"
  ) {
    return invalid();
  }
  const consumer = requiredString(target.consumer);
  const targetId = requiredString(target.id);
  if (!isLocalId(consumer) || !isLocalId(targetId)) {
    return invalid();
  }

  const collapsed = new Map<string, ProviderBindingPlanEntry>();
  for (const value_ of boundedArray(target.bindings, { nonEmpty: true })) {
    const binding = exactRecord(value_, [
      "channel",
      "entry",
      "providerClass",
      "providerEntry",
      "providerKind",
      "rawName",
    ]);
    identity(binding.entry);
    const rawName = requiredString(binding.rawName);
    if (!isRawSourceName(rawName)) {
      return invalid();
    }
    const row = Object.freeze({
      channel: lifecycle(binding.channel),
      class: providerClass(binding.providerClass),
      entry: metadataIdentifier(binding.providerEntry),
      kind: metadataIdentifier(binding.providerKind),
      rawName,
    });
    const key = canonicalJson(row);
    collapsed.set(key, row);
  }
  if (collapsed.size > MAXIMUM_BINDING_PLAN_ROWS) {
    return invalid();
  }
  const plan: ProviderBindingPlan = Object.freeze({
    adapterAbi,
    bindings: Object.freeze([...collapsed.values()]),
    format: "astilba.env.binding-plan/v1",
    target: targetId,
  });
  return Object.freeze({ consumer, plan });
};

const rebuildSnapshot = async (value: JsonValue): Promise<PlanningSnapshot> => {
  const root = exactRecord(value, [
    "consumers",
    "entries",
    "format",
    "rules",
    "targets",
  ]);
  if (root.format !== "astilba.env.planning-snapshot/v1") {
    return invalid();
  }
  const entryDefinitions = boundedArray(root.entries, { nonEmpty: true }).map(
    logicalEntryDefinition
  );
  const consumerRows = boundedArray(root.consumers, { nonEmpty: true }).map(
    consumerDefinition
  );
  const contract = consumerRows[0]?.contract;
  if (contract === undefined) {
    return invalid();
  }
  const targetRows = boundedArray(root.targets, { nonEmpty: true }).map(
    reverseBindingPlan
  );
  const foldedTargets = new Set<string>();
  for (const target of targetRows) {
    const folded = asciiCaseFold(target.plan.target);
    if (foldedTargets.has(folded)) {
      return invalid();
    }
    foldedTargets.add(folded);
  }

  const compiled = await compileContract({
    consumers: Object.freeze(consumerRows.map((row) => row.definition)),
    entries: Object.freeze(entryDefinitions),
    id: contract,
    rules: boundedArray(root.rules),
  });
  const targets: readonly PlanningSnapshotTarget[] = Object.freeze(
    targetRows.map((row) =>
      Object.freeze({
        bindingPlan: row.plan,
        consumer: row.consumer,
      })
    )
  );
  return createPlanningSnapshot({ compiled, targets });
};

export const decodePlanningSnapshotBytes = async (
  bytes: Uint8Array
): Promise<PlanningSnapshot> => {
  try {
    if (bytes.byteLength > MAXIMUM_SERIAL_BYTES) {
      return invalid();
    }
    let source: string;
    try {
      source = TEXT_DECODER.decode(bytes);
    } catch {
      return invalid();
    }
    if (source.startsWith("\uFEFF") || !source.endsWith("\n")) {
      return invalid();
    }
    const body = source.slice(0, -1);
    const parsed = parseBoundedJsonValue(body, SERIAL_LIMITS);
    refuseRecognisedNewerFormats(parsed);
    if (canonicalJson(parsed) !== body) {
      return invalid();
    }
    const rebuilt = await rebuildSnapshot(parsed);
    if (canonicalJson(rebuilt) !== body) {
      return invalid();
    }
    return rebuilt;
  } catch (error) {
    if (error instanceof PlanningSnapshotDecodeError) {
      throw error;
    }
    return invalid();
  }
};
