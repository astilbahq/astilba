---
version: 1
slug: "src-features-projects-project-detail-tsx-fd6d6c46"
primary_target: "apps/platform/src/features/projects/project-detail.tsx"
related_targets:
  [
    "apps/platform/src/features/projects/create-environment.tsx",
    "apps/platform/src/features/projects/projects.styles.ts",
  ]
---

# Project detail and environment creation

Visitor mode: Operate. Extend the accepted Projects and shell identity for developers and small teams. Preserve existing environment editing, server-side permissions, localisation and typed routes.

## Direction contract

THESIS: Make the next environment choice clear, with creation on a separate page rather than adding another task to the overview.

OWN-WORLD: Inherit Aligned stack: neutral light/dark tokens, system sans, open surfaces, shared Panda/Base UI controls and quiet separator-led lists.

STORY: Open a project, recognise each environment's configuration type, open one or create a new environment. Creation retains Visual, Compose and preview-template choices and continues into the created environment.

FIRST VIEWPORT: A Projects breadcrumb leads to the 24px project name. An Environments heading and New environment action precede wrapping list rows. The separate creation page has project navigation, a 24px heading and a narrow start-aligned form. Mobile and RTL retain reading and focus order.

FORM: Code-led, precisely scoped extension of the established world; inherited seed:c778080728021839. State changes provide feedback and preserve drafts on failure; no invented health indicators or decorative motion.

FINISH: unreviewed and undocumented is unfinished; this build ends with the finish review, the verdict, DESIGN.md, and every shipping raster carrying its provenance
