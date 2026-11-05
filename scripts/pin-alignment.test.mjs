import assert from "node:assert/strict";
import test from "node:test";
import { parseWorkspace, pinProblems } from "./pin-alignment.mjs";

const digest = `sha256:${"a".repeat(64)}`;
const image = `mcr.microsoft.com/playwright:v1.64.1-noble@${digest}`;
const workspaceText = `packages:
  - apps/platform
catalog:
  # A comment.
  '@pandacss/dev': 2.4.0
  '@playwright/test': 1.64.1
  vite: npm:@voidzero-dev/vite-plus-core@1.4.0
  vite-plus: 1.4.0
overrides:
  vite@*: 'catalog:'
  vitest@*: 5.0.5
allowBuilds:
  '@swc/core@1.16.20': false
  esbuild@0.28.2: true
peerDependencyRules:
  allowedVersions:
    vite: 1.4.0
    'storybook@10>vite-plus': 1.4.0
`;
const lockfile = `lockfileVersion: '9.0'

packages:

  '@swc/core@1.16.20':
    resolution: {integrity: sha512-a}

  esbuild@0.28.2:
    resolution: {integrity: sha512-b}

  vite-plus@1.4.0:
    resolution: {integrity: sha512-c}
    peerDependencies:
      '@vitest/browser-playwright': 5.0.5

snapshots:
`;
const inputs = (change = () => {}) => {
  const value = {
    workspace: parseWorkspace(workspaceText),
    lockfile,
    manifests: {
      "package.json": { devDependencies: { "vite-plus": "catalog:" } },
      "apps/platform/package.json": {
        devDependencies: {
          "@pandacss/dev": "catalog:",
          "@pandacss/vite": "2.4.0",
          "@playwright/test": "catalog:",
          "@storybook/addon-a11y": "10.6.5",
          storybook: "10.6.5",
          vite: "catalog:",
          "vite-plus": "catalog:",
        },
      },
    },
    workflows: {
      ".github/workflows/canvas.yml": `image: ${image}\n`,
      ".github/workflows/application-browsers.yml": `  ${image} \\\n`,
    },
  };
  change(value);
  return value;
};

test("reads the workspace file and refuses what it can't read", () => {
  const workspace = parseWorkspace(workspaceText);
  assert.deepEqual(workspace.packages, ["apps/platform"]);
  assert.equal(workspace.catalog["@pandacss/dev"], "2.4.0");
  assert.equal(workspace.overrides["vite@*"], "catalog:");
  assert.equal(
    workspace.peerDependencyRules.allowedVersions["storybook@10>vite-plus"],
    "1.4.0",
  );
  for (const [text, line] of [
    ["catalog: {vite: 1}\n", 1],
    ["catalog:\n  vite: &pin 1.0.0\n", 2],
    ["a: 1\n    b: 2\n", 2],
    ["packages:\n  - a\n  b: 1\n", 3],
  ])
    assert.throws(() => parseWorkspace(text), new RegExp(`line ${line}$`));
});

test("accepts an aligned set", () => {
  assert.deepEqual(pinProblems(inputs()), []);
});

