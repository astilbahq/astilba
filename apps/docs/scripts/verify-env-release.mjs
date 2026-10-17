import { readFile } from "node:fs/promises";
import { resolve } from "node:path";

const root = process.cwd();
const readSource = (path) => readFile(resolve(root, path), "utf8");
const packageJson = JSON.parse(await readSource("package.json"));
const version = packageJson.dependencies?.["@astilba/env"];

if (typeof version !== "string" || !/^\d+\.\d+\.\d+$/u.test(version)) {
  throw new Error(
    "[env-release] package.json must pin @astilba/env to one exact semantic version."
  );
}

const requiredMarkers = new Map([
  ["astro.config.mjs", [`Astilba Env ${version} is a public alpha`]],
  [
    "public/.well-known/agent-skills/astilba-env-docs/SKILL.md",
    [
      `\`@astilba/env\` ${version} as a public alpha`,
      "Treat inventory as value-free name evidence only",
    ],
  ],
  [
    "public/agent-setup/prompt.md",
    [`\`@astilba/env\` ${version} as a public alpha`],
  ],
  [
    "../site/src/pages/env.astro",
    [`<code>@astilba/env</code> ${version} · public alpha`],
  ],
  [
    "../site/src/pages/index.astro",
    [`<code>@astilba/env</code> ${version} is a public alpha`],
  ],
  [
    "../site/src/pages/llms.txt.ts",
    [`@astilba/env ${version} is a public alpha`],
  ],
  [
    "src/content/docs/env.md",
    [
      `\`@astilba/env\` ${version} is a public alpha`,
      `pnpm add @astilba/env@${version} --save-exact`,
    ],
  ],
  [
    "src/content/docs/env/nextjs.md",
    [`\`@astilba/env\` ${version} has package-consumer evidence`],
  ],
  [
    "src/content/docs/env/migrate-from-next-dynamic-env.md",
    [`pnpm add @astilba/env@${version} --save-exact`],
  ],
  [
    "src/content/docs/env/inventory-and-drift.md",
    [
      `Env ${version} can make one declaration`,
      "astilba.env.contract-inventory/v1",
      "astilba.env.observed-name-inventory/v1",
      "Env never infers closed ownership",
    ],
  ],
  ["src/content/docs/env/nodejs.md", [`Env ${version} supports:`]],
  [
    "src/content/docs/env/quickstart.md",
    [
      `Env ${version} requires a supported Node.js release`,
      `pnpm add @astilba/env@${version} --save-exact`,
    ],
  ],
  [
    "src/content/docs/env/release-and-support.md",
    [
      `Astilba Env ${version}.`,
      `\`@astilba/env\` ${version} is a public alpha`,
      `pnpm add @astilba/env@${version} --save-exact`,
    ],
  ],
  [
    "src/content/docs/env/validation-and-standard-schema.md",
    [`Env ${version} requires opaque Standard Schema validation`],
  ],
  ["src/content/docs/env/vite.md", [`Env ${version} supports Vite`]],
  [
    "src/docs/products/env.ts",
    [`Released as @astilba/env ${version} for public-alpha evaluation`],
  ],
]);

for (const [path, markers] of requiredMarkers) {
  const source = await readSource(path);

  for (const marker of markers) {
    if (!source.includes(marker)) {
      throw new Error(
        `[env-release] ${path} is not aligned with @astilba/env ${version}; missing ${JSON.stringify(marker)}.`
      );
    }
  }
}

console.log(
  `[env-release] Verified ${requiredMarkers.size} release surfaces against @astilba/env ${version}.`
);
