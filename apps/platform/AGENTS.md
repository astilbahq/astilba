# Dashboard

Use the repository engineering principles and accessibility requirements. React, Panda, TanStack Router/Query and the generated OpenAPI client are the application baseline. Do not import experimental UI code or private design systems. Keep session/secret data out of localStorage; only harmless preferences may persist there. Preserve the user's draft across failed saves and conflicts. Avoid success-only mocks in browser tests: the fixture runs the real Go API and disposable PostgreSQL.

`pnpm --filter @astilba/dashboard check` checks formatting, accessibility lint, import cycles and types. `make verify-app` includes the production build and browser journeys. API bindings are generated from `contracts/platform`; never hand-edit or format the generated file. The Worker is an optional fixed-upstream adapter, not an arbitrary proxy or another domain-logic implementation.

See [frontend development](../../docs/frontend-development.md) for source boundaries, local commands and Panda conventions. Keep shared query options below routing; feature modules must not import the router instance or route definitions. Colocate feature styles and helpers, and promote them only when another feature needs them.

Use the shared control primitives and theme recipes instead of styling native inputs, buttons, labels or links in route selectors. Use the typed shared Link for application routes and Anchor for native href destinations. Use logical CSS properties and semantic colour roles; keep Panda strict token/property checking and the logical-property guards enabled.

Use Base UI (`@base-ui/react`) for custom interactive primitives such as menus, tooltips and dialogs, with Panda recipes for appearance. Native labels, anchors, selects, textareas and checkboxes remain valid accessible primitives; keep typed routing and native form semantics when adopting a Base UI counterpart.
