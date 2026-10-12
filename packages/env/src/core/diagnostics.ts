import type { CodecDescriptor, Lifecycle } from "./types.ts";

export type DiagnosticCode =
  | "ENV_BINDING_INVALID"
  | "ENV_BINDING_MISSING"
  | "ENV_CONTRACT_INVALID"
  | "ENV_INVALID_VALUE"
  | "ENV_LIFECYCLE_ACCESS"
  | "ENV_MISSING_VALUE"
  | "ENV_OPAQUE_UNSUPPORTED"
  | "ENV_RULE_VIOLATION"
  | "ENV_SOURCE_INVALID"
  | "ENV_VALIDATOR_ASYNC_UNSUPPORTED"
  | "ENV_VISIBILITY_ACCESS";

export type CoreDiagnostic = Readonly<{
  code: DiagnosticCode;
  codec?: CodecDescriptor["abi"];
  consumer?: string;
  entry?: string;
  entries?: readonly string[];
  lifecycle?: Lifecycle;
  rule?: string;
}>;

type DiagnosticFields = Omit<CoreDiagnostic, "code">;

function diagnostic(
  code: DiagnosticCode,
  fields: DiagnosticFields = {}
): CoreDiagnostic {
  const result: {
    code: DiagnosticCode;
    codec?: CoreDiagnostic["codec"];
    consumer?: string;
    entry?: string;
    entries?: readonly string[];
    lifecycle?: Lifecycle;
    rule?: string;
  } = { code };

  if (fields.consumer !== undefined) {
    result.consumer = fields.consumer;
  }
  if (fields.entry !== undefined) {
    result.entry = fields.entry;
  }
  if (fields.entries !== undefined) {
    result.entries = Object.freeze([...fields.entries]);
  }
  if (fields.lifecycle !== undefined) {
    result.lifecycle = fields.lifecycle;
  }
  if (fields.rule !== undefined) {
    result.rule = fields.rule;
  }
  if (fields.codec !== undefined) {
    result.codec = fields.codec;
  }

  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- Optional fields were assigned only after the exact public-field checks above.
  return Object.freeze(result) as CoreDiagnostic;
}

export class ContractDefinitionError extends Error {
  readonly diagnostic: CoreDiagnostic;

  constructor() {
    super("Astilba Env contract definition is invalid.");
    this.name = "ContractDefinitionError";
    this.diagnostic = diagnostic("ENV_CONTRACT_INVALID");
  }
}

export type Failure<TDiagnostic extends CoreDiagnostic = CoreDiagnostic> =
  Readonly<{
    diagnostic: TDiagnostic;
    ok: false;
  }>;

export type Success<T> = Readonly<{
  ok: true;
  value: T;
}>;

export type Result<T> = Failure | Success<T>;

export type AggregateFailure<
  TDiagnostic extends CoreDiagnostic = CoreDiagnostic,
> = Readonly<{
  diagnostics: readonly TDiagnostic[];
  ok: false;
}>;

export type AggregateResult<T> = AggregateFailure | Success<T>;

export function failure(
  code: DiagnosticCode,
  fields?: DiagnosticFields
): Failure {
  return Object.freeze({
    diagnostic: diagnostic(code, fields),
    ok: false as const,
  });
}

export function success<T>(value: T): Success<T> {
  return Object.freeze({ ok: true as const, value });
}

export function aggregateFailure(
  diagnostics: readonly CoreDiagnostic[]
): AggregateFailure {
  return Object.freeze({
    diagnostics: Object.freeze([...diagnostics]),
    ok: false as const,
  });
}
