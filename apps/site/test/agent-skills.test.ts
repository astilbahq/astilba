import { createHash } from "node:crypto";
import { readFile } from "node:fs/promises";

import { describe, expect, it } from "vitest";

import { GET } from "../src/pages/.well-known/agent-skills/index.json";

const skillDefinitions = [
  {
    description:
      "Consult Astilba's public Cache documentation to explain or evaluate the @astilba/cache public alpha without overstating completeness or production support.",
    name: "astilba-cache-docs",
  },
  {
    description:
      "Consult Astilba's public Env documentation to integrate the 0.3 configuration contract compiler without weakening browser, server, platform, inventory, or lifecycle boundaries.",
    name: "astilba-env-docs",
  },
] as const;

describe("Agent Skills discovery", () => {
  it("publishes every public skill with content-derived digests", async () => {
    const expectedSkills = await Promise.all(
      skillDefinitions.map(async ({ description, name }) => {
        const skill = await readFile(
          new URL(
            `../../docs/public/.well-known/agent-skills/${name}/SKILL.md`,
            import.meta.url
          )
        );

        return {
          description,
          digest: `sha256:${createHash("sha256").update(skill).digest("hex")}`,
          name,
          type: "skill-md",
          url: `/docs/.well-known/agent-skills/${name}/SKILL.md`,
        };
      })
    );

    const response = GET();

    expect(response.status).toBe(200);
    expect(response.headers.get("Content-Type")).toBe(
      "application/json; charset=utf-8"
    );
    await expect(response.json()).resolves.toEqual({
      $schema: "https://schemas.agentskills.io/discovery/0.2.0/schema.json",
      skills: expectedSkills,
    });
  });
});
