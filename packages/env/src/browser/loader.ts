/* oxlint-disable typescript/no-unsafe-assignment, typescript/no-unsafe-type-assertion -- The frozen protocol requires explicit staged reads, exact precedence, and defensive reflection over hostile inputs. */

import { bootstrapFailure } from "./failure.ts";
import type { BootstrapFailure, BootstrapFailureCode } from "./failure.ts";
import { parseBootstrapJson } from "./json.ts";
import type { JsonObject, JsonValue } from "./json.ts";

/** Exact protocol identifier accepted by this browser bootstrap implementation. */
export const BOOTSTRAP_PROTOCOL = "astilba.env.bootstrap/v1";
/** Maximum UTF-8 source size accepted for one bootstrap envelope, in bytes. */
export const MAXIMUM_BOOTSTRAP_BYTES = 65_536;

const GENERATED_FIELDS = [
  "codecAbi",
  "consumer",
  "contract",
  "decode",
  "digest",
  "format",
  "formatVersion",
  "generated",
  "kind",
  "lifecycle",
  "projectionAbi",
] as const;
const GENERATED_MODULE = /^astilba\.env\.generated-module\/v([1-9][0-9]*)$/;
const LOCAL_ID = /^[a-z][A-Za-z0-9]{0,63}$/u;
const CONTRACT_ID =
  /^[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?)+$/u;
const SHA256_DIGEST = /^sha256-[A-Za-z0-9_-]{42}[AEIMQUYcgkosw048]$/u;
const RESERVED_CONSUMER = /^(?:aux|com[1-9]|con|lpt[1-9]|nul|prn)$/u;
const JSON_MIME =
  /^application\/json[ \t]*(?:;[ \t]*charset[ \t]*=[ \t]*(?:utf-8|"utf-8")[ \t]*)?$/iu;
const TOP_LEVEL_FIELDS = [
  "audience",
  "consumer",
  "contract",
  "lifecycle",
  "projection",
  "protocol",
  "values",
] as const;
const REMAINING_REQUIRED_FIELDS = [
  "audience",
  "consumer",
  "contract",
  "lifecycle",
  "projection",
  "values",
] as const;
declare const projectionValues: unique symbol;

/** Audience bound into a public bootstrap; `origin` is an ASCII serialised origin. */
export type BrowserAudience = Readonly<{
  /** Exact origin allowed to consume the public values. */
  origin: string;
}>;
/** Record of decoded public values. Values are public data and must not contain secrets. */
export type BrowserValues = Readonly<Record<string, unknown>>;

/** Generated identity evidence for one browser-safe consumer projection. */
export type BrowserProjection<TValues extends object = BrowserValues> =
  Readonly<{
    /** Consumer identifier encoded by the generated projection. */
    consumer: string;
    /** Contract identifier encoded by the generated projection. */
    contract: string;
    /** Exact SHA-256 digest of the generated public projection. */
    digest: `sha256-${string}`;
    /** Lifecycle accepted by this browser projection. */
    lifecycle: "deployment" | "request";
    [projectionValues]: TValues;
  }>;

/** Public values after the complete bootstrap envelope has been validated. */
export type ValidatedBootstrap<TValues extends object = BrowserValues> =
  Readonly<{
    /** Audience proven by the envelope and matched against the expected audience. */
    audience: BrowserAudience;
    /** Readonly decoded public values for the generated consumer. */
    values: Readonly<TValues>;
  }>;

/** Options for same-origin network bootstrap delivery. */
export type LoadBootstrapOptions<TValues extends object = BrowserValues> =
  Readonly<{
    /** Same-origin endpoint from which to fetch the inert JSON envelope. */
    endpoint: string | URL;
    /** Audience that must match the envelope and final response origin. */
    expectedAudience: BrowserAudience;
    /** Fetch implementation to use; provide the browser's bound `fetch`. */
    fetch: typeof globalThis.fetch;
    /** Generated projection imported from a browser-safe generated module. */
    projection: BrowserProjection<TValues>;
    /** Base URL used to resolve `endpoint` and enforce same-origin delivery. */
    requestBaseUrl: string | URL;
  }>;

