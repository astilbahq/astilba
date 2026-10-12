import type {
  AggregateFailure,
  CoreDiagnostic,
  CoreDiagnostics,
  Success,
} from "./model.ts";

const comparisonKey = (item: CoreDiagnostic): string => {
  const entry = "entry" in item ? item.entry : "";
  const entries = "entries" in item ? item.entries.join("\u0000") : "";
  const rule = "rule" in item ? item.rule : "";
  return [entry, entries, rule, item.code].join("\u0001");
};

const diagnosticIdentity = (item: CoreDiagnostic): string => {
  const consumer = "consumer" in item ? item.consumer : "";
  const lifecycle = "lifecycle" in item ? item.lifecycle : "";
  const codec = "codec" in item ? item.codec : "";
  return [comparisonKey(item), consumer, lifecycle, codec].join("\u0002");
};

const compareDiagnostics = (
  left: CoreDiagnostic,
  right: CoreDiagnostic
): number => {
  const leftGroup = left.code === "ENV_RULE_VIOLATION" ? 1 : 0;
  const rightGroup = right.code === "ENV_RULE_VIOLATION" ? 1 : 0;
  if (leftGroup !== rightGroup) {
    return leftGroup - rightGroup;
  }
  const leftKey = comparisonKey(left);
  const rightKey = comparisonKey(right);
  if (leftKey === rightKey) {
    return 0;
  }
  return leftKey < rightKey ? -1 : 1;
};

export const diagnostic = (item: CoreDiagnostic): CoreDiagnostic => {
  if (!("consumer" in item)) {
    return Object.freeze({ code: item.code });
  }
  if (!("entry" in item) && !("entries" in item)) {
    return Object.freeze({
      code: item.code,
      consumer: item.consumer,
    });
  }
  if ("entries" in item) {
    return Object.freeze({
      code: item.code,
      consumer: item.consumer,
      entries: Object.freeze([...item.entries]),
      lifecycle: item.lifecycle,
      rule: item.rule,
    });
  }
  if (
    item.code === "ENV_OPAQUE_UNSUPPORTED" ||
    item.code === "ENV_VALIDATOR_ASYNC_UNSUPPORTED"
  ) {
    return Object.freeze({
      code: item.code,
      codec: item.codec,
      consumer: item.consumer,
      entry: item.entry,
      lifecycle: item.lifecycle,
    });
  }
  const output = {
    code: item.code,
    codec: item.codec,
    consumer: item.consumer,
    entry: item.entry,
    lifecycle: item.lifecycle,
  };
  return Object.freeze(output);
};

export const cloneDiagnostics = (
  items: readonly CoreDiagnostic[]
): CoreDiagnostics => {
  const owned = items.map(diagnostic).toSorted(compareDiagnostics);
  const unique: CoreDiagnostic[] = [];
  const identities = new Set<string>();
  for (const item of owned) {
    const identity = diagnosticIdentity(item);
    if (!identities.has(identity)) {
      unique.push(item);
      identities.add(identity);
    }
  }
  if (unique.length === 0) {
    throw new TypeError("A diagnostic is required.");
  }
  const [first, ...rest] = unique;
  if (!first) {
    throw new TypeError("A diagnostic is required.");
  }
  const diagnostics: [CoreDiagnostic, ...CoreDiagnostic[]] = [first, ...rest];
  return Object.freeze(diagnostics);
};

export const aggregateFailure = (
  diagnostics: readonly CoreDiagnostic[]
): AggregateFailure =>
  Object.freeze({
    diagnostics: cloneDiagnostics(diagnostics),
    ok: false as const,
  });

export const success = <TValue>(value: TValue): Success<TValue> =>
  Object.freeze({
    ok: true as const,
    value,
  });
