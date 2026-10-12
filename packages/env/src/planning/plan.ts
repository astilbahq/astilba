import type {
  CodecDescriptor,
  CompiledContract,
  FullEntryManifest,
} from "../core/index.ts";
import { isLocalId } from "../core/index.ts";
import { classifyCloudflareBindingKind } from "../provider/cloudflare.ts";
import type {
  ProviderBindingPlan,
  ProviderBindingPlanEntry,
} from "../provider/types.ts";
import type {
  ApplicationArtifactImpact,
  Confidence,
  ConsumerDescriptor,
  ConsumerImpact,
  DeclarativeCodecDescriptor,
  EntryIdentity,
  EntryLifecycle,
  ImpactPlan,
  ImpactPlanningInput,
  LogicalEntryDescriptor,
  OpaqueImplementationChange,
  PlannedAction,
  PlannedActionKind,
  PlanningSnapshot,
  ProviderBindingClass,
  ProvisioningImpact,
  TargetBindingDescriptor,
  TargetDescriptor,
  ValueChange,
} from "./types.ts";

const ACTION_ORDER: Readonly<Record<PlannedActionKind, number>> = {
  MANUAL_REVIEW: 0,
  SECURITY_REVIEW: 1,
  ADD_CONFIGURATION: 2,
  RECONFIGURE: 3,
  REBUILD_ADAPTER: 4,
  REBUILD_APPLICATION: 5,
  REVALIDATE: 6,
  ACTIVATE_ARTIFACT: 7,
  RETIRE_OLD_ARTIFACT: 8,
  REMOVE_CONFIGURATION: 9,
};
const MAXIMUM_CHANGE_MARKERS = 2048;

interface MutableAction {
  readonly confidence: Confidence;
  readonly consumer: string;
  readonly dependencies: Set<string>;
  readonly kind: PlannedActionKind;
  readonly reasons: Set<string>;
  readonly target: string;
}

interface TargetFlags {
  readonly add: Set<string>;
  readonly adapterRebuild: Set<string>;
  readonly manualReview: Set<string>;
  readonly reconfigure: Set<string>;
  readonly remove: Set<string>;
  readonly revalidate: Set<string>;
  readonly securityReview: Set<string>;
}

interface ConsumerFlags {
  applicationArtifact: ApplicationArtifactImpact;
  readonly applicationReasons: Set<string>;
  confidence: Confidence;
  readonly manualReview: Set<string>;
  readonly securityReview: Set<string>;
  readonly targets: Map<string, TargetFlags>;
}

interface IndexedSnapshot {
  readonly consumers: Map<string, ConsumerDescriptor>;
  readonly entries: Map<string, LogicalEntryDescriptor>;
  readonly targets: Map<string, TargetDescriptor>;
  readonly targetsByConsumer: Map<string, readonly TargetDescriptor[]>;
}

export class PlanningDefinitionError extends Error {
  public constructor(message: string) {
    super(message);
    this.name = "PlanningDefinitionError";
  }
}

export type PlanningSnapshotTarget = Readonly<{
  bindingPlan: ProviderBindingPlan;
  consumer: string;
}>;

export type CreatePlanningSnapshotInput = Readonly<{
  compiled: CompiledContract;
  targets: readonly PlanningSnapshotTarget[];
}>;

export function createPlanningSnapshot(
  input: CreatePlanningSnapshotInput
): PlanningSnapshot {
  const entries = input.compiled.full.manifest.entries
    .map(toPlanningEntry)
    .toSorted((left, right) =>
      compareText(identityKey(left.identity), identityKey(right.identity))
    );
  const entriesByIdentity = uniqueMap(
    entries,
    (entry) => identityKey(entry.identity),
    "compiled entry"
  );
  const projections = uniqueMap(
    input.compiled.projections,
    (projection) => projection.manifest.consumer,
    "compiled projection"
  );
  const consumers = input.compiled.full.manifest.consumers
    .map((selection): ConsumerDescriptor => {
      const projection = projections.get(selection.id);
      if (
        projection === undefined ||
        projection.manifest.contract !== input.compiled.full.manifest.contract
      ) {
        throw new PlanningDefinitionError(
          "compiled consumer projection is missing or belongs to another contract"
        );
      }
      const selectedKeys = sortedUnique(
        selection.entries.map((identity) => identityKey(identity))
      );
      const projectedKeys = sortedUnique(
        projection.manifest.entries.map((entry) => identityKey(entry.identity))
      );
      if (
        selectedKeys.length !== projectedKeys.length ||
        selectedKeys.some((key, index) => key !== projectedKeys[index])
      ) {
        throw new PlanningDefinitionError(
          "compiled projection differs from the full consumer selection"
        );
      }
      return Object.freeze({
        contract: projection.manifest.contract,
        entries: Object.freeze(
          projectedKeys.map((key) => {
            const entry = entriesByIdentity.get(key);
            if (entry === undefined) {
              throw new PlanningDefinitionError(
                "compiled projection references an unknown full entry"
              );
            }
            return entry.identity;
          })
        ),
        id: selection.id,
        projectionDigest: projection.digest,
        projectionKind: projection.manifest.kind,
      });
    })
    .toSorted((left, right) => compareText(left.id, right.id));
  if (projections.size !== consumers.length) {
    throw new PlanningDefinitionError(
      "compiled projections and consumers differ"
    );
  }

  const consumersById = uniqueMap(
    consumers,
    (consumer) => consumer.id,
    "compiled consumer"
  );
  const targets = input.targets
    .map((target) => {
      const consumer = consumersById.get(target.consumer);
      if (consumer === undefined) {
        throw new PlanningDefinitionError(
          "planning target references an unknown compiled consumer"
        );
      }
      return targetFromBindingPlan(
        target.bindingPlan,
        consumer,
        entriesByIdentity
      );
    })
    .toSorted((left, right) => compareText(left.id, right.id));
  uniqueMap(targets, (target) => target.id, "planning target");

  return Object.freeze({
    consumers: Object.freeze(consumers),
    entries: Object.freeze(entries),
    format: "astilba.env.planning-snapshot/v1" as const,
    rules: Object.freeze(
      (input.compiled.full.manifest.formatVersion === 2
        ? input.compiled.full.manifest.rules
        : []
      ).map((rule) =>
        Object.freeze({
          abi: rule.abi,
          entries: Object.freeze(
            rule.entries.map((identity) =>
              Object.freeze([identity[0], identity[1]] as const)
            )
          ),
          id: rule.id,
          kind: rule.kind,
        })
      )
    ),
    targets: Object.freeze(targets),
  });
}

export function planImpact(input: ImpactPlanningInput): ImpactPlan {
  try {
    return planImpactImplementation(input);
  } catch {
    // Every failure across this synchronous in-process seam is owned. In
    // particular, a nested DTO can pass indexing before a later comparison
    // triggers a caller-controlled proxy or forged error.
    throw new PlanningDefinitionError("planning marker input is invalid");
  }
}

function planImpactImplementation(input: ImpactPlanningInput): ImpactPlan {
  const captured = capturePlanningInput(input);
  const before = indexCapturedSnapshot(captured.before, "before");
  const after = indexCapturedSnapshot(captured.after, "after");
  const valueChanges = indexValueChanges(captured.valueChanges, before, after);
  const opaqueImplementationChanges = indexOpaqueImplementationChanges(
    captured.opaqueImplementationChanges,
    before,
    after
  );
  const consumerIds = sortedUnique([
    ...before.consumers.keys(),
    ...after.consumers.keys(),
  ]);
  const consumerFlags = new Map<string, ConsumerFlags>();

  for (const consumerId of consumerIds) {
    const flags = createConsumerFlags(before, after, consumerId);
    inspectConsumerChanges({
      after,
      before,
      consumerId,
      flags,
      opaqueImplementationChanges,
      valueChanges,
    });
    inspectTargetChanges(before, after, consumerId, flags);
    consumerFlags.set(consumerId, flags);
  }

  const consumers = consumerIds.map((consumerId) => {
    const flags = requireMapEntry(
      consumerFlags,
      consumerId,
      "planning consumer flags are missing"
    );
    return buildConsumerImpact(consumerId, flags);
  });
  const actions = buildActions(consumerIds, consumerFlags);

  return Object.freeze({
    actions: Object.freeze(actions),
    consumers: Object.freeze(consumers),
    format: "astilba.env.impact-plan/v1" as const,
  });
}