/** Options for a framework-transported, safely escaped bootstrap envelope. */
export type ParseBootstrapOptions<TValues extends object = BrowserValues> =
  Readonly<{
    /** Audience that must match the inert envelope. */
    expectedAudience: BrowserAudience;
    /** Generated projection imported from a browser-safe generated module. */
    projection: BrowserProjection<TValues>;
    /** Unescaped JSON envelope string supplied by framework transport. */
    source: string;
  }>;

/** Lazy application module started only after public bootstrap validation succeeds. */
export type BrowserApplicationModule<
  Result = void,
  TValues extends object = BrowserValues,
> = Readonly<{
  /** Receives validated values and audience; may return synchronously or asynchronously. */
  start(
    values: Readonly<TValues>,
    audience: BrowserAudience
  ): Result | Promise<Result>;
}>;

/** Options for loading a bootstrap and then lazily starting an application module. */
export type StartBrowserApplicationOptions<
  Result = void,
  TValues extends object = BrowserValues,
> = LoadBootstrapOptions<TValues> &
  Readonly<{
    /** Imports the application only after bootstrap validation succeeds. */
    importApplication: () => Promise<BrowserApplicationModule<Result, TValues>>;
  }>;

type GeneratedFailure = (
  code:
    | "BOOTSTRAP_UNKNOWN_FIELD"
    | "BOOTSTRAP_VALUE_INVALID"
    | "BOOTSTRAP_VALUE_MISSING"
) => never;

type GeneratedProjection = Readonly<{
  codecAbi: "astilba.env.codec/v1";
  consumer: string;
  contract: string;
  decode: (input: JsonObject, failure: GeneratedFailure) => unknown;
  digest: `sha256-${string}`;
  format: "astilba.env.projection";
  formatVersion: 1;
  generated: "astilba.env.generated-module/v1";
  kind: "public";
  lifecycle: "deployment" | "request";
  projectionAbi: "astilba.env.projection/v1";
}>;

type Callable = (...arguments_: never[]) => unknown;

const failure = (code: BootstrapFailureCode): never => {
  throw bootstrapFailure(code);
};

// oxlint-disable-next-line typescript/no-unnecessary-type-parameters -- This local Reflect.apply boundary preserves the callable result type for hardened dynamic imports.
const apply = <Result>(
  callable: Callable,
  receiver: unknown,
  arguments_: readonly unknown[]
): Result => Reflect.apply(callable, receiver, arguments_) as Result;

const readProperty = (value: unknown, key: PropertyKey): unknown =>
  (value as Record<PropertyKey, unknown>)[key];

const asciiFold = (value: string): string => value.toLowerCase();

const isObjectOrFunction = (value: unknown): value is object | Callable =>
  (typeof value === "object" && value !== null) || typeof value === "function";

const isJsonObject = (value: JsonValue): value is JsonObject =>
  typeof value === "object" && value !== null && !Array.isArray(value);

const isConsumerId = (value: unknown): value is string =>
  typeof value === "string" &&
  LOCAL_ID.test(value) &&
  !RESERVED_CONSUMER.test(asciiFold(value));

const isContractId = (value: unknown): value is string =>
  typeof value === "string" &&
  value.length >= 3 &&
  value.length <= 255 &&
  CONTRACT_ID.test(value);

const isDigest = (value: unknown): value is `sha256-${string}` =>
  typeof value === "string" && SHA256_DIGEST.test(value);

const ownDataValue = (value: object | Callable, key: PropertyKey): unknown => {
  const descriptor = Object.getOwnPropertyDescriptor(value, key);
  if (
    descriptor === undefined ||
    descriptor.enumerable !== true ||
    !Object.hasOwn(descriptor, "value")
  ) {
    return failure("BOOTSTRAP_PROJECTION_INVALID");
  }
  return descriptor.value;
};

