---
title: Declaration reference
description: Reference the Env 0.3 declaration fields, entry builders, codecs, consumers, targets, and co-presence rules.
---

The root `@astilba/env` export contains two runtime values:

```ts
import { defineEnvironment, env } from "@astilba/env";
```

`defineEnvironment` validates one complete declaration and returns an opaque `EnvironmentDefinition`. The `env` object creates branded entries, consumers, targets, and rules that only `defineEnvironment` can compile.

`EnvironmentDefinition` is also available as a type-only export. Use it to annotate a boundary that accepts any compiled Env declaration; do not construct or inspect one yourself.

## `defineEnvironment`

```ts
defineEnvironment({
  id,
  entries,
  consumers,
  targets,
  rules,
});
```

| Field | Requirement |
| --- | --- |
| `id` | A lowercase reverse-DNS identifier such as `com.example.application`. |
| `entries` | One or more logical entries created by an `env.public.*` or `env.private.*` builder. |
| `consumers` | One or more named `env.browser(...)` or `env.server(...)` selections. |
| `targets` | One or more named `env.process(...)` mappings. Each target binds one complete lifecycle selected by its consumer. |
| `rules` | Optional array of `env.together(...)` co-presence rules. |

Entry, consumer, target, and rule identifiers start with a lowercase ASCII letter, contain only ASCII letters or digits, and have at most 64 characters. Identifiers are also unique under ASCII case folding.

Process source names use the environment-variable form `[A-Z_][A-Z0-9_]{0,127}`.

## Visibility and lifecycle builders

Choose visibility and lifecycle before the codec:

```ts
env.public.build
env.public.deployment
env.public.request

env.private.deployment
env.private.request
```

Every builder below supports `required: false`. Entries are required by default.

There is no private build builder. `secret` and `opaque` are available only on private deployment and request builders.

## Built-in codecs

The source column describes an `env.process` source such as `process.env`. The browser bootstrap receives already-typed JSON values and validates them against the same portable contract.

| Builder | Source and output | Important options and defaults | Browser portable |
| --- | --- | --- | --- |
| `boolean(options?)` | Exact source token to `boolean`. | `trueInput: "true"`; `falseInput: "false"`; `blank: "missing"`. | Yes |
| `enum(values, options?)` | Exact source string to the declared string union. | 1–1,024 unique portable strings; see the declaration limits below. | Yes |
| `integer(options)` | Trimmed signed decimal to a safe integer in range. | Required `minimum` and `maximum`; `blank: "missing"`. | No |
| `json(shape, options?)` | Bounded JSON text to the exact typed shape. | `blank: "missing"`. | Yes |
| `origin(options?)` | Canonical HTTPS origin string. | No path, query, fragment, credentials, IP literal, or `localhost`; default port and trailing slash are normalised away. | Yes |
| `safeInteger(options)` | Canonical decimal to a safe integer in range. | Required `minimum` and `maximum`; no leading `+`, whitespace, or non-canonical leading zero; `blank: "missing"`. | Yes |
| `string(options?)` | Preserved portable string. | `minimumCodePoints: 0`; `maximumCodePoints: 65_535`. Empty string is valid unless you raise the minimum. | Yes |
| `stringList(options?)` | Comma-separated source to a readonly string array. | Empty items `drop`; defaults to 0–64 items and 1–1,024 code points per item. | Yes |
| `text(options?)` | Optional trim-aware server string. | `normalise: "preserve"`; `blank: "missing"`; 1–65,535 code points. | No |
| `secret(options?)` | Preserved private string with no trimming. | `blank: "missing"`; 1–65,535 code points. | No; private only |
| `opaque(options)` | Private source string through a caller-supplied synchronous Standard Schema v1 validator. | Exact `input` and `output` shapes plus value-free `semantics` and `revision`. | No; private only |

`integer` accepts conventional signed, whitespace-trimmed server input. Use `safeInteger` when the same canonical decimal contract must work in server and browser projections.

`text` treats a whitespace-only value as blank even when `normalise` is `"preserve"`. `string` preserves and can accept an empty string. Choose deliberately instead of relying on a global empty-string policy.

### Boolean options

```ts
enabled: env.public.deployment.boolean({
  blank: "invalid",
  falseInput: "disabled",
  trueInput: "enabled",
})
```

`trueInput` and `falseInput` are case-sensitive, non-empty printable ASCII tokens and must differ.

### String-list options

```ts
regions: env.public.deployment.stringList({
  emptyItems: "invalid",
  minimumItems: 1,
  maximumItems: 8,
  minimumItemCodePoints: 2,
  maximumItemCodePoints: 32,
})
```

The separator is always a comma. Items are not trimmed automatically.

### Portable JSON shapes

`json` uses data-only shape descriptors:

