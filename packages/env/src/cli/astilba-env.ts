#!/usr/bin/env node

import {
  execFile as execFileCallback,
  spawn,
  spawnSync,
} from "node:child_process";
import { constants as fileConstants } from "node:fs";
import { lstat, open, readFile, realpath } from "node:fs/promises";
import { extname, relative, resolve, sep } from "node:path";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";

import { materializeStringRecord } from "../adapters/record.ts";
import { parseBoundedJsonValue } from "../core/bounded-json.ts";
import {
  asciiCaseFold,
  canonicalJson,
  compileContract,
  findProjection,
  isLocalId,
  isRawSourceName,
} from "../core/index.ts";
import type {
  CompiledContract,
  JsonObject,
  JsonValue,
  ResolutionBinding,
} from "../core/index.ts";
import {
  checkContractInventory,
  compileContractInventory,
  InventoryFailure,
} from "../inventory/index.ts";
import type {
  InventoryFailureCode,
  InventoryOwnership,
} from "../inventory/index.ts";
import {
  createPlanningSnapshot,
  decodePlanningSnapshotBytes,
  planImpact,
  PlanningSnapshotDecodeError,
} from "../planning/index.ts";
import type {
  ImpactPlan,
  PlanningSnapshot,
  PlanningSnapshotTarget,
} from "../planning/index.ts";
import {
  compileProductFromCompilation,
  GeneratedDirectoryFailure,
  GeneratedOutputStaleError,
  prepareGeneratedOutput,
  writeGeneratedProduct,
} from "../product/index.ts";
import type { ProductCompilation } from "../product/index.ts";
import type { ProviderBindingPlan } from "../provider/types.ts";
import { checkProcessTarget } from "../runtime/index.ts";
import type { ProcessTargetDefinition } from "../runtime/index.ts";

// oxlint-disable-next-line typescript/strict-void-return -- Node's promisify overload returns a value by design.
const execFile = promisify(execFileCallback);
const MAXIMUM_SERIAL_BYTES = 8_388_608;
const MAXIMUM_OBSERVED_INVENTORY_BYTES = 1_048_576;
const SERIAL_LIMITS = Object.freeze({
  maximumArrayItems: 65_536,
  maximumBytes: MAXIMUM_SERIAL_BYTES,
  maximumContainerItems: 262_144,
  maximumDepth: 64,
  maximumObjectKeys: 262_144,
  maximumStringBytes: 1_048_576,
});
const OBSERVED_INVENTORY_LIMITS = Object.freeze({
  maximumArrayItems: 2048,
  maximumBytes: MAXIMUM_OBSERVED_INVENTORY_BYTES,
  maximumContainerItems: 8192,
  maximumDepth: 4,
  maximumObjectKeys: 8192,
  maximumStringBytes: 1024,
});

type Command = "check" | "generate" | "inventory" | "plan";
type InventoryOperation = "check" | "export";
type CliErrorCode =
  | "ENV_COMMAND_FAILED"
  | "ENV_GENERATED_FORMAT_UNSUPPORTED"
  | "ENV_GENERATED_INVALID"
  | "ENV_GENERATED_STALE"
  | InventoryFailureCode
  | "ENV_PLANNING_FORMAT_UNSUPPORTED"
  | "ENV_PLANNING_INVALID"
  | "ENV_USAGE";

type ParsedArguments = Readonly<{
  base?: string;
  checkGenerated: boolean;
  command: Command;
  config?: string;
  inventoryOperation?: InventoryOperation;
  json: boolean;
  observed?: string;
  ownership: InventoryOwnership;
  target?: string;
}>;

export type CliIo = Readonly<{
  cwd: string;
  environment: Readonly<Record<string, unknown>>;
  stderr: Pick<NodeJS.WriteStream, "write">;
  stdout: Pick<NodeJS.WriteStream, "write">;
}>;

type ValidatedConfigurationPath = Readonly<{
  path: string;
  projectRoot: string;
}>;

type CliTestHooks = Readonly<{
  compileConfiguration?: (
    configurationPath: string
  ) => Promise<ProductCompilation>;
}>;

class CliFailure extends Error {
  readonly code: CliErrorCode;
  readonly paths?: readonly string[];
  readonly status: 1 | 2;

  constructor(code: CliErrorCode, status: 1 | 2, paths?: readonly string[]) {
    super(code);
    this.name = "CliFailure";
    this.code = code;
    this.status = status;
    if (paths !== undefined) {
      this.paths = Object.freeze([...paths]);
    }
  }
}

const fixedMessage = (code: CliErrorCode): string => {
  switch (code) {
    case "ENV_COMMAND_FAILED": {
      return "Astilba Env command failed safely.";
    }
    case "ENV_GENERATED_FORMAT_UNSUPPORTED": {
      return "Astilba Env generated output format is unsupported.";
    }
    case "ENV_GENERATED_INVALID": {
      return "Astilba Env generated output is invalid.";
    }
    case "ENV_GENERATED_STALE": {
      return "Astilba Env generated output is stale.";
    }
    case "ENV_INVENTORY_INVALID": {
      return "Astilba Env contract inventory is invalid.";
    }
    case "ENV_INVENTORY_TARGET_UNKNOWN": {
      return "Astilba Env inventory target is unknown.";
    }
    case "ENV_INVENTORY_TARGET_UNSUPPORTED": {
      return "Astilba Env inventory target is unsupported.";
    }
    case "ENV_OBSERVED_INVALID": {
      return "Astilba Env observed inventory is invalid.";
    }
    case "ENV_OBSERVED_UNSUPPORTED": {
      return "Astilba Env observed inventory format is unsupported.";
    }
    case "ENV_PLANNING_FORMAT_UNSUPPORTED": {
      return "Astilba Env planning snapshot format is unsupported.";
    }
    case "ENV_PLANNING_INVALID": {
      return "Astilba Env planning snapshot is invalid.";
    }
    case "ENV_USAGE": {
      return "Invalid Astilba Env command arguments.";
    }
  }
};