const captureProjection = (options: object): GeneratedProjection => {
  let value: unknown;
  try {
    value = readProperty(options, "projection");
  } catch {
    return failure("BOOTSTRAP_PROJECTION_INVALID");
  }
  if (!isObjectOrFunction(value)) {
    return failure("BOOTSTRAP_PROJECTION_INVALID");
  }

  let keys: readonly PropertyKey[];
  let generated: unknown;
  try {
    keys = Reflect.ownKeys(value);
    if (!keys.includes("generated")) {
      return failure("BOOTSTRAP_PROJECTION_INVALID");
    }
    generated = ownDataValue(value, "generated");
  } catch {
    return failure("BOOTSTRAP_PROJECTION_INVALID");
  }

  if (typeof generated !== "string") {
    return failure("BOOTSTRAP_PROJECTION_INVALID");
  }
  const version = GENERATED_MODULE.exec(generated)?.[1];
  if (version === undefined) {
    return failure("BOOTSTRAP_PROJECTION_INVALID");
  }
  if (version !== "1") {
    return failure("BOOTSTRAP_GENERATED_FORMAT_UNSUPPORTED");
  }

  let prototype: object | null;
  try {
    if (Array.isArray(value)) {
      return failure("BOOTSTRAP_PROJECTION_INVALID");
    }
    prototype = Object.getPrototypeOf(value);
  } catch {
    return failure("BOOTSTRAP_PROJECTION_INVALID");
  }
  if (prototype !== Object.prototype && prototype !== null) {
    return failure("BOOTSTRAP_PROJECTION_INVALID");
  }

  const knownFields = new Set<string>(GENERATED_FIELDS);
  if (
    keys.length !== GENERATED_FIELDS.length ||
    keys.some((key) => typeof key !== "string" || !knownFields.has(key))
  ) {
    return failure("BOOTSTRAP_PROJECTION_INVALID");
  }

  const captured = Object.create(null) as Record<string, unknown>;
  captured.generated = generated;
  const remaining = keys
    .filter(
      (key): key is string => typeof key === "string" && key !== "generated"
    )
    .toSorted();
  try {
    for (const key of remaining) {
      captured[key] = ownDataValue(value, key);
    }
  } catch {
    return failure("BOOTSTRAP_PROJECTION_INVALID");
  }

  if (
    captured.codecAbi !== "astilba.env.codec/v1" ||
    !isConsumerId(captured.consumer) ||
    !isContractId(captured.contract) ||
    typeof captured.decode !== "function" ||
    !isDigest(captured.digest) ||
    captured.format !== "astilba.env.projection" ||
    captured.formatVersion !== 1 ||
    captured.kind !== "public" ||
    (captured.lifecycle !== "deployment" && captured.lifecycle !== "request") ||
    captured.projectionAbi !== "astilba.env.projection/v1"
  ) {
    return failure("BOOTSTRAP_PROJECTION_INVALID");
  }
  return captured as GeneratedProjection;
};

const captureExpectedOrigin = (options: object): string => {
  let audience: unknown;
  let origin: unknown;
  try {
    audience = readProperty(options, "expectedAudience");
    if (typeof audience !== "object" || audience === null) {
      return failure("BOOTSTRAP_AUDIENCE_MISMATCH");
    }
    origin = readProperty(audience, "origin");
  } catch {
    return failure("BOOTSTRAP_AUDIENCE_MISMATCH");
  }
  if (typeof origin !== "string") {
    return failure("BOOTSTRAP_AUDIENCE_MISMATCH");
  }
  try {
    const parsed = new URL(origin);
    if (
      (parsed.protocol !== "http:" && parsed.protocol !== "https:") ||
      parsed.origin === "null" ||
      parsed.origin !== origin
    ) {
      return failure("BOOTSTRAP_AUDIENCE_MISMATCH");
    }
    return parsed.origin;
  } catch {
    return failure("BOOTSTRAP_AUDIENCE_MISMATCH");
  }
};

const isUrlInput = (value: unknown): value is string | URL =>
  typeof value === "string" || value instanceof URL;

