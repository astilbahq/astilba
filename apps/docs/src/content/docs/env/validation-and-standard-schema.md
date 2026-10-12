---
title: Validation and Standard Schema
description: Choose portable built-in codecs, understand redacted failures, and reserve opaque Standard Schema validation for private Node.js targets.
---

Env validates configuration at an explicit lifecycle boundary. The declaration records the accepted source form and typed output; a generated target applies that contract when application code calls `check` or `load`.

Prefer a first-party codec whenever it can express the configuration. Use an `opaque` Standard Schema validator only for private server semantics that Env cannot represent portably.

## Start with built-in codecs

Built-in codecs make validation deterministic and portable:

```ts
entries: {
  enabled: env.public.deployment.boolean(),
  mode: env.public.deployment.enum(["standard", "compact"]),
  origin: env.public.deployment.origin(),
  port: env.private.deployment.safeInteger({
    maximum: 65_535,
    minimum: 1,
  }),
}
```

They define exact details such as case-sensitive Boolean tokens, canonical HTTPS origins, numeric ranges, blank handling, required presence, and bounded strings or JSON shapes.

Portable codecs can participate in public browser projections. A subset also works in the admitted [Cloudflare Workers deployment-target path](/docs/env/cloudflare-workers/). Check [Declaration reference](/docs/env/declaration-reference/#built-in-codecs) for each builder's input and portability.

## Choose the failure contract

Generated targets expose two operations:

```ts
const result = await check(source);
const configuration = await load(source);
```

`check` returns an explicit result:

```ts
if (!result.ok) {
  console.error("Configuration is invalid.", result.diagnostics);
} else {
  startApplication(result.value);
}
```

`load` returns the same typed configuration or throws `EnvironmentConfigurationError`.

This `await` form also works for targets that use built-in codecs only.

Both paths redact rejected values. Diagnostics may contain a stable error code, consumer, entry, lifecycle, or rule identity; they do not contain the value, a fragment, length, or hash. Do not log the source object around that boundary.

## Use `opaque` for private custom semantics

An opaque entry declares value-free input and output shapes plus an application-owned semantic identity:

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
  revision: "1",
  semantics: "com.example.service-options/v1",
})
```

Generation makes the schema requirement explicit in the target type:

```ts
import type { StandardSchemaV1 } from "@astilba/env/runtime";

import { load } from "./.astilba/env/serverDeployment.server";

type ServiceOptions = Readonly<{ region: string }>;

const serviceOptions: StandardSchemaV1<string, ServiceOptions> = {
  "~standard": {
    validate(input) {
      if (typeof input === "string") {
        try {
          const value: unknown = JSON.parse(input);

          if (
            typeof value === "object" &&
            value !== null &&
            !Array.isArray(value) &&
            Object.keys(value).length === 1 &&
            "region" in value &&
            typeof value.region === "string"
          ) {
            return {
              value: Object.freeze({ region: value.region }),
            };
          }
        } catch {
          // Return the same redacted issue as any other invalid input.
        }
      }

      return {
        issues: [{ message: "Invalid service options." }],
      };
    },
    vendor: "example",
    version: 1,
  },
};

const configuration = await load(process.env, { serviceOptions });
```

The schema's declared input and output types must exactly match the declaration shapes. Extra or missing schema keys fail the generated type gate.

Env has no first-party Zod adapter or named schema-library guarantee. You may pass any implementation that structurally satisfies Standard Schema v1 for the declared private opaque Node.js target; verify that library and schema version in your application, and keep its validation synchronous.

## Keep validation synchronous

Env 0.3.0 requires opaque Standard Schema validation to settle synchronously. A returned promise produces `ENV_VALIDATOR_ASYNC_UNSUPPORTED`.

Because the generated operation accepts an arbitrary validator implementation, `check` and `load` return promises for a target that contains an opaque entry even when the validator settles synchronously.

The CLI cannot validate an opaque target because it does not have your application schema map. Import the generated operation and pass the schemas in application code or a focused test.

## Respect the runtime boundaries

Opaque entries are:

- private;
- server-only;
- unavailable to browser consumers; and
- not admitted in the Cloudflare Workers support claim.

The exact input/output shapes, `semantics`, and `revision` provide value-free compatibility evidence. Env cannot prove that two arbitrary validator implementations behave identically, so planning reports `UNKNOWN` when compatibility depends on opaque behavior.

If custom validation belongs to business rules rather than the configuration boundary, load a built-in `text`, `secret`, or `json` value first and validate it in application code.
