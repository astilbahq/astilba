/** When a configuration value may be resolved. */
export type Lifecycle = "build" | "deployment" | "request";
// oxlint-disable-next-line sonarjs/redundant-type-aliases -- The frozen public declarations name contract identifiers explicitly.
type ContractId = string;
// oxlint-disable-next-line sonarjs/redundant-type-aliases -- The frozen public declarations name local identifiers explicitly.
/** Local contract identifier used for entries, consumers, and rules. */
export type LocalId = string;
// oxlint-disable-next-line sonarjs/redundant-type-aliases -- The frozen public declarations distinguish raw source names from local identifiers.
/** Property name read from an application-owned configuration source. */
export type RawSourceName = string;

/** Pair of the declaring fragment identifier and local entry identifier. */
export type EntryIdentity = readonly [fragmentId: string, localEntryId: string];

/** Portable JSON shape that can be represented in compiled contract metadata. */
export type PortableShape =
  | Readonly<{ kind: "boolean" }>
  | Readonly<{ kind: "null" }>
  | Readonly<{
      kind: "safe-integer";
      maximum: number;
      minimum: number;
    }>
  | Readonly<{ kind: "string" }>
  | Readonly<{
      items: PortableShape;
      kind: "array";
      maximumItems: number;
      minimumItems: number;
    }>
  | Readonly<{
      kind: "object";
      properties: readonly Readonly<{
        name: string;
        required: boolean;
        shape: PortableShape;
      }>[];
    }>;

/** Portable output shape for an application-owned private validator. */
export type OpaqueShape =
  | PortableShape
  | Readonly<{ kind: "optional"; value: PortableShape }>;

/** Input shape accepted before an opaque validator runs. */
export type OpaqueInputShape =
  | Readonly<{ kind: "string" }>
  | Readonly<{
      kind: "optional";
      value: Readonly<{ kind: "string" }>;
    }>;

/** Canonical descriptor for one generated entry codec. */
export type CodecDescriptor =
  | Readonly<{
      abi: "astilba.env.string-code-point/v1";
      kind: "string";
      maxCodePoints: number;
      minCodePoints: number;
    }>
  | Readonly<{
      abi: "astilba.env.origin-ascii/v1";
      kind: "origin";
    }>
  | Readonly<{
      abi: "astilba.env.enum/v1";
      kind: "enum";
      values: readonly string[];
    }>
  | Readonly<{
      abi: "astilba.env.boolean-exact/v1";
      blank: "invalid" | "missing";
      falseInput: string;
      kind: "boolean";
      trueInput: string;
    }>
  | Readonly<{
      abi: "astilba.env.safe-integer-decimal/v1";
      blank: "invalid" | "missing";
      kind: "safe-integer";
      maximum: number;
      minimum: number;
    }>
  | Readonly<{
      abi: "astilba.env.string-list-comma/v1";
      emptyItems: "drop" | "invalid";
      kind: "string-list";
      maximumItemCodePoints: number;
      maximumItems: number;
      minimumItemCodePoints: number;
      minimumItems: number;
      separator: ",";
    }>
  | Readonly<{
      abi: "astilba.env.json-exact/v1";
      blank: "invalid" | "missing";
      kind: "json";
      shape: PortableShape;
    }>
  | Readonly<{
      abi: "astilba.env.text/v1";
      blank: "invalid" | "missing";
      kind: "text";
      maxCodePoints: number;
      minCodePoints: number;
      normalise: "preserve" | "trim";
    }>
  | Readonly<{
      abi: "astilba.env.integer/v1";
      blank: "invalid" | "missing";
      default: null;
      kind: "integer";
      maximum: number;
      minimum: number;
    }>
  | Readonly<{
      abi: "astilba.env.opaque/v1";
      input: OpaqueInputShape;
      kind: "opaque";
      output: OpaqueShape;
      revision: string;
      semantics: string;
    }>;

type BrowserPortableCodecDescriptor = Exclude<
  CodecDescriptor,
  | Readonly<{ kind: "integer" }>
  | Readonly<{ kind: "opaque" }>
  | Readonly<{ kind: "text" }>
>;

/** Rule requiring all referenced entries to be present or absent together. */
export type PresentTogetherRule = Readonly<{
  /** Rule format identifier. */
  abi: "astilba.env.present-together/v1";
  /** Fully qualified identities of the entries governed by this rule. */
  entries: readonly EntryIdentity[];
  /** Local rule identifier. */
  id: LocalId;
  /** Discriminator for this rule form. */
  kind: "present-together";
}>;

