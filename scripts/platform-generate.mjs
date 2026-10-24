import { execFileSync } from "node:child_process";
import {
  mkdir,
  mkdtemp,
  readdir,
  readFile,
  rm,
  writeFile,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, relative, resolve } from "node:path";
import { pnpm } from "./platform-tools.mjs";

const root = resolve(import.meta.dirname, "..");
const check = process.argv.includes("--check");
const temporary = await mkdtemp(join(tmpdir(), "astilba-generation-"));
const run = (command, args, cwd) =>
  execFileSync(command, args, { cwd, stdio: "inherit" });
try {
  const module = join(root, "services/platform");
  const config = await readFile(join(module, "sqlc.yaml"), "utf8");
  await writeFile(
    join(temporary, "sqlc.yaml"),
    config
      .replace(
        "schema: db/migrations",
        `schema: "${relative(temporary, module)}/db/migrations"`,
      )
      .replace(
        "queries: db/queries",
        `queries: "${relative(temporary, module)}/db/queries"`,
      )
      .replace("out: db/generated", `out: "sqlc"`),
  );
  run(
    "go",
    ["tool", "sqlc", "generate", "-f", join(temporary, "sqlc.yaml")],
    module,
  );
  const apiConfig = (
    await readFile(join(root, "contracts/platform/oapi-codegen.yaml"), "utf8")
  ).replace("output: api.gen.go", `output: ${temporary}/api.gen.go`);
  await writeFile(join(temporary, "oapi.yaml"), apiConfig);
  run(
    "go",
    [
      "tool",
      "oapi-codegen",
      "-config",
      join(temporary, "oapi.yaml"),
      join(root, "contracts/platform/openapi.yaml"),
    ],
    module,
  );
  pnpm(
    [
      "--filter",
      "@astilba/api-contract",
      "exec",
      "openapi-typescript",
      "openapi.yaml",
      "-o",
      join(temporary, "schema.d.ts"),
    ],
    { cwd: root, stdio: "inherit" },
  );
  const files = [
    [join(temporary, "api.gen.go"), join(module, "internal/api/api.gen.go")],
    [
      join(temporary, "schema.d.ts"),
      join(root, "apps/platform/src/api/generated/schema.d.ts"),
    ],
  ];
  for (const name of await readdir(join(temporary, "sqlc")))
    files.push([
      join(temporary, "sqlc", name),
      join(module, "db/generated", name),
    ]);
  for (const [source, destination] of files) {
    const generated = await readFile(source);
    if (check) {
      if (!generated.equals(await readFile(destination)))
        throw new Error(
          `Stale generated file: ${destination.slice(root.length + 1)}`,
        );
    } else {
      await mkdir(resolve(destination, ".."), { recursive: true });
      await writeFile(destination, generated);
    }
  }
  console.log(
    check ? "Generated files are current." : "Generated files updated.",
  );
} finally {
  await rm(temporary, { recursive: true, force: true });
}
