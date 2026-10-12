import { createHash } from "node:crypto";
import { readFile } from "node:fs/promises";
import { resolve } from "node:path";

import { jsonResponse } from "../../../discovery";

export const prerender = true;

const skillDefinitions = [
  {
    description:
      "Consult Astilba's public Cache documentation to explain or evaluate the unreleased TypeScript cache preview without inventing installation or production support.",
    name: "astilba-cache-docs",
  },
  {
    description:
      "Consult Astilba's public Env documentation to integrate the 0.3 configuration contract compiler without weakening browser, server, platform, inventory, or lifecycle boundaries.",
    name: "astilba-env-docs",
  },
] as const;
const skills = await Promise.all(
  skillDefinitions.map(async ({ description, name }) => {
    const skillPath = resolve(
      process.cwd(),
      `../public/.well-known/agent-skills/${name}/SKILL.md`
    );
    const skill = await readFile(skillPath);

    return {
      description,
      digest: `sha256:${createHash("sha256").update(skill).digest("hex")}`,
      name,
      type: "skill-md",
      url: `/docs/.well-known/agent-skills/${name}/SKILL.md`,
    };
  })
);

export const GET = (): Response =>
  jsonResponse({
    $schema: "https://schemas.agentskills.io/discovery/0.2.0/schema.json",
    skills,
  });
