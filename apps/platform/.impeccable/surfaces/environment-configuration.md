---
version: 1
slug: "environment-configuration"
primary_target: "apps/platform/src/features/environments/environment-workspace.tsx"
related_targets:
  - "apps/platform/src/features/visual-editor.tsx"
  - "apps/platform/src/features/compose-editor.tsx"
  - "apps/platform/src/features/environments/environment.styles.ts"
---

# Environment Configuration

Visitor mode: Operate. Local refinement of Environment detail within the accepted Aligned stack, for hosted and self-hosted developers and small teams.

## Direction contract

THESIS: Put the saved revision, pending edits and next action in view before the editing work; separate inspecting history from resolving a real save conflict.

OWN-WORLD: Inherit neutral light/dark semantic tokens, system sans, open sections, separator-led history and shared Panda/Base UI controls. Code uses the existing monospace family.

STORY: Inspect or edit services, save a specification revision, then independently review deployment. Canvas positions stay a separate save. Comparisons preserve the working draft.

FIRST VIEWPORT: Below the existing environment tabs, a compact Configuration heading and revision context sit beside Save revision and Reload current. The editor follows with Canvas/Service list controls and a bounded work area. Secondary tools and revision history follow in document order. Controls wrap on mobile; comparisons stack.

FORM: Local extension of Overview-first grounded candidate7, inherited seed:5e9fe4ec54b1fb28. Signature interaction: inspect a historical revision beside the current editor text without changing the save base. Existing control transitions carry motion; no new entrance effect.

FINISH: unreviewed and undocumented is unfinished; this build ends with the finish review, the verdict, DESIGN.md, and every shipping raster carrying its provenance
