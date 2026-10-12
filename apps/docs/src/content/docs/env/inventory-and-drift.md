---
title: Check name inventory drift
description: Export a value-free target inventory and compare it with an application-owned provider or platform name list.
---

Env 0.3.0 can make one declaration the authority for the configuration names a process target expects. Export the declared names, convert a provider or platform list into Env's small observed format, then check required presence and explicit namespace ownership in CI.

This workflow compares names only. Env does not receive values, contact a provider, inspect secret kinds, sync configuration, or decide which namespace your application owns.

## Export the declared inventory

Choose one generated process target from `astilba.env.ts`:

```sh
pnpm exec astilba-env inventory export --target workerDeployment
```

Without `--json`, the command writes one canonical `astilba.env.contract-inventory/v1` document to standard output:

```json
{
  "entries": [
    {
      "entry": "apiKey",
      "lifecycle": "deployment",
      "name": "API_KEY",
      "required": true,
      "visibility": "private"
    },
    {
      "entry": "previousApiKey",
      "lifecycle": "deployment",
      "name": "PREVIOUS_API_KEY",
      "required": false,
      "visibility": "private"
    }
  ],
  "format": "astilba.env.contract-inventory/v1",
  "target": "workerDeployment"
}
```

The inventory contains declared metadata only: logical entry ID, lifecycle, bound source name, required presence, and visibility. It contains no value, value hash, provider kind, routing rule, or provider identity.

`inventory export` supports process targets. It refuses unknown, unsupported, empty, malformed, duplicate, or case-folded name mappings rather than emitting ambiguous evidence.

## Convert the observed names

Keep provider access in application-owned tooling. Query the provider with its supported CLI or API, discard every field except the names, then write this exact document:

```json
{
  "entries": [
    { "name": "API_KEY" },
    { "name": "UNRELATED_PLATFORM_KEY" }
  ],
  "format": "astilba.env.observed-name-inventory/v1"
}
```

Env does not accept Wrangler, Infisical, GitHub, or another provider's native response directly. A thin converter keeps authentication, pagination, destination selection, and provider-specific interpretation outside Env.

The observed format is deliberately strict:

- the top-level object has only `entries` and `format`;
- each entry has only `name`;
- names match `[A-Z_][A-Z0-9_]{0,127}` and are unique under ASCII case folding;
- at most 2,048 names and 1 MiB of JSON are accepted; and
- the CLI rejects symbolic links, invalid UTF-8, malformed JSON, unknown fields, and unsupported formats.

Treat the names as sensitive operational metadata. Do not upload expected or observed inventories as CI artefacts by default.

## Choose ownership explicitly

Check an open namespace when the destination legitimately contains names owned by other applications or tools:

```sh
pnpm exec astilba-env inventory check \
  --target workerDeployment \
  --observed ./observed-names.json
```

Open ownership is the default. Missing required names fail; missing optional names and unexpected names are notices.

Use closed ownership only when this target owns the complete observed namespace:

```sh
pnpm exec astilba-env inventory check \
  --target workerDeployment \
  --observed ./observed-names.json \
  --ownership closed
```

Closed ownership makes an unexpected name fail. Env never infers closed ownership from the target, provider, file name, or CI environment.

## Interpret the result

The check reports three issue codes:

| Code | Meaning | Fails open ownership | Fails closed ownership |
| --- | --- | --- | --- |
| `REQUIRED_MISSING` | A declared `required: true` name is absent. | Yes | Yes |
| `OPTIONAL_MISSING` | A declared `required: false` name is absent. | No | No |
| `UNEXPECTED_ENTRY` | An observed name is outside the target inventory. | No | Yes |

`required: false` models optional presence, such as an empty rotation slot. It is not warning severity. Keep application-specific warnings and escalation policy outside Env.

For CI, add `--json` and verify the response discriminator before consuming the report:

```sh
pnpm exec astilba-env inventory check \
  --target workerDeployment \
  --observed ./observed-names.json \
  --ownership closed \
  --json
```

```json
{
  "command": "inventory",
  "format": "astilba.env.cli.inventory/v1",
  "ok": false,
  "operation": "check",
  "report": {
    "format": "astilba.env.inventory-check/v1",
    "issues": [
      {
        "code": "OPTIONAL_MISSING",
        "entry": "previousApiKey",
        "name": "PREVIOUS_API_KEY"
      },
      {
        "code": "UNEXPECTED_ENTRY",
        "entry": null,
        "name": "UNRELATED_PLATFORM_KEY"
      }
    ],
    "ownership": "closed",
    "pass": false,
    "target": "workerDeployment"
  }
}
```

Exit `0` means the inventory is acceptable under the selected ownership mode. Exit `1` means drift or invalid operational evidence. Exit `2` is reserved for command-line misuse.

## Keep delivery separate

An inventory pass proves only that the supplied list contains the expected names. It does not prove that:

- a value is non-empty or valid;
- a provider stored the value as a secret;
- the application received the current value;
- the observed list came from the intended account, project, or environment; or
- a sync or prune operation is safe.

Use the generated target's `check(source)` or `load(source)` operation for runtime value validation. Keep provider sync, prune, routing, authentication, and destination selection in the system that already owns delivery.