const captureRequestUrl = (options: object, expectedOrigin: string): URL => {
  let endpoint: unknown;
  let requestBaseUrl: unknown;
  try {
    endpoint = readProperty(options, "endpoint");
  } catch {
    return failure("BOOTSTRAP_REQUEST_ORIGIN_MISMATCH");
  }
  if (!isUrlInput(endpoint)) {
    return failure("BOOTSTRAP_REQUEST_ORIGIN_MISMATCH");
  }
  try {
    requestBaseUrl = readProperty(options, "requestBaseUrl");
  } catch {
    return failure("BOOTSTRAP_REQUEST_ORIGIN_MISMATCH");
  }
  if (!isUrlInput(requestBaseUrl)) {
    return failure("BOOTSTRAP_REQUEST_ORIGIN_MISMATCH");
  }
  try {
    const requestUrl = new URL(endpoint, requestBaseUrl);
    if (requestUrl.origin !== expectedOrigin) {
      return failure("BOOTSTRAP_REQUEST_ORIGIN_MISMATCH");
    }
    return requestUrl;
  } catch {
    return failure("BOOTSTRAP_REQUEST_ORIGIN_MISMATCH");
  }
};

const captureSource = (options: object): string => {
  let source: unknown;
  try {
    source = readProperty(options, "source");
  } catch {
    return failure("BOOTSTRAP_INVALID_JSON");
  }
  return typeof source === "string"
    ? source
    : failure("BOOTSTRAP_INVALID_JSON");
};

const requireField = (value: JsonObject, field: string): void => {
  if (!Object.hasOwn(value, field)) {
    failure("BOOTSTRAP_FIELD_MISSING");
  }
};

const decodeConfiguration = <TValues extends object>(
  projection: GeneratedProjection,
  input: JsonObject
): Readonly<TValues> => {
  let issuedFailure: BootstrapFailure | undefined;
  const generatedFailure = (code: unknown): never => {
    const normalised =
      code === "BOOTSTRAP_UNKNOWN_FIELD" ||
      code === "BOOTSTRAP_VALUE_INVALID" ||
      code === "BOOTSTRAP_VALUE_MISSING"
        ? code
        : "BOOTSTRAP_VALUE_INVALID";
    issuedFailure = bootstrapFailure(normalised);
    // oxlint-disable-next-line eslint/no-throw-literal -- Generated failures must preserve their exact guarded BootstrapFailure identity.
    throw issuedFailure;
  };

  let decoded: unknown;
  try {
    decoded = apply(projection.decode, undefined, [input, generatedFailure]);
  } catch (error) {
    if (issuedFailure !== undefined && error === issuedFailure) {
      // oxlint-disable-next-line eslint/no-throw-literal -- Generated failures must preserve their exact guarded BootstrapFailure identity.
      throw issuedFailure;
    }
    return failure("BOOTSTRAP_VALUE_INVALID");
  }
  if (issuedFailure !== undefined) {
    // oxlint-disable-next-line eslint/no-throw-literal -- A swallowed generated failure remains authoritative by exact identity.
    throw issuedFailure;
  }

  let keys: readonly PropertyKey[];
  try {
    if (
      typeof decoded !== "object" ||
      decoded === null ||
      Array.isArray(decoded) ||
      Object.getPrototypeOf(decoded) !== null
    ) {
      return failure("BOOTSTRAP_VALUE_INVALID");
    }
    keys = Reflect.ownKeys(decoded);
  } catch {
    return failure("BOOTSTRAP_VALUE_INVALID");
  }
  const names: string[] = [];
  const folded = new Set<string>();
  for (const key of keys) {
    if (typeof key !== "string" || !LOCAL_ID.test(key)) {
      return failure("BOOTSTRAP_VALUE_INVALID");
    }
    const identity = asciiFold(key);
    if (folded.has(identity)) {
      return failure("BOOTSTRAP_VALUE_INVALID");
    }
    folded.add(identity);
    names.push(key);
  }
  const sortedNames = names.toSorted();

  const captured = new Map<string, unknown>();
  try {
    for (const name of sortedNames) {
      const descriptor = Object.getOwnPropertyDescriptor(decoded, name);
      if (
        descriptor === undefined ||
        descriptor.enumerable !== true ||
        !Object.hasOwn(descriptor, "value")
      ) {
        return failure("BOOTSTRAP_VALUE_INVALID");
      }
      captured.set(name, descriptor.value);
    }
  } catch {
    return failure("BOOTSTRAP_VALUE_INVALID");
  }

  const output = Object.create(null) as Record<string, unknown>;
  for (const name of sortedNames) {
    output[name] = captured.get(name);
  }
  return Object.freeze(output) as Readonly<TValues>;
};

