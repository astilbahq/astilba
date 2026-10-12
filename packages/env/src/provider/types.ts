import type { ConsumerProjectionManifest } from "../core/index.ts";
import type {
  Confidence,
  EntryLifecycle,
  ProviderBindingClass,
} from "../planning/types.ts";

export interface ProviderKindClassification {
  readonly class: ProviderBindingClass;
  readonly confidence: Confidence;
  readonly kind: string;
  readonly provider: "cloudflare-workers";
}

export interface ProviderBindingPlanEntry {
  readonly channel: EntryLifecycle;
  readonly class: ProviderBindingClass;
  readonly entry: string;
  readonly kind: string;
  readonly rawName: string;
}

export interface ProviderBindingPlan {
  readonly adapterAbi: string;
  readonly bindings: readonly ProviderBindingPlanEntry[];
  readonly format: "astilba.env.binding-plan/v1";
  readonly target: string;
}

export interface SecretBindingInventory {
  readonly bindings: readonly {
    readonly kind: "secret_text";
    readonly name: string;
  }[];
  readonly format: "astilba.env.binding-inventory/v1";
  readonly target: string;
}

export interface WranglerBindingMetadata {
  readonly bindings: readonly {
    readonly kind: "json" | "kv_namespace" | "plain_text";
    readonly name: string;
  }[];
  readonly format: "astilba.env.wrangler-metadata/v1";
  readonly source: "offline-jsonc";
}

type ProviderConformanceStatus =
  | "KIND_MISMATCH"
  | "MATCH"
  | "MISSING"
  | "UNVERIFIED";

export interface ProviderConformanceBinding {
  readonly class: ProviderBindingClass;
  readonly expectedKind: string;
  readonly name: string;
  readonly observedKind: string | null;
  readonly status: ProviderConformanceStatus;
}

export type ProviderConformanceIssueCode =
  | "DECLARED_CLASS_MISMATCH"
  | "DUPLICATE_BINDING_NAME"
  | "KIND_MISMATCH"
  | "MISSING_BINDING"
  | "SECRET_INVENTORY_UNVERIFIED"
  | "UNEXPECTED_BINDING"
  | "UNKNOWN_PROVIDER_KIND";

export interface ProviderConformanceIssue {
  readonly code: ProviderConformanceIssueCode;
  readonly name: string;
}

export type ProviderConformanceGrade =
  | "UNVERIFIED"
  | "checked-offline-configuration"
  | "synthetic-declared-inventory";

export interface ProviderConformanceReport {
  readonly bindings: readonly ProviderConformanceBinding[];
  readonly confidence: Confidence;
  readonly format: "astilba.env.provider-conformance/v1";
  readonly grade: ProviderConformanceGrade;
  readonly issues: readonly ProviderConformanceIssue[];
  readonly liveVerified: false;
  readonly pass: boolean;
  readonly target: string;
}

export interface CheckWranglerConformanceInput {
  readonly bindingPlan: ProviderBindingPlan;
  readonly projection: ConsumerProjectionManifest;
  readonly secretInventory?: unknown;
  readonly wranglerJsonc: string;
}
