import { parseTree } from "jsonc-parser";
import type { Node as JsoncNode, ParseError } from "jsonc-parser";

import { isLocalId, isRawSourceName } from "../core/index.ts";
import type { ConsumerProjectionManifest } from "../core/index.ts";
import { normalizeProjectionForProvider } from "../runtime/validation.ts";
import { classifyCloudflareBindingKind } from "./cloudflare.ts";
import type {
  CheckWranglerConformanceInput,
  ProviderBindingPlan,
  ProviderBindingPlanEntry,
  ProviderConformanceBinding,
  ProviderConformanceGrade,
  ProviderConformanceIssue,
  ProviderConformanceIssueCode,
  ProviderConformanceReport,
  SecretBindingInventory,
  WranglerBindingMetadata,
} from "./types.ts";

const PORTABLE_RAW_NAME = /^[A-Z_][A-Z0-9_]{0,127}$/u;
const MAXIMUM_JSONC_BYTES = 1_048_576;
const MAXIMUM_JSONC_ARRAY_ITEMS = 65_536;
const MAXIMUM_JSONC_CONTAINER_ITEMS = 65_536;
const MAXIMUM_JSONC_DEPTH = 64;
const MAXIMUM_JSONC_OBJECT_KEYS = 65_536;
const MAXIMUM_BINDING_ROWS = 2048;
const TEXT_ENCODER = new TextEncoder();
const METADATA_IDENTIFIER = /^[A-Za-z0-9][A-Za-z0-9._/@:+-]{0,254}$/u;
const AMBIGUOUS_OBSERVED_KIND = Symbol("ambiguous observed binding kind");

export type ProviderMetadataErrorCode =
  | "BINDING_PLAN_INVALID"
  | "BINDING_PLAN_UNSUPPORTED"
  | "SECRET_INVENTORY_INVALID"
  | "SECRET_INVENTORY_UNSUPPORTED"
  | "WRANGLER_JSONC_INVALID";

export class ProviderMetadataError extends Error {
  public readonly code: ProviderMetadataErrorCode;

  public constructor(code: ProviderMetadataErrorCode) {
    super(providerMetadataMessage(code));
    this.code = code;
    this.name = "ProviderMetadataError";
  }
}

