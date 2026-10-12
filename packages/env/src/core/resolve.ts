import { resolvePortableValue } from "./codecs.ts";
import { aggregateFailure, failure, success } from "./diagnostics.ts";
import type {
  AggregateResult,
  CoreDiagnostic,
  Failure,
  Result,
} from "./diagnostics.ts";
import { asciiCaseFold, isOutputName, isRawSourceName } from "./identity.ts";
import { deepFreezeJson } from "./json.ts";
import { resolveServerValue } from "./server-codecs.ts";
import type {
  BrowserProjectionManifest,
  ConsumerProjectionManifest,
  JsonValue,
  Lifecycle,
  ResolutionBinding,
  ResolvedConfiguration,
} from "./types.ts";

type ProjectionEntry = ConsumerProjectionManifest["entries"][number];

function isPlainRecord(value: unknown): value is Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    return false;
  }
  const prototype = Reflect.getPrototypeOf(value);
  return prototype === Object.prototype || prototype === null;
}

function bindingFields(value: unknown): ResolutionBinding | undefined {
  if (!isPlainRecord(value)) {
    return undefined;
  }
  const keys = Reflect.ownKeys(value);
  if (
    keys.length !== 2 ||
    !keys.includes("entry") ||
    !keys.includes("source")
  ) {
    return undefined;
  }
  const entryDescriptor = Object.getOwnPropertyDescriptor(value, "entry");
  const sourceDescriptor = Object.getOwnPropertyDescriptor(value, "source");
  if (
    entryDescriptor === undefined ||
    !("value" in entryDescriptor) ||
    sourceDescriptor === undefined ||
    !("value" in sourceDescriptor) ||
    !isOutputName(entryDescriptor.value) ||
    !isRawSourceName(sourceDescriptor.value)
  ) {
    return undefined;
  }
  return Object.freeze({
    entry: entryDescriptor.value,
    source: sourceDescriptor.value,
  });
}

function normalizeBindings(
  projection: ConsumerProjectionManifest,
  bindings: readonly ResolutionBinding[]
): ReadonlyMap<string, ResolutionBinding> | undefined {
  if (
    !Array.isArray(bindings) ||
    Object.getPrototypeOf(bindings) !== Array.prototype ||
    Reflect.ownKeys(bindings).length !== bindings.length + 1
  ) {
    return undefined;
  }

  const projectionNames = new Set(
    projection.entries.map((entry) => entry.name)
  );
  const byEntry = new Map<string, ResolutionBinding>();
  const foldedEntries = new Set<string>();
  const foldedSources = new Set<string>();

  for (let index = 0; index < bindings.length; index += 1) {
    const item = Object.getOwnPropertyDescriptor(bindings, String(index));
    if (item === undefined || !("value" in item)) {
      return undefined;
    }
    const binding = bindingFields(item.value);
    if (binding === undefined || !projectionNames.has(binding.entry)) {
      return undefined;
    }
    const entryKey = asciiCaseFold(binding.entry);
    const sourceKey = asciiCaseFold(binding.source);
    if (foldedEntries.has(entryKey) || foldedSources.has(sourceKey)) {
      return undefined;
    }
    foldedEntries.add(entryKey);
    foldedSources.add(sourceKey);
    byEntry.set(binding.entry, binding);
  }

  return byEntry;
}

function getOwnSourceValue(
  source: Record<string, unknown>,
  key: string
):
  | Readonly<{ found: false }>
  | Readonly<{ found: false; invalid: true }>
  | Readonly<{ found: true; value: unknown }> {
  const descriptor = Object.getOwnPropertyDescriptor(source, key);
  if (descriptor === undefined) {
    return Object.freeze({ found: false as const });
  }
  if (!("value" in descriptor)) {
    return Object.freeze({
      found: false as const,
      invalid: true as const,
    });
  }
  const resolved: unknown = descriptor.value;
  return Object.freeze({
    found: true as const,
    value: resolved,
  });
}

