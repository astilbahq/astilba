import { execFile, execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import {
  appendFileSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { pathToFileURL } from "node:url";
import { promisify } from "node:util";
import {
  download,
  FetchFailure,
  pullImages,
  runLogged,
  withFetchAttempts,
} from "./fetch-attempts.mjs";
import { escapeModule } from "./go-release-age.mjs";
import { renderSections } from "./report-issue.mjs";
import { osvScanner } from "./vulnerability-scan.mjs";

// Reports what the digest-pinned images and the pinned tools need: a known
// vulnerability with a fix, an upstream rebuild of a pinned tag, or a newer release in
// the same line. Pins stay as they are; a person bumps them. See
// docs/continuous-integration.md#pinned-images-and-tools.

const root = resolve(import.meta.dirname, "..");
const run = promisify(execFile);
const maxBuffer = 512 * 1024 * 1024;
const maximumAllowlistDays = 90;
const imageEntryFields = ["expires", "id", "image", "package", "reason"];
// Every host and CI runner is one of these.
const platforms = ["linux/amd64", "linux/arm64"];
const manifestTypes = [
  "application/vnd.oci.image.index.v1+json",
  "application/vnd.docker.distribution.manifest.list.v2+json",
  "application/vnd.oci.image.manifest.v1+json",
  "application/vnd.docker.distribution.manifest.v2+json",
].join(", ");

export const sections = {
  problems: "Could not check",
  images: "Image updates",
  tools: "Tool updates",
  vulnerabilities: "Vulnerabilities with a fix",
  exceptions: "Exceptions to remove",
  unfixed: "Vulnerabilities without a known fix",
  allowed: "Allowlisted",
};

// name[:tag]@sha256:digest, where a first component with a dot, a colon or "localhost"
// names the registry. Markdown is left out: it describes pins rather than using them.
const reference =
  /(?<![\w./:@-])((?:(?:[a-z0-9-]+(?:\.[a-z0-9-]+)+|localhost)(?::\d+)?\/)?[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*(?:\/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*):([\w][\w.-]{0,127})@sha256:([0-9a-f]{64})(?![0-9a-f])/g;

export function parseName(name) {
  const [first, ...rest] = name.split("/");
  const host =
    rest.length && (/[.:]/.test(first) || first === "localhost")
      ? first
      : undefined;
  let repository = host ? rest.join("/") : name;
  const registry = host === "index.docker.io" ? "docker.io" : (host ?? "docker.io");
  if (registry === "docker.io" && !repository.includes("/"))
    repository = `library/${repository}`;
  const image =
    registry === "docker.io"
      ? repository.replace(/^library\//, "")
      : `${registry}/${repository}`;
  return { registry, repository, image };
}

// Code that ships in the server image or runs on hosts, as opposed to CI and tests.
const shipped = (path) =>
  path === "Dockerfile" ||
  ((path.startsWith("services/platform/") ||
    path.startsWith("apps/platform/src/")) &&
    !/(_test\.go|\.test\.[jt]sx?|\.stories\.[jt]sx?)$/.test(path) &&
    !path.includes("/testdata/"));

// Every digest-pinned image reference in the given files, one entry per digest.
export function findImages(files) {
  const pins = new Map();
  for (const { path, text } of files) {
    if (path.endsWith(".md")) continue;
    for (const [, name, tag, digest] of text.matchAll(reference)) {
      const parsed = parseName(name);
      const key = `${parsed.registry}/${parsed.repository}@${digest}`;
      const pin = pins.get(key) ?? {
        ...parsed,
        tag,
        digest: `sha256:${digest}`,
        paths: new Set(),
      };
      pin.paths.add(path);
      pins.set(key, pin);
    }
  }
  return [...pins.values()]
    .map(({ paths, ...pin }) => ({
      ...pin,
      paths: [...paths].sort(),
      runtime: [...paths].some(shipped),
    }))
    .toSorted(
      (a, b) =>
        b.runtime - a.runtime ||
        a.image.localeCompare(b.image) ||
        a.digest.localeCompare(b.digest),
    );
}

// The Go tools each module runs with `go tool`, with the module and version that
// provide them, from the module's go.mod.
export function findGoTools(modules) {
  const tools = [];
  for (const { dir, text } of modules) {
    const plain = text.replace(/\/\/.*$/gm, "");
    const block = (keyword) => [
      ...[...plain.matchAll(new RegExp(`^${keyword}\\s*\\(([^)]*)\\)`, "gm"))]
        .flatMap(([, body]) => body.split("\n")),
      ...[...plain.matchAll(new RegExp(`^${keyword}\\s+([^(\\s].*)$`, "gm"))].map(
        ([, line]) => line,
      ),
    ]
      .map((line) => line.trim().split(/\s+/))
      .filter(([first]) => first);
    const requires = block("require");
    for (const [tool] of block("tool")) {
      const [path, version] =
        requires
          .filter(([path]) => tool === path || tool.startsWith(`${path}/`))
          .toSorted((a, b) => b[0].length - a[0].length)[0] ?? [];
      if (!path) throw new Error(`${dir}/go.mod: no requirement provides ${tool}`);
      tools.push({ dir, tool, module: path, version });
    }
  }
  return tools;
}

// Release binaries fetched from GitHub with the version written in the URL or in a
// constant of the same file.
const releaseDownload =
  /https:\/\/github\.com\/([\w.-]+\/[\w.-]+)\/releases\/download\/(v?)(?:\$\{(\w+)\}|([0-9][\w.+-]*))\//g;

export function findReleases(files) {
  const releases = new Map();
  for (const { path, text } of files) {
    if (path.endsWith(".md")) continue;
    for (const [, repository, prefix, constant, literal] of text.matchAll(
      releaseDownload,
    )) {
      const version =
        literal ??
        new RegExp(`\\b(?:const|let|var)\\s+${constant}\\s*=\\s*"([^"]+)"`).exec(
          text,
        )?.[1];
      if (!version)
        throw new Error(`${path}: cannot read the version of ${repository}`);
      const key = `${repository}@${prefix}${version}`;
      const release = releases.get(key) ?? {
        repository,
        version: `${prefix}${version}`,
        paths: new Set(),
      };
      release.paths.add(path);
      releases.set(key, release);
    }
  }
  return [...releases.values()].map(({ paths, ...release }) => ({
    ...release,
    paths: [...paths].sort(),
  }));
}

// A version's line is its shape with the numbers taken out, and its first number
// unless that is a year. So 18.4-alpine3.23 can become 18.5-alpine3.24, but not 19.0
// or 18.5-trixie; v2026.10.14 can become any later date.
function versionParts(version) {
  const parts = version.split(/(\d+)/);
  return {
    shape: parts.filter((_, index) => index % 2 === 0).join("#"),
    numbers: parts.filter((_, index) => index % 2).map(Number),
  };
}

function compareNumbers(a, b) {
  for (let index = 0; index < a.length; index++)
    if (a[index] !== b[index]) return a[index] - b[index];
  return 0;
}

export function newestInLine(current, candidates) {
  const mine = versionParts(current);
  let best;
  for (const candidate of candidates) {
    const other = versionParts(candidate);
    if (
      other.shape !== mine.shape ||
      other.numbers.length !== mine.numbers.length ||
      (mine.numbers[0] < 1000 && other.numbers[0] !== mine.numbers[0]) ||
      compareNumbers(other.numbers, mine.numbers) <= 0
    )
      continue;
    if (!best || compareNumbers(other.numbers, best.numbers) > 0)
      best = { version: candidate, numbers: other.numbers };
  }
  return best?.version;
}

const addDays = (date, days) =>
  new Date(Date.parse(date) + days * 86400000).toISOString().slice(0, 10);

// Image exceptions sit beside the dependency scan's in the same file. Each names one
// advisory for one package in one image, for at most 90 days.
export function parseImageAllowlist(document, today) {
  const entries = document.images ?? [];
  if (!Array.isArray(entries))
    throw new Error("Allowlist images must be an array");
  const seen = new Set();
  return entries.map((entry) => {
    const label = `Image allowlist entry ${entry.id ?? "without id"}`;
    if (Object.keys(entry).toSorted().join() !== imageEntryFields.join())
      throw new Error(`${label} must have exactly: ${imageEntryFields.join(", ")}`);
    for (const field of ["id", "image", "package", "reason"])
      if (typeof entry[field] !== "string" || !entry[field].trim())
        throw new Error(`${label} needs a ${field}`);
    if (
      !/^\d{4}-\d{2}-\d{2}$/.test(entry.expires) ||
      addDays(entry.expires, 0) !== entry.expires
    )
      throw new Error(`${label} expires must be a YYYY-MM-DD date`);
    if (entry.expires > addDays(today, maximumAllowlistDays))
      throw new Error(
        `${label} expires more than ${maximumAllowlistDays} days from now`,
      );
    const key = `${entry.id}|${entry.image}|${entry.package}`;
    if (seen.has(key)) throw new Error(`${label} is duplicated`);
    seen.add(key);
    return entry;
  });
}

// One finding per advisory group and package in OSV Scanner's report on an image.
// The fixed version comes from the advisory's range for the package's own ecosystem,
// such as Alpine:v3.23, so another release branch's fix is not shown.
export function imageFindings(report) {
  return (report.results ?? [])
    .flatMap((result) => result.packages ?? [])
    .flatMap(({ package: pkg, vulnerabilities = [], groups = [] }) =>
      groups.map((group) => {
        const advisories = vulnerabilities.filter((advisory) =>
          group.ids.includes(advisory.id),
        );
        const affected = advisories.flatMap((advisory) =>
          (advisory.affected ?? []).filter(
            (entry) => entry.package?.name === pkg.name,
          ),
        );
        const own = affected.filter(
          (entry) => entry.package.ecosystem === pkg.ecosystem,
        );
        const fixed = (own.length ? own : affected)
          .flatMap((entry) => entry.ranges ?? [])
          .flatMap((range) => range.events ?? [])
          .flatMap((event) => event.fixed ?? []);
        const ids = [...new Set([...group.ids, ...(group.aliases ?? [])])];
        return {
          id: group.ids[0],
          cve: ids.find((id) => /^CVE-\d+-\d+$/.test(id)) ?? "",
          ids,
          package: pkg.name,
          ecosystem: pkg.ecosystem ?? "",
          version: pkg.version,
          fixed: [...new Set(fixed)].join(", "),
          severity: group.max_severity ?? "",
          summary: advisories.find((advisory) => advisory.summary)?.summary ?? "",
        };
      }),
    );
}

const short = (digest) => digest.slice(0, 19);
const where = (pin) => `${pin.runtime ? "runtime" : "CI"}: ${pin.paths.join(", ")}`;

// Turns what was found into report entries. A problem has a key that stays the same
// while it does, so the issue comments only on what is new; anything that was not
// checked is itself a problem, so a gap cannot pass as a clean report.
export function assemble({ images, tools, entries, today }) {
  const problems = [];
  const info = [];
  const problem = (section, key, text) => problems.push({ section, key, text });
  const used = new Set();
  const unscanned = new Set();
  for (const pin of images) {
    const name = `${pin.image}:${pin.tag}`;
    for (const error of pin.errors)
      problem(sections.problems, `error|${name}|${pin.digest}|${error.step}`, `${name}: ${error.step} failed: ${error.message}`);
    if (pin.errors.some(({ step }) => step.startsWith("scan")))
      unscanned.add(pin.image);
    if (pin.moved)
      problem(
        sections.images,
        `moved|${name}|${pin.moved}`,
        `${name} was rebuilt upstream: the tag now points to ${short(pin.moved)}, pinned at ${short(pin.digest)} (${where(pin)})`,
      );
    if (pin.newer)
      problem(
        sections.images,
        `newer|${pin.image}|${pin.tag}|${pin.newer}`,
        `${name} has a newer release in its line: ${pin.newer} (${where(pin)})`,
      );
    const merged = new Map();
    for (const { platform, findings } of pin.scans)
      for (const finding of findings) {
        const key = `${finding.id}|${finding.package}|${finding.version}`;
        const current = merged.get(key) ?? { ...finding, platforms: [] };
        current.platforms.push(platform);
        merged.set(key, current);
      }
    const findings = [...merged.values()].toSorted(
      (a, b) =>
        (Number(b.severity) || 0) - (Number(a.severity) || 0) ||
        a.id.localeCompare(b.id),
    );
    for (const finding of findings) {
      const entry = entries.find(
        (candidate) =>
          candidate.image === pin.image &&
          candidate.package === finding.package &&
          finding.ids.includes(candidate.id),
      );
      const label = [finding.cve, finding.cve === finding.id ? "" : finding.id]
        .filter(Boolean)
        .join(" ");
      const text = [
        `[${pin.runtime ? "runtime" : "CI"}] ${name} (${finding.platforms.join(", ")}): ${label} in ${finding.package} ${finding.version}`,
        finding.fixed ? `, fixed in ${finding.fixed}` : ", no fix listed",
        finding.severity ? `, CVSS ${finding.severity}` : "",
        finding.summary ? `: ${finding.summary}` : "",
      ].join("");
      if (entry) used.add(entry);
      if (entry && entry.expires >= today)
        info.push({
          section: sections.allowed,
          text: `${text} (allowlisted until ${entry.expires}: ${entry.reason})`,
        });
      else if (entry)
        problem(
          sections.exceptions,
          `expired|${entry.id}|${entry.image}|${entry.package}`,
          `${text} (allowlist entry expired ${entry.expires})`,
        );
      else if (finding.fixed)
        problem(
          sections.vulnerabilities,
          `vulnerable|${pin.image}|${pin.digest}|${finding.id}|${finding.package}|${finding.version}`,
          text,
        );
      else info.push({ section: sections.unfixed, text });
    }
  }
  for (const entry of entries)
    if (!used.has(entry) && !unscanned.has(entry.image))
      problem(
        sections.exceptions,
        `stale|${entry.id}|${entry.image}|${entry.package}`,
        `Image allowlist entry ${entry.id} ${entry.package} in ${entry.image} matches no finding, remove it`,
      );
  for (const tool of tools) {
    if (tool.error)
      problem(sections.problems, `error|${tool.name}`, `${tool.name}: ${tool.error}`);
    else if (tool.newer)
      problem(
        sections.tools,
        `newer|${tool.name}|${tool.version}|${tool.newer}`,
        `${tool.name} ${tool.version} has a newer release in its line: ${tool.newer}${tool.published ? `, published ${tool.published.slice(0, 10)}` : ""} (${tool.paths.join(", ")})`,
      );
  }
  return { problems, info };
}

const sha256 = (bytes) =>
  `sha256:${createHash("sha256").update(bytes).digest("hex")}`;

// Asks a registry's distribution API as an anonymous client, taking the bearer token
// its challenge offers. A dropped connection, a rate limit or a server error repeats
// as other downloads do.
export function registry(fetchImpl = fetch) {
  const tokens = new Map();
  const host = (name) =>
    name === "docker.io" ? "registry-1.docker.io" : name;
  const attempt = async (url, init) => {
    let response;
    try {
      response = await fetchImpl(url, init);
    } catch (error) {
      throw new FetchFailure(error.cause?.message || error.message);
    }
    if (response.status === 429 || response.status >= 500) {
      await response.body?.cancel();
      throw new FetchFailure(`${url} returned ${response.status}`);
    }
    return response;
  };
  async function token(challenge, repository) {
    const fields = Object.fromEntries(
      [...challenge.matchAll(/(\w+)="([^"]*)"/g)].map(([, key, value]) => [
        key,
        value,
      ]),
    );
    if (!/^bearer /i.test(challenge) || !fields.realm?.startsWith("https://"))
      throw new Error(`unexpected registry challenge: ${challenge}`);
    const url = new URL(fields.realm);
    if (fields.service) url.searchParams.set("service", fields.service);
    url.searchParams.set("scope", fields.scope ?? `repository:${repository}:pull`);
    const response = await attempt(url, {});
    if (!response.ok)
      throw new Error(`${url.origin} refused a token: ${response.status}`);
    const body = await response.json();
    return body.token ?? body.access_token;
  }
  async function request(pin, path, { method = "GET", accept } = {}) {
    const url = `https://${host(pin.registry)}/v2/${pin.repository}/${path}`;
    const key = `${pin.registry}/${pin.repository}`;
    return withFetchAttempts(`${method} ${url}`, async () => {
      for (let tries = 0; ; tries++) {
        const headers = { ...(accept && { accept }) };
        if (tokens.has(key)) headers.authorization = `Bearer ${tokens.get(key)}`;
        const response = await attempt(url, { method, headers });
        if (response.status === 401 && tries === 0) {
          await response.body?.cancel();
          tokens.set(
            key,
            await token(response.headers.get("www-authenticate") ?? "", pin.repository),
          );
          continue;
        }
        if (!response.ok) {
          await response.body?.cancel();
          throw new Error(`${url} returned ${response.status}`);
        }
        return response;
      }
    }, { delay: (_attempt, elapsed) => (elapsed < 240_000 ? 15_000 : undefined) });
  }
  return {
    // A manifest by digest is checked against that digest, so the registry cannot
    // describe different contents.
    async manifest(pin, digest) {
      const bytes = Buffer.from(
        await (await request(pin, `manifests/${digest}`, { accept: manifestTypes })).arrayBuffer(),
      );
      if (sha256(bytes) !== digest)
        throw new Error(`${pin.image}@${digest} returned other bytes`);
      return JSON.parse(bytes);
    },
    async tagDigest(pin) {
      const response = await request(pin, `manifests/${pin.tag}`, {
        method: "HEAD",
        accept: manifestTypes,
      });
      const digest = response.headers.get("docker-content-digest");
      if (/^sha256:[0-9a-f]{64}$/.test(digest ?? "")) return digest;
      const bytes = Buffer.from(
        await (await request(pin, `manifests/${pin.tag}`, { accept: manifestTypes })).arrayBuffer(),
      );
      return sha256(bytes);
    },
    async blob(pin, digest) {
      const bytes = Buffer.from(
        await (await request(pin, `blobs/${digest}`)).arrayBuffer(),
      );
      if (sha256(bytes) !== digest)
        throw new Error(`${pin.image} blob ${digest} returned other bytes`);
      return JSON.parse(bytes);
    },
    async tags(pin) {
      const tags = [];
      for (let path = "tags/list?n=1000"; path; ) {
        const response = await request(pin, path);
        tags.push(...((await response.json()).tags ?? []));
        const next = /<[^>]*\/tags\/list(\?[^>]*)>;\s*rel="?next"?/.exec(
          response.headers.get("link") ?? "",
        )?.[1];
        path = next && `tags/list${next}`;
      }
      return tags;
    },
  };
}

// The single-platform manifests a pin stands for on the platforms Astilba runs.
export async function platformManifests(client, pin, manifest) {
  if (!manifest.manifests) {
    const config = await client.blob(pin, manifest.config.digest);
    const platform = `${config.os}/${config.architecture}`;
    return platforms.includes(platform) ? [{ platform, digest: pin.digest }] : [];
  }
  return platforms.flatMap((platform) => {
    const [os, architecture] = platform.split("/");
    const entry = manifest.manifests.find(
      ({ platform: p }) =>
        p?.os === os &&
        p?.architecture === architecture &&
        (architecture !== "arm64" || [undefined, "v8"].includes(p.variant)),
    );
    return entry ? [{ platform, digest: entry.digest }] : [];
  });
}

async function scanImage(scanner, pin, { platform, digest }, directory) {
  const ref = `${pin.registry === "docker.io" ? "docker.io" : pin.registry}/${pin.repository}@${digest}`;
  const archive = join(directory, "image.tar");
  try {
    await pullImages([ref]);
    await runLogged("docker", ["save", "--output", archive, ref]);
    // Exit status 1 means findings; anything else is a scan that did not complete.
    const stdout = await withFetchAttempts(`Scanning ${ref}`, () =>
      run(scanner, ["scan", "image", "--archive", archive, "--format", "json"], {
        maxBuffer,
      }).then(
        (result) => result.stdout,
        (error) => {
          if (error.code === 1) return error.stdout;
          throw new FetchFailure(error.stderr?.trim().split("\n").at(-1) || error.message);
        },
      ),
    );
    return { platform, findings: imageFindings(JSON.parse(stdout)) };
  } finally {
    rmSync(archive, { force: true });
    await runLogged("docker", ["image", "rm", "--force", ref]).catch(() => {});
  }
}

async function checkImage(client, scanner, pin, directory) {
  const result = { ...pin, scans: [], errors: [] };
  const step = async (name, action) => {
    try {
      return await action();
    } catch (error) {
      result.errors.push({ step: name, message: error.message });
    }
  };
  const manifest = await step("reading the pinned manifest", () =>
    client.manifest(pin, pin.digest),
  );
  const targets = manifest
    ? ((await step("finding its platforms", () =>
        platformManifests(client, pin, manifest),
      )) ?? [])
    : [];
  if (manifest && !targets.length && !result.errors.length)
    result.errors.push({
      step: "finding its platforms",
      message: `no ${platforms.join(" or ")} image`,
    });
  await step("reading the tag", async () => {
    const current = await client.tagDigest(pin);
    if (current === pin.digest) return;
    // A pin of one platform's manifest is current while the tag's index holds it.
    const tagged = await client.manifest(pin, current);
    if (!tagged.manifests?.some(({ digest }) => digest === pin.digest))
      result.moved = current;
  });
  await step("listing tags", async () => {
    result.newer = newestInLine(pin.tag, await client.tags(pin));
  });
  for (const target of targets) {
    const scan = await step(`scanning ${target.platform}`, () =>
      scanImage(scanner, pin, target, directory),
    );
    if (scan) result.scans.push(scan);
  }
  return result;
}

const githubHeaders = () => ({
  accept: "application/vnd.github+json",
  ...(process.env.GITHUB_TOKEN && {
    authorization: `Bearer ${process.env.GITHUB_TOKEN}`,
  }),
});

async function checkRelease(release) {
  const name = release.repository;
  try {
    const listed = JSON.parse(
      await download(
        `https://api.github.com/repos/${release.repository}/releases?per_page=100`,
        { headers: githubHeaders() },
      ),
    ).filter(({ draft, prerelease }) => !draft && !prerelease);
    const newer = newestInLine(
      release.version,
      listed.map(({ tag_name }) => tag_name),
    );
    const published = listed.find(({ tag_name }) => tag_name === newer)?.published_at;
    return { ...release, name, newer, published };
  } catch (error) {
    return { ...release, name, error: `checking releases failed: ${error.message}` };
  }
}

async function checkGoTool({ dir, tool, module, version }) {
  const name = `${module} (go tool ${tool.split("/").at(-1)})`;
  const paths = [`${dir}/go.mod`];
  try {
    const proxy = `https://proxy.golang.org/${escapeModule(module)}/@v`;
    const versions = String(await download(`${proxy}/list`))
      .split("\n")
      .filter(Boolean);
    const newer = newestInLine(version, versions);
    const published =
      newer &&
      JSON.parse(await download(`${proxy}/${escapeModule(newer)}.info`)).Time;
    return { name, version, paths, newer, published };
  } catch (error) {
    return { name, version, paths, error: `checking the Go proxy failed: ${error.message}` };
  }
}

function trackedFiles() {
  return execFileSync("git", ["ls-files", "-z"], { cwd: root, encoding: "utf8" })
    .split("\0")
    .filter(Boolean)
    .flatMap((path) => {
      try {
        const bytes = readFileSync(join(root, path));
        return bytes.includes(0) ? [] : [{ path, text: bytes.toString("utf8") }];
      } catch {
        // A path deleted from the working tree but still in the index.
        return [];
      }
    });
}

if (
  process.argv[1] &&
  import.meta.url === pathToFileURL(process.argv[1]).href
) {
  const today = new Date().toISOString().slice(0, 10);
  const entries = parseImageAllowlist(
    JSON.parse(
      readFileSync(join(root, ".github/vulnerability-allowlist.json"), "utf8"),
    ),
    today,
  );
  const files = trackedFiles();
  const pins = findImages(files);
  const goTools = findGoTools(
    files
      .filter(({ path }) => /(^|\/)go\.mod$/.test(path))
      .map(({ path, text }) => ({ dir: dirname(path), text })),
  );
  const releases = findReleases(files);
  console.log(
    `Checking ${pins.length} pinned images, ${goTools.length} Go tools and ${releases.length} release binaries.`,
  );
  const client = registry();
  const scanner = await osvScanner();
  const directory = mkdtempSync(join(tmpdir(), "astilba-pin-report-"));
  const images = [];
  try {
    // One at a time, so the runner's disk holds one image.
    for (const pin of pins) {
      console.log(`Checking ${pin.image}:${pin.tag}@${short(pin.digest)}`);
      images.push(await checkImage(client, scanner, pin, directory));
    }
  } finally {
    rmSync(directory, { recursive: true, force: true });
  }
  const tools = [
    ...(await Promise.all(goTools.map(checkGoTool))),
    ...(await Promise.all(releases.map(checkRelease))),
  ];
  const report = assemble({ images, tools, entries, today });
  const text = renderSections(report);
  console.log(text || "Every pinned image and tool is current, with no known vulnerability.");
  if (process.env.GITHUB_STEP_SUMMARY)
    appendFileSync(
      process.env.GITHUB_STEP_SUMMARY,
      `## Pinned images and tools\n\n${text || "Nothing to report."}\n`,
    );
  if (process.env.PIN_REPORT)
    writeFileSync(process.env.PIN_REPORT, JSON.stringify(report));
  if (report.problems.length) process.exitCode = 1;
}
