import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { pathToFileURL } from "node:url";

const dependencyFields = ["dependencies", "devDependencies"];
// openapi-typescript 7 can't load TypeScript 7. The contract keeps its own compiler, as
// an alias because Dependabot skips aliases but merges two plain pins of one package and
// would move this one to 7. It goes once the generator supports the catalog's version.
const generatorCompiler = ({ path, name, version }) =>
  path === "contracts/platform/package.json" &&
  name === "typescript" &&
  /^npm:typescript@\d+\.\d+\.\d+$/.test(version);

// Reads the block mappings and lists pnpm-workspace.yaml uses. Anything else is
// refused, because a value this can't read would otherwise go unchecked.
export function parseWorkspace(text) {
  const lines = [];
  for (const [index, raw] of text.split("\n").entries()) {
    const line = raw.replace(/(^|\s)#.*$/, "").trimEnd();
    if (line)
      lines.push({
        number: index + 1,
        indent: line.search(/\S/),
        content: line.trimStart(),
      });
  }
  let next = 0;
  const refuse = ({ number }) => {
    throw new Error(`Unread pnpm-workspace.yaml line ${number}`);
  };
  const block = (indent) => {
    const list = lines[next].content.startsWith("- ");
    const value = list ? [] : {};
    while (lines[next]?.indent === indent) {
      const line = lines[next++];
      if (list) {
        if (!line.content.startsWith("- ")) refuse(line);
        value.push(unquote(line.content.slice(2)));
        continue;
      }
      const entry = /^('[^']*'|"[^"]*"|[^\s:'"][^:]*?):(?:\s+(.*))?$/.exec(
        line.content,
      );
      if (!entry || /^[[{&*|>!]/.test(entry[2] ?? "")) refuse(line);
      if (entry[2] !== undefined) value[unquote(entry[1])] = unquote(entry[2]);
      else if (lines[next]?.indent > indent)
        value[unquote(entry[1])] = block(lines[next].indent);
      else refuse(line);
    }
    if (lines[next]?.indent > indent) refuse(lines[next]);
    return value;
  };
  const root = lines.length ? block(0) : {};
  if (next < lines.length) refuse(lines[next]);
  return root;
}

function unquote(value) {
  return /^(['"]).*\1$/.test(value) ? value.slice(1, -1) : value;
}

// The `packages:` section of a pnpm lockfile, as `name@version` to its raw lines.
function lockfilePackages(lockfile) {
  const start = lockfile.indexOf("\npackages:\n");
  const end = lockfile.indexOf("\nsnapshots:\n");
  if (start < 0 || end < start)
    throw new Error("Unread pnpm-lock.yaml: no packages section");
  const packages = new Map();
  let current;
  for (const line of lockfile.slice(start, end).split("\n")) {
    const header = /^ {2}'?([^'\s]+@[^'\s]+?)'?:$/.exec(line);
    if (header) packages.set((current = header[1]), []);
    else if (current && line.startsWith("    "))
      packages.get(current).push(line.trim());
  }
  return packages;
}

// Every version written down, in the catalog or in a manifest.
function allPins(catalog, manifests) {
  const pins = Object.entries(catalog).map(([name, version]) => ({
    path: "pnpm-workspace.yaml",
    name,
    version,
  }));
  for (const [path, manifest] of Object.entries(manifests))
    for (const field of dependencyFields)
      for (const [name, version] of Object.entries(manifest[field] ?? {}))
        if (version !== "catalog:") pins.push({ path, name, version });
  return pins;
}

export function pinProblems({ workspace, lockfile, manifests, workflows }) {
  const problems = [];
  const catalog = workspace.catalog ?? {};
  const pins = allPins(catalog, manifests);
  const packages = lockfilePackages(lockfile);

  // A manifest holding its own version would drift away from the catalog unseen.
  for (const pin of pins)
    if (
      pin.path !== "pnpm-workspace.yaml" &&
      catalog[pin.name] !== undefined &&
      !generatorCompiler(pin)
    )
      problems.push(
        `${pin.path}: ${pin.name} must be "catalog:", found ${pin.version}`,
      );

  const vitePlus = catalog["vite-plus"];
  const core = `npm:@voidzero-dev/vite-plus-core@${vitePlus}`;
  if (catalog.vite !== core)
    problems.push(
      `pnpm-workspace.yaml: catalog vite must be ${core}, found ${catalog.vite}`,
    );
  const overrides = workspace.overrides ?? {};
  if (overrides["vite@*"] !== "catalog:")
    problems.push(
      `pnpm-workspace.yaml: override vite@* must be "catalog:", found ${overrides["vite@*"]}`,
    );
  const allowed = workspace.peerDependencyRules?.allowedVersions ?? {};
  if (allowed.vite !== vitePlus)
    problems.push(
      `pnpm-workspace.yaml: allowed peer vite must be ${vitePlus}, found ${allowed.vite}`,
    );

  // The lockfile shows the override rather than what Vite+ wants from vitest, but
  // Vite+ peers on the browser runner of the same release as its bundled one.
  const runner = packages
    .get(`vite-plus@${vitePlus}`)
    ?.map((line) => /^'@vitest\/browser-playwright': (\S+)$/.exec(line)?.[1])
    .find(Boolean);
  if (!runner)
    problems.push(
      `pnpm-lock.yaml: can't read the Vite+ ${vitePlus} runner version`,
    );
  else if (overrides["vitest@*"] !== runner)
    problems.push(
      `pnpm-workspace.yaml: override vitest@* must be ${runner} for Vite+ ${vitePlus}, found ${overrides["vitest@*"]}`,
    );

  // A selector for another Storybook major matches nothing, and pnpm says so only as
  // a warning on a fresh resolve.
  const storybook = pins.find(({ name }) => name === "storybook")?.version;
  const selectors = Object.keys(allowed).filter((key) =>
    key.endsWith(">vite-plus"),
  );
  if (storybook && selectors.length !== 1)
    problems.push(
      `pnpm-workspace.yaml: expected one storybook@<major>>vite-plus peer rule, found ${selectors.length}`,
    );
  for (const selector of selectors) {
    if (allowed[selector] !== vitePlus)
      problems.push(
        `pnpm-workspace.yaml: allowed peer ${selector} must be ${vitePlus}, found ${allowed[selector]}`,
      );
    if (
      storybook &&
      selector !== `storybook@${storybook.split(".")[0]}>vite-plus`
    )
      problems.push(
        `pnpm-workspace.yaml: peer rule ${selector} doesn't match Storybook ${storybook}`,
      );
  }

  for (const [label, matches] of [
    ["@pandacss/*", (name) => name.startsWith("@pandacss/")],
    [
      "Storybook",
      (name) => name === "storybook" || name.startsWith("@storybook/"),
    ],
  ]) {
    const members = pins.filter(({ name }) => matches(name));
    if (new Set(members.map(({ version }) => version)).size > 1)
      problems.push(
        `${label} versions differ: ${members.map(({ path, name, version }) => `${name} ${version} in ${path}`).join(", ")}`,
      );
  }

  const images = new Set();
  for (const [path, text] of Object.entries(workflows))
    for (const [reference] of text.matchAll(
      /mcr\.microsoft\.com\/playwright\S*/g,
    )) {
      images.add(reference);
      const parsed = /:v([^-@]+)-[a-z]+@sha256:[0-9a-f]{64}$/.exec(reference);
      if (!parsed)
        problems.push(`${path}: ${reference} needs a version tag and a digest`);
      else if (parsed[1] !== catalog["@playwright/test"])
        problems.push(
          `${path}: ${reference} doesn't match @playwright/test ${catalog["@playwright/test"]}`,
        );
    }
  if (images.size === 0)
    problems.push(".github/workflows: no Playwright image found");
  if (images.size > 1)
    problems.push(
      `.github/workflows: Playwright images differ: ${[...images].join(", ")}`,
    );

  // Each approval names one release, so a new release's scripts need a fresh decision.
  // Lockfile keys are all name@exact, so a bare name or a range can't match.
  for (const key of Object.keys(workspace.allowBuilds ?? {}))
    if (!packages.has(key))
      problems.push(
        `pnpm-workspace.yaml: allowBuilds ${key} must be an exact version in pnpm-lock.yaml`,
      );
  return problems;
}

function read(root) {
  const text = (path) => readFileSync(join(root, path), "utf8");
  const workspace = parseWorkspace(text("pnpm-workspace.yaml"));
  const manifests = {};
  for (const directory of [".", ...workspace.packages]) {
    const path =
      directory === "." ? "package.json" : `${directory}/package.json`;
    manifests[path] = JSON.parse(text(path));
  }
  const workflows = {};
  for (const name of readdirSync(join(root, ".github/workflows")))
    workflows[`.github/workflows/${name}`] = text(`.github/workflows/${name}`);
  return { workspace, lockfile: text("pnpm-lock.yaml"), manifests, workflows };
}

if (
  process.argv[1] &&
  import.meta.url === pathToFileURL(process.argv[1]).href
) {
  const problems = pinProblems(read(process.cwd()));
  if (problems.length) {
    console.error(problems.join("\n"));
    console.error(
      "See docs/frontend-development.md#dependency-pins for how these pins move together.",
    );
    process.exit(1);
  }
  console.log("Dependency pins are aligned.");
}
