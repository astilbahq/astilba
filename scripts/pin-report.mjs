import { execFile, execFileSync } from "node:child_process";
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
  outlastOutage,
  pullImages,
  runLogged,
  withFetchAttempts,
} from "./fetch-attempts.mjs";
import { escapeModule } from "./go-release-age.mjs";
import { platformManifests, platforms, registry } from "./image-registry.mjs";
import {
  findGoTools,
  findImages,
  findReleases,
  newestInLine,
} from "./pin-sources.mjs";
import { clip, maxAliases, renderSections } from "./report-issue.mjs";
import { osvScanner } from "./vulnerability-scan.mjs";

// Reports what the digest-pinned images and the pinned tools need: a known
// vulnerability with a fix, an upstream rebuild of a pinned tag, or a newer release in
// the same line. Pins stay as they are; a person bumps them. See
// docs/continuous-integration.md#pinned-images-and-tools.

const root = resolve(import.meta.dirname, "..");
const run = promisify(execFile);
const maxBuffer = 512 * 1024 * 1024;

export const sections = {
  problems: "Could not check",
  images: "Image updates",
  tools: "Tool updates",
  vulnerabilities: "Vulnerabilities to act on",
  other: "Other vulnerabilities",
};

// One finding per advisory group and package in OSV Scanner's report on an image.
// The fixed version comes from the advisory's range for the package's own ecosystem,
// such as Alpine:v3.23, so another release branch's fix is not shown. The CVE may be
// an alias, or inside a distribution's id such as ALPINE-CVE-2026-1.
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
        const fixed = affected
          .filter((entry) => entry.package.ecosystem === pkg.ecosystem)
          .flatMap((entry) => entry.ranges ?? [])
          .flatMap((range) => range.events ?? [])
          .flatMap((event) => event.fixed ?? []);
        const ids = [...new Set([...group.ids, ...(group.aliases ?? [])])];
        return {
          id: group.ids[0],
          cve:
            ids
              .map((id) => /(?:^|-)(CVE-\d+-\d+)$/.exec(id)?.[1])
              .find(Boolean) ?? "",
          ids,
          package: pkg.name,
          ecosystem: pkg.ecosystem ?? "",
          version: pkg.version,
          fixed: [...new Set(fixed)].join(", "),
          severity: group.max_severity ?? "",
          summary:
            advisories.find((advisory) => advisory.summary)?.summary ?? "",
        };
      }),
    );
}

// Steps that fetch outlast an outage, as downloads do, until one has failed for that
// long. The rest of the run then tries each step a few times, so a broken registry or
// scanner cannot keep the job past its time limit before it writes the report.
let outlasted = false;
const patience = (attempt, elapsed) => {
  if (outlasted) return [20_000, 40_000][attempt];
  const wait = outlastOutage(attempt, elapsed);
  outlasted = wait === undefined;
  return wait;
};
// Checks start only until this point, and every step ends by it, so whatever hangs the
// report is written within the job's two hours.
let deadline = Infinity;
const remaining = () => Math.max(deadline - Date.now(), 1);
const runMinutes = 90;

const short = (digest) => digest.slice(0, 19);
const where = (pin) =>
  `${pin.runtime ? "runtime" : "CI"}: ${pin.paths.slice(0, 3).join(", ")}${pin.paths.length > 3 ? ` and ${pin.paths.length - 3} more` : ""}`;
const score = ({ severity }) => Number(severity) || 0;
// CVSS bands, with an advisory that has no score, as most Go ones, apart.
const band = (finding) =>
  !finding.severity
    ? "unscored"
    : score(finding) >= 9
      ? "critical"
      : score(finding) >= 7
        ? "high"
        : "lower";

function describe(finding) {
  const label = finding.id.endsWith(finding.cve)
    ? finding.id
    : `${finding.cve} ${finding.id}`;
  return [
    `${label} in ${finding.package} ${finding.version}`,
    finding.fixed ? `, fixed in ${finding.fixed}` : ", no fix listed",
    finding.severity ? `, CVSS ${finding.severity}` : "",
  ].join("");
}

function counted(findings) {
  const bands = Object.groupBy(findings, band);
  return ["critical", "high", "unscored", "lower"]
    .filter((name) => bands[name])
    .map((name) => `${bands[name].length} ${name}`)
    .join(", ");
}