/** One browser-safe entry selected by a public projection. */
export type PublicProjectionEntry = Readonly<{
  /** Portable codec used to decode the public source. */
  codec: BrowserPortableCodecDescriptor;
  /** Declaring fragment and entry identifiers. */
  identity: EntryIdentity;
  /** Permitted resolution lifecycle. */
  lifecycle: Lifecycle;
  /** Local entry identifier used in the resolved configuration. */
  name: LocalId;
  /** Whether a missing source is a configuration failure. */
  required: boolean;
}>;

/** One server entry selected by a server projection. */
export type ServerProjectionEntry = Readonly<{
  /** Codec used to decode or validate the source. */
  codec: CodecDescriptor;
  /** Declaring fragment and entry identifiers. */
  identity: EntryIdentity;
  /** Permitted resolution lifecycle. */
  lifecycle: Lifecycle;
  /** Local entry identifier used in the resolved configuration. */
  name: LocalId;
  /** Whether a missing source is a configuration failure. */
  required: boolean;
  /** Whether the entry is permitted in browser-safe projections. */
  visibility: "private" | "public";
}>;

type ProjectionBase = Readonly<{
  canonicalisation: "astilba.jcs/v1";
  codecAbi: "astilba.env.codec/v1";
  consumer: LocalId;
  contract: ContractId;
  format: "astilba.env.projection";
  projectionAbi: "astilba.env.projection/v1";
}>;

type PublicProjectionV1 = ProjectionBase &
  Readonly<{
    entries: readonly PublicProjectionEntry[];
    formatVersion: 1;
    kind: "public";
  }>;

type ServerProjectionV1 = ProjectionBase &
  Readonly<{
    entries: readonly ServerProjectionEntry[];
    formatVersion: 1;
    kind: "server";
  }>;

type ServerProjectionV2 = ProjectionBase &
  Readonly<{
    entries: readonly ServerProjectionEntry[];
    formatVersion: 2;
    kind: "server";
    rules: readonly PresentTogetherRule[];
  }>;

/** Compiled public or server projection accepted by process runtime helpers. */
export type ProcessProjection =
  | PublicProjectionV1
  | ServerProjectionV1
  | ServerProjectionV2;

/** Value-free reason produced when target validation or resolution fails. */
export type CoreDiagnostic =
  | Readonly<{
      code: "ENV_CONTRACT_INVALID" | "ENV_FORMAT_UNSUPPORTED";
    }>
  | Readonly<{
      code: "ENV_SOURCE_INVALID";
      consumer: LocalId;
    }>
  | Readonly<{
      code: "ENV_INVALID_VALUE" | "ENV_MISSING_VALUE";
      codec: CodecDescriptor["abi"];
      consumer: LocalId;
      entry: LocalId;
      lifecycle: Lifecycle;
    }>
  | Readonly<{
      code: "ENV_OPAQUE_UNSUPPORTED" | "ENV_VALIDATOR_ASYNC_UNSUPPORTED";
      codec: "astilba.env.opaque/v1";
      consumer: LocalId;
      entry: LocalId;
      lifecycle: "deployment" | "request";
    }>
  | Readonly<{
      code: "ENV_RULE_VIOLATION";
      consumer: LocalId;
      entries: readonly LocalId[];
      lifecycle: Lifecycle;
      rule: LocalId;
    }>;

/** Non-empty deterministic list of configuration diagnostics. */
export type CoreDiagnostics = readonly [CoreDiagnostic, ...CoreDiagnostic[]];

/** Failed configuration result containing value-free diagnostics. */
export type AggregateFailure = Readonly<{
  /** Non-empty validation or resolution failures. */
  diagnostics: CoreDiagnostics;
  /** Discriminator for a failed result. */
  ok: false;
}>;

/** Successful configuration result. */
export type Success<TValue> = Readonly<{
  /** Discriminator for a successful result. */
  ok: true;
  /** Resolved typed configuration. */
  value: TValue;
}>;

/** Non-throwing configuration outcome. */
export type AggregateResult<TValue> = AggregateFailure | Success<TValue>;

/** Frozen record of resolved application-owned configuration values. */
export type OwnedConfiguration = Readonly<Record<string, unknown>>;

/** Fully validated target representation used internally during resolution. */
export type NormalizedTarget = Readonly<{
  /** Entry-to-source bindings retained from generated target metadata. */
  bindings: readonly Readonly<{
    /** Local selected entry identifier. */
    entry: LocalId;
    /** Source property name. */
    source: RawSourceName;
  }>[];
  /** Generated-module format identifier. */
  generated: "astilba.env.generated-module/v1";
  /** Lifecycle carried by the generated target. */
  lifecycle: Lifecycle;
  /** Validated public or server projection. */
  projection: ProcessProjection;
  /** Entries selected after projection and binding validation. */
  selected: readonly (PublicProjectionEntry | ServerProjectionEntry)[];
}>;
