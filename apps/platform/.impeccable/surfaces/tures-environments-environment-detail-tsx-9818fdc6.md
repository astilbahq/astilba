---
version: 1
slug: "tures-environments-environment-detail-tsx-9818fdc6"
primary_target: "apps/platform/src/features/environments/environment-detail.tsx"
related_targets:
  [
    "apps/platform/src/features/environments/environment-overview.tsx",
    "apps/platform/src/features/environments/environment.styles.ts",
    "apps/platform/src/features/projects/project-detail.tsx",
  ]
---

# Environment detail

Visitor mode: Operate. Overview-first extension of the accepted shell for developers and small teams. Both hosted and self-hosted; preserve permissions and editing/deployment behavior.

## Direction contract

THESIS: Recognise an environment and its saved services before entering an editor; replace the undifferentiated stack of operational panels with clear local work areas.

OWN-WORLD: Inherit Aligned stack: neutral light/dark semantic tokens, system sans, open surfaces, separator-led lists, shared Panda/Base UI controls. Users work in both bright and dim spaces; retain their theme preference.

STORY: Read configuration and deployment context, inspect saved services, then enter Configuration, Deployments or Settings. Saving a revision and applying a deployment remain separate actions. Unsaved specification and layout survive local navigation.

FIRST VIEWPORT: Project breadcrumbs precede a 24px environment title and a trailing Review and deploy action. Local tabs lead to two open summary columns, then a full-width service inventory with Open configuration. On mobile, summaries and wrapping service rows stack in reading order. The chosen overview-first HTML sketch sets arrangement and density.

FORM: Code-led Overview first, grounded candidate7, seed:5e9fe4ec54b1fb28. Signature interaction: move from the calm saved-service inventory into the retained editing workspace without losing in-progress changes. Motion uses existing control transitions, no page entrance or invented live-health indicators.

FINISH: unreviewed and undocumented is unfinished; this build ends with the finish review, the verdict, DESIGN.md, and every shipping raster carrying its provenance