export function inspectWranglerJsonc(jsonc: string): WranglerBindingMetadata {
  try {
    if (
      typeof jsonc !== "string" ||
      jsonc.startsWith("\uFEFF") ||
      TEXT_ENCODER.encode(jsonc).byteLength > MAXIMUM_JSONC_BYTES
    ) {
      throw new ProviderMetadataError("WRANGLER_JSONC_INVALID");
    }
    const errors: ParseError[] = [];
    const root = parseTree(jsonc, errors, {
      allowEmptyContent: false,
      allowTrailingComma: true,
      disallowComments: false,
    });
    if (root === undefined || errors.length > 0 || root.type !== "object") {
      throw new ProviderMetadataError("WRANGLER_JSONC_INVALID");
    }

    assertNoDuplicateProperties(root, "WRANGLER_JSONC_INVALID");
    assertJsoncLimits(root, "WRANGLER_JSONC_INVALID");
    if (objectProperty(root, "env") !== undefined) {
      throw new ProviderMetadataError("WRANGLER_JSONC_INVALID");
    }
    const bindings = new Map<string, "json" | "kv_namespace" | "plain_text">();
    const vars = objectProperty(root, "vars");
    if (vars !== undefined) {
      if (vars.type !== "object") {
        throw new ProviderMetadataError("WRANGLER_JSONC_INVALID");
      }
      const entries = objectEntries(vars);
      if (entries.length > MAXIMUM_BINDING_ROWS) {
        throw new ProviderMetadataError("WRANGLER_JSONC_INVALID");
      }
      for (const [name, valueNode] of entries) {
        assertPortableName(name, "WRANGLER_JSONC_INVALID");
        addWranglerBinding(
          bindings,
          name,
          valueNode.type === "string" ? "plain_text" : "json",
          "WRANGLER_JSONC_INVALID"
        );
      }
    }

    const kvNamespaces = objectProperty(root, "kv_namespaces");
    if (kvNamespaces !== undefined) {
      if (kvNamespaces.type !== "array") {
        throw new ProviderMetadataError("WRANGLER_JSONC_INVALID");
      }
      const namespaces = kvNamespaces.children ?? [];
      if (namespaces.length > MAXIMUM_BINDING_ROWS) {
        throw new ProviderMetadataError("WRANGLER_JSONC_INVALID");
      }
      for (const namespace of namespaces) {
        if (namespace.type !== "object") {
          throw new ProviderMetadataError("WRANGLER_JSONC_INVALID");
        }
        const bindingNode = objectProperty(namespace, "binding");
        const name = stringNodeValue(bindingNode, "WRANGLER_JSONC_INVALID");
        assertPortableName(name, "WRANGLER_JSONC_INVALID");
        addWranglerBinding(
          bindings,
          name,
          "kv_namespace",
          "WRANGLER_JSONC_INVALID"
        );
      }
    }

    return Object.freeze({
      bindings: Object.freeze(
        [...bindings]
          .toSorted(([left], [right]) => compareText(left, right))
          .map(([name, kind]) => Object.freeze({ kind, name }))
      ),
      format: "astilba.env.wrangler-metadata/v1",
      source: "offline-jsonc",
    });
  } catch (error) {
    if (error instanceof ProviderMetadataError) {
      throw error;
    }
    throw new ProviderMetadataError("WRANGLER_JSONC_INVALID");
  }
}

export function parseSecretBindingInventory(
  input: unknown
): SecretBindingInventory {
  try {
    const record = exactDataRecord(
      input,
      ["bindings", "format", "target"],
      "SECRET_INVENTORY_INVALID"
    );
    if (
      isRecognisedGreaterVersion(
        record.format,
        "astilba.env.binding-inventory/v"
      )
    ) {
      throw new ProviderMetadataError("SECRET_INVENTORY_UNSUPPORTED");
    }
    if (
      record.format !== "astilba.env.binding-inventory/v1" ||
      typeof record.target !== "string" ||
      !isLocalId(record.target)
    ) {
      throw new ProviderMetadataError("SECRET_INVENTORY_INVALID");
    }
    const inputBindings = exactDataArray(
      record.bindings,
      MAXIMUM_BINDING_ROWS,
      "SECRET_INVENTORY_INVALID"
    );

    const names = new Set<string>();
    const foldedNames = new Set<string>();
    const bindings = inputBindings.map((inputBinding) => {
      const binding = exactDataRecord(
        inputBinding,
        ["kind", "name"],
        "SECRET_INVENTORY_INVALID"
      );
      if (binding.kind !== "secret_text" || typeof binding.name !== "string") {
        throw new ProviderMetadataError("SECRET_INVENTORY_INVALID");
      }
      assertPortableName(binding.name, "SECRET_INVENTORY_INVALID");
      const folded = asciiFold(binding.name);
      if (names.has(binding.name) || foldedNames.has(folded)) {
        throw new ProviderMetadataError("SECRET_INVENTORY_INVALID");
      }
      names.add(binding.name);
      foldedNames.add(folded);
      return Object.freeze({
        kind: "secret_text" as const,
        name: binding.name,
      });
    });

    bindings.sort((left, right) => compareText(left.name, right.name));
    return Object.freeze({
      bindings: Object.freeze(bindings),
      format: "astilba.env.binding-inventory/v1",
      target: record.target,
    });
  } catch (error) {
    if (error instanceof ProviderMetadataError) {
      throw error;
    }
    throw new ProviderMetadataError("SECRET_INVENTORY_INVALID");
  }
}

