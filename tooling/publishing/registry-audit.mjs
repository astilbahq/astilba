import { createHash } from "node:crypto";
import { lstat, readFile } from "node:fs/promises";
import nodePath from "node:path";
import { pathToFileURL } from "node:url";
import { isDeepStrictEqual } from "node:util";

import { parseManifest } from "../../packages/env/scripts/artifact-manifest.mjs";
import { cutover, releaseVersion } from "./identity.mjs";
import { validateUiManifest } from "./ui-artifacts.mjs";

const { resolve } = nodePath;

const predicates = [
  "https://slsa.dev/provenance/v1",
  "https://github.com/npm/attestation/tree/main/specs/publish/v0.1",
];
const gitObject = /^[0-9a-f]{40}$/u;
const sha512 = /^[0-9a-f]{128}$/u;
const repository = `https://github.com/${cutover.repository}`;

const readStatement = (entry, predicateType) => {
  const matches = entry.attestationBundles.filter(
    (value) => value?.predicateType === predicateType
  );
  if (matches.length !== 1) {
    throw new Error("Each attestation kind must occur exactly once.");
  }
  const envelope = matches[0].bundle?.dsseEnvelope;
  if (
    envelope?.payloadType !== "application/vnd.in-toto+json" ||
    typeof envelope.payload !== "string" ||
    envelope.payload.length > 1_398_104
  ) {
    throw new Error("Verified attestation payload is missing or oversized.");
  }
  const bytes = Buffer.from(envelope.payload, "base64");
  if (bytes.toString("base64") !== envelope.payload) {
    throw new Error("Verified attestation payload is not canonical base64.");
  }
  const statement = JSON.parse(bytes.toString("utf-8"));
  const type =
    predicateType === predicates[0]
      ? "https://in-toto.io/Statement/v1"
      : "https://in-toto.io/Statement/v0.1";
  if (statement?._type !== type || statement.predicateType !== predicateType) {
    throw new Error(
      "Verified attestation statement type does not match its bundle."
    );
  }
  return statement;
};

const verifySubject = (statement, expected) => {
  if (
    !isDeepStrictEqual(statement.subject, [
      {
        digest: { sha512: expected.sha512 },
        name: `pkg:npm/${expected.name.replaceAll("@", "%40")}@${expected.version}`,
      },
    ])
  ) {
    throw new Error(
      "Signed attestation subject differs from the verified archive identity."
    );
  }
};

const verifyProvenance = (statement, product, tag, commit) => {
  const build = statement.predicate?.buildDefinition;
  const ref = `refs/tags/${tag}`;
  if (
    build?.buildType !==
      "https://slsa-framework.github.io/github-actions-buildtypes/workflow/v1" ||
    !isDeepStrictEqual(build.externalParameters?.workflow, {
      path: `.github/workflows/${cutover.products[product].workflow}`,
      ref,
      repository,
    }) ||
    !isDeepStrictEqual(build.resolvedDependencies, [
      { digest: { gitCommit: commit }, uri: `git+${repository}@${ref}` },
    ]) ||
    statement.predicate.runDetails?.builder?.id !==
      "https://github.com/actions/runner/github-hosted"
  ) {
    throw new Error(
      "Signed provenance does not match the expected monorepo workflow, tag and commit."
    );
  }
};

const verifyExpectedIdentity = (expected, product, version) => {
  const { packages } = cutover.products[product];
  if (
    !gitObject.test(expected?.commit) ||
    !Array.isArray(expected.packages) ||
    expected.packages.length !== packages.length ||
    expected.packages.some(
      (entry, index) =>
        entry?.name !== packages[index].name ||
        entry.version !== version ||
        !sha512.test(entry.sha512)
    )
  ) {
    throw new Error(
      "Expected identity must come from the exact verified product archives and commit."
    );
  }
};

const archiveIdentity = async (directory, entry) => {
  // Env's parser and UI's manifest validator already restrict archive names to safe basenames.
  const archive = resolve(directory, entry.archive.name);
  const stat = await lstat(archive);
  if (!stat.isFile() || stat.isSymbolicLink() || stat.size > 64 * 1024 * 1024) {
    throw new Error("Verified archive must be a bounded regular file.");
  }
  const data = await readFile(archive);
  if (
    data.length !== entry.archive.bytes ||
    createHash("sha256").update(data).digest("hex") !== entry.archive.sha256
  ) {
    throw new Error(
      "Verified archive bytes differ from the artifact manifest."
    );
  }
  return {
    name: entry.name,
    sha512: createHash("sha512").update(data).digest("hex"),
    version: entry.version,
  };
};

