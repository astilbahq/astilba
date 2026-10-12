import { normalizeCodecDescriptor } from "./descriptor.ts";
import { ContractDefinitionError } from "./diagnostics.ts";
import { sha256Digest } from "./digest.ts";
import {
  asciiCaseFold,
  isConsumerId,
  isContractId,
  isLocalId,
  isOutputName,
} from "./identity.ts";
import { canonicalJson, canonicalJsonBytes, deepFreezeJson } from "./json.ts";
import {
  CANONICALISATION_ABI,
  CODEC_ABI,
  CONTRACT_FORMAT,
  FORMAT_VERSION,
  PRESENT_TOGETHER_RULE_ABI,
  PROJECTION_ABI,
  PROJECTION_FORMAT,
  RULES_FORMAT_VERSION,
} from "./types.ts";
import type {
  CodecDescriptor,
  CompiledContract,
  CompiledManifest,
  CompiledProjection,
  ContractRuleDefinition,
  ConsumerDefinition,
  ConsumerProjectionManifest,
  ConsumerSelectionManifest,
  ContractDefinition,
  EntryIdentity,
  FullContractManifest,
  FullEntryManifest,
  JsonValue,
  PublicCodecDescriptor,
  BrowserProjectionEntry,
  BrowserProjectionManifest,
  ServerProjectionManifest,
} from "./types.ts";

type NormalizedContract = Readonly<{
  full: FullContractManifest;
  projections: readonly ConsumerProjectionManifest[];
}>;

function isPublicCodec(codec: CodecDescriptor): codec is PublicCodecDescriptor {
  return (
    codec.kind === "boolean" ||
    codec.kind === "enum" ||
    codec.kind === "json" ||
    codec.kind === "origin" ||
    codec.kind === "safe-integer" ||
    codec.kind === "string" ||
    codec.kind === "string-list"
  );
}

function failDefinition(): never {
  throw new ContractDefinitionError();
}

function freezeJsonData<TValue>(value: TValue): TValue {
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- This internal boundary accepts only contract data assembled from JSON-compatible primitives.
  return deepFreezeJson(value as unknown as JsonValue) as unknown as TValue;
}

function plainRecordKeys(value: unknown): readonly string[] {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    return failDefinition();
  }
  const prototype = Reflect.getPrototypeOf(value);
  if (prototype !== Object.prototype && prototype !== null) {
    return failDefinition();
  }
  const ownKeys = Reflect.ownKeys(value);
  const keys: string[] = [];
  for (const key of ownKeys) {
    if (typeof key !== "string") {
      return failDefinition();
    }
    keys.push(key);
  }
  for (const key of keys) {
    const descriptor = Object.getOwnPropertyDescriptor(value, key);
    if (
      descriptor === undefined ||
      !("value" in descriptor) ||
      descriptor.enumerable !== true
    ) {
      return failDefinition();
    }
  }
  return keys;
}

function requireExactKeys(
  value: unknown,
  expected: readonly string[]
): asserts value is Record<string, unknown> {
  const keys = plainRecordKeys(value);
  if (
    keys.length !== expected.length ||
    keys.some((key) => !expected.includes(key))
  ) {
    failDefinition();
  }
}

function ownValue(value: Record<string, unknown>, key: string): unknown {
  const descriptor = Object.getOwnPropertyDescriptor(value, key);
  if (descriptor === undefined || !("value" in descriptor)) {
    return failDefinition();
  }
  return descriptor.value;
}

function requireDenseArray(value: unknown): readonly unknown[] {
  if (
    !Array.isArray(value) ||
    Object.getPrototypeOf(value) !== Array.prototype
  ) {
    return failDefinition();
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
    return failDefinition();
  }
  for (let index = 0; index < value.length; index += 1) {
    const descriptor = Object.getOwnPropertyDescriptor(value, String(index));
    if (descriptor === undefined || !("value" in descriptor)) {
      return failDefinition();
    }
  }
  return value;
}