function inspectConsumerChanges(input: {
  readonly after: IndexedSnapshot;
  readonly before: IndexedSnapshot;
  readonly consumerId: string;
  readonly flags: ConsumerFlags;
  readonly opaqueImplementationChanges: ReadonlySet<string>;
  readonly valueChanges: ReadonlyMap<string, readonly string[] | null>;
}): void {
  const {
    after,
    before,
    consumerId,
    flags,
    opaqueImplementationChanges,
    valueChanges,
  } = input;
  const beforeConsumer = before.consumers.get(consumerId);
  const afterConsumer = after.consumers.get(consumerId);
  const beforeSelections = selectionKeys(beforeConsumer);
  const afterSelections = selectionKeys(afterConsumer);
  const selectedKeys = sortedUnique([...beforeSelections, ...afterSelections]);

  if (beforeConsumer === undefined && afterConsumer !== undefined) {
    requireApplication(flags, "REBUILD", "CONSUMER_ADDED");
  } else if (beforeConsumer !== undefined && afterConsumer === undefined) {
    requireApplication(flags, "COORDINATED_ROLLOUT", "CONSUMER_REMOVED");
  } else if (beforeConsumer !== undefined && afterConsumer !== undefined) {
    inspectConsumerIdentityChange(
      beforeConsumer,
      afterConsumer,
      before,
      after,
      flags
    );
  }

  for (const entryKey of selectedKeys) {
    const beforeSelected = beforeSelections.has(entryKey);
    const afterSelected = afterSelections.has(entryKey);
    const beforeEntry = before.entries.get(entryKey);
    const afterEntry = after.entries.get(entryKey);

    if (
      beforeEntry?.codec.kind === "opaque" ||
      afterEntry?.codec.kind === "opaque"
    ) {
      flags.confidence = "UNKNOWN";
    }

    if (
      opaqueImplementationChanges.has(entryKey) &&
      (beforeSelected || afterSelected)
    ) {
      flags.confidence = "UNKNOWN";
      flags.manualReview.add("OPAQUE_IMPLEMENTATION_CHANGED_WITHOUT_METADATA");
    }

    if (!beforeSelected && afterSelected) {
      if (afterEntry?.codec.kind === "opaque") {
        flags.manualReview.add("OPAQUE_CODEC_SEMANTICS");
      }
      requireApplication(
        flags,
        "REBUILD",
        afterEntry?.required === true
          ? "SELECTED_REQUIRED_ENTRY_ADDED"
          : "SELECTED_OPTIONAL_ENTRY_ADDED"
      );
      if (afterEntry?.required === true) {
        for (const target of targetsSelecting(after, consumerId, entryKey)) {
          targetFlags(flags, target.id).add.add(
            "SELECTED_REQUIRED_ENTRY_ADDED"
          );
        }
      }
      continue;
    }

    if (beforeSelected && !afterSelected) {
      if (beforeEntry?.codec.kind === "opaque") {
        flags.manualReview.add("OPAQUE_CODEC_SEMANTICS");
      }
      requireApplication(
        flags,
        "COORDINATED_ROLLOUT",
        "SELECTED_ENTRY_REMOVED"
      );
      for (const target of targetsSelecting(before, consumerId, entryKey)) {
        targetFlags(flags, target.id).remove.add("SELECTED_ENTRY_REMOVED");
      }
      continue;
    }

    if (
      !beforeSelected ||
      !afterSelected ||
      beforeEntry === undefined ||
      afterEntry === undefined
    ) {
      continue;
    }

    inspectEntryDescriptorChange(
      beforeEntry,
      afterEntry,
      flags,
      before,
      after,
      consumerId
    );
    inspectValueChange(
      afterEntry,
      valueChanges.get(entryKey),
      flags,
      after,
      consumerId
    );
  }
}

function inspectConsumerIdentityChange(
  beforeConsumer: ConsumerDescriptor,
  afterConsumer: ConsumerDescriptor,
  before: IndexedSnapshot,
  after: IndexedSnapshot,
  flags: ConsumerFlags
): void {
  const targets = [
    ...(before.targetsByConsumer.get(beforeConsumer.id) ?? []),
    ...(after.targetsByConsumer.get(afterConsumer.id) ?? []),
  ];
  const uniqueTargets = new Map(targets.map((target) => [target.id, target]));

  if (beforeConsumer.contract !== afterConsumer.contract) {
    requireApplication(flags, "COORDINATED_ROLLOUT", "CONTRACT_ID_CHANGED");
    for (const target of uniqueTargets.values()) {
      const changes = targetFlags(flags, target.id);
      changes.reconfigure.add("CONTRACT_ID_CHANGED");
      changes.revalidate.add("CONTRACT_ID_CHANGED");
    }
  }

  if (beforeConsumer.projectionKind !== afterConsumer.projectionKind) {
    requireApplication(flags, "COORDINATED_ROLLOUT", "PROJECTION_KIND_CHANGED");
    flags.securityReview.add("PROJECTION_KIND_CHANGED");
    for (const target of uniqueTargets.values()) {
      const changes = targetFlags(flags, target.id);
      changes.adapterRebuild.add("PROJECTION_KIND_CHANGED");
      changes.reconfigure.add("PROJECTION_KIND_CHANGED");
      changes.revalidate.add("PROJECTION_KIND_CHANGED");
    }
  }

  if (beforeConsumer.projectionDigest !== afterConsumer.projectionDigest) {
    requireApplication(flags, "REBUILD", "PROJECTION_DIGEST_CHANGED");
    for (const target of uniqueTargets.values()) {
      targetFlags(flags, target.id).revalidate.add("PROJECTION_DIGEST_CHANGED");
    }
  }
}

function inspectEntryDescriptorChange(
  before: LogicalEntryDescriptor,
  after: LogicalEntryDescriptor,
  flags: ConsumerFlags,
  beforeSnapshot: IndexedSnapshot,
  afterSnapshot: IndexedSnapshot,
  consumerId: string
): void {
  if (before.outputName !== after.outputName) {
    requireApplication(flags, "COORDINATED_ROLLOUT", "OUTPUT_NAME_CHANGED");
  }

  if (before.visibility !== after.visibility) {
    requireApplication(
      flags,
      before.visibility === "public" && after.visibility === "private"
        ? "COORDINATED_ROLLOUT"
        : "REBUILD",
      "VISIBILITY_CHANGED"
    );
    flags.securityReview.add(
      before.visibility === "private" && after.visibility === "public"
        ? "PRIVATE_ENTRY_BECAME_PUBLIC"
        : "PUBLIC_ENTRY_BECAME_PRIVATE"
    );
  }

  if (before.lifecycle !== after.lifecycle) {
    inspectLifecycleChange(
      before,
      after,
      flags,
      beforeSnapshot,
      afterSnapshot,
      consumerId
    );
  }

  if (before.required !== after.required) {
    requireApplication(flags, "REBUILD", "REQUIREDNESS_CHANGED");
    if (!before.required && after.required) {
      for (const target of targetsSelecting(
        afterSnapshot,
        consumerId,
        identityKey(after.identity)
      )) {
        const targetChange = targetFlags(flags, target.id);
        targetChange.add.add("OPTIONAL_ENTRY_BECAME_REQUIRED");
        targetChange.revalidate.add("OPTIONAL_ENTRY_BECAME_REQUIRED");
      }
    }
  }

  const codecChange = compareCodecs(before.codec, after.codec);
  if (codecChange === "EQUAL") {
    return;
  }

  requireApplication(flags, "REBUILD", "CODEC_CHANGED");
  if (codecChange === "UNKNOWN") {
    flags.confidence = "UNKNOWN";
    flags.manualReview.add("OPAQUE_CODEC_SEMANTICS");
  }
  if (codecChange !== "WIDENED") {
    for (const target of targetsSelecting(
      afterSnapshot,
      consumerId,
      identityKey(after.identity)
    )) {
      targetFlags(flags, target.id).revalidate.add(
        codecChange === "UNKNOWN" ? "OPAQUE_CODEC_SEMANTICS" : "CODEC_NARROWED"
      );
    }
  }
}