// One finding per advisory, package and version across an image's platforms, worst
// first.
function mergeScans(scans) {
  const merged = new Map();
  for (const { platform, findings } of scans)
    for (const finding of findings) {
      const key = `${finding.id}|${finding.package}|${finding.version}`;
      const current = merged.get(key) ?? { ...finding, platforms: new Set() };
      current.platforms.add(platform);
      merged.set(key, current);
    }
  return [...merged.values()].toSorted(
    (a, b) =>
      score(b) - score(a) ||
      Boolean(b.fixed) - Boolean(a.fixed) ||
      a.id.localeCompare(b.id),
  );
}

// Turns what was found into report entries. Each problem has a key that stays the same
// while it does, so the issue says only what is new, and a line of its own for that
// notice; the issue's body shows one summary line per image instead. Anything that was
// not checked is itself a problem, so a gap cannot pass as a clean report.
//
// What to act on: an update, and a fixable vulnerability in a runtime image that is
// critical, high or unscored. The rest is counted for information: a CI image runs
// only fixtures, and an advisory without a fix leaves nothing to do but wait.
export function assemble({ images, tools }) {
  const problems = [];
  const summary = [];
  const info = [];
  const problem = (section, key, text, { summarized, aliases } = {}) => {
    // A check that did not run leaves what it would have found unknown.
    const check = section === sections.problems;
    problems.push({
      section,
      key,
      text,
      ...(check && { check }),
      ...(aliases && { aliases }),
    });
    if (!summarized) summary.push({ section, text });
  };
  for (const pin of images) {
    const name = `${pin.image}:${pin.tag}`;
    for (const error of pin.errors)
      problem(
        sections.problems,
        `image-error|${pin.image}|${pin.digest}|${error.step}`,
        `${name}: ${error.step} failed: ${error.message}`,
      );
    if (pin.moved)
      problem(
        sections.images,
        `image-moved|${pin.image}|${pin.digest}|${pin.moved}`,
        `${name} was rebuilt upstream: the tag now points to ${short(pin.moved)}, pinned at ${short(pin.digest)} (${where(pin)})`,
      );
    if (pin.newer)
      problem(
        sections.images,
        `image-newer|${pin.image}|${pin.tag}|${pin.newer}`,
        `${name} has a newer release in its line: ${pin.newer} (${where(pin)})`,
      );
    const findings = mergeScans(pin.scans);
    const fixable = findings.filter(({ fixed }) => fixed);
    const act = pin.runtime
      ? fixable.filter((finding) => band(finding) !== "lower")
      : [];
    const label = `[${pin.runtime ? "runtime" : "CI"}] ${name}`;
    const vulnerable = (finding, id) =>
      `image-vulnerable|${pin.image}|${pin.digest}|${id}|${finding.package}|${finding.version}`;
    for (const finding of act)
      problem(
        sections.vulnerabilities,
        vulnerable(finding, finding.id),
        `${label} (${[...finding.platforms].join(", ")}): ${describe(finding)}${finding.summary ? `: ${finding.summary}` : ""}`,
        {
          summarized: true,
          // Another id joining the advisory's group can become its first.
          aliases: finding.ids
            .filter((id) => id !== finding.id)
            .toSorted()
            .slice(0, maxAliases)
            .map((id) => vulnerable(finding, id)),
        },
      );
    const unfixed = findings.length - fixable.length;
    const rest = [
      fixable.length > act.length &&
        `${fixable.length - act.length} more with a fix (${counted(fixable.filter((finding) => !act.includes(finding)))})`,
      unfixed &&
        `${unfixed} without a fix (${counted(findings.filter(({ fixed }) => !fixed))})`,
    ].filter(Boolean);
    const worst = `Worst: ${(act.length ? act : findings)
      .slice(0, 3)
      .map(describe)
      .join("; ")}`;
    if (act.length)
      summary.push({
        section: sections.vulnerabilities,
        text: `${label}: ${act.length} to act on (${counted(act)})${rest.length ? `, and ${rest.join(", ")}` : ""}. ${worst}`,
      });
    else if (findings.length)
      info.push({
        section: sections.other,
        text: `${label}: ${rest.join(", ")}. ${worst}`,
      });
    for (const { platform } of pin.scans.filter(({ empty }) => empty))
      info.push({
        section: sections.other,
        text: `${label} (${platform}): OSV Scanner found no packages it can read, as in an image of one static binary`,
      });
  }
  for (const tool of tools) {
    if (tool.error)
      problem(
        sections.problems,
        `tool-error|${tool.name}|${tool.version}`,
        `${tool.name}: ${tool.error}`,
      );
    else if (tool.newer)
      problem(
        sections.tools,
        `tool-newer|${tool.name}|${tool.version}|${tool.newer}`,
        `${tool.name} ${tool.version} has a newer release in its line: ${tool.newer}${tool.published ? `, published ${tool.published.slice(0, 10)}` : ""} (${tool.paths.join(", ")})`,
      );
  }
  return { problems, summary, info };
}