export function checkWranglerBindingConformance(
  input: CheckWranglerConformanceInput
): ProviderConformanceReport {
  try {
    const normalizedInput = normalizeConformanceInput(input);
    const bindingPlan = validateBindingPlan(normalizedInput.bindingPlan);
    validateProjectionMapping(bindingPlan, normalizedInput.projection);
    const wrangler = inspectWranglerJsonc(normalizedInput.wranglerJsonc);
    const requiresSecrets = bindingPlan.bindings.some(
      (binding) => binding.channel !== "build" && binding.kind === "secret_text"
    );
    const inventory =
      normalizedInput.secretInventory === undefined
        ? undefined
        : parseSecretBindingInventory(normalizedInput.secretInventory);

    if (inventory !== undefined && inventory.target !== bindingPlan.target) {
      throw new ProviderMetadataError("SECRET_INVENTORY_INVALID");
    }

    const issues: ProviderConformanceIssue[] = [];
    const { expected, hasUnknown } = expectedProviderBindings(
      bindingPlan,
      issues
    );
    const observed = new Map<string, string | typeof AMBIGUOUS_OBSERVED_KIND>();

    for (const binding of wrangler.bindings) {
      mergeObservedBinding(observed, binding.name, binding.kind, issues);
    }
    for (const binding of inventory?.bindings ?? []) {
      mergeObservedBinding(observed, binding.name, binding.kind, issues);
    }

    const bindings: ProviderConformanceBinding[] = [];
    for (const [name, expectation] of [...expected].toSorted(
      ([left], [right]) => compareText(left, right)
    )) {
      const observedKind = observed.get(name);
      const secretInventoryMissing =
        expectation.kind === "secret_text" && inventory === undefined;
      let status: ProviderConformanceBinding["status"];
      if (secretInventoryMissing) {
        addIssue(issues, "SECRET_INVENTORY_UNVERIFIED", name);
      }
      if (observedKind !== undefined && observedKind !== expectation.kind) {
        status = "KIND_MISMATCH";
        addIssue(issues, "KIND_MISMATCH", name);
      } else if (secretInventoryMissing) {
        status = "UNVERIFIED";
      } else if (observedKind === undefined) {
        status = "MISSING";
        addIssue(issues, "MISSING_BINDING", name);
      } else {
        status = "MATCH";
      }
      bindings.push(
        Object.freeze({
          class: expectation.class,
          expectedKind: expectation.kind,
          name,
          observedKind:
            observedKind === AMBIGUOUS_OBSERVED_KIND
              ? null
              : (observedKind ?? null),
          status,
        })
      );
    }

    for (const name of [...observed.keys()].toSorted(compareText)) {
      if (!expected.has(name)) {
        addIssue(issues, "UNEXPECTED_BINDING", name);
      }
    }

    const grade = conformanceGrade(requiresSecrets, inventory);
    const sortedIssues = issues
      .toSorted(
        (left, right) =>
          compareText(left.code, right.code) ||
          compareText(left.name ?? "", right.name ?? "")
      )
      .map((issue) => Object.freeze(issue));

    return Object.freeze({
      bindings: Object.freeze(bindings),
      confidence: hasUnknown ? "UNKNOWN" : "PROVEN",
      format: "astilba.env.provider-conformance/v1",
      grade,
      issues: Object.freeze(sortedIssues),
      liveVerified: false,
      pass: grade !== "UNVERIFIED" && !hasUnknown && sortedIssues.length === 0,
      target: bindingPlan.target,
    });
  } catch (error) {
    if (error instanceof ProviderMetadataError) {
      throw error;
    }
    throw new ProviderMetadataError("BINDING_PLAN_INVALID");
  }
}

