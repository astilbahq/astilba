import { createHash } from "node:crypto";
import { readFile, readdir, writeFile } from "node:fs/promises";
import { resolve } from "node:path";

import { DOCS_BASE_PATH, withDocsBase } from "../src/docs/urls.ts";

const skillsDirectory = resolve(
  process.cwd(),
  "dist",
  DOCS_BASE_PATH.slice(1),
  ".well-known/agent-skills"
);
const indexPath = resolve(skillsDirectory, "index.json");
const schema = "https://schemas.agentskills.io/discovery/0.2.0/schema.json";
const skillNamePattern = /^[a-z0-9]+(?:-[a-z0-9]+)*$/;

const parseFrontmatterString = (frontmatter, field, artifact) => {
  const matches = [
    ...frontmatter.matchAll(
      new RegExp(`^${field}:\\s*("(?:[^"\\\\]|\\\\.)*")\\s*$`, "gm")
    ),
  ];

  if (matches.length !== 1) {
    throw new Error(
      `[agent-skills] ${artifact} must define ${field} exactly once as a JSON string in its frontmatter.`
    );
  }

  return JSON.parse(matches[0][1]);
};

const entries = await readdir(skillsDirectory, { withFileTypes: true });
const skills = [];

for (const entry of entries) {
  if (!entry.isDirectory()) {
    continue;
  }

  const artifact = `${entry.name}/SKILL.md`;
  const bytes = await readFile(resolve(skillsDirectory, artifact));
  const content = bytes.toString("utf8");
  const frontmatter = content.match(
    /^---\r?\n([\s\S]*?)\r?\n---(?:\r?\n|$)/
  )?.[1];

  if (!frontmatter) {
    throw new Error(
      `[agent-skills] ${artifact} must begin with YAML frontmatter.`
    );
  }

  const name = parseFrontmatterString(frontmatter, "name", artifact);
  const description = parseFrontmatterString(
    frontmatter,
    "description",
    artifact
  );

  if (!skillNamePattern.test(name) || name.length > 64) {
    throw new Error(
      `[agent-skills] ${artifact} has an invalid skill name: ${JSON.stringify(name)}.`
    );
  }

  if (name !== entry.name) {
    throw new Error(
      `[agent-skills] ${artifact} name must match its directory.`
    );
  }

  if (!description.trim() || description.length > 1024) {
    throw new Error(
      `[agent-skills] ${artifact} description must contain 1–1024 characters.`
    );
  }

  skills.push({
    name,
    type: "skill-md",
    description,
    url: withDocsBase(`/.well-known/agent-skills/${name}/SKILL.md`),
    digest: `sha256:${createHash("sha256").update(bytes).digest("hex")}`,
  });
}

skills.sort((left, right) => left.name.localeCompare(right.name));

if (skills.length === 0) {
  throw new Error("[agent-skills] No public skills were found.");
}

await writeFile(
  indexPath,
  `${JSON.stringify({ $schema: schema, skills }, null, 2)}\n`,
  "utf8"
);

console.log(`[agent-skills] Indexed ${skills.length} public skill.`);