async function scanImage(scanner, pin, { platform, digest }, directory) {
  const ref = `${pin.registry}/${pin.repository}@${digest}`;
  const archive = join(directory, "image.tar");
  try {
    await pullImages([ref], ["docker"], {
      delay: patience,
      limitMs: remaining(),
    });
    await runLogged("docker", ["save", "--output", archive, ref], {
      timeoutMs: remaining(),
    });
    // Exit status 1 means findings; anything else is a scan that did not complete.
    // The scan asks OSV's API, so it outlasts an outage as downloads do.
    const stdout = await withFetchAttempts(
      `Scanning ${ref}`,
      (timeoutMs) =>
        run(
          scanner,
          ["scan", "image", "--archive", archive, "--format", "json"],
          { maxBuffer, timeout: Number.isFinite(timeoutMs) ? timeoutMs : 0 },
        ).then(
          (result) => result.stdout,
          (error) => {
            if (error.code === 1) return error.stdout;
            // An image of one static binary, such as busybox, has nothing to read.
            if (/No package sources found/.test(error.stderr ?? "")) return "";
            throw new FetchFailure(
              error.stderr?.trim().split("\n").at(-1) || error.message,
            );
          },
        ),
      { delay: patience, limitMs: remaining() },
    );
    if (!stdout) return { platform, findings: [], empty: true };
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
    const published = listed.find(
      ({ tag_name }) => tag_name === newer,
    )?.published_at;
    return { ...release, name, newer, published };
  } catch (error) {
    return {
      ...release,
      name,
      error: `checking releases failed: ${error.message}`,
    };
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
    return {
      name,
      version,
      paths,
      error: `checking the Go proxy failed: ${error.message}`,
    };
  }
}

function trackedFiles() {
  return execFileSync("git", ["ls-files", "-z"], {
    cwd: root,
    encoding: "utf8",
  })
    .split("\0")
    .filter(Boolean)
    .flatMap((path) => {
      try {
        const bytes = readFileSync(join(root, path));
        return bytes.includes(0)
          ? []
          : [{ path, text: bytes.toString("utf8") }];
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
  deadline = Date.now() + runMinutes * 60_000;
  const client = registry(fetch, { delay: patience, remaining });
  const scanner = await osvScanner();
  const directory = mkdtempSync(join(tmpdir(), "astilba-pin-report-"));
  const images = [];
  try {
    // One at a time, so the runner's disk holds one image.
    for (const pin of pins) {
      console.log(`Checking ${pin.image}:${pin.tag}@${short(pin.digest)}`);
      images.push(
        Date.now() < deadline
          ? await checkImage(client, scanner, pin, directory)
          : {
              ...pin,
              scans: [],
              errors: [
                {
                  step: "checking",
                  message: `not started within the run's ${runMinutes} minutes`,
                },
              ],
            },
      );
    }
  } finally {
    rmSync(directory, { recursive: true, force: true });
  }
  const tools = [
    ...(await Promise.all(goTools.map(checkGoTool))),
    ...(await Promise.all(releases.map(checkRelease))),
  ];
  const report = assemble({ images, tools });
  // The run's summary lists every finding; the issue shows only the summary lines.
  const text = renderSections({
    problems: report.summary,
    info: [
      ...report.info,
      ...images.flatMap((pin) =>
        mergeScans(pin.scans).map((finding) => ({
          section: "Every finding",
          text: `[${pin.runtime ? "runtime" : "CI"}] ${pin.image}:${pin.tag}: ${describe(finding)}`,
        })),
      ),
    ],
  });
  console.log(
    text ||
      "Every pinned image and tool is current, with no known vulnerability.",
  );
  // GitHub refuses a step summary over 1 MiB.
  if (process.env.GITHUB_STEP_SUMMARY)
    appendFileSync(
      process.env.GITHUB_STEP_SUMMARY,
      clip(
        `## Pinned images and tools\n\n${text || "Nothing to report."}\n`,
        900_000,
        "\n\n…cut to fit the summary. The job's log has the rest.\n",
      ),
    );
  if (process.env.PIN_REPORT)
    writeFileSync(process.env.PIN_REPORT, JSON.stringify(report));
  if (report.problems.length) process.exitCode = 1;
}