function expectedProviderBindings(
  plan: ProviderBindingPlan,
  issues: ProviderConformanceIssue[]
): Readonly<{
  expected: Map<
    string,
    { class: ProviderConformanceBinding["class"]; kind: string }
  >;
  hasUnknown: boolean;
}> {
  const expected = new Map<
    string,
    { class: ProviderConformanceBinding["class"]; kind: string }
  >();
  const foldedNames = new Map<string, string>();
  let hasUnknown = false;

  for (const binding of plan.bindings) {
    assertPortableName(binding.rawName, "BINDING_PLAN_INVALID");
    const classification = planBindingClassification(binding);
    if (classification.class !== binding.class) {
      addIssue(issues, "DECLARED_CLASS_MISMATCH", binding.rawName);
    }
    if (classification.class === "unknown") {
      hasUnknown = true;
      addIssue(issues, "UNKNOWN_PROVIDER_KIND", binding.rawName);
    }
    if (binding.channel === "build") {
      continue;
    }
    const folded = asciiFold(binding.rawName);
    if (
      expected.has(binding.rawName) ||
      (foldedNames.has(folded) && foldedNames.get(folded) !== binding.rawName)
    ) {
      addIssue(issues, "DUPLICATE_BINDING_NAME", binding.rawName);
      continue;
    }
    foldedNames.set(folded, binding.rawName);

    expected.set(binding.rawName, {
      class: classification.class,
      kind: binding.kind,
    });
  }

  return Object.freeze({ expected, hasUnknown });
}

function planBindingClassification(
  binding: ProviderBindingPlanEntry
): Readonly<{ class: ProviderConformanceBinding["class"] }> {
  return binding.channel === "build" && binding.kind === "public_text"
    ? Object.freeze({ class: "non-confidential" as const })
    : classifyCloudflareBindingKind(binding.kind);
}

function validateProjectionMapping(
  plan: ProviderBindingPlan,
  projection: unknown
): void {
  try {
    const entries = normalizeProjectionForProvider(projection).entries;
    const byName = new Map<string, (typeof entries)[number]>();
    for (const entry of entries) {
      if (byName.has(entry.name)) {
        throw new ProviderMetadataError("BINDING_PLAN_INVALID");
      }
      byName.set(entry.name, entry);
    }
    const mapped = new Set<string>();
    const capabilityByChannel = new Set<string>();
    for (const binding of plan.bindings) {
      const entry = byName.get(binding.entry);
      if (entry !== undefined) {
        if (entry.lifecycle !== binding.channel || mapped.has(entry.name)) {
          throw new ProviderMetadataError("BINDING_PLAN_INVALID");
        }
        mapped.add(entry.name);
        continue;
      }
      if (planBindingClassification(binding).class !== "capability") {
        throw new ProviderMetadataError("BINDING_PLAN_INVALID");
      }
      if (capabilityByChannel.has(binding.channel)) {
        throw new ProviderMetadataError("BINDING_PLAN_INVALID");
      }
      const expanded = entries.filter(
        (candidate) =>
          candidate.lifecycle === binding.channel && !mapped.has(candidate.name)
      );
      if (expanded.length === 0) {
        throw new ProviderMetadataError("BINDING_PLAN_INVALID");
      }
      capabilityByChannel.add(binding.channel);
      for (const candidate of expanded) {
        mapped.add(candidate.name);
      }
    }
    if (
      mapped.size === 0 ||
      entries.some((entry) => entry.required && !mapped.has(entry.name))
    ) {
      throw new ProviderMetadataError("BINDING_PLAN_INVALID");
    }
  } catch {
    throw new ProviderMetadataError("BINDING_PLAN_INVALID");
  }
}

