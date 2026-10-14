# `@astilba/ui`

Accessible React controls for Astilba products, built with native semantics, Base UI where behaviour warrants it, and statically extracted Panda CSS.

## Installation

```sh
pnpm add @astilba/ui @astilba/tokens
```

React and React DOM are peer dependencies.

## Usage

Import the framework-neutral tokens and component styles once:

```ts
import "@astilba/tokens/css";
import "@astilba/ui/styles.css";
```

Then use the components from React:

```tsx
import {
  Button,
  Collapsible,
  Dialog,
  Field,
  Input,
  LinkButton,
  Menu,
  ScrollArea,
  Tabs,
  Textarea,
  Tooltip,
  TooltipProvider,
} from "@astilba/ui";

export const Actions = () => (
  <TooltipProvider>
    <Button appearance="primary">Continue</Button>
    <LinkButton href="/docs">Read the docs</LinkButton>
    <Tooltip label="More information">
      <Button>Details</Button>
    </Tooltip>
    <Collapsible.Root>
      <Collapsible.Trigger>Deployment details</Collapsible.Trigger>
      <Collapsible.Panel>Deployed from a verified artifact.</Collapsible.Panel>
    </Collapsible.Root>
    <Dialog.Root>
      <Dialog.Trigger>Review release</Dialog.Trigger>
      <Dialog.Portal>
        <Dialog.Backdrop />
        <Dialog.Viewport>
          <Dialog.Popup>
            <Dialog.Title>Release review</Dialog.Title>
            <Dialog.Description>
              The artifact was verified before publication.
            </Dialog.Description>
            <Dialog.Close>Done</Dialog.Close>
          </Dialog.Popup>
        </Dialog.Viewport>
      </Dialog.Portal>
    </Dialog.Root>
    <Menu.Root>
      <Menu.Trigger>More actions</Menu.Trigger>
      <Menu.Portal>
        <Menu.Positioner>
          <Menu.Popup>
            <Menu.Item label="Refresh">Refresh</Menu.Item>
            <Menu.LinkItem href="/docs" label="Read the docs">
              Read the docs
            </Menu.LinkItem>
          </Menu.Popup>
        </Menu.Positioner>
      </Menu.Portal>
    </Menu.Root>
    <ScrollArea.Root>
      <ScrollArea.Viewport fade="block">
        <ScrollArea.Content>{/* Scrollable content */}</ScrollArea.Content>
      </ScrollArea.Viewport>
      <ScrollArea.Scrollbar>
        <ScrollArea.Thumb />
      </ScrollArea.Scrollbar>
    </ScrollArea.Root>
    <Tabs.Root defaultValue="overview">
      <Tabs.List aria-label="Project sections">
        <Tabs.Tab value="overview">Overview</Tabs.Tab>
        <Tabs.Tab value="activity">Activity</Tabs.Tab>
        <Tabs.Indicator />
      </Tabs.List>
      <Tabs.Panel value="overview">Project overview</Tabs.Panel>
      <Tabs.Panel value="activity">Recent activity</Tabs.Panel>
    </Tabs.Root>
    <Field.Root>
      <Field.Label htmlFor="project-name">Project name</Field.Label>
      <Input
        aria-describedby="project-name-help"
        id="project-name"
        name="projectName"
      />
      <Field.Description id="project-name-help">
        Used for the package and repository name.
      </Field.Description>
    </Field.Root>
    <Field.Root>
      <Field.Label htmlFor="project-description">Description</Field.Label>
      <Textarea id="project-description" name="description" rows={3} />
    </Field.Root>
  </TooltipProvider>
);
```

Links remain native anchors. `Button` uses Base UI's button primitive and defaults to `type="button"`.

Component subpaths are also available when a consumer needs the narrowest possible server or browser module graph:

```tsx
import { LinkButton } from "@astilba/ui/link-button";
import { Collapsible } from "@astilba/ui/collapsible";
import { Dialog } from "@astilba/ui/dialog";
import { Field } from "@astilba/ui/field";
import { Input } from "@astilba/ui/input";
import { Menu } from "@astilba/ui/menu";
import { ScrollArea } from "@astilba/ui/scroll-area";
import { Tabs } from "@astilba/ui/tabs";
import { Textarea } from "@astilba/ui/textarea";
import { Tooltip, TooltipProvider } from "@astilba/ui/tooltip";
```

