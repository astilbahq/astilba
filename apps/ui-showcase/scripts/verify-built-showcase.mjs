import { readFile, readdir, stat } from "node:fs/promises";
import path from "node:path";

const dist = path.resolve("dist");
const readArtifact = (artifactPath) =>
  readFile(path.resolve(dist, artifactPath), "utf-8");

const [headers, index, robots, assetEntries] = await Promise.all([
  readArtifact("_headers"),
  readArtifact("index.html"),
  readArtifact("robots.txt"),
  readdir(path.resolve(dist, "assets")),
]);

for (const fragment of ['content="noindex, nofollow"']) {
  if (!index.includes(fragment)) {
    throw new Error(`Built showcase is missing ${JSON.stringify(fragment)}.`);
  }
}

for (const header of [
  "Content-Security-Policy:",
  "Permissions-Policy:",
  "Referrer-Policy:",
  "X-Robots-Tag: noindex, nofollow",
]) {
  if (!headers.includes(header)) {
    throw new Error(`Built showcase headers are missing ${header}.`);
  }
}

if (robots.trim() !== "User-agent: *\nDisallow: /") {
  throw new Error("Built showcase robots.txt must prevent indexing.");
}

const assets = await Promise.all(
  assetEntries.map(async (entry) => ({
    entry,
    stats: await stat(path.resolve(dist, "assets", entry)),
  }))
);

if (
  !assets.some(({ entry, stats }) => stats.isFile() && entry.endsWith(".js"))
) {
  throw new Error("Built showcase is missing its JavaScript asset.");
}

const scripts = assets.filter(
  ({ entry, stats }) => stats.isFile() && entry.endsWith(".js")
);
const scriptContents = await Promise.all(
  scripts.map(({ entry }) =>
    readFile(path.resolve(dist, "assets", entry), "utf-8")
  )
);
for (const fragment of ["https://astilba.com/", "Astilba home"]) {
  if (!scriptContents.some((script) => script.includes(fragment))) {
    throw new Error(
      `Built showcase scripts are missing ${JSON.stringify(fragment)}.`
    );
  }
}

if (
  !assets.some(({ entry, stats }) => stats.isFile() && entry.endsWith(".css"))
) {
  throw new Error("Built showcase is missing its CSS asset.");
}