function validateBindingPlan(plan: unknown): ProviderBindingPlan {
  try {
    const record = exactDataRecord(
      plan,
      ["adapterAbi", "bindings", "format", "target"],
      "BINDING_PLAN_INVALID"
    );
    if (
      isRecognisedGreaterVersion(record.format, "astilba.env.binding-plan/v") ||
      isRecognisedGreaterVersion(
        record.adapterAbi,
        "astilba.env.adapter.cloudflare-workers/v"
      )
    ) {
      throw new ProviderMetadataError("BINDING_PLAN_UNSUPPORTED");
    }
    if (
      record.format !== "astilba.env.binding-plan/v1" ||
      record.adapterAbi !== "astilba.env.adapter.cloudflare-workers/v1" ||
      typeof record.target !== "string" ||
      !isLocalId(record.target)
    ) {
      throw new ProviderMetadataError("BINDING_PLAN_INVALID");
    }
    const inputBindings = exactDataArray(
      record.bindings,
      MAXIMUM_BINDING_ROWS,
      "BINDING_PLAN_INVALID"
    );
    if (inputBindings.length === 0) {
      throw new ProviderMetadataError("BINDING_PLAN_INVALID");
    }
    const bindings: ProviderBindingPlanEntry[] = [];
    const entries = new Set<string>();
    const rawNames = new Set<string>();
    for (const inputBinding of inputBindings) {
      const binding = exactDataRecord(
        inputBinding,
        ["channel", "class", "entry", "kind", "rawName"],
        "BINDING_PLAN_INVALID"
      );
      if (
        typeof binding.channel !== "string" ||
        typeof binding.class !== "string" ||
        typeof binding.entry !== "string" ||
        typeof binding.kind !== "string" ||
        typeof binding.rawName !== "string" ||
        !isLocalId(binding.entry) ||
        !isRawSourceName(binding.rawName) ||
        !METADATA_IDENTIFIER.test(binding.kind) ||
        !["build", "deployment", "request"].includes(binding.channel) ||
        !["capability", "confidential", "non-confidential", "unknown"].includes(
          binding.class
        )
      ) {
        throw new ProviderMetadataError("BINDING_PLAN_INVALID");
      }
      const foldedEntry = asciiFold(binding.entry);
      const foldedRawName = asciiFold(binding.rawName);
      if (entries.has(foldedEntry) || rawNames.has(foldedRawName)) {
        throw new ProviderMetadataError("BINDING_PLAN_INVALID");
      }
      entries.add(foldedEntry);
      rawNames.add(foldedRawName);
      bindings.push(
        Object.freeze({
          // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- The preceding literal-membership gates establish this frozen protocol union.
          channel: binding.channel as ProviderBindingPlanEntry["channel"],
          // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- The preceding literal-membership gates establish this frozen protocol union.
          class: binding.class as ProviderBindingPlanEntry["class"],
          entry: binding.entry,
          kind: binding.kind,
          rawName: binding.rawName,
        })
      );
    }
    return Object.freeze({
      adapterAbi: record.adapterAbi,
      bindings: Object.freeze(bindings),
      format: "astilba.env.binding-plan/v1",
      target: record.target,
    });
  } catch (error) {
    if (
      error instanceof ProviderMetadataError &&
      error.code === "BINDING_PLAN_UNSUPPORTED"
    ) {
      throw error;
    }
    throw new ProviderMetadataError("BINDING_PLAN_INVALID");
  }
}

function isRecognisedGreaterVersion(value: unknown, prefix: string): boolean {
  if (typeof value !== "string" || !value.startsWith(prefix)) {
    return false;
  }
  const suffix = value.slice(prefix.length);
  return /^[1-9][0-9]*$/u.test(suffix) && suffix !== "1";
}

function conformanceGrade(
  requiresSecrets: boolean,
  inventory: SecretBindingInventory | undefined
): ProviderConformanceGrade {
  if (requiresSecrets && inventory === undefined) {
    return "UNVERIFIED";
  }
  return inventory === undefined
    ? "checked-offline-configuration"
    : "synthetic-declared-inventory";
}

