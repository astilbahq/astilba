import assert from "node:assert/strict";
import { test } from "node:test";

import braces from "braces";

const nested = (open, close, depth) =>
  `${open.repeat(depth)}a${close.repeat(depth)}`;

for (const method of ["parse", "compile", "expand", "stringify"]) {
  for (const [open, close] of [
    ["{", "}"],
    ["(", ")"],
  ]) {
    test(`${method} rejects excessive ${open}${close} nesting below the character limit`, () => {
      assert.throws(() => braces[method](nested(open, close, 4000)), {
        message: /exceeds max depth/u,
        name: "SyntaxError",
      });
    });
  }
}

for (const maxDepth of [Number.POSITIVE_INFINITY, Number.NaN, 10_000]) {
  test(`maxDepth=${maxDepth} cannot disable the nesting guard`, () => {
    assert.throws(() => braces.expand(nested("{", "}", 4000), { maxDepth }), {
      message: /exceeds max depth/u,
      name: "SyntaxError",
    });
  });
}

const nestedAst = (depth) => {
  let node = { type: "text", value: "a" };
  for (let index = 0; index < depth; index += 1) {
    const parent = {
      commas: 0,
      nodes: [
        { type: "open", value: "{" },
        node,
        { type: "close", value: "}" },
      ],
      ranges: 0,
      type: "brace",
    };
    for (const child of parent.nodes) {
      child.parent = parent;
    }
    node = parent;
  }
  const root = { nodes: [node], type: "root" };
  node.parent = root;
  return root;
};

for (const method of ["compile", "expand", "stringify"]) {
  test(`${method} bounds caller-created AST traversal`, () => {
    assert.throws(() => braces[method](nestedAst(150)), {
      message: /exceeds max depth/u,
      name: "RangeError",
    });
  });
}

test("ordinary nested alternatives preserve their expansion order", () => {
  assert.deepEqual(braces.expand("src/{ui,docs}/{a,{b,c}}.ts"), [
    "src/ui/a.ts",
    "src/ui/b.ts",
    "src/ui/c.ts",
    "src/docs/a.ts",
    "src/docs/b.ts",
    "src/docs/c.ts",
  ]);
});

test("padded and stepped ranges preserve their expansions", () => {
  assert.deepEqual(braces.expand("v{01..03}"), ["v01", "v02", "v03"]);
  assert.deepEqual(braces.expand("{1..5..2}"), ["1", "3", "5"]);
});

test("escaped braces remain literal", () => {
  assert.deepEqual(braces.expand(String.raw`\{a,b\}`), ["{a,b}"]);
});