function arrayItem(value: readonly unknown[], index: number): unknown {
  const descriptor = Object.getOwnPropertyDescriptor(value, String(index));
  if (descriptor === undefined || !("value" in descriptor)) {
    return failDefinition();
  }
  return descriptor.value;
}

function compareIdentity(left: EntryIdentity, right: EntryIdentity): number {
  if (left[0] !== right[0]) {
    return left[0] < right[0] ? -1 : 1;
  }
  if (left[1] === right[1]) {
    return 0;
  }
  return left[1] < right[1] ? -1 : 1;
}

function normalizeIdentity(value: unknown): EntryIdentity {
  const tuple = requireDenseArray(value);
  if (tuple.length !== 2) {
    return failDefinition();
  }
  const fragment = arrayItem(tuple, 0);
  const localId = arrayItem(tuple, 1);
  if (!isContractId(fragment) || !isLocalId(localId)) {
    return failDefinition();
  }
  return Object.freeze([fragment, localId] as const);
}

function normalizeEntry(value: unknown): FullEntryManifest {
  const keys = plainRecordKeys(value);
  const hasOutput = keys.includes("output");
  const expected = [
    "codec",
    "fragment",
    "id",
    "lifecycle",
    ...(hasOutput ? ["output"] : []),
    "required",
    "visibility",
  ];
  if (
    keys.length !== expected.length ||
    keys.some((key) => !expected.includes(key))
  ) {
    return failDefinition();
  }
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- plainRecordKeys just proved the value is an own-data-property record.
  const record = value as Record<string, unknown>;
  const fragment = ownValue(record, "fragment");
  const id = ownValue(record, "id");
  const lifecycle = ownValue(record, "lifecycle");
  const required = ownValue(record, "required");
  const visibility = ownValue(record, "visibility");
  const output = hasOutput ? ownValue(record, "output") : id;

  if (
    !isContractId(fragment) ||
    !isLocalId(id) ||
    !isOutputName(output) ||
    (lifecycle !== "build" &&
      lifecycle !== "deployment" &&
      lifecycle !== "request") ||
    typeof required !== "boolean" ||
    (visibility !== "private" && visibility !== "public") ||
    (lifecycle === "build" && visibility === "private")
  ) {
    return failDefinition();
  }

  const codec = normalizeCodecDescriptor(
    // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- The exact entry record gate establishes codec's descriptor boundary.
    ownValue(record, "codec") as CodecDescriptor
  );
  if (
    visibility === "private" &&
    codec.kind === "integer" &&
    codec.default !== null
  ) {
    return failDefinition();
  }

  return freezeJsonData({
    codec,
    identity: [fragment, id],
    lifecycle,
    name: output,
    required,
    visibility,
  });
}

function normalizeRule(
  value: unknown,
  entries: ReadonlyMap<string, ReadonlyMap<string, FullEntryManifest>>
): ContractRuleDefinition {
  requireExactKeys(value, ["abi", "entries", "id", "kind"]);
  const id = ownValue(value, "id");
  if (
    ownValue(value, "abi") !== PRESENT_TOGETHER_RULE_ABI ||
    ownValue(value, "kind") !== "present-together" ||
    !isLocalId(id)
  ) {
    return failDefinition();
  }

  const inputEntries = requireDenseArray(ownValue(value, "entries"));
  if (inputEntries.length < 2) {
    return failDefinition();
  }
  const identities: EntryIdentity[] = [];
  for (let index = 0; index < inputEntries.length; index += 1) {
    const identity = normalizeIdentity(arrayItem(inputEntries, index));
    if (
      identities.some(
        (candidate) =>
          candidate[0] === identity[0] && candidate[1] === identity[1]
      ) ||
      lookupEntry(entries, identity) === undefined
    ) {
      return failDefinition();
    }
    identities.push(identity);
  }
  identities.sort(compareIdentity);

  const lifecycles = new Set(
    identities.map((identity) => lookupEntry(entries, identity)?.lifecycle)
  );
  if (lifecycles.size !== 1 || lifecycles.has(undefined)) {
    return failDefinition();
  }

  return freezeJsonData({
    abi: PRESENT_TOGETHER_RULE_ABI,
    entries: identities,
    id,
    kind: "present-together",
  });
}