```ts
clientConfiguration: env.public.deployment.json({
  kind: "object",
  properties: [
    {
      name: "region",
      required: true,
      shape: { kind: "string" },
    },
    {
      name: "retryCount",
      required: false,
      shape: {
        kind: "safe-integer",
        minimum: 0,
        maximum: 5,
      },
    },
  ],
})
```

Shape kinds are:

- `string`, `boolean`, and `null`;
- `safe-integer` with `minimum` and `maximum`;
- `array` with `items`, `minimumItems`, and `maximumItems`; and
- `object` with named, required or optional `properties`.

Objects are exact: unknown properties are rejected. Values are copied into frozen, owned data before the application receives them.

### Declaration limits

Env bounds declaration size and portable value work before runtime resolution:

| Surface | Limit |
| --- | --- |
| Enum choices | 1–1,024 unique choices; each choice is at most 65,535 UTF-8 bytes and all choices together are at most 65,536 bytes. |
| String lists | Defaults to at most 64 items and 1,024 code points per item. Options may raise those values to at most 1,024 items or 65,536 code points per item, while `maximumItems × maximumItemCodePoints` must not exceed 65,536. |
| Portable shapes | At most 8 levels and 256 shape nodes. Arrays declare at most 1,024 items; objects declare at most 256 unique keys. |
| Portable object keys | At most 255 UTF-8 bytes; `__proto__`, `constructor`, and `prototype` are rejected. |
| Portable string values | At most 65,536 UTF-8 bytes when copied through a declared portable shape. |

### Opaque schemas

Use `opaque` only for private semantics that a built-in codec cannot express:

```ts
serviceOptions: env.private.deployment.opaque({
  input: { kind: "string" },
  output: {
    kind: "object",
    properties: [
      {
        name: "region",
        required: true,
        shape: { kind: "string" },
      },
    ],
  },
  semantics: "com.example.service-options/v1",
  revision: "1",
})
```

The generated target requires a schema map and returns promises:

```ts
import { load } from "./.astilba/env/serverDeployment.server";

const configuration = await load(process.env, {
  serviceOptions: serviceOptionsSchema,
});
```

The schema's declared input and output types must exactly match the shapes in the declaration. Validation must settle synchronously; a returned promise produces `ENV_VALIDATOR_ASYNC_UNSUPPORTED`.

An opaque input shape is either `{ kind: "string" }` or an optional string wrapper:

```ts
input: {
  kind: "optional",
  value: { kind: "string" },
}
```

An opaque output may use any portable shape above or wrap one with the same `optional` form. Entry presence and schema input are separate decisions:

- with `required: false` and a non-optional input, a missing source omits the entry without calling the validator;
- with an optional input, a missing source calls the validator with `undefined`; and
- if the validator returns `undefined` for an optional output, a required entry fails with `ENV_MISSING_VALUE` while an optional entry is omitted.

Returning `undefined` for a non-optional output fails with `ENV_INVALID_VALUE`. The entry builder's `required` option controls whether the whole entry may be absent. A `required` flag inside an object shape controls only that named output property. Omitting a required property, adding an unknown property, or returning another value that does not match the declared shape fails with `ENV_INVALID_VALUE`; omitting a property marked `required: false` is valid.

The CLI `check` command cannot accept application schema implementations. Validate an opaque target through its generated `check` or `load` function.

## Consumers

```ts
env.browser(["apiOrigin", "featureMode"])
env.server(["databaseUrl", "port"])
```

An explicit list must contain at least one unique entry. Omitting the list selects every declared entry:

```ts
env.server()
```

Browser consumers may select only public entries using `boolean`, `enum`, `json`, `origin`, `safeInteger`, `string`, or `stringList`. They cannot select an entry that belongs to an `env.together` rule; co-presence rules are server-projection only in 0.3.

A browser consumer that selects one or more build entries requires exactly one complete build target. This gives generation one unambiguous source mapping for the emitted browser values.

## Process targets

```ts
env.process("server", {
  databaseUrl: "DATABASE_URL",
  port: "PORT",
})
```

The first argument names an existing consumer. The record maps logical entry names to raw source names.

A target must bind all entries selected by that consumer for one lifecycle. Split build, deployment, and request bindings into separate targets. One target cannot map two logical entries to the same raw source name. You can define alternate complete targets for the same consumer and lifecycle when your application needs different source mappings.

## Co-presence rules

Use `env.together` when optional entries form one configuration unit:

```ts
rules: [
  env.together("smtpCredentials", [
    "smtpHost",
    "smtpUser",
    "smtpPassword",
  ]),
]
```

Resolution succeeds when all rule entries are present or all are absent. A partial set returns `ENV_RULE_VIOLATION` with the logical rule and entry names, not their values.

Each rule requires at least two unique entries from the same lifecycle. Co-presence rules are server-projection only in 0.3; browser consumers cannot select rule entries. A server consumer that selects any entry in a rule must select every entry in that rule. Keep a co-presence rule within one operational configuration unit.
