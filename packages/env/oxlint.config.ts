import { defineConfig } from "oxlint";
import core from "ultracite/oxlint/core";
import vitest from "ultracite/oxlint/vitest";

export default defineConfig({
  extends: [
    core,
    vitest,
    {
      overrides: [
        {
          files: ["**/*.{test,spec}.{ts,tsx,js,jsx}"],
          plugins: ["vitest"],
          rules: {
            // Conformance cases often prove several descriptors from one
            // immutable result. Splitting those assertions would hide that they
            // share identity.
            "vitest/max-expects": "off",
            // Exact booleans are protocol evidence; truthiness would also accept
            // values such as 1 or an object and therefore weaken the proof.
            "vitest/prefer-to-be-falsy": "off",
            "vitest/prefer-to-be-truthy": "off",
          },
        },
      ],
    },
  ],
  // This exact fixture is copied into a clean archive consumer and type-checked
  // there against the installed package declarations rather than this checkout.
  ignorePatterns: [
    "dist/**",
    "coverage/**",
    "test/fixtures/matrix/astilba.env.ts",
  ],
  rules: {
    "func-style": "off",
    // Filesystem walks, measurement samples, and lifecycle resolution are
    // intentionally ordered. Parallelising them changes observable semantics.
    "no-await-in-loop": "off",
    "no-inline-comments": "off",
    // Hoisted helpers keep the public operations at the top of long,
    // validation-heavy modules.
    "no-use-before-define": "off",
    "no-plusplus": "off",
    "prefer-destructuring": "off",
    // Exhaustive discriminated-union switches intentionally have no fallback;
    // an unreachable default would mask a future member from TypeScript.
    "default-case": "off",
    // Named Node built-ins are used consistently across source and tests.
    "unicorn/import-style": "off",
    // The frozen public error names omit the conventional Error suffix.
    "unicorn/custom-error-definition": "off",
    // Small local helpers are kept next to the proof or generator that gives
    // them context, even if they happen not to close over a local binding.
    "unicorn/consistent-function-scoping": "off",
    "unicorn/no-await-expression-member": "off",
    "unicorn/no-lonely-if": "off",
    "unicorn/relative-url-style": "off",
    // `undefined` is a first-class missing-input value at Env's resolution
    // boundary; omitting the argument changes both type and intent.
    "unicorn/no-useless-undefined": "off",
    // These size heuristics push hardened parsers and validators into smaller,
    // less auditable fragments without adding a correctness property.
    "eslint/complexity": "off",
    "eslint/max-classes-per-file": "off",
    "oxc/no-barrel-file": "off",
    "sort-keys": "off",
    // Node child-process and stream events require explicit promise bridges;
    // no upstream promise exists for these callbacks.
    "promise/avoid-new": "off",
    "promise/param-names": "off",
    "promise/prefer-await-to-callbacks": "off",
    "promise/prefer-await-to-then": "off",
    // Exhaustive discriminated-union switches return from every supported
    // case; a synthetic fallback would weaken future-version detection.
    "typescript/consistent-return": "off",
    "typescript/method-signature-style": "off",
  },
  overrides: [
    {
      files: ["examples/**/.astilba/env/**/*.ts"],
      rules: {
        // These exact registry-generated modules are validated by
        // `generate --check`; their source shape is not application-authored.
        "eslint/no-shadow": "off",
        "import/consistent-type-specifier-style": "off",
        "typescript/no-misused-spread": "off",
        "typescript/no-unsafe-type-assertion": "off",
        "typescript/dot-notation": "off",
        "unicorn/filename-case": "off",
        "unicorn/numeric-separators-style": "off",
      },
    },
    {
      files: ["scripts/**/*.mjs"],
      rules: {
        // Verification scripts use JSDoc only where it gives
        // `tsc --checkJs` useful shape information. Requiring prose on every tag
        // turns those annotations into maintenance-only documentation.
        "jsdoc/require-param-description": "off",
        "jsdoc/require-returns-description": "off",
      },
    },
    {
      files: ["examples/scripts/verify-adoption.mjs"],
      rules: {
        // This executable verifier intentionally performs runtime shape checks
        // over process, filesystem, registry, and HTTP boundaries. CheckJS
        // cannot express those dynamic shapes without obscuring the checks.
        "eslint/no-promise-executor-return": "off",
        "jsdoc/require-param-description": "off",
        "no-unreachable-loop": "off",
        "typescript/no-unsafe-argument": "off",
        "typescript/no-unsafe-assignment": "off",
        "typescript/no-unsafe-call": "off",
        "typescript/no-unsafe-member-access": "off",
        "typescript/no-unsafe-return": "off",
        "typescript/strict-boolean-expressions": "off",
        "typescript/strict-void-return": "off",
      },
    },
    {
      files: [
        "scripts/verify-next-consumer.mjs",
        "src/authoring/environment.ts",
        "src/cli/astilba-env.ts",
        "src/core/contract.ts",
        "src/core/resolve.ts",
        "src/core/shapes.ts",
        "src/planning/markers.test.ts",
        "src/planning/plan.ts",
        "src/product/compilation.ts",
        "src/product/generate.ts",
        "src/provider/wrangler.ts",
      ],
      rules: {
        // These files use compact, ordered selectors, three-way comparators, or
        // adversarial proofs where repeated if branches obscure the ordering.
        "eslint/no-nested-ternary": "off",
        "unicorn/no-nested-ternary": "off",
      },
    },
    {
      files: ["scripts/verify-next-consumer.mjs"],
      rules: {
        // The Next 15 middleware and Next 16 proxy fixtures are clearer as two
        // explicit generated writes than as one conditional expression.
        "unicorn/prefer-ternary": "off",
      },
    },
    {
      files: ["scripts/verify-consumer.mjs"],
      rules: {
        // Every package manager must independently consume and smoke-test the
        // packed archive; the finite verification matrix is intentional.
        "no-unreachable-loop": "off",
      },
    },
    {
      files: [
        "scripts/verify-portable-equivalence.mjs",
        "src/product/generate.ts",
      ],
      rules: {
        // These strings are source-code fixtures. The `${...}` text must survive
        // literally so the generated module, rather than the generator, owns it.
        "eslint/no-template-curly-in-string": "off",
      },
    },
    {
      files: ["src/artifacts/tree.ts", "src/core/digest.ts"],
      rules: {
        // Node open flags and the frozen base64url codec are bit protocols; the
        // operators are the operation being implemented, not shorthand maths.
        "eslint/no-bitwise": "off",
      },
    },
    {
      files: ["src/browser/loader.ts"],
      rules: {
        // This compact expression is part of the browser-budgeted frozen parser.
        // Named captures or a Unicode mode change add no validation property.
        "eslint/prefer-named-capture-group": "off",
        "eslint/require-unicode-regexp": "off",
      },
    },
    {
      files: ["src/core/json.ts"],
      rules: {
        // Canonical JSON validates individual UTF-16 code units so it can reject
        // lone surrogates before encoding; code-point iteration skips that state.
        "unicorn/prefer-code-point": "off",
      },
    },
    {
      files: ["src/core/types.ts"],
      rules: {
        // These declaration forms and aliases are frozen public protocol names.
        // Replacing them changes the emitted declaration contract.
        "typescript/no-empty-interface": "off",
        "typescript/no-empty-object-type": "off",
      },
    },
    {
      files: ["src/browser/browser.test.ts"],
      plugins: ["vitest"],
      rules: {
        // These hostile doubles deliberately observe the exact receiver, throw
        // non-Error identities, reject with exact values, and construct lone
        // surrogate inputs. Normalizing them would weaken the boundary proofs.
        "typescript/no-this-alias": "off",
        "typescript/only-throw-error": "off",
        "typescript/prefer-promise-reject-errors": "off",
        "unicorn/no-this-assignment": "off",
        "unicorn/prefer-code-point": "off",
        "vitest/require-mock-type-parameters": "off",
      },
    },
    {
      files: ["src/cli/astilba-env.test.ts"],
      plugins: ["vitest"],
      rules: {
        // The CLI matrix supplies assertion labels and structurally forged
        // mocks; those are stronger diagnostics than the generic Vitest forms.
        "vitest/require-mock-type-parameters": "off",
        "vitest/require-to-throw-message": "off",
        "vitest/require-top-level-describe": "off",
        "vitest/valid-expect": "off",
      },
    },
    {
      files: ["src/planning/markers.test.ts", "src/provider/wrangler.test.ts"],
      plugins: ["vitest"],
      rules: {
        // These tests assert through stable failure helpers; Vitest cannot see
        // the nested expectations when checking the outer test callback.
        "vitest/expect-expect": "off",
      },
    },
    {
      files: ["src/product/generate.test.ts"],
      plugins: ["vitest"],
      rules: {
        // Generated-output boundary cases intentionally mutate fresh Maps to
        // cross one limit at a time; rebuilding each Map obscures that delta.
        "unicorn/no-immediate-mutation": "off",
        // Compile-time contract probes precede the runtime suite by design.
        "vitest/require-top-level-describe": "off",
      },
    },
    {
      files: ["src/authoring/environment-types.test.ts"],
      rules: {
        // This compile-time proof must import one deliberately absent public
        // name under `@ts-expect-error`, which requires a separate import line.
        "eslint/no-duplicate-imports": "off",
        "import/no-duplicates": "off",
      },
    },
  ],
});