function inspectLifecycleChange(
  before: LogicalEntryDescriptor,
  after: LogicalEntryDescriptor,
  flags: ConsumerFlags,
  beforeSnapshot: IndexedSnapshot,
  afterSnapshot: IndexedSnapshot,
  consumerId: string
): void {
  const entryKey = identityKey(after.identity);
  const previousTargets = targetsSelecting(
    beforeSnapshot,
    consumerId,
    entryKey
  );
  const nextTargets = targetsSelecting(afterSnapshot, consumerId, entryKey);
  const transition = `${before.lifecycle}:${after.lifecycle}`;

  switch (transition) {
    case "build:deployment": {
      requireApplication(flags, "REBUILD", "BUILD_TO_DEPLOYMENT");
      prepareNewLifecycle(flags, nextTargets, "BUILD_TO_DEPLOYMENT", true);
      return;
    }
    case "build:request": {
      requireApplication(flags, "COORDINATED_ROLLOUT", "BUILD_TO_REQUEST");
      flags.securityReview.add("REQUEST_CACHE_ISOLATION_REVIEW");
      prepareNewLifecycle(flags, nextTargets, "BUILD_TO_REQUEST", true);
      return;
    }
    case "deployment:build": {
      requireApplication(flags, "COORDINATED_ROLLOUT", "DEPLOYMENT_TO_BUILD");
      flags.securityReview.add("VALUE_FREEZING_REVIEW");
      retireRuntimeLifecycle(flags, previousTargets, "DEPLOYMENT_TO_BUILD");
      return;
    }
    case "deployment:request": {
      requireApplication(flags, "COORDINATED_ROLLOUT", "DEPLOYMENT_TO_REQUEST");
      flags.securityReview.add("REQUEST_CACHE_ISOLATION_REVIEW");
      reconfigureLifecycle(flags, nextTargets, "DEPLOYMENT_TO_REQUEST");
      return;
    }
    case "request:deployment": {
      requireApplication(flags, "COORDINATED_ROLLOUT", "REQUEST_TO_DEPLOYMENT");
      flags.securityReview.add("DEPLOYMENT_SCOPE_REVIEW");
      prepareNewLifecycle(flags, nextTargets, "REQUEST_TO_DEPLOYMENT", true);
      return;
    }
    case "request:build": {
      requireApplication(flags, "COORDINATED_ROLLOUT", "REQUEST_TO_BUILD");
      flags.securityReview.add("VALUE_FREEZING_REVIEW");
      retireRuntimeLifecycle(flags, previousTargets, "REQUEST_TO_BUILD");
      return;
    }
    default: {
      flags.confidence = "UNKNOWN";
      flags.manualReview.add("UNKNOWN_LIFECYCLE_TRANSITION");
    }
  }
}

function prepareNewLifecycle(
  flags: ConsumerFlags,
  targets: readonly TargetDescriptor[],
  reason: string,
  revalidate: boolean
): void {
  for (const target of targets) {
    const changes = targetFlags(flags, target.id);
    changes.add.add(reason);
    changes.adapterRebuild.add(reason);
    if (revalidate) {
      changes.revalidate.add(reason);
    }
  }
}

function retireRuntimeLifecycle(
  flags: ConsumerFlags,
  targets: readonly TargetDescriptor[],
  reason: string
): void {
  for (const target of targets) {
    const changes = targetFlags(flags, target.id);
    changes.adapterRebuild.add(reason);
    changes.remove.add(reason);
  }
}

function reconfigureLifecycle(
  flags: ConsumerFlags,
  targets: readonly TargetDescriptor[],
  reason: string
): void {
  for (const target of targets) {
    const changes = targetFlags(flags, target.id);
    changes.adapterRebuild.add(reason);
    changes.reconfigure.add(reason);
    changes.revalidate.add(reason);
  }
}

function inspectValueChange(
  entry: LogicalEntryDescriptor,
  changedTargets: readonly string[] | null | undefined,
  flags: ConsumerFlags,
  after: IndexedSnapshot,
  consumerId: string
): void {
  if (changedTargets === undefined) {
    return;
  }

  if (entry.lifecycle === "build") {
    requireApplication(flags, "REBUILD", "BUILD_VALUE_CHANGED");
    return;
  }

  const allowedTargets =
    changedTargets === null ? null : new Set(changedTargets);
  for (const target of targetsSelecting(
    after,
    consumerId,
    identityKey(entry.identity)
  )) {
    if (allowedTargets === null || allowedTargets.has(target.id)) {
      targetFlags(flags, target.id).reconfigure.add(
        entry.lifecycle === "deployment"
          ? "DEPLOYMENT_VALUE_CHANGED"
          : "REQUEST_VALUE_CHANGED"
      );
    }
  }
}

function inspectTargetChanges(
  before: IndexedSnapshot,
  after: IndexedSnapshot,
  consumerId: string,
  flags: ConsumerFlags
): void {
  const beforeTargets = new Map(
    (before.targetsByConsumer.get(consumerId) ?? []).map((target) => [
      target.id,
      target,
    ])
  );
  const afterTargets = new Map(
    (after.targetsByConsumer.get(consumerId) ?? []).map((target) => [
      target.id,
      target,
    ])
  );
  const targetIds = sortedUnique([
    ...beforeTargets.keys(),
    ...afterTargets.keys(),
  ]);

  for (const targetId of targetIds) {
    const beforeTarget = beforeTargets.get(targetId);
    const afterTarget = afterTargets.get(targetId);
    const targetChange = targetFlags(flags, targetId);

    if (beforeTarget === undefined && afterTarget !== undefined) {
      targetChange.add.add("TARGET_ADDED");
      targetChange.adapterRebuild.add("TARGET_ADDED");
      requireUnknownProviderReview(afterTarget, flags, targetChange);
      continue;
    }

    if (beforeTarget !== undefined && afterTarget === undefined) {
      targetChange.remove.add("TARGET_REMOVED");
      requireApplication(flags, "COORDINATED_ROLLOUT", "TARGET_REMOVED");
      requireUnknownProviderReview(beforeTarget, flags, targetChange);
      continue;
    }

    if (beforeTarget === undefined || afterTarget === undefined) {
      continue;
    }

    if (beforeTarget.adapterAbi !== afterTarget.adapterAbi) {
      targetChange.adapterRebuild.add("ADAPTER_ABI_CHANGED");
      targetChange.revalidate.add("ADAPTER_ABI_CHANGED");
    }

    inspectBindingChanges(beforeTarget, afterTarget, flags, targetChange);
  }
}

function inspectBindingChanges(
  beforeTarget: TargetDescriptor,
  afterTarget: TargetDescriptor,
  flags: ConsumerFlags,
  targetChange: TargetFlags
): void {
  const beforeBindings = bindingMap(beforeTarget);
  const afterBindings = bindingMap(afterTarget);
  const bindingKeys = sortedUnique([
    ...beforeBindings.keys(),
    ...afterBindings.keys(),
  ]);

  for (const entryKey of bindingKeys) {
    const beforeBinding = beforeBindings.get(entryKey);
    const afterBinding = afterBindings.get(entryKey);

    if (beforeBinding === undefined && afterBinding !== undefined) {
      targetChange.add.add("TARGET_BINDING_ADDED");
      requireUnknownProviderReviewForBinding(
        afterTarget,
        afterBinding,
        flags,
        targetChange
      );
      continue;
    }
    if (beforeBinding !== undefined && afterBinding === undefined) {
      targetChange.remove.add("TARGET_BINDING_REMOVED");
      requireUnknownProviderReviewForBinding(
        beforeTarget,
        beforeBinding,
        flags,
        targetChange
      );
      continue;
    }
    if (beforeBinding === undefined || afterBinding === undefined) {
      continue;
    }

    const rawNameChanged = beforeBinding.rawName !== afterBinding.rawName;
    const channelChanged = beforeBinding.channel !== afterBinding.channel;
    const adapterAbiChanged =
      beforeTarget.adapterAbi !== afterTarget.adapterAbi;
    const providerEntryChanged =
      beforeBinding.providerEntry !== afterBinding.providerEntry;
    const beforeProviderClass = authoritativeProviderClass(
      beforeTarget.adapterAbi,
      beforeBinding.providerKind,
      beforeBinding.channel
    );
    const afterProviderClass = authoritativeProviderClass(
      afterTarget.adapterAbi,
      afterBinding.providerKind,
      afterBinding.channel
    );
    const providerKindChanged =
      beforeBinding.providerKind !== afterBinding.providerKind ||
      beforeProviderClass !== afterProviderClass;
    const hasUnknownClass =
      beforeProviderClass === "unknown" || afterProviderClass === "unknown";

    if (hasUnknownClass) {
      flags.confidence = "UNKNOWN";
      if (
        rawNameChanged ||
        channelChanged ||
        adapterAbiChanged ||
        providerEntryChanged ||
        providerKindChanged
      ) {
        targetChange.manualReview.add("UNKNOWN_PROVIDER_BINDING_CLASS");
        targetChange.securityReview.add("UNKNOWN_PROVIDER_BINDING_CLASS");
      }
    }

    if (rawNameChanged) {
      targetChange.reconfigure.add("BINDING_SOURCE_CHANGED");
      targetChange.adapterRebuild.add("BINDING_SOURCE_CHANGED");
    }

    if (providerEntryChanged) {
      targetChange.reconfigure.add("BINDING_PROVIDER_ENTRY_CHANGED");
      targetChange.revalidate.add("BINDING_PROVIDER_ENTRY_CHANGED");
      targetChange.adapterRebuild.add("BINDING_PROVIDER_ENTRY_CHANGED");
    }

    if (providerKindChanged) {
      targetChange.reconfigure.add("PROVIDER_BINDING_KIND_CHANGED");
      targetChange.revalidate.add("PROVIDER_BINDING_KIND_CHANGED");

      if (
        beforeProviderClass === "confidential" &&
        afterProviderClass !== "confidential"
      ) {
        targetChange.securityReview.add("CONFIDENTIALITY_DOWNGRADE");
      }
    }
  }
}