function normalizeRules(
  values: readonly unknown[],
  entries: ReadonlyMap<string, ReadonlyMap<string, FullEntryManifest>>
): readonly ContractRuleDefinition[] {
  const rules = new Map<string, ContractRuleDefinition>();
  const folded = new Set<string>();
  for (let index = 0; index < values.length; index += 1) {
    const rule = normalizeRule(arrayItem(values, index), entries);
    const existing = rules.get(rule.id);
    if (existing !== undefined) {
      if (canonicalJson(existing) !== canonicalJson(rule)) {
        return failDefinition();
      }
      continue;
    }
    const foldedId = asciiCaseFold(rule.id);
    if (folded.has(foldedId)) {
      return failDefinition();
    }
    folded.add(foldedId);
    rules.set(rule.id, rule);
  }
  return Object.freeze(
    [...rules.values()].toSorted((left, right) =>
      left.id < right.id ? -1 : left.id === right.id ? 0 : 1
    )
  );
}

function normalizeConsumer(value: unknown): ConsumerDefinition {
  requireExactKeys(value, ["entries", "id", "kind"]);
  const id = ownValue(value, "id");
  const kind = ownValue(value, "kind");
  const inputEntries = requireDenseArray(ownValue(value, "entries"));
  if (!isConsumerId(id) || (kind !== "browser" && kind !== "server")) {
    return failDefinition();
  }

  const unique: EntryIdentity[] = [];
  for (let index = 0; index < inputEntries.length; index += 1) {
    const identity = normalizeIdentity(arrayItem(inputEntries, index));
    if (
      unique.some(
        (candidate) =>
          candidate[0] === identity[0] && candidate[1] === identity[1]
      )
    ) {
      return failDefinition();
    }
    unique.push(identity);
  }
  unique.sort(compareIdentity);

  return Object.freeze({
    entries: Object.freeze(unique),
    id,
    kind,
  });
}

function descriptorBytes(entry: FullEntryManifest): string {
  return canonicalJson(entry);
}

function lookupEntry(
  entries: ReadonlyMap<string, ReadonlyMap<string, FullEntryManifest>>,
  identity: EntryIdentity
): FullEntryManifest | undefined {
  return entries.get(identity[0])?.get(identity[1]);
}

function normalizeEntries(
  values: readonly unknown[]
): readonly FullEntryManifest[] {
  const byFragment = new Map<string, Map<string, FullEntryManifest>>();
  const foldedByFragment = new Map<string, Set<string>>();

  for (let index = 0; index < values.length; index += 1) {
    const entry = normalizeEntry(arrayItem(values, index));
    const [fragment, localId] = entry.identity;
    let fragmentEntries = byFragment.get(fragment);
    if (fragmentEntries === undefined) {
      fragmentEntries = new Map();
      byFragment.set(fragment, fragmentEntries);
      foldedByFragment.set(fragment, new Set());
    }

    const existing = fragmentEntries.get(localId);
    if (existing !== undefined) {
      if (descriptorBytes(existing) !== descriptorBytes(entry)) {
        failDefinition();
      }
      continue;
    }

    const folded = asciiCaseFold(localId);
    const foldedNames = foldedByFragment.get(fragment);
    if (foldedNames === undefined || foldedNames.has(folded)) {
      failDefinition();
    }
    foldedNames.add(folded);
    fragmentEntries.set(localId, entry);
  }

  const entries = [...byFragment.values()].flatMap((fragment) => [
    ...fragment.values(),
  ]);
  entries.sort((left, right) => compareIdentity(left.identity, right.identity));
  return Object.freeze(entries);
}

