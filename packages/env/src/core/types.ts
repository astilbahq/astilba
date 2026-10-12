import type {
  OpaqueInputShapeDescriptor,
  OpaqueShapeDescriptor,
  PortableShapeDescriptor,
} from "./shapes.ts";

export type { OpaqueShapeDescriptor } from "./shapes.ts";

export const CONTRACT_FORMAT = "astilba.env.contract" as const;
export const PROJECTION_FORMAT = "astilba.env.projection" as const;
export const FORMAT_VERSION = 1 as const;
export const CANONICALISATION_ABI = "astilba.jcs/v1" as const;
export const CODEC_ABI = "astilba.env.codec/v1" as const;
export const PROJECTION_ABI = "astilba.env.projection/v1" as const;

export const STRING_CODEC_ABI = "astilba.env.string-code-point/v1" as const;
export const ORIGIN_CODEC_ABI = "astilba.env.origin-ascii/v1" as const;
export const ENUM_CODEC_ABI = "astilba.env.enum/v1" as const;
export const BOOLEAN_CODEC_ABI = "astilba.env.boolean-exact/v1" as const;
export const SAFE_INTEGER_CODEC_ABI =
  "astilba.env.safe-integer-decimal/v1" as const;
export const STRING_LIST_CODEC_ABI =
  "astilba.env.string-list-comma/v1" as const;
export const JSON_CODEC_ABI = "astilba.env.json-exact/v1" as const;
export const OPAQUE_CODEC_ABI = "astilba.env.opaque/v1" as const;
export const TEXT_CODEC_ABI = "astilba.env.text/v1" as const;
export const INTEGER_CODEC_ABI = "astilba.env.integer/v1" as const;
export const PRESENT_TOGETHER_RULE_ABI =
  "astilba.env.present-together/v1" as const;
export const RULES_FORMAT_VERSION = 2 as const;

export type Visibility = "private" | "public";
export type Lifecycle = "build" | "deployment" | "request";
type ConsumerKind = "browser" | "server";

export type JsonScalar = boolean | null | number | string;
export interface JsonArray extends ReadonlyArray<JsonValue> {}
export interface JsonObject {
  readonly [key: string]: JsonValue;
}
export type JsonValue = JsonArray | JsonObject | JsonScalar;

export type EntryIdentity = readonly [fragmentId: string, localEntryId: string];

export type StringCodecDescriptor = Readonly<{
  abi: typeof STRING_CODEC_ABI;
  kind: "string";
  maxCodePoints: number;
  minCodePoints: number;
}>;

export type OriginCodecDescriptor = Readonly<{
  abi: typeof ORIGIN_CODEC_ABI;
  kind: "origin";
}>;

export type EnumCodecDescriptor = Readonly<{
  abi: typeof ENUM_CODEC_ABI;
  kind: "enum";
  values: readonly string[];
}>;

export type BooleanCodecDescriptor = Readonly<{
  abi: typeof BOOLEAN_CODEC_ABI;
  blank: "invalid" | "missing";
  falseInput: string;
  kind: "boolean";
  trueInput: string;
}>;

export type SafeIntegerCodecDescriptor = Readonly<{
  abi: typeof SAFE_INTEGER_CODEC_ABI;
  blank: "invalid" | "missing";
  kind: "safe-integer";
  maximum: number;
  minimum: number;
}>;

export type StringListCodecDescriptor = Readonly<{
  abi: typeof STRING_LIST_CODEC_ABI;
  emptyItems: "drop" | "invalid";
  kind: "string-list";
  maximumItemCodePoints: number;
  maximumItems: number;
  minimumItemCodePoints: number;
  minimumItems: number;
  separator: ",";
}>;

export type JsonCodecDescriptor<
  TShape extends PortableShapeDescriptor = PortableShapeDescriptor,
> = Readonly<{
  abi: typeof JSON_CODEC_ABI;
  blank: "invalid" | "missing";
  kind: "json";
  shape: TShape;
}>;

export type PortableCodecDescriptor =
  | EnumCodecDescriptor
  | OriginCodecDescriptor
  | StringCodecDescriptor;

export type PublicCodecDescriptor =
  | BooleanCodecDescriptor
  | JsonCodecDescriptor
  | PortableCodecDescriptor
  | SafeIntegerCodecDescriptor
  | StringListCodecDescriptor;

export type OpaqueCodecDescriptor<
  TInput extends OpaqueInputShapeDescriptor = OpaqueInputShapeDescriptor,
  TOutput extends OpaqueShapeDescriptor = OpaqueShapeDescriptor,
> = Readonly<{
  abi: typeof OPAQUE_CODEC_ABI;
  input: TInput;
  kind: "opaque";
  output: TOutput;
  revision: string;
  semantics: string;
}>;