function requireUnknownProviderReview(
  target: TargetDescriptor,
  flags: ConsumerFlags,
  targetChange: TargetFlags
): void {
  for (const binding of target.bindings) {
    requireUnknownProviderReviewForBinding(
      target,
      binding,
      flags,
      targetChange
    );
  }
}

function requireUnknownProviderReviewForBinding(
  target: TargetDescriptor,
  binding: TargetBindingDescriptor,
  flags: ConsumerFlags,
  targetChange: TargetFlags
): void {
  if (
    authoritativeProviderClass(
      target.adapterAbi,
      binding.providerKind,
      binding.channel
    ) !== "unknown"
  ) {
    return;
  }
  flags.confidence = "UNKNOWN";
  targetChange.manualReview.add("UNKNOWN_PROVIDER_BINDING_CLASS");
  targetChange.securityReview.add("UNKNOWN_PROVIDER_BINDING_CLASS");
}

function authoritativeProviderClass(
  adapterAbi: string,
  providerKind: string,
  channel: EntryLifecycle
): ProviderBindingClass {
  if (
    providerKind === "public_text" &&
    (adapterAbi === "astilba.env.adapter.json-bootstrap/v1" ||
      adapterAbi === "astilba.env.adapter.process-record/v1" ||
      (adapterAbi === "astilba.env.adapter.cloudflare-workers/v1" &&
        channel === "build"))
  ) {
    return "non-confidential";
  }
  if (
    adapterAbi === "astilba.env.adapter.process-record/v1" &&
    providerKind === "private_text"
  ) {
    return "confidential";
  }
  if (adapterAbi === "astilba.env.adapter.cloudflare-workers/v1") {
    return classifyCloudflareBindingKind(providerKind).class;
  }
  return "unknown";
}

function buildConsumerImpact(
  consumer: string,
  flags: ConsumerFlags
): ConsumerImpact {
  const targets = [...flags.targets.keys()].toSorted(compareText);
  const provisioning = new Set<ProvisioningImpact>();
  let adapterArtifact: "NONE" | "REBUILD" = "NONE";
  let hasSecurityReview = flags.securityReview.size > 0;

  for (const target of flags.targets.values()) {
    if (target.add.size > 0) {
      provisioning.add("ADD");
    }
    if (target.reconfigure.size > 0) {
      provisioning.add("RECONFIGURE");
    }
    if (target.revalidate.size > 0) {
      provisioning.add("REVALIDATE");
    }
    if (target.remove.size > 0) {
      provisioning.add("REMOVE_AFTER_ROLLOUT");
    }
    if (target.adapterRebuild.size > 0) {
      adapterArtifact = "REBUILD";
    }
    hasSecurityReview ||= target.securityReview.size > 0;
  }

  const provisioningOrder: readonly ProvisioningImpact[] = [
    "ADD",
    "RECONFIGURE",
    "REVALIDATE",
    "REMOVE_AFTER_ROLLOUT",
  ];
  const security: readonly "REVIEW"[] = hasSecurityReview ? ["REVIEW"] : [];

  return Object.freeze({
    adapterArtifact,
    applicationArtifact: flags.applicationArtifact,
    confidence: flags.confidence,
    consumer,
    provisioning: Object.freeze(
      provisioningOrder.filter((item) => provisioning.has(item))
    ),
    security: Object.freeze(security),
    targets: Object.freeze(targets),
  });
}

function buildActions(
  consumerIds: readonly string[],
  allFlags: ReadonlyMap<string, ConsumerFlags>
): PlannedAction[] {
  const mutableActions = new Map<string, MutableAction>();

  for (const consumer of consumerIds) {
    const flags = requireMapEntry(
      allFlags,
      consumer,
      "planning consumer flags are missing"
    );
    const targetIds = [...flags.targets.keys()].toSorted(compareText);
    const actionTargets = targetIds.length === 0 ? ["*"] : targetIds;

    for (const target of actionTargets) {
      const perTarget = flags.targets.get(target);
      addActionIfNeeded(
        mutableActions,
        consumer,
        target,
        "MANUAL_REVIEW",
        unionSets(flags.manualReview, perTarget?.manualReview),
        flags.confidence
      );
      addActionIfNeeded(
        mutableActions,
        consumer,
        target,
        "SECURITY_REVIEW",
        unionSets(flags.securityReview, perTarget?.securityReview),
        flags.confidence
      );
      if (perTarget !== undefined) {
        addActionIfNeeded(
          mutableActions,
          consumer,
          target,
          "ADD_CONFIGURATION",
          perTarget.add,
          flags.confidence
        );
        addActionIfNeeded(
          mutableActions,
          consumer,
          target,
          "RECONFIGURE",
          perTarget.reconfigure,
          flags.confidence
        );
        addActionIfNeeded(
          mutableActions,
          consumer,
          target,
          "REBUILD_ADAPTER",
          perTarget.adapterRebuild,
          flags.confidence
        );
        addActionIfNeeded(
          mutableActions,
          consumer,
          target,
          "REVALIDATE",
          perTarget.revalidate,
          flags.confidence
        );
        addActionIfNeeded(
          mutableActions,
          consumer,
          target,
          "REMOVE_CONFIGURATION",
          perTarget.remove,
          flags.confidence
        );
      }
    }

    addActionIfNeeded(
      mutableActions,
      consumer,
      "*",
      "REBUILD_APPLICATION",
      flags.applicationReasons,
      flags.confidence
    );

    const needsActivation =
      flags.applicationArtifact !== "NONE" ||
      [...flags.targets.values()].some(
        (target) => target.adapterRebuild.size > 0
      );
    if (needsActivation) {
      for (const target of actionTargets) {
        addActionIfNeeded(
          mutableActions,
          consumer,
          target,
          "ACTIVATE_ARTIFACT",
          new Set(["UPDATED_ARTIFACT_READY"]),
          flags.confidence
        );
      }
    }

    if (flags.applicationArtifact === "COORDINATED_ROLLOUT") {
      for (const target of actionTargets) {
        addActionIfNeeded(
          mutableActions,
          consumer,
          target,
          "RETIRE_OLD_ARTIFACT",
          new Set(["COORDINATED_ROLLOUT_REQUIRED"]),
          flags.confidence
        );
      }
    }
  }

  addDependencies(mutableActions);

  return [...mutableActions.entries()]
    .toSorted((left, right) => compareActions(left[1], right[1]))
    .map(([id, action]) =>
      Object.freeze({
        after: Object.freeze([...action.dependencies].toSorted(compareText)),
        confidence: action.confidence,
        consumer: action.consumer,
        id,
        kind: action.kind,
        reasons: Object.freeze([...action.reasons].toSorted(compareText)),
        target: action.target,
      })
    );
}

function addDependencies(actions: Map<string, MutableAction>): void {
  for (const action of actions.values()) {
    const reviewIds = [
      actionId(action.consumer, action.target, "MANUAL_REVIEW"),
      actionId(action.consumer, action.target, "SECURITY_REVIEW"),
    ].filter((id) => actions.has(id));

    if (
      action.kind === "ADD_CONFIGURATION" ||
      action.kind === "RECONFIGURE" ||
      action.kind === "REBUILD_ADAPTER" ||
      action.kind === "REBUILD_APPLICATION"
    ) {
      for (const reviewId of reviewIds) {
        action.dependencies.add(reviewId);
      }
    }

    if (action.kind === "REVALIDATE") {
      for (const kind of [
        "MANUAL_REVIEW",
        "SECURITY_REVIEW",
        "ADD_CONFIGURATION",
        "RECONFIGURE",
        "REBUILD_ADAPTER",
      ] as const) {
        addDependencyIfPresent(
          actions,
          action,
          action.consumer,
          action.target,
          kind
        );
      }
    }

    if (action.kind === "ACTIVATE_ARTIFACT") {
      for (const kind of [
        "MANUAL_REVIEW",
        "SECURITY_REVIEW",
        "ADD_CONFIGURATION",
        "RECONFIGURE",
        "REBUILD_ADAPTER",
        "REVALIDATE",
      ] as const) {
        addDependencyIfPresent(
          actions,
          action,
          action.consumer,
          action.target,
          kind
        );
      }
      addDependencyIfPresent(
        actions,
        action,
        action.consumer,
        "*",
        "REBUILD_APPLICATION"
      );
    }

    if (action.kind === "RETIRE_OLD_ARTIFACT") {
      addDependencyIfPresent(
        actions,
        action,
        action.consumer,
        action.target,
        "ACTIVATE_ARTIFACT"
      );
    }

    if (action.kind === "REMOVE_CONFIGURATION") {
      for (const reviewId of reviewIds) {
        action.dependencies.add(reviewId);
      }
      const retireId = actionId(
        action.consumer,
        action.target,
        "RETIRE_OLD_ARTIFACT"
      );
      const activateId = actionId(
        action.consumer,
        action.target,
        "ACTIVATE_ARTIFACT"
      );
      if (actions.has(retireId)) {
        action.dependencies.add(retireId);
      } else if (actions.has(activateId)) {
        action.dependencies.add(activateId);
      }
    }
  }
}

