# React Flow editor validation

PLAT-1 validates React Flow with small Astilba controls, real properties and a service-list alternative. This is a disposable browser experiment, not the production dashboard. It has no API, authentication, deployment execution or remote persistence. Example image references are text only.

## Reproduce

Use Node 24.18.1 and pnpm 11.17.0 from the repository root:

```sh
pnpm install --frozen-lockfile
pnpm --filter @astilba/canvas-spike exec playwright install chromium webkit
make verify-canvas
```

On Linux, install the browser system dependencies with `playwright install --with-deps chromium webkit` on a disposable development host, as CI does. `make verify-canvas` builds the application, runs the two domain test groups and tests the production preview in Chromium and WebKit. The preview owns loopback port 54171 and fails if that port is already in use; it never reuses an unknown server. Reports and screenshots go under the ignored `.artifacts/plat1/` directory. Timings are recorded, not used as CI speed thresholds.

For interactive use:

```sh
pnpm --filter @astilba/canvas-spike dev
```

Choose the starter or 250-service workspace. Each has separate browser-local storage. Switching workspaces or reloading discards unsaved edits; save each part explicitly first. Delete the four `astilba:plat1:*` keys in this origin's browser storage to reset saved fixtures.

## Editing and persistence

- Select and drag services, use the zoom/fit controls, or focus a service and use arrow keys (Shift moves farther). The Services view supports selecting, searching and editing without navigating the graph. The property panel supplies a native connection form for keyboard and assistive-technology use. Delete/Backspace do not remove nodes or edges; use the labelled connection-removal buttons.
- Applications connect to a database through `DATABASE_URL`. Applications and databases attach a volume at `/data`. Pointer handles and the form share the same direction, type, existence and single-binding rules. Removing a connection allows choosing another target.
- Add an application, PostgreSQL service or volume, then edit its name, image, port or capacity. Incomplete fields remain editable; Save draft reports invalid fields and preserves the previously saved draft.
- Save layout writes only positions and viewport. Save draft writes only the service/binding document. Selection is transient. React Flow's internal node representation is never the stored deployment draft. Hidden canvas state survives a switch to the service list.
- The versioned storage reader checks shapes, IDs, types, bounds and references before loading. Unreadable entries produce a visible warning and remain untouched until the corresponding explicit save. This is recovery for a local fixture, not a production migration scheme.

## Pinned stack and licences

| Package | Version | Licence |
| --- | --- | --- |
| React / React DOM | 19.3.1 | MIT |
| React Flow (`@xyflow/react`) | 12.12.0 | MIT |
| Panda dev, Vite plugin and presets | 2.3.0 | MIT |
| Vite / React plugin | 8.3.5 / 6.1.2 | MIT |
| TypeScript | 7.0.2 | Apache-2.0 |
| Playwright test | 1.64.1 | Apache-2.0 |
| axe Playwright integration / axe-core | 4.14.0 | MPL-2.0 |
| Biome | 2.6.2 | MIT OR Apache-2.0 |
| React / React DOM type declarations | 19.3.0 | MIT |
| Node type declarations | 24.19.2 | MIT |

Direct dependencies are exact and transitive resolutions are in the root lockfile. The workspace retains its 24-hour release-age gate and explicit build-script permissions. Parcel watcher's source-build hook is denied: supported platforms use its optional prebuilt binary. No React Compiler or automatic layout engine is enabled. Panda's generated styles and the production build stay uncommitted.

The application runtime uses the MIT-licensed React/React Flow packages; retain their notices when distributing assets. React Flow's attribution remains visible. The MPL-licensed axe and Lightning CSS packages are development tools, not application runtime dependencies. This experiment does not modify their sources. Inspect the complete distribution's notices when packaging the production application under the repository's AGPL-3.0-only licence; this browser spike does not distribute the development toolchain.

## Evidence and decision

Local production-build validation on 22 October 2026 uses Chromium 156.0.8078.4 and Playwright WebKit 27.2. Both exercise pointer connection creation, property changes, distinct layout/draft saves and reload, invalid-input recovery, keyboard movement, zoom/fit, list-based editing and connection removal. Axe checks the canvas and mobile list/property view against WCAG A/AA rules, including 2.2 tags. A 390 × 844 CSS-pixel layout reflows without horizontal page overflow. Automated axe checks do not establish manual screen-reader conformance.

The 250-service measurement uses an Apple M5 Pro with 18 logical CPUs and 64 GiB RAM, Darwin 27.0.0, Chromium headless at 1440 × 1000 CSS pixels. All 250 custom nodes remain mounted. After selecting Web 1 and centring it, the probe resets optional committed-render counters and drags the node 160 × 100 pixels in 120 pointer steps. `requestAnimationFrame` intervals and the browser's long-task observer cover the drag.

| Measurement | Standalone browser run | Full `make verify` run |
| --- | --- | --- |
| Frame interval, median / p95 / maximum | 16.7 / 16.7 / 16.8 ms | 16.7 / 33.3 / 33.4 ms |
| Frame samples | 123 | 160 |
| Observed long tasks (over 50 ms) | 0 | 0 |
| Dragged custom-node commits | 121 | 121 |
| Other custom-node commits | 0 | 0 |
| Property-panel commits during drag | 0 | 0 |

The canvas component itself updates during dragging; its memoized custom nodes do not all update with it. High-frequency positions stay in the canvas, service objects retain identity when unchanged, and the parent receives only a dirty-state transition. Add `?profile=1` to enable these counters; ordinary use does not collect them. The reproducible probe writes the hardware and raw measurements to `.artifacts/plat1/performance.json` on every run. Other hardware, browser scheduling and CI contention will produce different timings. This measures the synthetic interaction, not input latency, production-scale limits, network updates or a capacity guarantee.

**Decision:** retain React Flow and Panda for the production editor, with Astilba's domain model and layout stored separately. Keep the service-list alternative and native connection controls. There were no existing dashboard controls to reuse, so these small controls live only in the experiment. The production application still needs authenticated persistence, revision/conflict handling, unsaved-change navigation, richer resource types and a broader assistive-technology/device evaluation. None is scaffolded here.

API references: [React Flow performance](https://reactflow.dev/learn/advanced-use/performance), [React Flow accessibility](https://reactflow.dev/learn/advanced-use/accessibility), [Panda with Vite](https://panda-css.com/docs/get-started/vite), [Playwright configuration](https://playwright.dev/docs/test-configuration).