function mergeObservedBinding(
  observed: Map<string, string | typeof AMBIGUOUS_OBSERVED_KIND>,
  name: string,
  kind: string,
  issues: ProviderConformanceIssue[]
): void {
  const existing = observed.get(name);
  if (existing !== undefined) {
    addIssue(issues, "DUPLICATE_BINDING_NAME", name);
    observed.set(name, AMBIGUOUS_OBSERVED_KIND);
    return;
  }
  observed.set(name, kind);
}

function addWranglerBinding(
  bindings: Map<string, "json" | "kv_namespace" | "plain_text">,
  name: string,
  kind: "json" | "kv_namespace" | "plain_text",
  errorCode: ProviderMetadataErrorCode
): void {
  if (bindings.size >= MAXIMUM_BINDING_ROWS) {
    throw new ProviderMetadataError(errorCode);
  }
  const foldedName = asciiFold(name);
  for (const existingName of bindings.keys()) {
    if (asciiFold(existingName) === foldedName) {
      throw new ProviderMetadataError(errorCode);
    }
  }
  bindings.set(name, kind);
}

function assertNoDuplicateProperties(
  node: JsoncNode,
  errorCode: ProviderMetadataErrorCode
): void {
  if (node.type === "object") {
    const seen = new Set<string>();
    for (const property of node.children ?? []) {
      if (property.type !== "property") {
        throw new ProviderMetadataError(errorCode);
      }
      const [keyNode, valueNode] = property.children ?? [];
      if (
        keyNode?.type !== "string" ||
        typeof keyNode.value !== "string" ||
        valueNode === undefined ||
        seen.has(keyNode.value)
      ) {
        throw new ProviderMetadataError(errorCode);
      }
      seen.add(keyNode.value);
      assertNoDuplicateProperties(valueNode, errorCode);
    }
    return;
  }
  if (node.type === "array") {
    for (const child of node.children ?? []) {
      assertNoDuplicateProperties(child, errorCode);
    }
  }
}

function assertJsoncLimits(
  node: JsoncNode,
  errorCode: ProviderMetadataErrorCode
): void {
  let containerItems = 0;
  const inspect = (current: JsoncNode, parentDepth: number): void => {
    if (current.type === "object" || current.type === "array") {
      const depth = parentDepth + 1;
      if (depth > MAXIMUM_JSONC_DEPTH) {
        throw new ProviderMetadataError(errorCode);
      }
      const children = current.children ?? [];
      const maximum =
        current.type === "object"
          ? MAXIMUM_JSONC_OBJECT_KEYS
          : MAXIMUM_JSONC_ARRAY_ITEMS;
      if (children.length > maximum) {
        throw new ProviderMetadataError(errorCode);
      }
      containerItems += children.length;
      if (containerItems > MAXIMUM_JSONC_CONTAINER_ITEMS) {
        throw new ProviderMetadataError(errorCode);
      }
      for (const child of children) {
        if (current.type === "object") {
          const value = child.children?.[1];
          if (child.type !== "property" || value === undefined) {
            throw new ProviderMetadataError(errorCode);
          }
          inspect(value, depth);
        } else {
          inspect(child, depth);
        }
      }
    }
  };
  inspect(node, 0);
}

function objectEntries(node: JsoncNode): readonly [string, JsoncNode][] {
  if (node.type !== "object") {
    return [];
  }
  return (node.children ?? []).map((property) => {
    const [keyNode, valueNode] = property.children ?? [];
    if (
      property.type !== "property" ||
      keyNode?.type !== "string" ||
      typeof keyNode.value !== "string" ||
      valueNode === undefined
    ) {
      throw new ProviderMetadataError("WRANGLER_JSONC_INVALID");
    }
    return [keyNode.value, valueNode] as const;
  });
}

function objectProperty(
  objectNode: JsoncNode,
  propertyName: string
): JsoncNode | undefined {
  return objectEntries(objectNode).find(([name]) => name === propertyName)?.[1];
}

function stringNodeValue(
  node: JsoncNode | undefined,
  errorCode: ProviderMetadataErrorCode
): string {
  if (node?.type !== "string" || typeof node.value !== "string") {
    throw new ProviderMetadataError(errorCode);
  }
  return node.value;
}