function resolveOne(
  projection: ConsumerProjectionManifest,
  entry: ProjectionEntry,
  lifecycle: Lifecycle,
  binding: ResolutionBinding | undefined,
  source: Record<string, unknown>
): Result<readonly [string, JsonValue] | undefined> {
  const fields = {
    codec: entry.codec.abi,
    consumer: projection.consumer,
    entry: entry.name,
    lifecycle: entry.lifecycle,
  } as const;

  if (entry.lifecycle !== lifecycle) {
    return failure("ENV_LIFECYCLE_ACCESS", fields);
  }
  if (binding === undefined) {
    return failure("ENV_BINDING_MISSING", fields);
  }
  if (entry.codec.kind === "opaque") {
    return failure("ENV_OPAQUE_UNSUPPORTED", fields);
  }

  const raw = getOwnSourceValue(source, binding.source);
  if (!raw.found) {
    if ("invalid" in raw) {
      return failure("ENV_SOURCE_INVALID", {
        consumer: projection.consumer,
      });
    }
    if (entry.codec.kind === "integer" || entry.codec.kind === "text") {
      const checked = resolveServerValue(entry.codec, undefined);
      if (!checked.ok) {
        return failure("ENV_INVALID_VALUE", fields);
      }
      if (!checked.present) {
        return entry.required
          ? failure("ENV_MISSING_VALUE", fields)
          : success(undefined);
      }
      return success(Object.freeze([entry.name, checked.value] as const));
    }
    return entry.required
      ? failure("ENV_MISSING_VALUE", fields)
      : success(undefined);
  }
  if (entry.codec.kind === "integer" || entry.codec.kind === "text") {
    const checked = resolveServerValue(entry.codec, raw.value);
    if (!checked.ok) {
      return failure("ENV_INVALID_VALUE", fields);
    }
    if (!checked.present) {
      return entry.required
        ? failure("ENV_MISSING_VALUE", fields)
        : success(undefined);
    }
    return success(Object.freeze([entry.name, checked.value] as const));
  }

  const checked = resolvePortableValue(entry.codec, raw.value);
  if (!checked.ok) {
    return failure("ENV_INVALID_VALUE", fields);
  }
  if (!checked.present) {
    return entry.required
      ? failure("ENV_MISSING_VALUE", fields)
      : success(undefined);
  }
  return success(Object.freeze([entry.name, checked.value] as const));
}

function validateResolutionInputs(
  projection: ConsumerProjectionManifest,
  bindings: readonly ResolutionBinding[],
  source: unknown
):
  | Readonly<{
      bindings: ReadonlyMap<string, ResolutionBinding>;
      ok: true;
      source: Record<string, unknown>;
    }>
  | Readonly<{
      ok: false;
      result: Failure;
    }> {
  if (!isPlainRecord(source)) {
    return Object.freeze({
      ok: false as const,
      result: failure("ENV_SOURCE_INVALID", {
        consumer: projection.consumer,
      }),
    });
  }
  const normalizedBindings = normalizeBindings(projection, bindings);
  if (normalizedBindings === undefined) {
    return Object.freeze({
      ok: false as const,
      result: failure("ENV_BINDING_INVALID", {
        consumer: projection.consumer,
      }),
    });
  }
  return Object.freeze({
    bindings: normalizedBindings,
    ok: true as const,
    source,
  });
}

const identityKey = (identity: readonly [string, string]): string =>
  `${identity[0]}\u0000${identity[1]}`;

const compareDiagnostics = (
  left: CoreDiagnostic,
  right: CoreDiagnostic
): number => {
  const leftGroup = left.code === "ENV_RULE_VIOLATION" ? 1 : 0;
  const rightGroup = right.code === "ENV_RULE_VIOLATION" ? 1 : 0;
  if (leftGroup !== rightGroup) {
    return leftGroup - rightGroup;
  }
  const leftKey = [
    left.entry ?? "",
    left.entries?.join("\u0000") ?? "",
    left.rule ?? "",
    left.code,
  ].join("\u0001");
  const rightKey = [
    right.entry ?? "",
    right.entries?.join("\u0000") ?? "",
    right.rule ?? "",
    right.code,
  ].join("\u0001");
  return leftKey < rightKey ? -1 : leftKey === rightKey ? 0 : 1;
};