const misalignments = [
  // What Dependabot leaves behind: it moves the catalog's vite-plus line and nothing else.
  [
    "vite-plus moved alone",
    (v) => (v.workspace.catalog["vite-plus"] = "1.5.0"),
    /catalog vite must be npm:@voidzero-dev\/vite-plus-core@1\.5\.0/,
  ],
  [
    "the vite alias moved alone",
    (v) =>
      (v.workspace.catalog.vite = "npm:@voidzero-dev/vite-plus-core@1.5.0"),
    /catalog vite must be/,
  ],
  [
    "a hand-written vite override",
    (v) =>
      (v.workspace.overrides["vite@*"] =
        "npm:@voidzero-dev/vite-plus-core@1.4.0"),
    /override vite@\* must be "catalog:"/,
  ],
  [
    "the vite peer rule",
    (v) => (v.workspace.peerDependencyRules.allowedVersions.vite = "1.3.0"),
    /allowed peer vite must be 1\.4\.0/,
  ],
  [
    "the Storybook peer rule's version",
    (v) =>
      (v.workspace.peerDependencyRules.allowedVersions[
        "storybook@10>vite-plus"
      ] = "1.3.0"),
    /allowed peer storybook@10>vite-plus must be 1\.4\.0/,
  ],
  [
    "a Storybook major past the peer rule",
    (v) => {
      v.manifests["apps/platform/package.json"].devDependencies.storybook =
        "11.0.0";
      v.manifests["apps/platform/package.json"].devDependencies[
        "@storybook/addon-a11y"
      ] = "11.0.0";
    },
    /peer rule storybook@10>vite-plus doesn't match Storybook 11\.0\.0/,
  ],
  [
    "a missing Storybook peer rule",
    (v) =>
      delete v.workspace.peerDependencyRules.allowedVersions[
        "storybook@10>vite-plus"
      ],
    /expected one storybook@<major>>vite-plus peer rule, found 0/,
  ],
  [
    "the vitest override",
    (v) => (v.workspace.overrides["vitest@*"] = "5.1.0"),
    /override vitest@\* must be 5\.0\.5/,
  ],
  [
    "Vite+ missing from the lockfile",
    (v) =>
      (v.lockfile = v.lockfile.replace("vite-plus@1.4.0", "vite-plus@1.3.0")),
    /can't read the Vite\+ 1\.4\.0 runner/,
  ],
  [
    "a manifest off the catalog",
    (v) => (v.manifests["package.json"].devDependencies["vite-plus"] = "1.4.0"),
    /package\.json: vite-plus must be "catalog:"/,
  ],
  [
    "a Panda package apart",
    (v) =>
      (v.manifests["apps/platform/package.json"].devDependencies[
        "@pandacss/vite"
      ] = "2.3.0"),
    /@pandacss\/\* versions differ/,
  ],
  [
    "a Storybook package apart",
    (v) =>
      (v.manifests["apps/platform/package.json"].devDependencies.storybook =
        "10.6.6"),
    /Storybook versions differ/,
  ],
  [
    "the Playwright package moved alone",
    (v) => (v.workspace.catalog["@playwright/test"] = "1.65.0"),
    /canvas\.yml: \S+ doesn't match @playwright\/test 1\.65\.0/,
  ],
  [
    "one workflow's Playwright tag",
    (v) =>
      (v.workflows[".github/workflows/canvas.yml"] =
        `image: ${image.replace("1.64.1", "1.65.0")}\n`),
    /Playwright images differ/,
  ],
  [
    "one workflow's Playwright digest",
    (v) =>
      (v.workflows[".github/workflows/canvas.yml"] =
        `image: ${image.replace("sha256:a", "sha256:b")}\n`),
    /Playwright images differ/,
  ],
  [
    "a Playwright image without a digest",
    (v) =>
      (v.workflows[".github/workflows/canvas.yml"] =
        "image: mcr.microsoft.com/playwright:v1.64.1-noble\n"),
    /needs a version tag and a digest/,
  ],
  [
    "no Playwright image",
    (v) => (v.workflows = {}),
    /no Playwright image found/,
  ],
  [
    "an allowBuilds key without a version",
    (v) => {
      v.workspace.allowBuilds.esbuild = "true";
    },
    /allowBuilds esbuild must be an exact version/,
  ],
  [
    "an allowBuilds range",
    (v) => {
      v.workspace.allowBuilds["esbuild@^0.28.2"] = "true";
    },
    /allowBuilds esbuild@\^0\.28\.2 must be an exact version/,
  ],
  [
    "a stale allowBuilds key",
    (v) => {
      v.workspace.allowBuilds["esbuild@0.28.1"] = "true";
    },
    /allowBuilds esbuild@0\.28\.1 must be an exact version in pnpm-lock\.yaml/,
  ],
];

for (const [name, change, expected] of misalignments)
  test(`refuses ${name}`, () => {
    const problems = pinProblems(inputs(change));
    assert.ok(
      problems.some((problem) => expected.test(problem)),
      problems.join("\n"),
    );
  });