export type TextCodecDescriptor = Readonly<{
  abi: typeof TEXT_CODEC_ABI;
  blank: "invalid" | "missing";
  kind: "text";
  maxCodePoints: number;
  minCodePoints: number;
  normalise: "preserve" | "trim";
}>;

export type IntegerCodecDescriptor = Readonly<{
  abi: typeof INTEGER_CODEC_ABI;
  blank: "invalid" | "missing";
  default: null;
  kind: "integer";
  maximum: number;
  minimum: number;
}>;

type ServerCodecDescriptor = IntegerCodecDescriptor | TextCodecDescriptor;

export type CodecDescriptor =
  | OpaqueCodecDescriptor
  | PublicCodecDescriptor
  | ServerCodecDescriptor;

type PresentTogetherRuleDefinition = Readonly<{
  abi: typeof PRESENT_TOGETHER_RULE_ABI;
  entries: readonly EntryIdentity[];
  id: string;
  kind: "present-together";
}>;

export type ContractRuleDefinition = PresentTogetherRuleDefinition;

export type EntryDefinition = Readonly<{
  codec: CodecDescriptor;
  fragment: string;
  id: string;
  lifecycle: Lifecycle;
  output?: string;
  required: boolean;
  visibility: Visibility;
}>;

export type ConsumerDefinition = Readonly<{
  entries: readonly EntryIdentity[];
  id: string;
  kind: ConsumerKind;
}>;

export type ContractDefinition = Readonly<{
  consumers: readonly ConsumerDefinition[];
  entries: readonly EntryDefinition[];
  id: string;
  rules?: readonly ContractRuleDefinition[];
}>;

export type FullEntryManifest = Readonly<{
  codec: CodecDescriptor;
  identity: EntryIdentity;
  lifecycle: Lifecycle;
  name: string;
  required: boolean;
  visibility: Visibility;
}>;

export type ConsumerSelectionManifest = Readonly<{
  entries: readonly EntryIdentity[];
  id: string;
  kind: ConsumerKind;
}>;

type FullContractManifestBase = Readonly<{
  canonicalisation: typeof CANONICALISATION_ABI;
  codecAbi: typeof CODEC_ABI;
  consumers: readonly ConsumerSelectionManifest[];
  contract: string;
  entries: readonly FullEntryManifest[];
  format: typeof CONTRACT_FORMAT;
  projectionAbi: typeof PROJECTION_ABI;
}>;

export type FullContractManifest =
  | (FullContractManifestBase &
      Readonly<{
        formatVersion: typeof FORMAT_VERSION;
      }>)
  | (FullContractManifestBase &
      Readonly<{
        formatVersion: typeof RULES_FORMAT_VERSION;
        rules: readonly ContractRuleDefinition[];
      }>);

export type BrowserProjectionEntry = Readonly<{
  codec: PublicCodecDescriptor;
  identity: EntryIdentity;
  lifecycle: Lifecycle;
  name: string;
  required: boolean;
}>;

export type ServerProjectionEntry = FullEntryManifest;

type ProjectionManifestBase = Readonly<{
  canonicalisation: typeof CANONICALISATION_ABI;
  codecAbi: typeof CODEC_ABI;
  consumer: string;
  contract: string;
  format: typeof PROJECTION_FORMAT;
  projectionAbi: typeof PROJECTION_ABI;
}>;

export type BrowserProjectionManifest = ProjectionManifestBase &
  Readonly<{
    entries: readonly BrowserProjectionEntry[];
    formatVersion: typeof FORMAT_VERSION;
    kind: "public";
  }>;

export type ServerProjectionManifest =
  | (ProjectionManifestBase &
      Readonly<{
        entries: readonly ServerProjectionEntry[];
        formatVersion: typeof FORMAT_VERSION;
        kind: "server";
      }>)
  | (ProjectionManifestBase &
      Readonly<{
        entries: readonly ServerProjectionEntry[];
        formatVersion: typeof RULES_FORMAT_VERSION;
        kind: "server";
        rules: readonly ContractRuleDefinition[];
      }>);

export type ConsumerProjectionManifest =
  | BrowserProjectionManifest
  | ServerProjectionManifest;

export type Sha256Digest = `sha256-${string}`;

export type CompiledManifest<TManifest> = Readonly<{
  bytes: Uint8Array;
  digest: Sha256Digest;
  manifest: TManifest;
  text: string;
}>;

export type CompiledProjection = CompiledManifest<ConsumerProjectionManifest>;

export type CompiledContract = Readonly<{
  full: CompiledManifest<FullContractManifest>;
  projections: readonly CompiledProjection[];
}>;

export type ResolutionBinding = Readonly<{
  entry: string;
  source: string;
}>;

export type ResolvedConfiguration = Readonly<Record<string, JsonValue>>;

export type Compatibility = "EQUAL" | "UNKNOWN" | "UNEQUAL";