function indexEntries(
  entries: readonly FullEntryManifest[]
): ReadonlyMap<string, ReadonlyMap<string, FullEntryManifest>> {
  const byFragment = new Map<string, Map<string, FullEntryManifest>>();
  for (const entry of entries) {
    const [fragment, localId] = entry.identity;
    let fragmentEntries = byFragment.get(fragment);
    if (fragmentEntries === undefined) {
      fragmentEntries = new Map();
      byFragment.set(fragment, fragmentEntries);
    }
    fragmentEntries.set(localId, entry);
  }
  return byFragment;
}

function normalizeConsumers(
  values: readonly unknown[],
  entries: ReadonlyMap<string, ReadonlyMap<string, FullEntryManifest>>
): readonly ConsumerDefinition[] {
  const consumers = new Map<string, ConsumerDefinition>();
  const folded = new Set<string>();

  for (let index = 0; index < values.length; index += 1) {
    const consumer = normalizeConsumer(arrayItem(values, index));
    const existing = consumers.get(consumer.id);
    if (existing !== undefined) {
      if (canonicalJson(existing) !== canonicalJson(consumer)) {
        failDefinition();
      }
      continue;
    }
    const foldedId = asciiCaseFold(consumer.id);
    if (folded.has(foldedId)) {
      failDefinition();
    }
    folded.add(foldedId);

    const outputNames = new Set<string>();
    for (const identity of consumer.entries) {
      const entry = lookupEntry(entries, identity);
      if (entry === undefined) {
        failDefinition();
      }
      const output = asciiCaseFold(entry.name);
      if (outputNames.has(output)) {
        failDefinition();
      }
      outputNames.add(output);
      if (
        consumer.kind === "browser" &&
        (entry.visibility === "private" ||
          entry.codec.kind === "opaque" ||
          entry.codec.kind === "integer" ||
          entry.codec.kind === "text")
      ) {
        failDefinition();
      }
    }
    consumers.set(consumer.id, consumer);
  }

  return Object.freeze(
    [...consumers.values()].toSorted((left, right) =>
      left.id < right.id ? -1 : left.id === right.id ? 0 : 1
    )
  );
}

function commonProjectionFields(
  contract: string,
  consumer: string,
  hasRules: boolean
) {
  return {
    canonicalisation: CANONICALISATION_ABI,
    codecAbi: CODEC_ABI,
    consumer,
    contract,
    format: PROJECTION_FORMAT,
    formatVersion: hasRules ? RULES_FORMAT_VERSION : FORMAT_VERSION,
    projectionAbi: PROJECTION_ABI,
  } as const;
}

function createProjection(
  contract: string,
  consumer: ConsumerDefinition,
  entries: ReadonlyMap<string, ReadonlyMap<string, FullEntryManifest>>,
  rules: readonly ContractRuleDefinition[]
): ConsumerProjectionManifest {
  const selected = consumer.entries.map((identity) => {
    const entry = lookupEntry(entries, identity);
    if (entry === undefined) {
      return failDefinition();
    }
    return entry;
  });
  selected.sort((left, right) =>
    left.name < right.name ? -1 : left.name === right.name ? 0 : 1
  );
  const selectedIdentities = new Set(
    consumer.entries.map((identity) => canonicalJson(identity))
  );
  const selectedRules = rules.filter((rule) => {
    const selectedCount = rule.entries.filter((identity) =>
      selectedIdentities.has(canonicalJson(identity))
    ).length;
    if (selectedCount !== 0 && selectedCount !== rule.entries.length) {
      return failDefinition();
    }
    if (selectedCount !== 0 && consumer.kind === "browser") {
      return failDefinition();
    }
    return selectedCount === rule.entries.length;
  });

  if (consumer.kind === "browser") {
    const publicEntries: BrowserProjectionEntry[] = selected.map((entry) => {
      if (entry.visibility !== "public" || !isPublicCodec(entry.codec)) {
        return failDefinition();
      }
      return {
        codec: entry.codec,
        identity: entry.identity,
        lifecycle: entry.lifecycle,
        name: entry.name,
        required: entry.required,
      };
    });
    // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- This branch builds every required browser manifest field from validated contract data.
    return freezeJsonData({
      ...commonProjectionFields(contract, consumer.id, false),
      entries: publicEntries,
      kind: "public",
    }) as BrowserProjectionManifest;
  }

  if (selectedRules.length === 0) {
    // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- This branch builds every required server manifest field from validated contract data.
    return freezeJsonData({
      ...commonProjectionFields(contract, consumer.id, false),
      entries: selected,
      kind: "server",
    }) as ServerProjectionManifest;
  }
  return freezeJsonData({
    ...commonProjectionFields(contract, consumer.id, true),
    entries: selected,
    kind: "server",
    rules: selectedRules,
  });
}