export function resolveLifecycleAll(
  projection: ConsumerProjectionManifest,
  lifecycle: Lifecycle,
  bindings: readonly ResolutionBinding[],
  source: unknown
): AggregateResult<ResolvedConfiguration> {
  if (
    lifecycle !== "build" &&
    lifecycle !== "deployment" &&
    lifecycle !== "request"
  ) {
    return aggregateFailure([
      failure("ENV_LIFECYCLE_ACCESS", {
        consumer: projection.consumer,
      }).diagnostic,
    ]);
  }
  const inputs = validateResolutionInputs(projection, bindings, source);
  if (!inputs.ok) {
    return aggregateFailure([inputs.result.diagnostic]);
  }

  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- The null-prototype output receives only codec-validated JsonValue results below.
  const output = Object.create(null) as Record<string, JsonValue>;
  const states = new Map<string, "absent" | "invalid" | "present">();
  const diagnostics: CoreDiagnostic[] = [];
  const selected = projection.entries
    .filter((entry) => entry.lifecycle === lifecycle)
    .toSorted((left, right) =>
      left.name < right.name ? -1 : left.name === right.name ? 0 : 1
    );
  for (const entry of selected) {
    const result = resolveOne(
      projection,
      entry,
      lifecycle,
      inputs.bindings.get(entry.name),
      inputs.source
    );
    const key = identityKey(entry.identity);
    if (!result.ok) {
      diagnostics.push(result.diagnostic);
      states.set(key, "invalid");
      continue;
    }
    if (result.value === undefined) {
      states.set(key, "absent");
      continue;
    }
    states.set(key, "present");
    const [name, value] = result.value;
    Object.defineProperty(output, name, {
      configurable: true,
      enumerable: true,
      value,
      writable: true,
    });
  }

  if ("rules" in projection) {
    const entriesByIdentity = new Map(
      selected.map((entry) => [identityKey(entry.identity), entry])
    );
    for (const rule of projection.rules) {
      const ruleEntries = rule.entries.map((identity) =>
        entriesByIdentity.get(identityKey(identity))
      );
      if (
        ruleEntries.some((entry) => entry === undefined) ||
        ruleEntries.some((entry) => entry?.lifecycle !== lifecycle)
      ) {
        continue;
      }
      const ruleStates = rule.entries.map((identity) =>
        states.get(identityKey(identity))
      );
      if (ruleStates.some((state) => state === "invalid")) {
        continue;
      }
      const presentCount = ruleStates.filter(
        (state) => state === "present"
      ).length;
      if (presentCount !== 0 && presentCount !== ruleStates.length) {
        diagnostics.push(
          failure("ENV_RULE_VIOLATION", {
            consumer: projection.consumer,
            entries: ruleEntries
              .map((entry) => entry?.name)
              .filter((name): name is string => name !== undefined)
              .toSorted(),
            lifecycle,
            rule: rule.id,
          }).diagnostic
        );
      }
    }
  }

  if (diagnostics.length > 0) {
    diagnostics.sort(compareDiagnostics);
    return aggregateFailure(diagnostics);
  }
  return success(deepFreezeJson(output));
}

export function resolveLifecycle(
  projection: ConsumerProjectionManifest,
  lifecycle: Lifecycle,
  bindings: readonly ResolutionBinding[],
  source: unknown
): Result<ResolvedConfiguration> {
  const result = resolveLifecycleAll(projection, lifecycle, bindings, source);
  if (result.ok) {
    return result;
  }
  const first = result.diagnostics[0];
  return first === undefined
    ? failure("ENV_SOURCE_INVALID", { consumer: projection.consumer })
    : Object.freeze({ diagnostic: first, ok: false as const });
}

export function resolvePublicLifecycle(
  projection: BrowserProjectionManifest,
  lifecycle: Lifecycle,
  bindings: readonly ResolutionBinding[],
  source: unknown
): Result<ResolvedConfiguration> {
  if (projection.kind !== "public") {
    return failure("ENV_VISIBILITY_ACCESS", {
      consumer: projection.consumer,
    });
  }
  return resolveLifecycle(projection, lifecycle, bindings, source);
}

export function resolveEntry(
  projection: ConsumerProjectionManifest,
  entryName: string,
  lifecycle: Lifecycle,
  bindings: readonly ResolutionBinding[],
  source: unknown
): Result<JsonValue | undefined> {
  const entry = projection.entries.find(
    (candidate) => candidate.name === entryName
  );
  if (entry === undefined) {
    return failure("ENV_BINDING_INVALID", {
      consumer: projection.consumer,
    });
  }
  const inputs = validateResolutionInputs(projection, bindings, source);
  if (!inputs.ok) {
    return inputs.result;
  }
  const result = resolveOne(
    projection,
    entry,
    lifecycle,
    inputs.bindings.get(entry.name),
    inputs.source
  );
  if (!result.ok) {
    return result;
  }
  if (result.value === undefined) {
    return success(undefined);
  }
  return success(result.value[1]);
}