function addDependencyIfPresent(
  actions: ReadonlyMap<string, MutableAction>,
  action: MutableAction,
  consumer: string,
  target: string,
  kind: PlannedActionKind
): void {
  const dependencyId = actionId(consumer, target, kind);
  if (actions.has(dependencyId)) {
    action.dependencies.add(dependencyId);
  }
}

function addActionIfNeeded(
  actions: Map<string, MutableAction>,
  consumer: string,
  target: string,
  kind: PlannedActionKind,
  reasons: ReadonlySet<string>,
  confidence: Confidence
): void {
  if (reasons.size === 0) {
    return;
  }

  const id = actionId(consumer, target, kind);
  const existing = actions.get(id);
  if (existing === undefined) {
    actions.set(id, {
      confidence,
      consumer,
      dependencies: new Set(),
      kind,
      reasons: new Set(reasons),
      target,
    });
    return;
  }

  for (const reason of reasons) {
    existing.reasons.add(reason);
  }
}

function createConsumerFlags(
  before: IndexedSnapshot,
  after: IndexedSnapshot,
  consumerId: string
): ConsumerFlags {
  const targetIds = sortedUnique([
    ...(before.targetsByConsumer.get(consumerId) ?? []).map(({ id }) => id),
    ...(after.targetsByConsumer.get(consumerId) ?? []).map(({ id }) => id),
  ]);
  return {
    applicationArtifact: "NONE",
    applicationReasons: new Set(),
    confidence: "PROVEN",
    manualReview: new Set(),
    securityReview: new Set(),
    targets: new Map(
      targetIds.map((targetId) => [targetId, createTargetFlags()])
    ),
  };
}

function createTargetFlags(): TargetFlags {
  return {
    add: new Set(),
    adapterRebuild: new Set(),
    manualReview: new Set(),
    reconfigure: new Set(),
    remove: new Set(),
    revalidate: new Set(),
    securityReview: new Set(),
  };
}

function targetFlags(flags: ConsumerFlags, targetId: string): TargetFlags {
  const existing = flags.targets.get(targetId);
  if (existing !== undefined) {
    return existing;
  }
  const created = createTargetFlags();
  flags.targets.set(targetId, created);
  return created;
}

function requireApplication(
  flags: ConsumerFlags,
  impact: Exclude<ApplicationArtifactImpact, "NONE">,
  reason: string
): void {
  if (
    impact === "COORDINATED_ROLLOUT" ||
    flags.applicationArtifact === "NONE"
  ) {
    flags.applicationArtifact = impact;
  }
  flags.applicationReasons.add(reason);
}

function compareCodecs(
  before: DeclarativeCodecDescriptor,
  after: DeclarativeCodecDescriptor
): "EQUAL" | "NARROWED" | "UNKNOWN" | "WIDENED" {
  if (before.kind === "opaque" && after.kind === "opaque") {
    return stableOpaqueMetadataBytes(before) ===
      stableOpaqueMetadataBytes(after)
      ? "EQUAL"
      : "UNKNOWN";
  }
  if (before.kind === "opaque" || after.kind === "opaque") {
    return "UNKNOWN";
  }

  if (stableCodecBytes(before) === stableCodecBytes(after)) {
    return "EQUAL";
  }

  if (
    before.kind === "string" &&
    after.kind === "string" &&
    before.abi === after.abi &&
    after.minCodePoints <= before.minCodePoints &&
    after.maxCodePoints >= before.maxCodePoints
  ) {
    return "WIDENED";
  }

  if (
    before.kind === "text" &&
    after.kind === "text" &&
    before.abi === after.abi &&
    before.blank === after.blank &&
    before.normalise === after.normalise &&
    after.minCodePoints <= before.minCodePoints &&
    after.maxCodePoints >= before.maxCodePoints
  ) {
    return "WIDENED";
  }

  if (
    before.kind === "integer" &&
    after.kind === "integer" &&
    before.abi === after.abi &&
    before.blank === after.blank &&
    before.default === after.default &&
    after.minimum <= before.minimum &&
    after.maximum >= before.maximum
  ) {
    return "WIDENED";
  }

  if (
    before.kind === "safe-integer" &&
    after.kind === "safe-integer" &&
    before.abi === after.abi &&
    before.blank === after.blank &&
    after.minimum <= before.minimum &&
    after.maximum >= before.maximum
  ) {
    return "WIDENED";
  }

  if (
    before.kind === "enum" &&
    after.kind === "enum" &&
    before.abi === after.abi
  ) {
    const afterValues = new Set(after.values);
    if (before.values.every((value) => afterValues.has(value))) {
      return "WIDENED";
    }
  }

  return "NARROWED";
}

function stableOpaqueMetadataBytes(
  codec: Extract<DeclarativeCodecDescriptor, { kind: "opaque" }>
): string {
  return JSON.stringify({
    abi: codec.abi,
    input: codec.input,
    kind: codec.kind,
    output: codec.output,
    revision: codec.revision,
    semantics: codec.semantics,
  });
}

function stableCodecBytes(
  codec: Exclude<DeclarativeCodecDescriptor, { kind: "opaque" }>
): string {
  if (codec.kind === "boolean") {
    return JSON.stringify({
      abi: codec.abi,
      blank: codec.blank,
      falseInput: codec.falseInput,
      kind: codec.kind,
      trueInput: codec.trueInput,
    });
  }
  if (codec.kind === "enum") {
    return JSON.stringify({
      abi: codec.abi,
      kind: codec.kind,
      values: [...codec.values].toSorted(compareText),
    });
  }
  if (codec.kind === "origin") {
    return JSON.stringify({ abi: codec.abi, kind: codec.kind });
  }
  if (codec.kind === "integer") {
    return JSON.stringify({
      abi: codec.abi,
      blank: codec.blank,
      default: codec.default,
      kind: codec.kind,
      maximum: codec.maximum,
      minimum: codec.minimum,
    });
  }
  if (codec.kind === "json") {
    return JSON.stringify({
      abi: codec.abi,
      blank: codec.blank,
      kind: codec.kind,
      shape: codec.shape,
    });
  }
  if (codec.kind === "safe-integer") {
    return JSON.stringify({
      abi: codec.abi,
      blank: codec.blank,
      kind: codec.kind,
      maximum: codec.maximum,
      minimum: codec.minimum,
    });
  }
  if (codec.kind === "string-list") {
    return JSON.stringify({
      abi: codec.abi,
      emptyItems: codec.emptyItems,
      kind: codec.kind,
      maximumItemCodePoints: codec.maximumItemCodePoints,
      maximumItems: codec.maximumItems,
      minimumItemCodePoints: codec.minimumItemCodePoints,
      minimumItems: codec.minimumItems,
      separator: codec.separator,
    });
  }
  if (codec.kind === "text") {
    return JSON.stringify({
      abi: codec.abi,
      blank: codec.blank,
      kind: codec.kind,
      maxCodePoints: codec.maxCodePoints,
      minCodePoints: codec.minCodePoints,
      normalise: codec.normalise,
    });
  }
  return JSON.stringify({
    abi: codec.abi,
    kind: codec.kind,
    maxCodePoints: codec.maxCodePoints,
    minCodePoints: codec.minCodePoints,
  });
}

function indexCapturedSnapshot(
  snapshot: PlanningSnapshot,
  label: "after" | "before"
): IndexedSnapshot {
  try {
    return indexSnapshot(snapshot, label);
  } catch {
    // Snapshots cross the same hostile marker seam as value markers. A proxy
    // observation failure must not leak a caller-controlled error identity.
    throw new PlanningDefinitionError("planning marker input is invalid");
  }
}