const validateEnvelope = <TValues extends object>(
  parsed: JsonValue,
  projection: GeneratedProjection,
  expectedOrigin: string
): ValidatedBootstrap<TValues> => {
  if (!isJsonObject(parsed)) {
    return failure("BOOTSTRAP_FIELD_INVALID");
  }

  requireField(parsed, "protocol");
  if (typeof parsed.protocol !== "string") {
    return failure("BOOTSTRAP_FIELD_INVALID");
  }
  if (parsed.protocol !== BOOTSTRAP_PROTOCOL) {
    return failure("BOOTSTRAP_PROTOCOL_UNSUPPORTED");
  }
  for (const field of REMAINING_REQUIRED_FIELDS) {
    requireField(parsed, field);
  }
  if (
    Object.keys(parsed).some(
      (field) => !(TOP_LEVEL_FIELDS as readonly string[]).includes(field)
    )
  ) {
    return failure("BOOTSTRAP_UNKNOWN_FIELD");
  }

  if (
    !isJsonObject(parsed.audience as JsonValue) ||
    typeof parsed.consumer !== "string" ||
    typeof parsed.contract !== "string" ||
    typeof parsed.lifecycle !== "string" ||
    typeof parsed.projection !== "string" ||
    !isJsonObject(parsed.values as JsonValue)
  ) {
    return failure("BOOTSTRAP_FIELD_INVALID");
  }

  const audience = parsed.audience as JsonObject;
  requireField(audience, "origin");
  if (Object.keys(audience).some((field) => field !== "origin")) {
    return failure("BOOTSTRAP_UNKNOWN_FIELD");
  }
  if (typeof audience.origin !== "string") {
    return failure("BOOTSTRAP_FIELD_INVALID");
  }

  if (parsed.contract !== projection.contract) {
    return failure("BOOTSTRAP_CONTRACT_MISMATCH");
  }
  if (
    parsed.consumer !== projection.consumer ||
    parsed.projection !== projection.digest
  ) {
    return failure("BOOTSTRAP_PROJECTION_MISMATCH");
  }
  if (parsed.lifecycle !== projection.lifecycle) {
    return failure("BOOTSTRAP_LIFECYCLE_MISMATCH");
  }
  if (audience.origin !== expectedOrigin) {
    return failure("BOOTSTRAP_AUDIENCE_MISMATCH");
  }

  const values = decodeConfiguration<TValues>(
    projection,
    parsed.values as JsonObject
  );
  const ownedAudience = Object.freeze({
    origin: expectedOrigin,
  });
  return Object.freeze({
    audience: ownedAudience,
    values,
  });
};

const parseSource = <TValues extends object>(
  source: string,
  projection: GeneratedProjection,
  expectedOrigin: string
): ValidatedBootstrap<TValues> => {
  if (source.codePointAt(0) === 0xfe_ff) {
    return failure("BOOTSTRAP_INVALID_JSON");
  }
  return validateEnvelope<TValues>(
    parseBootstrapJson(source),
    projection,
    expectedOrigin
  );
};

const captureFetch = (options: object): Callable => {
  let fetch: unknown;
  try {
    fetch = readProperty(options, "fetch");
  } catch {
    return failure("BOOTSTRAP_FETCH_FAILED");
  }
  return typeof fetch === "function"
    ? (fetch as Callable)
    : failure("BOOTSTRAP_FETCH_FAILED");
};

const fetchBootstrap = async (
  fetch: Callable,
  requestUrl: URL
): Promise<unknown> => {
  try {
    return await apply(fetch, undefined, [
      requestUrl,
      {
        cache: "no-store",
        redirect: "error",
      },
    ]);
  } catch {
    return failure("BOOTSTRAP_FETCH_FAILED");
  }
};

const readResponseField = (
  response: unknown,
  key: string,
  code: BootstrapFailureCode
): unknown => {
  try {
    return readProperty(response, key);
  } catch {
    return failure(code);
  }
};

