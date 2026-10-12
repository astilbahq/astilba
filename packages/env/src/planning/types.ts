import type {
  OpaqueShapeDescriptor,
  PortableShapeDescriptor,
} from "../core/shapes.ts";

export type Confidence = "PROVEN" | "UNKNOWN";

export type EntryIdentity = readonly [fragmentId: string, localEntryId: string];

export type EntryLifecycle = "build" | "deployment" | "request";

type EntryVisibility = "private" | "public";

export type ProviderBindingClass =
  | "capability"
  | "confidential"
  | "non-confidential"
  | "unknown";

export type DeclarativeCodecDescriptor =
  | {
      readonly abi: string;
      readonly blank: "invalid" | "missing";
      readonly falseInput: string;
      readonly kind: "boolean";
      readonly trueInput: string;
    }
  | {
      readonly abi: string;
      readonly kind: "enum";
      readonly values: readonly string[];
    }
  | {
      readonly abi: string;
      readonly kind: "origin";
    }
  | {
      readonly abi: string;
      readonly blank: "invalid" | "missing";
      readonly kind: "json";
      readonly shape: PortableShapeDescriptor;
    }
  | {
      readonly abi: string;
      readonly kind: "string";
      readonly maxCodePoints: number;
      readonly minCodePoints: number;
    }
  | {
      readonly abi: string;
      readonly blank: "invalid" | "missing";
      readonly kind: "safe-integer";
      readonly maximum: number;
      readonly minimum: number;
    }
  | {
      readonly abi: string;
      readonly emptyItems: "drop" | "invalid";
      readonly kind: "string-list";
      readonly maximumItemCodePoints: number;
      readonly maximumItems: number;
      readonly minimumItemCodePoints: number;
      readonly minimumItems: number;
      readonly separator: ",";
    }
  | {
      readonly abi: "astilba.env.opaque/v1";
      readonly input: OpaqueShapeDescriptor;
      readonly kind: "opaque";
      readonly output: OpaqueShapeDescriptor;
      readonly revision: string;
      readonly semantics: string;
    }
  | {
      readonly abi: string;
      readonly blank: "invalid" | "missing";
      readonly default: null;
      readonly kind: "integer";
      readonly maximum: number;
      readonly minimum: number;
    }
  | {
      readonly abi: string;
      readonly blank: "invalid" | "missing";
      readonly kind: "text";
      readonly maxCodePoints: number;
      readonly minCodePoints: number;
      readonly normalise: "preserve" | "trim";
    };

export interface LogicalEntryDescriptor {
  readonly codec: DeclarativeCodecDescriptor;
  readonly identity: EntryIdentity;
  readonly lifecycle: EntryLifecycle;
  readonly outputName: string;
  readonly required: boolean;
  readonly visibility: EntryVisibility;
}

export interface ConsumerDescriptor {
  readonly contract: string;
  readonly entries: readonly EntryIdentity[];
  readonly id: string;
  readonly projectionDigest: string;
  readonly projectionKind: "public" | "server";
}

export interface TargetBindingDescriptor {
  readonly channel: EntryLifecycle;
  readonly entry: EntryIdentity;
  readonly providerEntry: string;
  readonly providerClass: ProviderBindingClass;
  readonly providerKind: string;
  readonly rawName: string;
}

export interface TargetDescriptor {
  readonly adapterAbi: string;
  readonly bindings: readonly TargetBindingDescriptor[];
  readonly consumer: string;
  readonly id: string;
}

export interface PlanningSnapshot {
  readonly consumers: readonly ConsumerDescriptor[];
  readonly entries: readonly LogicalEntryDescriptor[];
  readonly format: "astilba.env.planning-snapshot/v1";
  readonly rules: readonly Readonly<{
    readonly abi: "astilba.env.present-together/v1";
    readonly entries: readonly EntryIdentity[];
    readonly id: string;
    readonly kind: "present-together";
  }>[];
  readonly targets: readonly TargetDescriptor[];
}

/**
 * The caller derives these identities while both configurations are available
 * ephemerally. Old values, new values, value fragments, lengths, and hashes
 * are deliberately outside the planning API.
 */
export interface ValueChange {
  readonly entry: EntryIdentity;
  readonly targets?: readonly string[];
}

/**
 * This marker exists only for a test harness or trusted caller that knows an
 * opaque implementation changed without its declared metadata changing.
 * Astilba cannot discover that violation itself.
 */
export interface OpaqueImplementationChange {
  readonly entry: EntryIdentity;
}

export interface ImpactPlanningInput {
  readonly after: PlanningSnapshot;
  readonly before: PlanningSnapshot;
  readonly opaqueImplementationChanges?: readonly OpaqueImplementationChange[];
  readonly valueChanges?: readonly ValueChange[];
}

export type ApplicationArtifactImpact =
  | "COORDINATED_ROLLOUT"
  | "NONE"
  | "REBUILD";

type AdapterArtifactImpact = "NONE" | "REBUILD";

export type ProvisioningImpact =
  | "ADD"
  | "RECONFIGURE"
  | "REMOVE_AFTER_ROLLOUT"
  | "REVALIDATE";

export interface ConsumerImpact {
  readonly adapterArtifact: AdapterArtifactImpact;
  readonly applicationArtifact: ApplicationArtifactImpact;
  readonly confidence: Confidence;
  readonly consumer: string;
  readonly provisioning: readonly ProvisioningImpact[];
  readonly security: readonly "REVIEW"[];
  readonly targets: readonly string[];
}

export type PlannedActionKind =
  | "ACTIVATE_ARTIFACT"
  | "ADD_CONFIGURATION"
  | "MANUAL_REVIEW"
  | "REBUILD_ADAPTER"
  | "REBUILD_APPLICATION"
  | "RECONFIGURE"
  | "REMOVE_CONFIGURATION"
  | "RETIRE_OLD_ARTIFACT"
  | "REVALIDATE"
  | "SECURITY_REVIEW";

export interface PlannedAction {
  readonly after: readonly string[];
  readonly confidence: Confidence;
  readonly consumer: string;
  readonly id: string;
  readonly kind: PlannedActionKind;
  readonly reasons: readonly string[];
  readonly target: string;
}

export interface ImpactPlan {
  readonly actions: readonly PlannedAction[];
  readonly consumers: readonly ConsumerImpact[];
  readonly format: "astilba.env.impact-plan/v1";
}
