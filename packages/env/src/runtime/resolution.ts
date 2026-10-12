import { resolvePortableValue } from "../core/codecs.ts";
import { deepFreezeJson } from "../core/json.ts";
import { resolveServerValue } from "../core/server-codecs.ts";
import type { JsonValue } from "../core/types.ts";
import type {
  AggregateResult,
  CoreDiagnostic,
  NormalizedTarget,
  OwnedConfiguration,
  ServerProjectionEntry,
} from "./model.ts";
import { aggregateFailure, diagnostic, success } from "./results.ts";
import { resolveStandardSchemaRuntime } from "./standard-schema.ts";

const MAXIMUM_SOURCE_VALUE_BYTES = 1_048_576;

type EntryState = "absent" | "invalid" | "present";

type MaterializedSource =
  | Readonly<{
      ok: false;
    }>
  | Readonly<{
      ok: true;
      values: Readonly<Record<string, string | undefined>>;
    }>;

const identityKey = (identity: readonly [string, string]): string =>
  `${identity[0]}\u0000${identity[1]}`;

const withinUtf8Bound = (value: string): boolean => {
  let bytes = 0;
  for (let index = 0; index < value.length; index += 1) {
    // oxlint-disable-next-line unicorn/prefer-code-point -- UTF-8 accounting must inspect UTF-16 code units so surrogate pairs count as one four-byte scalar.
    const code = value.charCodeAt(index);
    if (code <= 0x7f) {
      bytes += 1;
    } else if (code <= 0x7_ff) {
      bytes += 2;
    } else if (code >= 0xd8_00 && code <= 0xdb_ff) {
      // oxlint-disable-next-line unicorn/prefer-code-point -- The paired-surrogate check intentionally reads the next UTF-16 code unit.
      const next = value.charCodeAt(index + 1);
      if (next >= 0xdc_00 && next <= 0xdf_ff) {
        bytes += 4;
        index += 1;
      } else {
        bytes += 3;
      }
    } else {
      bytes += 3;
    }
    if (bytes > MAXIMUM_SOURCE_VALUE_BYTES) {
      return false;
    }
  }
  return true;
};

const materializeSource = (
  target: NormalizedTarget,
  source: unknown
): MaterializedSource => {
  try {
    if (
      typeof source !== "object" ||
      source === null ||
      Array.isArray(source)
    ) {
      return Object.freeze({ ok: false as const });
    }
    const names = target.bindings.map((binding) => binding.source).toSorted();
    const output: Record<string, string | undefined> = {};
    Object.setPrototypeOf(output, null);
    for (const name of names) {
      const descriptor = Object.getOwnPropertyDescriptor(source, name);
      if (
        descriptor !== undefined &&
        (!("value" in descriptor) ||
          (descriptor.value !== undefined &&
            typeof descriptor.value !== "string"))
      ) {
        return Object.freeze({ ok: false as const });
      }
      Object.defineProperty(output, name, {
        configurable: true,
        enumerable: true,
        value: descriptor === undefined ? undefined : descriptor.value,
        writable: true,
      });
    }
    return Object.freeze({
      ok: true as const,
      values: Object.freeze(output),
    });
  } catch {
    return Object.freeze({ ok: false as const });
  }
};

const entryDiagnostic = (
  target: NormalizedTarget,
  entry: NormalizedTarget["selected"][number],
  code:
    | "ENV_INVALID_VALUE"
    | "ENV_MISSING_VALUE"
    | "ENV_VALIDATOR_ASYNC_UNSUPPORTED"
): CoreDiagnostic => {
  if (code === "ENV_VALIDATOR_ASYNC_UNSUPPORTED") {
    if (entry.codec.kind !== "opaque" || entry.lifecycle === "build") {
      return diagnostic({ code: "ENV_CONTRACT_INVALID" });
    }
    return diagnostic({
      code,
      codec: entry.codec.abi,
      consumer: target.projection.consumer,
      entry: entry.name,
      lifecycle: entry.lifecycle,
    });
  }
  return diagnostic({
    code,
    codec: entry.codec.abi,
    consumer: target.projection.consumer,
    entry: entry.name,
    lifecycle: entry.lifecycle,
  });
};

const resolveFirstParty = (
  entry: NormalizedTarget["selected"][number],
  input: string | undefined
):
  | Readonly<{ ok: false }>
  | Readonly<{ ok: true; present: false }>
  | Readonly<{ ok: true; present: true; value: JsonValue }> => {
  try {
    if (entry.codec.kind === "integer") {
      return resolveServerValue(entry.codec, input);
    }
    if (entry.codec.kind === "text") {
      return resolveServerValue(entry.codec, input);
    }
    if (entry.codec.kind === "opaque") {
      return Object.freeze({ ok: false as const });
    }
    return resolvePortableValue(entry.codec, input);
  } catch {
    return Object.freeze({ ok: false as const });
  }
};