const validateResponse = (
  response: unknown,
  expectedOrigin: string
): unknown => {
  const redirected = readResponseField(
    response,
    "redirected",
    "BOOTSTRAP_REDIRECTED"
  );
  if (redirected !== false) {
    return failure("BOOTSTRAP_REDIRECTED");
  }

  const status = readResponseField(
    response,
    "status",
    "BOOTSTRAP_HTTP_STATUS_INVALID"
  );
  if (
    typeof status !== "number" ||
    !Number.isInteger(status) ||
    status < 200 ||
    status > 299
  ) {
    return failure("BOOTSTRAP_HTTP_STATUS_INVALID");
  }

  const url = readResponseField(
    response,
    "url",
    "BOOTSTRAP_FINAL_ORIGIN_MISMATCH"
  );
  if (typeof url !== "string") {
    return failure("BOOTSTRAP_FINAL_ORIGIN_MISMATCH");
  }
  try {
    if (new URL(url).origin !== expectedOrigin) {
      return failure("BOOTSTRAP_FINAL_ORIGIN_MISMATCH");
    }
  } catch {
    return failure("BOOTSTRAP_FINAL_ORIGIN_MISMATCH");
  }

  const headers = readResponseField(
    response,
    "headers",
    "BOOTSTRAP_INVALID_MIME"
  );
  let get: unknown;
  try {
    get = readProperty(headers, "get");
  } catch {
    return failure("BOOTSTRAP_INVALID_MIME");
  }
  if (typeof get !== "function") {
    return failure("BOOTSTRAP_INVALID_MIME");
  }
  let contentType: unknown;
  try {
    contentType = apply(get as Callable, headers, ["content-type"]);
  } catch {
    return failure("BOOTSTRAP_INVALID_MIME");
  }
  if (typeof contentType !== "string" || !JSON_MIME.test(contentType)) {
    return failure("BOOTSTRAP_INVALID_MIME");
  }
  return response;
};

const cancelAfterBodyLimit = (
  reader: object | Callable,
  cancel: Callable
): void => {
  try {
    const result = apply(cancel, reader, []);
    // oxlint-disable-next-line promise/spec-only -- The protocol requires best-effort observation of genuine cancellation promises without awaiting them.
    void Promise.prototype.then.call(
      result,
      () => null,
      () => null
    );
  } catch {
    // The selected body failure remains authoritative.
  }
};

const readBody = async (response: unknown): Promise<Uint8Array> => {
  let body: unknown;
  let getReader: unknown;
  let reader: unknown;
  let read: unknown;
  let cancel: unknown;
  try {
    body = readProperty(response, "body");
    if (typeof body !== "object" || body === null) {
      return failure("BOOTSTRAP_BODY_READ_FAILED");
    }
    getReader = readProperty(body, "getReader");
    if (typeof getReader !== "function") {
      return failure("BOOTSTRAP_BODY_READ_FAILED");
    }
    reader = apply(getReader as Callable, body, []);
    if (typeof reader !== "object" || reader === null) {
      return failure("BOOTSTRAP_BODY_READ_FAILED");
    }
    read = readProperty(reader, "read");
    cancel = readProperty(reader, "cancel");
    if (typeof read !== "function" || typeof cancel !== "function") {
      return failure("BOOTSTRAP_BODY_READ_FAILED");
    }
  } catch {
    return failure("BOOTSTRAP_BODY_READ_FAILED");
  }

  const retained = new Uint8Array(MAXIMUM_BOOTSTRAP_BYTES + 1);
  let retainedLength = 0;
  let total = 0;
  while (true) {
    let result: unknown;
    try {
      result = await apply(read as Callable, reader, []);
    } catch {
      return failure("BOOTSTRAP_BODY_READ_FAILED");
    }
    if (typeof result !== "object" || result === null) {
      return failure("BOOTSTRAP_BODY_READ_FAILED");
    }

    let done: unknown;
    try {
      done = readProperty(result, "done");
    } catch {
      return failure("BOOTSTRAP_BODY_READ_FAILED");
    }
    if (typeof done !== "boolean") {
      return failure("BOOTSTRAP_BODY_READ_FAILED");
    }
    if (done) {
      break;
    }

    let chunk: unknown;
    try {
      chunk = readProperty(result, "value");
    } catch {
      return failure("BOOTSTRAP_BODY_READ_FAILED");
    }
    if (!(chunk instanceof Uint8Array)) {
      return failure("BOOTSTRAP_BODY_READ_FAILED");
    }

    try {
      const chunkLength = chunk.byteLength;
      total = Math.min(MAXIMUM_BOOTSTRAP_BYTES + 1, total + chunkLength);
      const copyLength = Math.min(
        chunkLength,
        retained.byteLength - retainedLength
      );
      if (copyLength > 0) {
        retained.set(chunk.subarray(0, copyLength), retainedLength);
        retainedLength += copyLength;
      }
    } catch {
      return failure("BOOTSTRAP_BODY_READ_FAILED");
    }
    if (
      retainedLength >= 3 &&
      retained[0] === 0xef &&
      retained[1] === 0xbb &&
      retained[2] === 0xbf
    ) {
      cancelAfterBodyLimit(reader, cancel as Callable);
      return failure("BOOTSTRAP_INVALID_JSON");
    }
    if (total > MAXIMUM_BOOTSTRAP_BYTES) {
      cancelAfterBodyLimit(reader, cancel as Callable);
      return failure("BOOTSTRAP_BODY_TOO_LARGE");
    }
  }
  return retained.slice(0, retainedLength);
};