The root entry remains tree-shakeable. Component JavaScript contains no Panda runtime; Panda generates the static class contract and stylesheet at build time.

The component stylesheet contains recipe defaults only. Consumer Panda utilities remain later in the `astilba` layer order and can override those defaults without specificity workarounds.

`Collapsible.Panel` owns only the disclosure transition. Consumers retain their own trigger presentation, content layout, chevrons, and state persistence. Set the panel transition to `none` in a consumer class while restoring persisted state to avoid animating initialization. Keep padding and borders on a child of the measured panel so its closed block size can reach zero cleanly.

`Dialog` owns modal focus containment, dismissal, focus return, labelling from `Dialog.Title` and `Dialog.Description`, and the backdrop and popup transitions. `Dialog.createHandle` connects triggers rendered outside `Dialog.Root` and passes each trigger's `payload` to the root's render function. Consumers retain popup content, actions, sizing beyond the default 32rem width, and any full-viewport presentation. On iOS Safari the backdrop is absolutely positioned so it reaches beneath the browser chrome; give `body` `position: relative` so it covers the whole page.

`ScrollArea` owns overflow-edge feedback, focus treatment, and a scrollbar that appears on hover, focus, or active scrolling. Pass `fade="block"` to `ScrollArea.Viewport` for a vertical edge fade, and set `direction="rtl"` on `ScrollArea.Root` when the scroll coordinates follow right-to-left reading order. Consumers retain sizing, content layout, overscroll policy, and scroll-position persistence. Include `ScrollArea.Content` whenever horizontal overflow is possible.

`Tabs` owns tab and panel associations, roving keyboard focus, selected state, and a measured indicator that follows the active tab. Selection uses manual activation by default; pass `activateOnFocus` to `Tabs.List` when every panel is available immediately and arrow-key focus should also select it. Consumers retain labels, panel content, surrounding layout, and controlled state.

`Field`, `Input`, and `Textarea` are intentionally native form wrappers. Give every control a stable `id`, connect its label with `htmlFor`, and list persistent guidance in `aria-describedby`. Error elements may stay mounted, but keep them hidden and omit their ID from `aria-describedby` until the error is active; hidden referenced text still contributes to the accessible description. Product code retains validation, form state, layout, and submission behaviour.

## Astro

Install and configure Astro's official React integration before importing these components. Astro can then server-render them without a client directive when native HTML behaviour is sufficient:

```astro
---
import { LinkButton } from "@astilba/ui/link-button";
---

<LinkButton href="/docs">Read the docs</LinkButton>
```

Fields can live in a server-rendered React wrapper when that keeps a larger Astro template easier to read:

```tsx
import { Field } from "@astilba/ui/field";
import { Input } from "@astilba/ui/input";

export const ProjectNameField = () => (
  <Field.Root>
    <Field.Label htmlFor="project-name">Project name</Field.Label>
    <Input
      aria-describedby="project-name-help"
      id="project-name"
      name="projectName"
    />
    <Field.Description id="project-name-help">
      Used for the package and repository name.
    </Field.Description>
  </Field.Root>
);
```

This wrapper still renders as static HTML and needs no client directive. Because these field components are native and context-free, consumers may also import their named parts and place them directly in an `.astro` template.

Add an Astro client directive when a component needs React-managed browser behaviour, including state, event handlers, effects, context, or an interactive primitive such as `Menu`, `ScrollArea`, `Tabs`, or `Tooltip`. `ScrollArea` needs hydration before it can measure overflow, update edge signals, and position its thumb. `Tabs` needs hydration for selection and to measure its active indicator. Server-rendered controls can also be enhanced by a separate Astro or vanilla browser script without hydrating React.

## Compatibility

- The package is ESM-only.
- Supported React and React DOM versions are declared as peer dependencies.
- Consumers own their reset, fonts, layout, routing, and application state.
- Import `@astilba/tokens/css` and `@astilba/ui/styles.css` once for the complete component styling contract.
- Base UI writes inline geometry for positioned or measured primitives, including `ScrollArea` and the `Tabs` indicator. Strict CSP consumers must account for those style attributes in `style-src-attr`. `ScrollArea` disables Base UI's inline scrollbar-hiding style element and ships the equivalent rule in the static component stylesheet.