const usageFailure = (): never => {
  throw new CliFailure("ENV_USAGE", 2);
};

const operationFailure = (): never => {
  throw new CliFailure("ENV_COMMAND_FAILED", 1);
};

const commandFromToken = (value: string | undefined): Command | null =>
  value === "check" ||
  value === "generate" ||
  value === "inventory" ||
  value === "plan"
    ? value
    : null;

const requireValue = (arguments_: readonly string[], index: number): string => {
  const value = arguments_[index + 1];
  if (value === undefined || value.startsWith("--")) {
    return usageFailure();
  }
  return value;
};

const validateConfigurationExtension = (path: string): void => {
  const extension = extname(path);
  if (extension !== ".ts" && extension !== ".mts") {
    usageFailure();
  }
};

const validateGitReference = (reference: string): void => {
  if (!/^[A-Za-z0-9][A-Za-z0-9._/@{}^~:+-]*$/u.test(reference)) {
    usageFailure();
  }
};

const parseArguments = (arguments_: readonly string[]): ParsedArguments => {
  const command = commandFromToken(arguments_[0]);
  if (command === null) {
    return usageFailure();
  }
  const operationToken = command === "inventory" ? arguments_[1] : undefined;
  const inventoryOperation: InventoryOperation | undefined =
    operationToken === "check" || operationToken === "export"
      ? operationToken
      : undefined;
  if (command === "inventory" && inventoryOperation === undefined) {
    return usageFailure();
  }

  const seen = new Set<string>();
  let base: string | undefined;
  let checkGenerated = false;
  let config: string | undefined;
  let json = false;
  let observed: string | undefined;
  let ownership: InventoryOwnership = "open";
  let target: string | undefined;
  const firstOptionIndex = command === "inventory" ? 2 : 1;
  for (let index = firstOptionIndex; index < arguments_.length; index += 1) {
    const argument = arguments_[index];
    if (
      argument === undefined ||
      !argument.startsWith("--") ||
      argument.includes("=") ||
      seen.has(argument)
    ) {
      return usageFailure();
    }
    seen.add(argument);
    switch (argument) {
      case "--base": {
        if (command !== "plan") {
          return usageFailure();
        }
        base = requireValue(arguments_, index);
        index += 1;
        break;
      }
      case "--check": {
        if (command !== "generate") {
          return usageFailure();
        }
        checkGenerated = true;
        break;
      }
      case "--config": {
        config = requireValue(arguments_, index);
        validateConfigurationExtension(config);
        index += 1;
        break;
      }
      case "--json": {
        json = true;
        break;
      }
      case "--observed": {
        if (command !== "inventory" || inventoryOperation !== "check") {
          return usageFailure();
        }
        observed = requireValue(arguments_, index);
        index += 1;
        break;
      }
      case "--ownership": {
        if (command !== "inventory" || inventoryOperation !== "check") {
          return usageFailure();
        }
        const value = requireValue(arguments_, index);
        if (value !== "closed" && value !== "open") {
          return usageFailure();
        }
        ownership = value;
        index += 1;
        break;
      }
      case "--target": {
        if (command !== "check" && command !== "inventory") {
          return usageFailure();
        }
        target = requireValue(arguments_, index);
        index += 1;
        break;
      }
      default: {
        return usageFailure();
      }
    }
  }

  const configurationPath = config ?? "astilba.env.ts";
  validateConfigurationExtension(configurationPath);
  if (command === "check" || command === "inventory") {
    if (target === undefined || !isLocalId(target)) {
      return usageFailure();
    }
  }
  if (command === "inventory" && inventoryOperation === "check") {
    if (observed === undefined || observed.length === 0) {
      return usageFailure();
    }
  }
  if (command === "plan") {
    if (base === undefined) {
      return usageFailure();
    }
    validateGitReference(base);
  }

  return Object.freeze({
    ...(base === undefined ? {} : { base }),
    checkGenerated,
    command,
    ...(config === undefined ? {} : { config }),
    ...(inventoryOperation === undefined ? {} : { inventoryOperation }),
    json,
    ...(observed === undefined ? {} : { observed }),
    ownership,
    ...(target === undefined ? {} : { target }),
  });
};

const isMissing = (error: unknown): error is NodeJS.ErrnoException =>
  error instanceof Error && "code" in error && error.code === "ENOENT";

