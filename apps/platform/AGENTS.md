# Dashboard

Use the repository engineering principles and accessibility requirements. React, Panda, TanStack Router/Query and the generated OpenAPI client are the application baseline. Do not import experimental UI code or private design systems. Keep session/secret data out of localStorage; only harmless preferences may persist there. Preserve the user's draft across failed saves and conflicts. Avoid success-only mocks in browser tests: the fixture runs the real Go API and disposable PostgreSQL.

`pnpm --filter @astilba/dashboard check` checks formatting, accessibility lint and types. `make verify-app` includes the production build and browser journeys. API bindings are generated from `contracts/platform`; never hand-edit or format the generated file. The Worker is an optional fixed-upstream adapter, not an arbitrary proxy or another domain-logic implementation.