function assertPortableName(
  name: string,
  errorCode: ProviderMetadataErrorCode
): void {
  if (!PORTABLE_RAW_NAME.test(name)) {
    throw new ProviderMetadataError(errorCode);
  }
}

function captureDataRecord(
  value: unknown,
  errorCode: ProviderMetadataErrorCode
): Readonly<{
  keys: readonly string[];
  values: ReadonlyMap<string, unknown>;
}> {
  if (value === null || typeof value !== "object") {
    throw new ProviderMetadataError(errorCode);
  }

  const prototype = Reflect.getPrototypeOf(value);
  if (prototype !== Object.prototype && prototype !== null) {
    throw new ProviderMetadataError(errorCode);
  }

  const ownKeys = Reflect.ownKeys(value);
  if (ownKeys.some((key) => typeof key !== "string")) {
    throw new ProviderMetadataError(errorCode);
  }

  const keys: string[] = [];
  for (const key of ownKeys) {
    if (typeof key !== "string") {
      throw new ProviderMetadataError(errorCode);
    }
    keys.push(key);
  }
  keys.sort(compareText);
  const values = new Map<string, unknown>();
  for (const key of keys) {
    const descriptor = Object.getOwnPropertyDescriptor(value, key);
    if (
      descriptor === undefined ||
      !("value" in descriptor) ||
      descriptor.enumerable !== true
    ) {
      throw new ProviderMetadataError(errorCode);
    }
    values.set(key, descriptor.value);
  }

  return Object.freeze({ keys: Object.freeze(keys), values });
}

function exactDataRecord(
  value: unknown,
  expected: readonly string[],
  errorCode: ProviderMetadataErrorCode
): Record<string, unknown> {
  try {
    const captured = captureDataRecord(value, errorCode);
    if (
      captured.keys.length !== expected.length ||
      captured.keys.some((key) => !expected.includes(key))
    ) {
      throw new ProviderMetadataError(errorCode);
    }

    // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- The record is populated only from the captured own-data-property map.
    const result = Object.create(null) as Record<string, unknown>;
    for (const key of expected) {
      result[key] = captured.values.get(key);
    }
    return Object.freeze(result);
  } catch {
    throw new ProviderMetadataError(errorCode);
  }
}

function exactDataRecordWithOptional(
  value: unknown,
  required: readonly string[],
  optional: string,
  errorCode: ProviderMetadataErrorCode
): Readonly<{ hasOptional: boolean; record: Record<string, unknown> }> {
  try {
    const captured = captureDataRecord(value, errorCode);
    const hasOptional = captured.keys.includes(optional);
    const expected = hasOptional ? [...required, optional] : required;
    if (
      captured.keys.length !== expected.length ||
      captured.keys.some((key) => !expected.includes(key))
    ) {
      throw new ProviderMetadataError(errorCode);
    }

    // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- The record is populated only from the captured own-data-property map.
    const record = Object.create(null) as Record<string, unknown>;
    for (const key of expected) {
      record[key] = captured.values.get(key);
    }
    return Object.freeze({ hasOptional, record: Object.freeze(record) });
  } catch {
    throw new ProviderMetadataError(errorCode);
  }
}