const requireTypeModuleScope = async (projectRoot: string): Promise<void> => {
  let current = projectRoot;
  while (true) {
    const packagePath = resolve(current, "package.json");
    try {
      const source = await readFile(packagePath, "utf-8");
      const parsed = JSON.parse(source) as unknown;
      if (
        typeof parsed !== "object" ||
        parsed === null ||
        Reflect.get(parsed, "type") !== "module"
      ) {
        operationFailure();
      }
      return;
    } catch (error) {
      if (!isMissing(error)) {
        operationFailure();
      }
    }
    const parent = resolve(current, "..");
    if (parent === current) {
      operationFailure();
    }
    current = parent;
  }
};

const validateConfigurationPath = async (
  cwd: string,
  configuredPath: string | undefined
): Promise<ValidatedConfigurationPath> => {
  const requested = resolve(cwd, configuredPath ?? "astilba.env.ts");
  validateConfigurationExtension(requested);
  let metadata;
  try {
    metadata = await lstat(requested);
  } catch {
    return operationFailure();
  }
  if (metadata.isSymbolicLink() || !metadata.isFile()) {
    return operationFailure();
  }

  let projectRoot: string;
  try {
    projectRoot = await realpath(resolve(requested, ".."));
  } catch {
    return operationFailure();
  }
  const path = resolve(
    projectRoot,
    requested.slice(requested.lastIndexOf(sep) + 1)
  );
  if (extname(path) === ".ts") {
    await requireTypeModuleScope(projectRoot);
  }
  return Object.freeze({ path, projectRoot });
};

const isJsonObject = (value: JsonValue): value is JsonObject => {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    return false;
  }
  const prototype: unknown = Object.getPrototypeOf(value);
  return prototype === Object.prototype || prototype === null;
};

const hasExactRecordFields = <TKey extends string>(
  value: JsonValue,
  expected: readonly TKey[]
): value is JsonObject & Readonly<Record<TKey, JsonValue>> => {
  if (!isJsonObject(value)) {
    return false;
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
    return false;
  }
  for (const key of expected) {
    const descriptor = Object.getOwnPropertyDescriptor(value, key);
    if (
      descriptor === undefined ||
      !("value" in descriptor) ||
      descriptor.enumerable !== true
    ) {
      return false;
    }
  }
  return true;
};

const requireRecord = <const TKey extends string>(
  value: JsonValue,
  expected: readonly TKey[]
): JsonObject & Readonly<Record<TKey, JsonValue>> =>
  hasExactRecordFields(value, expected) ? value : operationFailure();

const decodeUtf8 = (bytes: Uint8Array, failure: () => never): string => {
  if (
    bytes.byteLength > MAXIMUM_SERIAL_BYTES ||
    (bytes[0] === 0xef && bytes[1] === 0xbb && bytes[2] === 0xbf)
  ) {
    return failure();
  }
  try {
    return new TextDecoder("utf-8", {
      fatal: true,
      ignoreBOM: true,
    }).decode(bytes);
  } catch {
    return failure();
  }
};

const compilationHelper = (): string => {
  const extension = import.meta.filename.endsWith(".ts") ? ".ts" : ".js";
  return fileURLToPath(new URL(`./compile${extension}`, import.meta.url));
};

