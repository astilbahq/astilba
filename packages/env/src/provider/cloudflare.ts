import type { ProviderKindClassification } from "./types.ts";

export function classifyCloudflareBindingKind(
  kind: string
): ProviderKindClassification {
  if (kind === "secret_text") {
    return Object.freeze({
      class: "confidential",
      confidence: "PROVEN",
      kind,
      provider: "cloudflare-workers",
    });
  }
  if (kind === "plain_text") {
    return Object.freeze({
      class: "non-confidential",
      confidence: "PROVEN",
      kind,
      provider: "cloudflare-workers",
    });
  }
  if (kind === "kv_namespace") {
    return Object.freeze({
      class: "capability",
      confidence: "PROVEN",
      kind,
      provider: "cloudflare-workers",
    });
  }
  return Object.freeze({
    class: "unknown",
    confidence: "UNKNOWN",
    kind,
    provider: "cloudflare-workers",
  });
}