function normalizeContractDefinition(definition: unknown): NormalizedContract {
  const definitionKeys = plainRecordKeys(definition);
  const hasRules = definitionKeys.includes("rules");
  const expectedKeys = [
    "consumers",
    "entries",
    "id",
    ...(hasRules ? ["rules"] : []),
  ];
  requireExactKeys(definition, expectedKeys);
  const contract = ownValue(definition, "id");
  if (!isContractId(contract)) {
    return failDefinition();
  }

  const inputEntries = requireDenseArray(ownValue(definition, "entries"));
  const entries = normalizeEntries(inputEntries);
  const entryIndex = indexEntries(entries);
  const rules = hasRules
    ? normalizeRules(
        requireDenseArray(ownValue(definition, "rules")),
        entryIndex
      )
    : Object.freeze([]);
  const inputConsumers = requireDenseArray(ownValue(definition, "consumers"));
  const consumers = normalizeConsumers(inputConsumers, entryIndex);

  const selections: ConsumerSelectionManifest[] = consumers.map((consumer) => ({
    entries: consumer.entries,
    id: consumer.id,
    kind: consumer.kind,
  }));
  const full = (
    rules.length === 0
      ? freezeJsonData({
          canonicalisation: CANONICALISATION_ABI,
          codecAbi: CODEC_ABI,
          consumers: selections,
          contract,
          entries,
          format: CONTRACT_FORMAT,
          formatVersion: FORMAT_VERSION,
          projectionAbi: PROJECTION_ABI,
        })
      : freezeJsonData({
          canonicalisation: CANONICALISATION_ABI,
          codecAbi: CODEC_ABI,
          consumers: selections,
          contract,
          entries,
          format: CONTRACT_FORMAT,
          formatVersion: RULES_FORMAT_VERSION,
          projectionAbi: PROJECTION_ABI,
          rules,
        })
  ) as FullContractManifest;
  const projections = consumers.map((consumer) =>
    createProjection(contract, consumer, entryIndex, rules)
  );

  return Object.freeze({
    full,
    projections: Object.freeze(projections),
  });
}

async function compileManifest<TManifest>(
  manifest: TManifest
): Promise<CompiledManifest<TManifest>> {
  const text = canonicalJson(manifest);
  const canonicalBytes = canonicalJsonBytes(manifest);
  const digest = await sha256Digest(canonicalBytes);
  return Object.freeze({
    get bytes() {
      return new Uint8Array(canonicalBytes);
    },
    digest,
    manifest,
    text,
  });
}

export async function compileContract(
  definition: unknown
): Promise<CompiledContract> {
  const normalized = normalizeContractDefinition(definition);
  const full = await compileManifest(normalized.full);
  const projections: CompiledProjection[] = [];
  for (const projection of normalized.projections) {
    projections.push(await compileManifest(projection));
  }
  return Object.freeze({
    full,
    projections: Object.freeze(projections),
  });
}

export function findProjection(
  compiled: CompiledContract,
  consumer: string
): CompiledProjection | undefined {
  return compiled.projections.find(
    (projection) => projection.manifest.consumer === consumer
  );
}

export function defineContract(
  definition: ContractDefinition
): ContractDefinition {
  normalizeContractDefinition(definition);
  return definition;
}

export function presentTogetherRule(
  id: string,
  entries: readonly EntryIdentity[]
): ContractRuleDefinition {
  return {
    abi: PRESENT_TOGETHER_RULE_ABI,
    entries,
    id,
    kind: "present-together",
  };
}