const collectBounded = async (
  stream: NodeJS.ReadableStream
): Promise<Uint8Array> => {
  const chunks: Uint8Array[] = [];
  let total = 0;
  for await (const chunk of stream) {
    const bytes =
      typeof chunk === "string"
        ? new TextEncoder().encode(chunk)
        : new Uint8Array(chunk);
    total += bytes.byteLength;
    if (total > MAXIMUM_SERIAL_BYTES) {
      operationFailure();
    }
    chunks.push(bytes);
  }
  const output = new Uint8Array(total);
  let offset = 0;
  for (const chunk of chunks) {
    output.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return output;
};

const compileConfiguration = async (
  configurationPath: string
): Promise<ProductCompilation> => {
  const child = spawn(
    process.execPath,
    [...process.execArgv, compilationHelper(), configurationPath],
    {
      env: process.env,
      stdio: ["ignore", "pipe", "pipe", "pipe"],
    }
  );
  if (
    child.stdout === null ||
    child.stderr === null ||
    child.stdio[3] === undefined ||
    child.stdio[3] === null
  ) {
    child.kill();
    return operationFailure();
  }
  child.stdout.resume();
  child.stderr.resume();
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- The null checks above establish this fourth stdio channel is the requested pipe.
  const output = child.stdio[3] as NodeJS.ReadableStream;
  const bytesPromise = collectBounded(output).catch((error: unknown) => {
    child.kill();
    throw error;
  });
  const statusPromise = new Promise<number>((resolveStatus, reject) => {
    child.once("error", reject);
    child.once("close", (status, signal) => {
      if (signal !== null || status === null) {
        reject(new Error("Compilation child terminated."));
        return;
      }
      resolveStatus(status);
    });
  });

  let bytes: Uint8Array;
  let status: number;
  try {
    [bytes, status] = await Promise.all([bytesPromise, statusPromise]);
  } catch {
    return operationFailure();
  }
  if (status !== 0) {
    return operationFailure();
  }
  return await decodeCliCompilationV1ForTest(bytes);
};

const fullContractDefinition = (manifest: JsonValue): unknown => {
  const record = requireRecord(
    manifest,
    (() => {
      if (isJsonObject(manifest) && manifest.formatVersion === 2) {
        return [
          "canonicalisation",
          "codecAbi",
          "consumers",
          "contract",
          "entries",
          "format",
          "formatVersion",
          "projectionAbi",
          "rules",
        ];
      }
      return [
        "canonicalisation",
        "codecAbi",
        "consumers",
        "contract",
        "entries",
        "format",
        "formatVersion",
        "projectionAbi",
      ];
    })()
  );
  if (!Array.isArray(record.entries) || !Array.isArray(record.consumers)) {
    return operationFailure();
  }
  const entries = record.entries.map((entryValue) => {
    // oxlint-disable-next-line typescript/no-unsafe-argument -- record.entries is accepted only after exact external-snapshot structure validation.
    const entry = requireRecord(entryValue, [
      "codec",
      "identity",
      "lifecycle",
      "name",
      "required",
      "visibility",
    ]);
    if (
      !Array.isArray(entry.identity) ||
      entry.identity.length !== 2 ||
      typeof entry.identity[0] !== "string" ||
      typeof entry.identity[1] !== "string"
    ) {
      return operationFailure();
    }
    const id = entry.identity[1];
    const output = entry.name;
    return {
      codec: entry.codec,
      fragment: entry.identity[0],
      id,
      lifecycle: entry.lifecycle,
      ...(output === id ? {} : { output }),
      required: entry.required,
      visibility: entry.visibility,
    };
  });
  const consumers = record.consumers.map((consumerValue) => {
    // oxlint-disable-next-line typescript/no-unsafe-argument -- record.consumers is accepted only after exact external-snapshot structure validation.
    const consumer = requireRecord(consumerValue, ["entries", "id", "kind"]);
    return {
      entries: consumer.entries,
      id: consumer.id,
      kind: consumer.kind,
    };
  });
  return {
    consumers,
    entries,
    id: record.contract,
    ...(Array.isArray(record.rules) ? { rules: record.rules } : {}),
  };
};

const compareText = (left: string, right: string): number =>
  left < right ? -1 : left === right ? 0 : 1;

const decodeProcessBindingPlan = (
  value: JsonValue,
  consumer: string,
  compiled: CompiledContract
): ProviderBindingPlan => {
  const record = requireRecord(value, [
    "adapterAbi",
    "bindings",
    "format",
    "target",
  ]);
  if (
    record.adapterAbi !== "astilba.env.adapter.process-record/v1" ||
    record.format !== "astilba.env.binding-plan/v1" ||
    typeof record.target !== "string" ||
    !isLocalId(record.target) ||
    !Array.isArray(record.bindings) ||
    record.bindings.length === 0 ||
    record.bindings.length > 2048
  ) {
    return operationFailure();
  }
  const projection = findProjection(compiled, consumer);
  if (projection === undefined) {
    return operationFailure();
  }
  const entries = new Map(
    projection.manifest.entries.map((entry) => [entry.name, entry])
  );
  const selected = new Set<string>();
  const rawNames = new Set<string>();
  const bindings: ProviderBindingPlan["bindings"][number][] = [];
  let selectedLifecycle: "build" | "deployment" | "request" | undefined;
  let previousKey: string | undefined;
  for (const input of record.bindings) {
    // oxlint-disable-next-line typescript/no-unsafe-argument -- Each binding comes from the decoded snapshot array and is checked by requireRecord.
    const binding = requireRecord(input, [
      "channel",
      "class",
      "entry",
      "kind",
      "rawName",
    ]);
    if (
      (binding.channel !== "build" &&
        binding.channel !== "deployment" &&
        binding.channel !== "request") ||
      typeof binding.entry !== "string" ||
      !isLocalId(binding.entry) ||
      typeof binding.rawName !== "string" ||
      !isRawSourceName(binding.rawName)
    ) {
      return operationFailure();
    }
    selectedLifecycle ??= binding.channel;
    if (binding.channel !== selectedLifecycle) {
      return operationFailure();
    }
    const entry = entries.get(binding.entry);
    if (
      entry === undefined ||
      entry.lifecycle !== binding.channel ||
      selected.has(binding.entry)
    ) {
      return operationFailure();
    }
    const privateEntry =
      "visibility" in entry && entry.visibility === "private";
    const expectedKind = privateEntry ? "private_text" : "public_text";
    const expectedClass = privateEntry ? "confidential" : "non-confidential";
    if (binding.kind !== expectedKind || binding.class !== expectedClass) {
      return operationFailure();
    }
    const foldedRawName = asciiCaseFold(binding.rawName);
    if (rawNames.has(foldedRawName)) {
      return operationFailure();
    }
    const key = `${binding.entry}\u0000${binding.rawName}`;
    if (previousKey !== undefined && compareText(previousKey, key) >= 0) {
      return operationFailure();
    }
    previousKey = key;
    selected.add(binding.entry);
    rawNames.add(foldedRawName);
    bindings.push(
      Object.freeze({
        channel: binding.channel,
        class: expectedClass,
        entry: binding.entry,
        kind: expectedKind,
        rawName: binding.rawName,
      })
    );
  }
  if (
    selectedLifecycle === undefined ||
    projection.manifest.entries.some(
      (entry) =>
        entry.lifecycle === selectedLifecycle && !selected.has(entry.name)
    )
  ) {
    return operationFailure();
  }
  return Object.freeze({
    adapterAbi: "astilba.env.adapter.process-record/v1",
    bindings: Object.freeze(bindings),
    format: "astilba.env.binding-plan/v1",
    target: record.target,
  });
};

/**
 * Test-only access to the private CliCompilationV1 decoder.
 *
 * @internal
 */
export const decodeCliCompilationV1ForTest = async (
  bytes: Uint8Array
): Promise<ProductCompilation> => {
  const source = decodeUtf8(bytes, operationFailure);
  let parsed: JsonValue;
  try {
    parsed = parseBoundedJsonValue(source, SERIAL_LIMITS);
  } catch {
    return operationFailure();
  }
  if (canonicalJson(parsed) !== source) {
    return operationFailure();
  }
  const record = requireRecord(parsed, [
    "contract",
    "format",
    "projections",
    "targets",
  ]);
  if (
    record.contract === undefined ||
    record.format !== "astilba.env.cli-compilation/v1" ||
    !Array.isArray(record.projections) ||
    !Array.isArray(record.targets)
  ) {
    return operationFailure();
  }

  let compiled;
  try {
    compiled = await compileContract(fullContractDefinition(record.contract));
  } catch {
    return operationFailure();
  }
  if (
    canonicalJson(compiled.full.manifest) !== canonicalJson(record.contract)
  ) {
    return operationFailure();
  }
  const expectedProjections = compiled.projections.map(
    (projection) => projection.manifest
  );
  if (
    canonicalJson(expectedProjections) !== canonicalJson(record.projections)
  ) {
    return operationFailure();
  }

  const targets: PlanningSnapshotTarget[] = [];
  let previousTarget: string | undefined;
  const foldedTargets = new Set<string>();
  for (const value of record.targets) {
    // oxlint-disable-next-line typescript/no-unsafe-argument -- Each target comes from the decoded snapshot array and is checked by requireRecord.
    const target = requireRecord(value, ["consumer", "plan"]);
    if (
      typeof target.consumer !== "string" ||
      !isLocalId(target.consumer) ||
      target.plan === undefined
    ) {
      return operationFailure();
    }
    const plan = decodeProcessBindingPlan(
      target.plan,
      target.consumer,
      compiled
    );
    if (previousTarget !== undefined && plan.target <= previousTarget) {
      return operationFailure();
    }
    const folded = asciiCaseFold(plan.target);
    if (foldedTargets.has(folded)) {
      return operationFailure();
    }
    foldedTargets.add(folded);
    previousTarget = plan.target;
    targets.push({
      bindingPlan: plan,
      consumer: target.consumer,
    });
  }
  try {
    createPlanningSnapshot({ compiled, targets });
  } catch {
    return operationFailure();
  }
  return Object.freeze({
    compiled,
    targets: Object.freeze(targets),
  });
};

const writeJson = (
  io: CliIo,
  value: unknown,
  stream: "stderr" | "stdout" = "stdout"
): void => {
  io[stream].write(`${canonicalJson(value)}\n`);
};

const readHistoricalSnapshot = async (
  projectRoot: string,
  reference: string
): Promise<PlanningSnapshot> => {
  let gitRootSource: string;
  try {
    const result = await execFile(
      "git",
      ["-C", projectRoot, "rev-parse", "--show-toplevel"],
      { encoding: "utf-8" }
    );
    gitRootSource = result.stdout;
  } catch {
    return operationFailure();
  }
  let gitRoot: string;
  try {
    gitRoot = await realpath(gitRootSource.trim());
  } catch {
    return operationFailure();
  }
  const canonicalProjectRoot =
    await realpath(projectRoot).catch(operationFailure);
  const snapshotPath = resolve(
    canonicalProjectRoot,
    ".astilba/env/snapshot.json"
  );
  const gitPath = relative(gitRoot, snapshotPath).split(sep).join("/");
  if (gitPath === "" || gitPath === ".." || gitPath.startsWith("../")) {
    return operationFailure();
  }

  let objectId: string;
  try {
    const result = await execFile(
      "git",
      [
        "-C",
        gitRoot,
        "rev-parse",
        "--verify",
        "--end-of-options",
        `${reference}^{commit}`,
      ],
      { encoding: "utf-8" }
    );
    const lines = result.stdout.trim().split(/\r?\n/u);
    if (lines.length !== 1 || !/^[0-9a-f]{40,64}$/u.test(lines[0] ?? "")) {
      return usageFailure();
    }
    const [resolvedObjectId] = lines;
    if (resolvedObjectId === undefined) {
      return usageFailure();
    }
    objectId = resolvedObjectId;
  } catch (error) {
    if (
      typeof error === "object" &&
      error !== null &&
      "code" in error &&
      typeof error.code === "number"
    ) {
      return usageFailure();
    }
    return operationFailure();
  }

  let source: Uint8Array;
  try {
    const result = await execFile(
      "git",
      ["-C", gitRoot, "show", `${objectId}:${gitPath}`],
      {
        encoding: "buffer",
        maxBuffer: MAXIMUM_SERIAL_BYTES + 1,
      }
    );
    source = new Uint8Array(result.stdout);
  } catch (error) {
    if (
      error instanceof Error &&
      "code" in error &&
      error.code === "ERR_CHILD_PROCESS_STDIO_MAXBUFFER"
    ) {
      throw new CliFailure("ENV_PLANNING_INVALID", 1);
    }
    return operationFailure();
  }
  try {
    return await decodePlanningSnapshotBytes(source);
  } catch (error) {
    if (
      error instanceof PlanningSnapshotDecodeError &&
      error.code === "ENV_PLANNING_FORMAT_UNSUPPORTED"
    ) {
      throw new CliFailure("ENV_PLANNING_FORMAT_UNSUPPORTED", 1);
    }
    throw new CliFailure("ENV_PLANNING_INVALID", 1);
  }
};

const readObservedInventory = async (
  cwd: string,
  requestedPath: string
): Promise<JsonValue> => {
  let handle: Awaited<ReturnType<typeof open>> | undefined;
  try {
    const path = resolve(cwd, requestedPath);
    const noFollowFlag =
      process.platform !== "win32" &&
      typeof fileConstants.O_NOFOLLOW === "number"
        ? fileConstants.O_NOFOLLOW
        : 0;
    handle = await open(path, fileConstants.O_RDONLY + noFollowFlag);
    const [metadata, pathMetadata] = await Promise.all([
      handle.stat(),
      lstat(path),
    ]);
    if (
      !metadata.isFile() ||
      pathMetadata.isSymbolicLink() ||
      !hasSameObservedFileIdentity(pathMetadata, metadata) ||
      metadata.size > MAXIMUM_OBSERVED_INVENTORY_BYTES
    ) {
      throw new TypeError("Observed inventory file is invalid.");
    }
    const buffer = new Uint8Array(MAXIMUM_OBSERVED_INVENTORY_BYTES + 1);
    let byteLength = 0;
    while (byteLength < buffer.byteLength) {
      const { bytesRead } = await handle.read(
        buffer,
        byteLength,
        buffer.byteLength - byteLength,
        byteLength
      );
      if (bytesRead === 0) {
        break;
      }
      byteLength += bytesRead;
    }
    if (byteLength > MAXIMUM_OBSERVED_INVENTORY_BYTES) {
      throw new TypeError("Observed inventory file is invalid.");
    }
    const source = new TextDecoder("utf-8", {
      fatal: true,
      ignoreBOM: false,
    }).decode(buffer.subarray(0, byteLength));
    return parseBoundedJsonValue(source, OBSERVED_INVENTORY_LIMITS);
  } catch {
    throw new CliFailure("ENV_OBSERVED_INVALID", 1);
  } finally {
    await handle?.close().catch(() => undefined);
  }
};

type ObservedFileIdentity = Readonly<{ dev: number; ino: number }>;

const hasSameObservedFileIdentity = (
  pathIdentity: ObservedFileIdentity,
  handleIdentity: ObservedFileIdentity,
  platform: NodeJS.Platform = process.platform
): boolean => {
  if (pathIdentity.ino !== handleIdentity.ino) {
    return false;
  }
  // Node can report different device identifiers for lstat and FileHandle.stat on Windows.
  return platform === "win32" || pathIdentity.dev === handleIdentity.dev;
};

export const hasSameObservedFileIdentityForTest: typeof hasSameObservedFileIdentity =
  hasSameObservedFileIdentity;

const renderInventoryCheckReport = (
  report: ReturnType<typeof checkContractInventory>
): string => {
  let summary = "Astilba Env inventory drift was found";
  if (report.pass) {
    summary =
      report.issues.length === 0
        ? "Astilba Env inventory is conformant"
        : "Astilba Env inventory is conformant with notices";
  }
  const lines = [`${summary} (ownership: ${report.ownership}).`];
  for (const issue of report.issues) {
    lines.push(
      `${issue.code}: ${issue.name}${
        issue.entry === null ? "" : ` (${issue.entry})`
      }`
    );
  }
  return `${lines.join("\n")}\n`;
};

const inventoryCommand = async (
  compilation: ProductCompilation,
  parsed: ParsedArguments,
  io: CliIo
): Promise<number> => {
  if (parsed.inventoryOperation === undefined || parsed.target === undefined) {
    return operationFailure();
  }
  const inventory = compileContractInventory(compilation, parsed.target);
  if (parsed.inventoryOperation === "export") {
    if (parsed.json) {
      writeJson(io, {
        command: "inventory",
        format: "astilba.env.cli.inventory/v1",
        inventory,
        ok: true,
        operation: "export",
      });
    } else {
      writeJson(io, inventory);
    }
    return 0;
  }
  if (parsed.observed === undefined) {
    return operationFailure();
  }
  const observed = await readObservedInventory(io.cwd, parsed.observed);
  const report = checkContractInventory(inventory, observed, parsed.ownership);
  if (parsed.json) {
    writeJson(io, {
      command: "inventory",
      format: "astilba.env.cli.inventory/v1",
      ok: report.pass,
      operation: "check",
      report,
    });
  } else {
    io.stdout.write(renderInventoryCheckReport(report));
  }
  return report.pass ? 0 : 1;
};

const checkCommand = (
  compilation: ProductCompilation,
  targetId: string,
  json: boolean,
  io: CliIo,
  useAmbientEnvironment: boolean
): number => {
  const target = compilation.targets.find(
    (candidate) => candidate.bindingPlan.target === targetId
  );
  if (target === undefined) {
    return usageFailure();
  }
  const projection = findProjection(compilation.compiled, target.consumer);
  const lifecycle = target.bindingPlan.bindings[0]?.channel;
  if (
    projection === undefined ||
    lifecycle === undefined ||
    target.bindingPlan.bindings.some((binding) => binding.channel !== lifecycle)
  ) {
    return operationFailure();
  }
  const bindings: readonly ResolutionBinding[] =
    target.bindingPlan.bindings.map((binding) =>
      Object.freeze({
        entry: binding.entry,
        source: binding.rawName,
      })
    );
  const definition: ProcessTargetDefinition = {
    bindings,
    generated: "astilba.env.generated-module/v1",
    lifecycle,
    projection: projection.manifest,
  };
  const result = checkProcessTarget<Readonly<Record<string, JsonValue>>>(
    definition,
    useAmbientEnvironment
      ? materializeAmbientEnvironment([target.bindingPlan])
      : io.environment
  );
  if (result.ok) {
    if (json) {
      writeJson(io, {
        command: "check",
        format: "astilba.env.cli.check/v1",
        ok: true,
        target: targetId,
      });
    } else {
      io.stdout.write("Astilba Env configuration is valid.\n");
    }
    return 0;
  }

  if (json) {
    writeJson(io, {
      command: "check",
      diagnostics: result.diagnostics,
      format: "astilba.env.cli.check/v1",
      ok: false,
      target: targetId,
    });
  } else {
    io.stdout.write("Astilba Env configuration is invalid.\n");
  }
  return 1;
};

const selectedPublicBuildPlans = (
  compilation: ProductCompilation
): readonly ProductCompilation["targets"][number]["bindingPlan"][] => {
  const targets = [...compilation.targets].toSorted((left, right) =>
    compareText(left.bindingPlan.target, right.bindingPlan.target)
  );
  const targetByName = new Map(
    targets.map((target) => [target.bindingPlan.target, target])
  );
  const targetNames = [...targetByName.keys()];
  if (targetNames.length !== targets.length) {
    throw new TypeError("Astilba Env process target IDs must be unique.");
  }
  const plans: ProductCompilation["targets"][number]["bindingPlan"][] = [];
  for (const projection of [...compilation.compiled.projections].toSorted(
    (left, right) =>
      compareText(left.manifest.consumer, right.manifest.consumer)
  )) {
    if (
      projection.manifest.kind !== "public" ||
      !projection.manifest.entries.some((entry) => entry.lifecycle === "build")
    ) {
      continue;
    }
    const buildTarget = targetNames.find(
      (target) =>
        targetByName.get(target)?.consumer === projection.manifest.consumer &&
        targetByName
          .get(target)
          ?.bindingPlan.bindings.every(
            (binding) => binding.channel === "build"
          ) === true
    );
    if (buildTarget === undefined) {
      continue;
    }
    const plan = targetByName.get(buildTarget)?.bindingPlan;
    if (plan !== undefined) {
      plans.push(plan);
    }
  }
  return Object.freeze(plans);
};

const materializeAmbientEnvironment = (
  plans: readonly ProductCompilation["targets"][number]["bindingPlan"][]
): Readonly<Record<string, string | undefined>> => {
  const names: string[] = [];
  const seen = new Set<string>();
  for (const plan of plans) {
    const planNames = new Set<string>();
    for (const binding of plan.bindings) {
      const folded = asciiCaseFold(binding.rawName);
      if (planNames.has(folded)) {
        throw new TypeError("Case-folding source collision");
      }
      planNames.add(folded);
    }
    for (const binding of plan.bindings) {
      const folded = asciiCaseFold(binding.rawName);
      if (seen.has(folded)) {
        continue;
      }
      seen.add(folded);
      names.push(binding.rawName);
    }
  }
  return materializeStringRecord(process.env, names);
};

const renderPlan = (plan: ImpactPlan): string =>
  plan.actions.length === 0
    ? "No environment actions are required.\n"
    : "Astilba Env found required environment actions.\n";

const mappedFailure = (error: unknown): CliFailure => {
  if (error instanceof CliFailure) {
    return error;
  }
  if (error instanceof GeneratedOutputStaleError) {
    return new CliFailure("ENV_GENERATED_STALE", 1, error.paths);
  }
  if (error instanceof GeneratedDirectoryFailure) {
    return new CliFailure(error.code, 1);
  }
  if (error instanceof InventoryFailure) {
    return new CliFailure(
      error.code,
      error.code === "ENV_INVENTORY_TARGET_UNKNOWN" ||
        error.code === "ENV_INVENTORY_TARGET_UNSUPPORTED"
        ? 2
        : 1
    );
  }
  return new CliFailure("ENV_COMMAND_FAILED", 1);
};

export const runCli = async (
  arguments_: readonly string[],
  overrides: Partial<CliIo> = {},
  hooks: CliTestHooks = {}
): Promise<number> => {
  const environment = overrides.environment;
  const useAmbientEnvironment = environment === undefined;
  const io: CliIo = {
    cwd: overrides.cwd ?? process.cwd(),
    environment: environment ?? process.env,
    stderr: overrides.stderr ?? process.stderr,
    stdout: overrides.stdout ?? process.stdout,
  };
  const json = arguments_.includes("--json");
  const errorCommand = commandFromToken(arguments_[0]);
  const compile = hooks.compileConfiguration ?? compileConfiguration;

  try {
    const parsed = parseArguments(arguments_);
    const configuration = await validateConfigurationPath(
      io.cwd,
      parsed.config
    );
    switch (parsed.command) {
      case "generate": {
        const prepared = await prepareGeneratedOutput({
          check: parsed.checkGenerated,
          projectRoot: configuration.projectRoot,
        });
        const compilation = await compile(configuration.path);
        const product = compileProductFromCompilation(
          compilation,
          useAmbientEnvironment
            ? materializeAmbientEnvironment(
                selectedPublicBuildPlans(compilation)
              )
            : io.environment
        );
        const result = await writeGeneratedProduct(
          product,
          prepared,
          parsed.checkGenerated
        );
        if (parsed.json) {
          writeJson(io, {
            command: "generate",
            files: result.files,
            format: "astilba.env.cli.generate/v1",
            mode: result.mode,
            ok: true,
          });
        } else {
          io.stdout.write(
            parsed.checkGenerated
              ? "Astilba Env generated output is current.\n"
              : "Astilba Env generated output was written.\n"
          );
        }
        return 0;
      }
      case "check": {
        const compilation = await compile(configuration.path);
        if (parsed.target === undefined) {
          return operationFailure();
        }
        return checkCommand(
          compilation,
          parsed.target,
          parsed.json,
          io,
          useAmbientEnvironment
        );
      }
      case "inventory": {
        const compilation = await compile(configuration.path);
        return await inventoryCommand(compilation, parsed, io);
      }
      case "plan": {
        if (parsed.base === undefined) {
          return operationFailure();
        }
        const before = await readHistoricalSnapshot(
          configuration.projectRoot,
          parsed.base
        );
        const compilation = await compile(configuration.path);
        const current = createPlanningSnapshot({
          compiled: compilation.compiled,
          targets: compilation.targets,
        });
        const plan = planImpact({
          after: current,
          before,
        });
        if (parsed.json) {
          writeJson(io, {
            command: "plan",
            format: "astilba.env.cli.plan/v1",
            ok: true,
            plan,
          });
        } else {
          io.stdout.write(renderPlan(plan));
        }
        return plan.consumers.some(
          (consumer) => consumer.confidence === "UNKNOWN"
        )
          ? 1
          : 0;
      }
    }
  } catch (error) {
    const failure = mappedFailure(error);
    if (json) {
      const errorRecord: JsonObject = {
        code: failure.code,
        message: fixedMessage(failure.code),
        ...(failure.paths === undefined ? {} : { paths: failure.paths }),
      };
      writeJson(
        io,
        {
          command: errorCommand,
          error: errorRecord,
          format: "astilba.env.cli.error/v1",
          ok: false,
        },
        "stderr"
      );
    } else {
      io.stderr.write(`${fixedMessage(failure.code)}\n`);
    }
    return failure.status;
  }
};

const hasNativeTypeScript = (): boolean => {
  const features = process.features as Readonly<{
    typescript?: unknown;
  }>;
  return (
    features.typescript === true ||
    features.typescript === "strip" ||
    features.typescript === "transform"
  );
};

const stripFlags = [
  "--experimental-strip-types",
  "--no-experimental-strip-types",
  "--no-strip-types",
  "--strip-types",
] as const;

const isFlagForm = (argument: string, flag: string): boolean =>
  argument === flag || argument.startsWith(`${flag}=`);

export const prepareTypeScriptExecArguments = (
  arguments_: readonly string[]
): readonly string[] => {
  const retained = arguments_.filter(
    (argument) => !stripFlags.some((flag) => isFlagForm(argument, flag))
  );
  return [
    ...retained,
    "--experimental-strip-types",
    ...(retained.includes("--disable-warning=ExperimentalWarning")
      ? []
      : ["--disable-warning=ExperimentalWarning"]),
  ];
};

const hasEnabledTypeStripping = (): boolean =>
  process.execArgv.some(
    (argument) =>
      isFlagForm(argument, "--experimental-strip-types") ||
      isFlagForm(argument, "--strip-types")
  );

const writeExecutableFailure = (arguments_: readonly string[]): void => {
  const json = arguments_.includes("--json");
  if (json) {
    process.stderr.write(
      `${canonicalJson({
        command: commandFromToken(arguments_[0]),
        error: {
          code: "ENV_COMMAND_FAILED",
          message: fixedMessage("ENV_COMMAND_FAILED"),
        },
        format: "astilba.env.cli.error/v1",
        ok: false,
      })}\n`
    );
    return;
  }
  process.stderr.write(`${fixedMessage("ENV_COMMAND_FAILED")}\n`);
};

const runExecutableCli = async (
  executable: string,
  arguments_: readonly string[]
): Promise<number> => {
  if (!hasNativeTypeScript() && !hasEnabledTypeStripping()) {
    const result = spawnSync(
      process.execPath,
      [
        ...prepareTypeScriptExecArguments(process.execArgv),
        executable,
        ...arguments_,
      ],
      { stdio: "inherit" }
    );
    if (
      result.error !== undefined ||
      result.signal !== null ||
      result.status === null
    ) {
      writeExecutableFailure(arguments_);
      return 1;
    }
    return result.status;
  }
  return await runCli(arguments_);
};

if (process.argv[1] !== undefined) {
  const executable = await realpath(resolve(process.argv[1])).catch(() => "");
  const current = await realpath(import.meta.filename).catch(() => "");
  if (executable !== "" && executable === current) {
    process.exitCode = await runExecutableCli(current, process.argv.slice(2));
  }
}