const decodeBody = (body: Uint8Array): string => {
  try {
    return new TextDecoder("utf-8", {
      fatal: true,
      ignoreBOM: true,
    }).decode(body);
  } catch {
    return failure("BOOTSTRAP_INVALID_UTF8");
  }
};

/**
 * Fetches one same-origin JSON bootstrap, validates its transport and
 * generated-projection identity, and returns readonly public values.
 *
 * Rejects with {@link BootstrapFailure} on malformed, redirected,
 * cross-origin, oversized, or mismatched responses.
 */
export const loadBrowserBootstrap = async <const TValues extends object>(
  options: LoadBootstrapOptions<TValues>
): Promise<ValidatedBootstrap<TValues>> => {
  const objectOptions = options as object;
  const projection = captureProjection(objectOptions);
  const expectedOrigin = captureExpectedOrigin(objectOptions);
  const requestUrl = captureRequestUrl(objectOptions, expectedOrigin);
  const fetch = captureFetch(objectOptions);
  const response = validateResponse(
    await fetchBootstrap(fetch, requestUrl),
    expectedOrigin
  );
  const source = decodeBody(await readBody(response));
  return parseSource<TValues>(source, projection, expectedOrigin);
};

/**
 * Validates an already transported inert JSON bootstrap without performing a
 * fetch. The caller must safely transport `source`; this API never executes
 * it as JavaScript.
 */
export const parseBrowserBootstrap = <const TValues extends object>(
  options: ParseBootstrapOptions<TValues>
): ValidatedBootstrap<TValues> => {
  const objectOptions = options as object;
  const projection = captureProjection(objectOptions);
  const expectedOrigin = captureExpectedOrigin(objectOptions);
  const source = captureSource(objectOptions);
  if (new TextEncoder().encode(source).length > MAXIMUM_BOOTSTRAP_BYTES) {
    return failure("BOOTSTRAP_BODY_TOO_LARGE");
  }
  return parseSource<TValues>(source, projection, expectedOrigin);
};

/**
 * Loads a validated bootstrap, then lazily imports and starts an application.
 * The application module is not imported when bootstrap validation fails.
 */
export const startBrowserApplication = async <
  Result = void,
  const TValues extends object = BrowserValues,
>(
  options: StartBrowserApplicationOptions<Result, TValues>
): Promise<Result> => {
  const bootstrap = await loadBrowserBootstrap(options);
  const { importApplication } = options;
  const application = await apply<
    Promise<BrowserApplicationModule<Result, TValues>>
  >(importApplication, undefined, []);
  const { start } = application;
  return await apply<Result>(start, application, [
    bootstrap.values,
    bootstrap.audience,
  ]);
};
