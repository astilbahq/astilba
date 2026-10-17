# `@astilba/tokens`

Framework-neutral semantic tokens for Astilba interfaces.

## Workspace use

This is a private package used by apps in this monorepo; it isn't published to npm.

```sh
pnpm --filter <app> add @astilba/tokens@workspace:*
```

## Static CSS

Import the variables once near the root of an application:

```ts
import "@astilba/tokens/css";
```

Dark mode is the default. Set `data-theme="light"` on the document element to apply the light values.

## Panda CSS

Add the preset after Panda's base preset and use the `astilba` prefix:

```ts
import { defineConfig } from "@pandacss/dev";
import presetBase from "@pandacss/preset-base";
import { astilbaPreset } from "@astilba/tokens/panda";

export default defineConfig({
  prefix: "astilba",
  preflight: false,
  presets: [presetBase, astilbaPreset],
});
```

The static CSS and Panda preset expose the same semantic contract. Prefer semantic names such as `ink.default`, `surface.elevated`, `surface.field`, and `border.field` over raw palette values so consumers inherit theme changes.