function exactDataArray(
  value: unknown,
  maximum: number,
  errorCode: ProviderMetadataErrorCode
): readonly unknown[] {
  try {
    if (
      !Array.isArray(value) ||
      Object.getPrototypeOf(value) !== Array.prototype
    ) {
      throw new ProviderMetadataError(errorCode);
    }

    const ownKeys = Reflect.ownKeys(value);
    if (ownKeys.some((key) => typeof key !== "string")) {
      throw new ProviderMetadataError(errorCode);
    }

    const keys: string[] = [];
    for (const key of ownKeys) {
      if (typeof key !== "string") {
        throw new ProviderMetadataError(errorCode);
      }
      keys.push(key);
    }
    keys.sort(compareText);
    const descriptors = new Map<string, PropertyDescriptor>();
    for (const key of keys) {
      const descriptor = Object.getOwnPropertyDescriptor(value, key);
      if (descriptor === undefined) {
        throw new ProviderMetadataError(errorCode);
      }
      descriptors.set(key, descriptor);
    }

    const lengthDescriptor = descriptors.get("length");
    if (
      lengthDescriptor === undefined ||
      !("value" in lengthDescriptor) ||
      lengthDescriptor.enumerable !== false ||
      typeof lengthDescriptor.value !== "number" ||
      !Number.isSafeInteger(lengthDescriptor.value) ||
      lengthDescriptor.value < 0 ||
      lengthDescriptor.value > maximum
    ) {
      throw new ProviderMetadataError(errorCode);
    }

    const length = lengthDescriptor.value;
    if (
      keys.length !== length + 1 ||
      keys.some((key) => key !== "length" && !/^(?:0|[1-9][0-9]*)$/u.test(key))
    ) {
      throw new ProviderMetadataError(errorCode);
    }

    const result: unknown[] = [];
    for (let index = 0; index < length; index += 1) {
      const descriptor = descriptors.get(String(index));
      if (
        descriptor === undefined ||
        !("value" in descriptor) ||
        descriptor.enumerable !== true
      ) {
        throw new ProviderMetadataError(errorCode);
      }
      result.push(descriptor.value);
    }

    return Object.freeze(result);
  } catch {
    throw new ProviderMetadataError(errorCode);
  }
}

function normalizeConformanceInput(
  input: CheckWranglerConformanceInput
): Readonly<{
  bindingPlan: unknown;
  projection: ConsumerProjectionManifest;
  secretInventory: unknown;
  wranglerJsonc: string;
}> {
  const normalized = exactDataRecordWithOptional(
    input,
    ["bindingPlan", "projection", "wranglerJsonc"],
    "secretInventory",
    "BINDING_PLAN_INVALID"
  );
  if (typeof normalized.record.wranglerJsonc !== "string") {
    throw new ProviderMetadataError("WRANGLER_JSONC_INVALID");
  }
  return Object.freeze({
    bindingPlan: normalized.record.bindingPlan,
    // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- This provider-envelope field is passed only to the projection validator immediately after parsing.
    projection: normalized.record.projection as ConsumerProjectionManifest,
    secretInventory: normalized.hasOptional
      ? normalized.record.secretInventory
      : undefined,
    wranglerJsonc: normalized.record.wranglerJsonc,
  });
}

function addIssue(
  issues: ProviderConformanceIssue[],
  code: ProviderConformanceIssueCode,
  name: string
): void {
  if (
    issues.some(
      (issue) => issue.code === code && (issue.name ?? "") === (name ?? "")
    )
  ) {
    return;
  }
  issues.push({ code, name });
}

function asciiFold(value: string): string {
  return value.replaceAll(/[A-Z]/gu, (character) => character.toLowerCase());
}

function compareText(left: string, right: string): number {
  return left < right ? -1 : left > right ? 1 : 0;
}

function providerMetadataMessage(code: ProviderMetadataErrorCode): string {
  switch (code) {
    case "BINDING_PLAN_INVALID": {
      return "Binding plan metadata is invalid";
    }
    case "BINDING_PLAN_UNSUPPORTED": {
      return "Binding plan metadata version is unsupported";
    }
    case "SECRET_INVENTORY_INVALID": {
      return "Secret inventory metadata is invalid";
    }
    case "SECRET_INVENTORY_UNSUPPORTED": {
      return "Secret inventory metadata version is unsupported";
    }
    case "WRANGLER_JSONC_INVALID": {
      return "Wrangler JSONC metadata is invalid";
    }
  }
}