// npm audit signatures verifies cryptography; these additional checks bind its verified statements to independently verified build artifacts.
export const readRegistryIdentity = async (
  product,
  tag,
  commit,
  artifactDirectory
) => {
  const version = releaseVersion(product, tag);
  const directory = resolve(artifactDirectory);
  const info = await lstat(directory);
  if (!info.isDirectory() || info.isSymbolicLink()) {
    throw new Error("Verified artifact directory must be a real directory.");
  }
  const file = resolve(directory, "manifest.json");
  const metadata = await lstat(file);
  if (
    !metadata.isFile() ||
    metadata.isSymbolicLink() ||
    metadata.size > 1_048_576
  ) {
    throw new Error(
      "Verified artifact manifest must be a bounded regular file."
    );
  }
  const bytes = await readFile(file);
  const manifest =
    product === "env"
      ? parseManifest(bytes)
      : validateUiManifest(JSON.parse(bytes.toString("utf-8")));
  if (manifest.source.commit !== commit) {
    throw new Error(
      "Verified artifact commit differs from the expected release commit."
    );
  }
  const entries =
    product === "env"
      ? [{ ...manifest.package, archive: manifest.archive }]
      : manifest.packages;
  const packages = await Promise.all(
    entries.map((entry) => archiveIdentity(directory, entry))
  );
  const expected = { commit, packages };
  verifyExpectedIdentity(expected, product, version);
  return expected;
};

export const verifyRegistryAudit = (audit, product, tag, expected) => {
  const version = releaseVersion(product, tag);
  verifyExpectedIdentity(expected, product, version);
  if (
    !Array.isArray(audit?.invalid) ||
    audit.invalid.length ||
    !Array.isArray(audit.missing) ||
    audit.missing.length ||
    !Array.isArray(audit.verified)
  ) {
    throw new Error("npm signature audit is incomplete or not fully verified.");
  }
  for (const identity of expected.packages) {
    const { name } = identity;
    const matches = audit.verified.filter(
      (entry) => entry?.name === name && entry.version === version
    );
    if (matches.length !== 1) {
      throw new Error(
        "Verified audit must contain each exact published package once."
      );
    }
    const [entry] = matches;
    const url = new URL(entry.attestations?.url);
    if (
      url.origin !== "https://registry.npmjs.org" ||
      url.search ||
      url.hash ||
      url.username ||
      url.password ||
      decodeURIComponent(url.pathname) !==
        `/-/npm/v1/attestations/${name}@${version}` ||
      entry.attestations.provenance?.predicateType !== predicates[0] ||
      !Array.isArray(entry.attestationBundles)
    ) {
      throw new Error(
        "Published package provenance metadata is incomplete or off-registry."
      );
    }
    const provenance = readStatement(entry, predicates[0]);
    const publish = readStatement(entry, predicates[1]);
    verifySubject(provenance, identity);
    verifySubject(publish, identity);
    verifyProvenance(provenance, product, tag, expected.commit);
    if (
      !isDeepStrictEqual(publish.predicate, {
        name,
        registry: "https://registry.npmjs.org",
        version,
      })
    ) {
      throw new Error(
        "Signed publish attestation does not match the exact npm package identity."
      );
    }
  }
  return {
    packages: cutover.products[product].packages.map((p) => p.name),
    passed: true,
    version,
  };
};

if (
  process.argv[1] &&
  import.meta.url === pathToFileURL(process.argv[1]).href
) {
  const [file, product, tag, commit, directory, ...extra] =
    process.argv.slice(2);
  if (!file || !product || !tag || !commit || !directory || extra.length) {
    throw new Error(
      "Usage: registry-audit.mjs <audit.json> <env|ui> <namespaced-tag> <expected-commit> <verified-artifact-directory>"
    );
  }
  const expected = await readRegistryIdentity(product, tag, commit, directory);
  process.stdout.write(
    `${JSON.stringify(verifyRegistryAudit(JSON.parse(await readFile(file, "utf-8")), product, tag, expected))}\n`
  );
}