function indexSnapshot(
  snapshot: PlanningSnapshot,
  label: "after" | "before"
): IndexedSnapshot {
  requireExactDataFields(snapshot, [
    "consumers",
    "entries",
    "format",
    "rules",
    "targets",
  ]);
  if (
    snapshot.format !== "astilba.env.planning-snapshot/v1" ||
    !isArrayValue(snapshot.consumers) ||
    !isArrayValue(snapshot.entries) ||
    !isArrayValue(snapshot.rules) ||
    !isArrayValue(snapshot.targets)
  ) {
    throw new PlanningDefinitionError(`${label} snapshot is invalid`);
  }
  const entries = uniqueMap(
    snapshot.entries,
    (entry) => identityKey(entry.identity),
    `${label} entry`
  );
  const consumers = uniqueMap(
    snapshot.consumers,
    ({ id }) => id,
    `${label} consumer`
  );
  const targets = uniqueMap(
    snapshot.targets,
    ({ id }) => id,
    `${label} target`
  );
  const targetsByConsumer = new Map<string, TargetDescriptor[]>();

  for (const consumer of consumers.values()) {
    if (
      !isNonEmptyString(consumer.contract) ||
      !isNonEmptyString(consumer.projectionDigest) ||
      (consumer.projectionKind !== "public" &&
        consumer.projectionKind !== "server")
    ) {
      throw new PlanningDefinitionError(
        `${label} consumer projection metadata is invalid`
      );
    }
    const selections = new Set<string>();
    for (const identity of consumer.entries) {
      const key = identityKey(identity);
      if (!entries.has(key)) {
        throw new PlanningDefinitionError(
          `${label} consumer references an unknown entry`
        );
      }
      if (selections.has(key)) {
        throw new PlanningDefinitionError(
          `${label} consumer selects one entry more than once`
        );
      }
      selections.add(key);
    }
  }

  for (const target of targets.values()) {
    const targetConsumer = consumers.get(target.consumer);
    if (targetConsumer === undefined) {
      throw new PlanningDefinitionError(
        `${label} target references an unknown consumer`
      );
    }
    const selectedEntries = selectionKeys(targetConsumer);
    const targetBindings = bindingMap(target);
    for (const [entryKey, targetBinding] of targetBindings) {
      if (
        !isNonEmptyString(targetBinding.providerEntry) ||
        !isNonEmptyString(targetBinding.providerKind) ||
        !isNonEmptyString(targetBinding.rawName) ||
        (targetBinding.providerClass !== "capability" &&
          targetBinding.providerClass !== "confidential" &&
          targetBinding.providerClass !== "non-confidential" &&
          targetBinding.providerClass !== "unknown")
      ) {
        throw new PlanningDefinitionError(
          `${label} target binding metadata is invalid`
        );
      }
      const entry = entries.get(entryKey);
      if (entry === undefined) {
        throw new PlanningDefinitionError(
          `${label} target binding references an unknown entry`
        );
      }
      if (!selectedEntries.has(entryKey)) {
        throw new PlanningDefinitionError(
          `${label} target binds an entry its consumer does not select`
        );
      }
      if (targetBinding.channel !== entry.lifecycle) {
        throw new PlanningDefinitionError(
          `${label} target binding channel differs from entry lifecycle`
        );
      }
    }
    const processChannel =
      target.adapterAbi === "astilba.env.adapter.process-record/v1"
        ? target.bindings[0]?.channel
        : undefined;
    for (const entryKey of selectedEntries) {
      const entry = entries.get(entryKey);
      if (
        entry?.required === true &&
        (processChannel === undefined || entry.lifecycle === processChannel) &&
        !targetBindings.has(entryKey)
      ) {
        throw new PlanningDefinitionError(
          `${label} target omits a required selected entry binding`
        );
      }
    }
    const consumerTargets = targetsByConsumer.get(target.consumer) ?? [];
    consumerTargets.push(target);
    targetsByConsumer.set(target.consumer, consumerTargets);
  }

  for (const [consumer, consumerTargets] of targetsByConsumer) {
    targetsByConsumer.set(
      consumer,
      consumerTargets.toSorted((left, right) => compareText(left.id, right.id))
    );
  }

  return { consumers, entries, targets, targetsByConsumer };
}

function indexValueChanges(
  changes: readonly ValueChange[],
  before: IndexedSnapshot,
  after: IndexedSnapshot
): ReadonlyMap<string, readonly string[] | null> {
  const indexed = new Map<string, readonly string[] | null>();
  for (const change of normalizeValueChanges(changes)) {
    const key = identityKey(change.entry);
    const beforeEntry = before.entries.get(key);
    const afterEntry = after.entries.get(key);
    if (beforeEntry === undefined || afterEntry === undefined) {
      throw new PlanningDefinitionError(
        "value change references an unknown entry"
      );
    }
    if (beforeEntry.lifecycle !== afterEntry.lifecycle) {
      throw new PlanningDefinitionError("value change lifecycle differs");
    }
    if (afterEntry.lifecycle === "build" && change.targets !== undefined) {
      throw new PlanningDefinitionError(
        "build value change must not select targets"
      );
    }
    if (change.targets !== undefined) {
      if (change.targets.length === 0) {
        throw new PlanningDefinitionError(
          "value change target selection is empty"
        );
      }
      let previousTarget: string | undefined;
      for (const targetId of change.targets) {
        if (
          !isLocalId(targetId) ||
          (previousTarget !== undefined &&
            compareText(previousTarget, targetId) >= 0)
        ) {
          throw new PlanningDefinitionError(
            "value change targets must be valid, sorted, and unique"
          );
        }
        const target = after.targets.get(targetId);
        if (target === undefined || !bindingMap(target).has(key)) {
          throw new PlanningDefinitionError(
            "value change target does not bind the selected entry"
          );
        }
        previousTarget = targetId;
      }
    }
    const existing = indexed.get(key);
    if (existing === null || change.targets === undefined) {
      indexed.set(key, null);
    } else {
      indexed.set(
        key,
        Object.freeze(sortedUnique([...(existing ?? []), ...change.targets]))
      );
    }
  }
  return indexed;
}

function indexOpaqueImplementationChanges(
  changes: readonly OpaqueImplementationChange[],
  before: IndexedSnapshot,
  after: IndexedSnapshot
): ReadonlySet<string> {
  const indexed = new Set<string>();
  for (const change of normalizeOpaqueImplementationChanges(changes)) {
    const key = identityKey(change.entry);
    const beforeEntry = before.entries.get(key);
    const afterEntry = after.entries.get(key);
    if (beforeEntry === undefined || afterEntry === undefined) {
      throw new PlanningDefinitionError(
        "opaque implementation change references an unknown entry"
      );
    }
    if (
      beforeEntry.codec.kind !== "opaque" ||
      afterEntry.codec.kind !== "opaque"
    ) {
      throw new PlanningDefinitionError(
        "opaque implementation change references a declarative codec"
      );
    }
    if (
      stableOpaqueMetadataBytes(beforeEntry.codec) !==
      stableOpaqueMetadataBytes(afterEntry.codec)
    ) {
      throw new PlanningDefinitionError(
        "opaque implementation change metadata differs"
      );
    }
    indexed.add(key);
  }
  return indexed;
}

function normalizeValueChanges(
  changes: readonly ValueChange[]
): readonly ValueChange[] {
  return exactMarkerArray(changes).map((value) => {
    const record = exactMarkerRecord(value, ["entry"], ["targets"]);
    const entry = exactMarkerIdentity(record.entry);
    if (record.targets === undefined) {
      return Object.freeze({ entry });
    }
    const targets = exactMarkerArray(record.targets).map((target) => {
      if (typeof target !== "string") {
        throw new PlanningDefinitionError("value change target is invalid");
      }
      return target;
    });
    return Object.freeze({ entry, targets: Object.freeze(targets) });
  });
}

function normalizeOpaqueImplementationChanges(
  changes: readonly OpaqueImplementationChange[]
): readonly OpaqueImplementationChange[] {
  return exactMarkerArray(changes).map((value) => {
    const record = exactMarkerRecord(value, ["entry"]);
    return Object.freeze({ entry: exactMarkerIdentity(record.entry) });
  });
}

