import { execFileSync } from "node:child_process";
import { readFile } from "node:fs/promises";
import { expect } from "@playwright/test";

export async function authFixtureSQL(statement: string) {
  const fixture: { host: string; container: string; owner: string } =
    JSON.parse(
      await readFile(
        new URL(
          "../../../.artifacts/p8/browser/hosted-fixture.json",
          import.meta.url,
        ),
        "utf8",
      ),
    );
  const ownership = execFileSync(
    "docker",
    [
      "--host",
      fixture.host,
      "inspect",
      "--format",
      '{{ index .Config.Labels "com.astilba.p1-test" }}',
      fixture.container,
    ],
    { encoding: "utf8" },
  ).trim();
  expect(fixture.host).toBe(process.env.PLAT_APP_DOCKER_HOST);
  expect(ownership).toBe(fixture.owner);
  execFileSync(
    "docker",
    [
      "--host",
      fixture.host,
      "exec",
      fixture.container,
      "psql",
      "--username",
      "postgres",
      "--dbname",
      "postgres",
      "-v",
      "ON_ERROR_STOP=1",
      "-c",
      statement,
    ],
    { stdio: "pipe" },
  );
}
