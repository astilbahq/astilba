import { readFile } from "node:fs/promises";
import { pathToFileURL } from "node:url";

import {
  buildReleasePlan,
  verifyReleaseEvidence,
} from "../../packages/env/scripts/github-release.mjs";
import { releaseVersion } from "./identity.mjs";

// Keep the legacy verifier's exact asset/state/byte rules; validate the namespace before adapting its v-tag input.
const legacyRelease = (source, tag) => {
  const version = releaseVersion("env", tag);
  const release = JSON.parse(source);
  if (release?.tag_name !== tag) {
    throw new Error(
      "GitHub release does not have the exact Env namespaced tag."
    );
  }
  return JSON.stringify({ ...release, tag_name: `v${version}` });
};

export const planNamespacedEnvRelease = async (options) => {
  const version = releaseVersion("env", options.expectedTag);
  const result = await buildReleasePlan({
    artifactDirectory: options.artifactDirectory,
    expectedTag: `v${version}`,
    releaseSource: legacyRelease(options.releaseSource, options.expectedTag),
  });
  return { ...result, tag: options.expectedTag };
};

export const verifyNamespacedEnvRelease = async (options) => {
  const version = releaseVersion("env", options.expectedTag);
  const result = await verifyReleaseEvidence({
    ...options,
    afterSource: legacyRelease(options.afterSource, options.expectedTag),
    beforeSource: legacyRelease(options.beforeSource, options.expectedTag),
    expectedTag: `v${version}`,
  });
  return { ...result, tag: options.expectedTag };
};

if (
  process.argv[1] &&
  import.meta.url === pathToFileURL(process.argv[1]).href
) {
  const [command, ...args] = process.argv.slice(2);
  if (command === "plan" && args.length === 3) {
    const [source, expectedTag, artifactDirectory] = args;
    process.stdout.write(
      `${JSON.stringify(await planNamespacedEnvRelease({ artifactDirectory, expectedTag, releaseSource: await readFile(source, "utf-8") }))}\n`
    );
  } else if (command === "verify" && args.length === 5) {
    const [before, after, expectedTag, artifactDirectory, downloadedDirectory] =
      args;
    process.stdout.write(
      `${JSON.stringify(await verifyNamespacedEnvRelease({ afterSource: await readFile(after, "utf-8"), artifactDirectory, beforeSource: await readFile(before, "utf-8"), downloadedDirectory, expectedTag }))}\n`
    );
  } else {
    throw new Error(
      "Usage: env-github-release.mjs plan <release.json> <env/tag> <artifact> | verify <before> <after> <env/tag> <artifact> <downloaded>"
    );
  }
}
