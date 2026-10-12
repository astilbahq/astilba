import type {
  AggregateResult,
  CoreDiagnostic,
  CoreDiagnostics,
  LocalId,
  ProcessProjection,
  RawSourceName,
} from "./model.ts";
import { opaqueRefusalDiagnostics, resolveTarget } from "./resolution.ts";
import { aggregateFailure, cloneDiagnostics, diagnostic } from "./results.ts";
import { validateProcessTarget } from "./validation.ts";

/**
 * Object containing raw configuration sources, usually `process.env` or a
 * Worker `Env` binding object. Values are read by the generated target only.
 */
export type ProcessSource = object;

/** Generated server target consumed by the runtime loader. */
export type ProcessTargetDefinition = Readonly<{
  /** Maps each selected entry name to the raw property name in `source`. */
  bindings: readonly Readonly<{
    /** Generated local entry identifier. */
    entry: LocalId;
    /** Raw source property read for this entry. */
    source: RawSourceName;
  }>[];
  /** Generated-module format identifier required by this runtime version. */
  generated: "astilba.env.generated-module/v1";
  /** Lifecycle at which this target is allowed to resolve values. */
  lifecycle: "build" | "deployment" | "request";
  /** Compiled projection whose identity and entry rules are validated first. */
  projection: ProcessProjection;
}>;

const CONSTRUCTION_TOKEN = Object.freeze({});
const ERROR_NAME = "EnvironmentConfigurationError";
const ERROR_MESSAGE = "Astilba Env configuration is invalid.";
const DIRECT_CONSTRUCTION_MESSAGE =
  "EnvironmentConfigurationError cannot be constructed directly.";

/**
 * Error thrown by a `load*` runtime helper when configuration is invalid.
 * Constructing it directly is rejected; use {@link diagnostics} to display
 * structured, value-free failure information.
 */
export class EnvironmentConfigurationError extends Error {
  /** Non-empty, deterministic diagnostics describing why loading failed. */
  declare readonly diagnostics: CoreDiagnostics;

  private constructor();
  private constructor(
    diagnostics?: readonly CoreDiagnostic[],
    token?: unknown
  ) {
    if (token !== CONSTRUCTION_TOKEN) {
      throw new TypeError(DIRECT_CONSTRUCTION_MESSAGE);
    }
    super(ERROR_MESSAGE);
    const owned = cloneDiagnostics(diagnostics ?? []);
    Object.defineProperties(this, {
      diagnostics: {
        configurable: false,
        enumerable: false,
        value: owned,
        writable: false,
      },
      message: {
        configurable: false,
        enumerable: false,
        value: ERROR_MESSAGE,
        writable: false,
      },
      name: {
        configurable: false,
        enumerable: false,
        value: ERROR_NAME,
        writable: false,
      },
    });
  }
}

export const createEnvironmentConfigurationError = (
  diagnostics: readonly CoreDiagnostic[]
): EnvironmentConfigurationError =>
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- Reflect.construct is the private-constructor bridge; the construction token enforces the runtime invariant.
  Reflect.construct(EnvironmentConfigurationError, [
    diagnostics,
    CONSTRUCTION_TOKEN,
  ]) as EnvironmentConfigurationError;

/**
 * Validates and resolves a generated process target without throwing for
 * configuration failures. Opaque validators are refused by this synchronous
 * Node path; use {@link checkProcessTargetWithSchemas} when required. When
 * this export resolves under the `workerd` condition, only deployment targets
 * are accepted and opaque-schema validation remains unavailable.
 */
export const checkProcessTarget = <const TConfiguration extends object>(
  definition: ProcessTargetDefinition,
  source: ProcessSource
): AggregateResult<TConfiguration> => {
  const validated = validateProcessTarget(definition);
  if (!validated.ok) {
    return aggregateFailure([diagnostic({ code: validated.code })]);
  }
  const opaque = opaqueRefusalDiagnostics(validated.target);
  if (opaque.length > 0) {
    return aggregateFailure(opaque);
  }
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- The generated target's frozen type projection supplies TConfiguration after runtime validation.
  return resolveTarget(
    validated.target,
    source
  ) as AggregateResult<TConfiguration>;
};

/**
 * Resolves a generated process target or throws
 * {@link EnvironmentConfigurationError} with value-free diagnostics. The
 * `workerd` conditional export accepts deployment targets only and cannot
 * validate opaque entries.
 */
// oxlint-disable-next-line typescript/no-unnecessary-type-parameters -- The generated module supplies the frozen configuration type at each load call.
export const loadProcessTarget = <const TConfiguration extends object>(
  definition: ProcessTargetDefinition,
  source: ProcessSource
): TConfiguration => {
  const result = checkProcessTarget<TConfiguration>(definition, source);
  if (!result.ok) {
    throw createEnvironmentConfigurationError(result.diagnostics);
  }
  return result.value;
};