const evaluateRules = (
  target: NormalizedTarget,
  states: ReadonlyMap<string, EntryState>
): readonly CoreDiagnostic[] => {
  if (!("rules" in target.projection)) {
    return [];
  }
  const selectedByIdentity = new Map(
    target.selected.map((entry) => [identityKey(entry.identity), entry])
  );
  const diagnostics: CoreDiagnostic[] = [];
  for (const rule of target.projection.rules) {
    const entries = rule.entries.map((identity) =>
      selectedByIdentity.get(identityKey(identity))
    );
    if (
      entries.some((entry) => entry === undefined) ||
      entries.some((entry) => entry?.lifecycle !== target.lifecycle)
    ) {
      continue;
    }
    const ruleStates = rule.entries.map((identity) =>
      states.get(identityKey(identity))
    );
    if (ruleStates.some((state) => state === "invalid")) {
      continue;
    }
    const present = ruleStates.filter((state) => state === "present").length;
    if (present !== 0 && present !== ruleStates.length) {
      diagnostics.push(
        diagnostic({
          code: "ENV_RULE_VIOLATION",
          consumer: target.projection.consumer,
          entries: entries
            .map((entry) => entry?.name)
            .filter((name): name is string => name !== undefined)
            .toSorted(),
          lifecycle: target.lifecycle,
          rule: rule.id,
        })
      );
    }
  }
  return diagnostics;
};

export const opaqueRefusalDiagnostics = (
  target: NormalizedTarget
): readonly CoreDiagnostic[] =>
  target.selected
    .filter(
      (
        entry
      ): entry is ServerProjectionEntry & {
        codec: Extract<ServerProjectionEntry["codec"], { kind: "opaque" }>;
        lifecycle: "deployment" | "request";
      } => entry.codec.kind === "opaque"
    )
    .map((entry) =>
      diagnostic({
        code: "ENV_OPAQUE_UNSUPPORTED",
        codec: entry.codec.abi,
        consumer: target.projection.consumer,
        entry: entry.name,
        lifecycle: entry.lifecycle,
      })
    );

export const resolveTarget = (
  target: NormalizedTarget,
  source: unknown,
  schemas?: ReadonlyMap<string, unknown>
): AggregateResult<OwnedConfiguration> => {
  const materialized = materializeSource(target, source);
  if (!materialized.ok) {
    return aggregateFailure([
      diagnostic({
        code: "ENV_SOURCE_INVALID",
        consumer: target.projection.consumer,
      }),
    ]);
  }
  const bindingByEntry = new Map(
    target.bindings.map((binding) => [binding.entry, binding.source])
  );
  const oversized = new Set<string>();
  for (const entry of target.selected) {
    const sourceName = bindingByEntry.get(entry.name);
    if (sourceName === undefined) {
      return aggregateFailure([diagnostic({ code: "ENV_CONTRACT_INVALID" })]);
    }
    const input = materialized.values[sourceName];
    if (input !== undefined && !withinUtf8Bound(input)) {
      oversized.add(entry.name);
    }
  }

  const output: Record<string, JsonValue> = {};
  Object.setPrototypeOf(output, null);
  const diagnostics: CoreDiagnostic[] = [];
  const states = new Map<string, EntryState>();
  for (const entry of target.selected) {
    const key = identityKey(entry.identity);
    const sourceName = bindingByEntry.get(entry.name);
    if (sourceName === undefined) {
      return aggregateFailure([diagnostic({ code: "ENV_CONTRACT_INVALID" })]);
    }
    if (oversized.has(entry.name)) {
      diagnostics.push(entryDiagnostic(target, entry, "ENV_INVALID_VALUE"));
      states.set(key, "invalid");
      continue;
    }
    const input = materialized.values[sourceName];
    if (entry.codec.kind === "opaque") {
      const result = resolveStandardSchemaRuntime({
        codec: entry.codec,
        input,
        required: entry.required,
        schema: schemas?.get(entry.name),
      });
      if (!result.ok) {
        diagnostics.push(entryDiagnostic(target, entry, result.code));
        states.set(key, "invalid");
        continue;
      }
      if (!result.present) {
        states.set(key, "absent");
        continue;
      }
      Object.defineProperty(output, entry.name, {
        configurable: true,
        enumerable: true,
        value: result.value,
        writable: true,
      });
      states.set(key, "present");
      continue;
    }

    const result = resolveFirstParty(entry, input);
    if (!result.ok) {
      diagnostics.push(entryDiagnostic(target, entry, "ENV_INVALID_VALUE"));
      states.set(key, "invalid");
      continue;
    }
    if (!result.present) {
      if (entry.required) {
        diagnostics.push(entryDiagnostic(target, entry, "ENV_MISSING_VALUE"));
        states.set(key, "invalid");
      } else {
        states.set(key, "absent");
      }
      continue;
    }
    Object.defineProperty(output, entry.name, {
      configurable: true,
      enumerable: true,
      value: result.value,
      writable: true,
    });
    states.set(key, "present");
  }

  diagnostics.push(...evaluateRules(target, states));
  if (diagnostics.length > 0) {
    return aggregateFailure(diagnostics);
  }
  try {
    return success(deepFreezeJson(output));
  } catch {
    return aggregateFailure([diagnostic({ code: "ENV_CONTRACT_INVALID" })]);
  }
};