function exactMarkerArray(value: unknown): readonly unknown[] {
  if (!Array.isArray(value)) {
    throw new PlanningDefinitionError("planning markers must use dense arrays");
  }
  const prototype = Reflect.getPrototypeOf(value);
  if (prototype !== Array.prototype) {
    throw new PlanningDefinitionError("planning markers must use dense arrays");
  }

  const ownKeys = Reflect.ownKeys(value);
  const keys: string[] = [];
  for (const key of ownKeys) {
    if (typeof key !== "string") {
      throw new PlanningDefinitionError(
        "planning markers must use dense arrays"
      );
    }
    keys.push(key);
  }
  const sortedKeys = keys.toSorted(compareText);
  const descriptors = new Map<string, PropertyDescriptor>();
  for (const key of sortedKeys) {
    const descriptor = Object.getOwnPropertyDescriptor(value, key);
    if (descriptor === undefined) {
      throw new PlanningDefinitionError(
        "planning markers must use dense arrays"
      );
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
    lengthDescriptor.value > MAXIMUM_CHANGE_MARKERS ||
    sortedKeys.length !== lengthDescriptor.value + 1
  ) {
    throw new PlanningDefinitionError("planning markers must use dense arrays");
  }

  const normalized: unknown[] = [];
  for (let index = 0; index < lengthDescriptor.value; index += 1) {
    const descriptor = descriptors.get(String(index));
    if (
      descriptor === undefined ||
      !("value" in descriptor) ||
      descriptor.enumerable !== true
    ) {
      throw new PlanningDefinitionError(
        "planning markers must use dense arrays"
      );
    }
    normalized.push(descriptor.value);
  }
  return Object.freeze(normalized);
}

function exactMarkerRecord(
  value: unknown,
  required: readonly string[],
  optional: readonly string[] = []
): Record<string, unknown> {
  if (typeof value !== "object" || value === null) {
    throw new PlanningDefinitionError("planning marker is invalid");
  }
  const prototype = Reflect.getPrototypeOf(value);
  if (prototype !== Object.prototype && prototype !== null) {
    throw new PlanningDefinitionError("planning marker is invalid");
  }

  const ownKeys = Reflect.ownKeys(value);
  const keys: string[] = [];
  for (const key of ownKeys) {
    if (typeof key !== "string") {
      throw new PlanningDefinitionError(
        "planning marker has unexpected fields"
      );
    }
    keys.push(key);
  }
  const sortedKeys = keys.toSorted(compareText);
  const descriptors = new Map<string, PropertyDescriptor>();
  for (const key of sortedKeys) {
    const descriptor = Object.getOwnPropertyDescriptor(value, key);
    if (descriptor === undefined) {
      throw new PlanningDefinitionError(
        "planning marker fields must be enumerable data properties"
      );
    }
    descriptors.set(key, descriptor);
  }

  if (
    sortedKeys.length < required.length ||
    sortedKeys.length > required.length + optional.length ||
    sortedKeys.some(
      (key) => !required.includes(key) && !optional.includes(key)
    ) ||
    required.some((key) => !descriptors.has(key))
  ) {
    throw new PlanningDefinitionError("planning marker has unexpected fields");
  }

  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- A null-prototype object is required so descriptor-copied hostile keys cannot inherit behavior.
  const normalized = Object.create(null) as Record<string, unknown>;
  for (const key of sortedKeys) {
    const descriptor = descriptors.get(key);
    if (
      descriptor === undefined ||
      !("value" in descriptor) ||
      descriptor.enumerable !== true
    ) {
      throw new PlanningDefinitionError(
        "planning marker fields must be enumerable data properties"
      );
    }
    normalized[key] = descriptor.value;
  }
  return Object.freeze(normalized);
}

function capturePlanningInput(input: ImpactPlanningInput): Readonly<{
  after: PlanningSnapshot;
  before: PlanningSnapshot;
  opaqueImplementationChanges: readonly OpaqueImplementationChange[];
  valueChanges: readonly ValueChange[];
}> {
  try {
    const record = exactMarkerRecord(
      input,
      ["after", "before"],
      ["opaqueImplementationChanges", "valueChanges"]
    );
    const opaque = record.opaqueImplementationChanges;
    const values = record.valueChanges;
    return Object.freeze({
      // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- Exact descriptor-only snapshot capture owns this untrusted boundary before indexed validation.
      after: capturePlanningSnapshot(
        record.after
      ) as unknown as PlanningSnapshot,
      // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- Exact descriptor-only snapshot capture owns this untrusted boundary before indexed validation.
      before: capturePlanningSnapshot(
        record.before
      ) as unknown as PlanningSnapshot,
      opaqueImplementationChanges:
        opaque === undefined
          ? Object.freeze([])
          : normalizeOpaqueImplementationChanges(
              // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- The exact marker-record descriptor supplies the optional opaque marker list to its owning normalizer.
              opaque as readonly OpaqueImplementationChange[]
            ),
      valueChanges:
        values === undefined
          ? Object.freeze([])
          : normalizeValueChanges(
              // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- The exact marker-record descriptor supplies the optional value marker list to its owning normalizer.
              values as readonly ValueChange[]
            ),
    });
  } catch {
    // Marker input is an untrusted in-process seam. Reflection, proxy, and
    // accessor failures are planning-definition failures, never caller errors.
    throw new PlanningDefinitionError("planning marker input is invalid");
  }
}

function capturePlanningSnapshot(value: unknown): Record<string, unknown> {
  return exactMarkerRecord(value, [
    "consumers",
    "entries",
    "format",
    "rules",
    "targets",
  ]);
}

function exactMarkerIdentity(value: unknown): EntryIdentity {
  const tuple = exactMarkerArray(value);
  if (
    tuple.length !== 2 ||
    typeof tuple[0] !== "string" ||
    typeof tuple[1] !== "string"
  ) {
    throw new PlanningDefinitionError("entry identity must be a string tuple");
  }
  return Object.freeze([tuple[0], tuple[1]] as const);
}

function toPlanningEntry(entry: FullEntryManifest): LogicalEntryDescriptor {
  return Object.freeze({
    codec: toPlanningCodec(entry.codec),
    identity: Object.freeze([entry.identity[0], entry.identity[1]] as const),
    lifecycle: entry.lifecycle,
    outputName: entry.name,
    required: entry.required,
    visibility: entry.visibility,
  });
}

function toPlanningCodec(codec: CodecDescriptor): DeclarativeCodecDescriptor {
  switch (codec.kind) {
    case "boolean": {
      return Object.freeze({
        abi: codec.abi,
        blank: codec.blank,
        falseInput: codec.falseInput,
        kind: codec.kind,
        trueInput: codec.trueInput,
      });
    }
    case "enum": {
      return Object.freeze({
        abi: codec.abi,
        kind: codec.kind,
        values: Object.freeze([...codec.values]),
      });
    }
    case "integer": {
      return Object.freeze({
        abi: codec.abi,
        blank: codec.blank,
        default: codec.default,
        kind: codec.kind,
        maximum: codec.maximum,
        minimum: codec.minimum,
      });
    }
    case "json": {
      return Object.freeze({
        abi: codec.abi,
        blank: codec.blank,
        kind: codec.kind,
        shape: codec.shape,
      });
    }
    case "opaque": {
      return Object.freeze({
        abi: codec.abi,
        input: codec.input,
        kind: codec.kind,
        output: codec.output,
        revision: codec.revision,
        semantics: codec.semantics,
      });
    }
    case "origin": {
      return Object.freeze({
        abi: codec.abi,
        kind: codec.kind,
      });
    }
    case "safe-integer": {
      return Object.freeze({
        abi: codec.abi,
        blank: codec.blank,
        kind: codec.kind,
        maximum: codec.maximum,
        minimum: codec.minimum,
      });
    }
    case "string": {
      return Object.freeze({
        abi: codec.abi,
        kind: codec.kind,
        maxCodePoints: codec.maxCodePoints,
        minCodePoints: codec.minCodePoints,
      });
    }
    case "text": {
      return Object.freeze({
        abi: codec.abi,
        blank: codec.blank,
        kind: codec.kind,
        maxCodePoints: codec.maxCodePoints,
        minCodePoints: codec.minCodePoints,
        normalise: codec.normalise,
      });
    }
    case "string-list": {
      return Object.freeze({
        abi: codec.abi,
        emptyItems: codec.emptyItems,
        kind: codec.kind,
        maximumItemCodePoints: codec.maximumItemCodePoints,
        maximumItems: codec.maximumItems,
        minimumItemCodePoints: codec.minimumItemCodePoints,
        minimumItems: codec.minimumItems,
        separator: codec.separator,
      });
    }
  }
}

function targetFromBindingPlan(
  plan: ProviderBindingPlan,
  consumer: ConsumerDescriptor,
  entriesByIdentity: ReadonlyMap<string, LogicalEntryDescriptor>
): TargetDescriptor {
  validateProviderBindingPlan(plan);
  const isProcessPlan =
    plan.adapterAbi === "astilba.env.adapter.process-record/v1";
  const processChannel = isProcessPlan ? plan.bindings[0]?.channel : undefined;
  if (isProcessPlan && processChannel === undefined) {
    throw new PlanningDefinitionError("provider binding plan is invalid");
  }
  const selectedEntries = consumer.entries.map((identity) => {
    const entry = entriesByIdentity.get(identityKey(identity));
    if (entry === undefined) {
      throw new PlanningDefinitionError(
        "compiled consumer selects an unknown entry"
      );
    }
    return entry;
  });
  const entriesByOutputName = uniqueMap(
    selectedEntries,
    (entry) => entry.outputName,
    `consumer ${consumer.id} output name`
  );
  const mappedEntries = new Set<string>();
  const bindings: TargetBindingDescriptor[] = [];
  const unmatchedCapabilities = new Map<
    EntryLifecycle,
    ProviderBindingPlanEntry
  >();

  const addBinding = (
    source: ProviderBindingPlanEntry,
    entry: LogicalEntryDescriptor
  ): void => {
    if (source.channel !== entry.lifecycle) {
      throw new PlanningDefinitionError(
        "provider binding channel differs from logical entry lifecycle"
      );
    }
    if (isProcessPlan) {
      const expectedKind =
        entry.visibility === "private" ? "private_text" : "public_text";
      const expectedClass =
        entry.visibility === "private" ? "confidential" : "non-confidential";
      if (source.kind !== expectedKind || source.class !== expectedClass) {
        throw new PlanningDefinitionError("provider binding plan is invalid");
      }
    }
    const key = identityKey(entry.identity);
    if (mappedEntries.has(key)) {
      throw new PlanningDefinitionError(
        "provider binding maps one logical entry more than once"
      );
    }
    mappedEntries.add(key);
    bindings.push(
      Object.freeze({
        channel: source.channel,
        entry: entry.identity,
        providerClass: authoritativeProviderClass(
          plan.adapterAbi,
          source.kind,
          source.channel
        ),
        providerEntry: source.entry,
        providerKind: source.kind,
        rawName: source.rawName,
      })
    );
  };

  for (const source of [...plan.bindings].toSorted(compareProviderBindings)) {
    const directEntry = entriesByOutputName.get(source.entry);
    if (directEntry !== undefined) {
      addBinding(source, directEntry);
      continue;
    }
    if (
      authoritativeProviderClass(
        plan.adapterAbi,
        source.kind,
        source.channel
      ) !== "capability"
    ) {
      throw new PlanningDefinitionError(
        "provider binding does not name a selected logical entry"
      );
    }
    if (unmatchedCapabilities.has(source.channel)) {
      throw new PlanningDefinitionError(
        "multiple provider capabilities ambiguously cover one lifecycle"
      );
    }
    unmatchedCapabilities.set(source.channel, source);
  }

  for (const [channel, source] of unmatchedCapabilities) {
    const coveredEntries = selectedEntries.filter(
      (entry) =>
        entry.lifecycle === channel &&
        !mappedEntries.has(identityKey(entry.identity))
    );
    if (coveredEntries.length === 0) {
      throw new PlanningDefinitionError(
        "provider capability does not cover any unbound selected entry"
      );
    }
    for (const entry of coveredEntries) {
      addBinding(source, entry);
    }
  }

  const requiredCoverage = isProcessPlan
    ? selectedEntries.filter(
        (entry) => entry.required && entry.lifecycle === processChannel
      )
    : selectedEntries.filter((entry) => entry.required);
  for (const entry of requiredCoverage) {
    if (!mappedEntries.has(identityKey(entry.identity))) {
      throw new PlanningDefinitionError(
        "provider binding plan omits a required selected entry"
      );
    }
  }

  bindings.sort(
    (left, right) =>
      compareText(identityKey(left.entry), identityKey(right.entry)) ||
      compareText(left.providerEntry, right.providerEntry) ||
      compareText(left.rawName, right.rawName)
  );
  return Object.freeze({
    adapterAbi: plan.adapterAbi,
    bindings: Object.freeze(bindings),
    consumer: consumer.id,
    id: plan.target,
  });
}

function validateProviderBindingPlan(plan: ProviderBindingPlan): void {
  requireExactDataFields(plan, ["adapterAbi", "bindings", "format", "target"]);
  if (
    plan.format !== "astilba.env.binding-plan/v1" ||
    (plan.adapterAbi !== "astilba.env.adapter.process-record/v1" &&
      plan.adapterAbi !== "astilba.env.adapter.cloudflare-workers/v1") ||
    !isNonEmptyString(plan.target) ||
    !isArrayValue(plan.bindings) ||
    plan.bindings.length === 0
  ) {
    throw new PlanningDefinitionError("provider binding plan is invalid");
  }

  const providerEntries = new Set<string>();
  let processChannel: EntryLifecycle | undefined;
  for (const binding of plan.bindings) {
    requireExactDataFields(binding, [
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
      (binding.class !== "capability" &&
        binding.class !== "confidential" &&
        binding.class !== "non-confidential" &&
        binding.class !== "unknown") ||
      !isNonEmptyString(binding.entry) ||
      !isNonEmptyString(binding.kind) ||
      !isNonEmptyString(binding.rawName) ||
      providerEntries.has(binding.entry)
    ) {
      throw new PlanningDefinitionError("provider binding plan is invalid");
    }
    if (plan.adapterAbi === "astilba.env.adapter.process-record/v1") {
      processChannel ??= binding.channel;
      if (binding.channel !== processChannel) {
        throw new PlanningDefinitionError("provider binding plan is invalid");
      }
    }
    providerEntries.add(binding.entry);
  }
}

function requireExactDataFields(
  value: unknown,
  expected: readonly string[]
): void {
  if (
    typeof value !== "object" ||
    value === null ||
    Array.isArray(value) ||
    (Object.getPrototypeOf(value) !== Object.prototype &&
      Object.getPrototypeOf(value) !== null)
  ) {
    throw new PlanningDefinitionError("planning input must use plain records");
  }
  const keys = Reflect.ownKeys(value);
  if (
    keys.length !== expected.length ||
    keys.some((key) => typeof key !== "string" || !expected.includes(key))
  ) {
    throw new PlanningDefinitionError("planning input has unexpected fields");
  }
  for (const key of expected) {
    const descriptor = Object.getOwnPropertyDescriptor(value, key);
    if (
      descriptor === undefined ||
      !("value" in descriptor) ||
      descriptor.enumerable !== true
    ) {
      throw new PlanningDefinitionError(
        "planning input fields must be enumerable data properties"
      );
    }
  }
}

function isArrayValue(value: unknown): boolean {
  return Array.isArray(value);
}

function requireMapEntry<T>(
  map: ReadonlyMap<string, T>,
  key: string,
  message: string
): T {
  const value = map.get(key);
  if (value === undefined) {
    throw new PlanningDefinitionError(message);
  }
  return value;
}

function isNonEmptyString(value: unknown): value is string {
  return typeof value === "string" && value.length > 0;
}

function compareProviderBindings(
  left: ProviderBindingPlanEntry,
  right: ProviderBindingPlanEntry
): number {
  return (
    compareText(left.channel, right.channel) ||
    compareText(left.entry, right.entry) ||
    compareText(left.kind, right.kind) ||
    compareText(left.rawName, right.rawName)
  );
}

function bindingMap(
  target: TargetDescriptor
): Map<string, TargetBindingDescriptor> {
  return uniqueMap(
    target.bindings,
    (binding) => identityKey(binding.entry),
    `target ${target.id} binding`
  );
}

function targetsSelecting(
  snapshot: IndexedSnapshot,
  consumerId: string,
  entryKey: string
): readonly TargetDescriptor[] {
  return (snapshot.targetsByConsumer.get(consumerId) ?? []).filter((target) =>
    bindingMap(target).has(entryKey)
  );
}

function selectionKeys(
  consumer: ConsumerDescriptor | undefined
): ReadonlySet<string> {
  if (consumer === undefined) {
    return new Set();
  }
  return new Set(consumer.entries.map((identity) => identityKey(identity)));
}

function uniqueMap<T>(
  values: readonly T[],
  keyOf: (value: T) => string,
  label: string
): Map<string, T> {
  const result = new Map<string, T>();
  for (const value of values) {
    const key = keyOf(value);
    if (result.has(key)) {
      throw new PlanningDefinitionError(`${label} is duplicated`);
    }
    result.set(key, value);
  }
  return result;
}

function identityKey(identity: EntryIdentity): string {
  if (
    !Array.isArray(identity) ||
    identity.length !== 2 ||
    typeof identity[0] !== "string" ||
    typeof identity[1] !== "string"
  ) {
    throw new PlanningDefinitionError("entry identity must be a string tuple");
  }
  return JSON.stringify([identity[0], identity[1]]);
}

function actionId(
  consumer: string,
  target: string,
  kind: PlannedActionKind
): string {
  return `${kind.toLowerCase().replaceAll("_", "-")}:${encodeURIComponent(consumer)}:${encodeURIComponent(target)}`;
}

function compareActions(left: MutableAction, right: MutableAction): number {
  return (
    ACTION_ORDER[left.kind] - ACTION_ORDER[right.kind] ||
    compareText(left.consumer, right.consumer) ||
    compareText(left.target, right.target) ||
    compareText(left.kind, right.kind)
  );
}

function compareText(left: string, right: string): number {
  return left < right ? -1 : left > right ? 1 : 0;
}

function sortedUnique(values: Iterable<string>): string[] {
  return [...new Set(values)].toSorted(compareText);
}

function unionSets(
  left: ReadonlySet<string>,
  right: ReadonlySet<string> | undefined
): ReadonlySet<string> {
  return new Set([...(left ?? []), ...(right ?? [])]);
}
