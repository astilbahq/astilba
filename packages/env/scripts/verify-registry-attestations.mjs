// @ts-check
/// <reference types="node" />

import { readFile } from "node:fs/promises";

const SLSA_PROVENANCE = "https://slsa.dev/provenance/v1";
const NPM_PUBLISH_ATTESTATION =
  "https://github.com/npm/attestation/tree/main/specs/publish/v0.1";

/** @param {unknown} value @returns {value is Record<string, unknown>} */
const isRecord = (value) =>
  typeof value === "object" && value !== null && !Array.isArray(value);

/** @param {unknown} value @returns {value is unknown[]} */
const isUnknownArray = (value) => Array.isArray(value);

const [path, packageName, version] = process.argv.slice(2);
if (
  path === undefined ||
  packageName !== "@astilba/env" ||
  version === undefined ||
  !/^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/u.test(version)
) {
  throw new Error(
    "Usage: node scripts/verify-registry-attestations.mjs <audit.json> @astilba/env <version>"
  );
}

/** @type {unknown} */
const audit = JSON.parse(await readFile(path, "utf-8"));
if (
  !isRecord(audit) ||
  !isUnknownArray(audit.invalid) ||
  audit.invalid.length !== 0 ||
  !isUnknownArray(audit.missing) ||
  audit.missing.length !== 0 ||
  !isUnknownArray(audit.verified)
) {
  throw new Error(
    "npm signature audit did not return a fully verified result."
  );
}

const matches = audit.verified.filter(
  (entry) =>
    isRecord(entry) && entry.name === packageName && entry.version === version
);
const target = matches[0];
if (
  matches.length !== 1 ||
  !isRecord(target) ||
  !isRecord(target.attestations) ||
  typeof target.attestations.url !== "string" ||
  !target.attestations.url.startsWith(
    "https://registry.npmjs.org/-/npm/v1/attestations/"
  ) ||
  !isRecord(target.attestations.provenance) ||
  target.attestations.provenance.predicateType !== SLSA_PROVENANCE ||
  !isUnknownArray(target.attestationBundles)
) {
  throw new Error("Published package provenance metadata is incomplete.");
}

const predicateTypes = new Set(
  target.attestationBundles.flatMap((entry) =>
    isRecord(entry) && typeof entry.predicateType === "string"
      ? [entry.predicateType]
      : []
  )
);
if (
  !predicateTypes.has(SLSA_PROVENANCE) ||
  !predicateTypes.has(NPM_PUBLISH_ATTESTATION)
) {
  throw new Error(
    "Published package is missing verified provenance or publish attestations."
  );
}

process.stdout.write(
  `${JSON.stringify({
    package: packageName,
    passed: true,
    predicates: [...predicateTypes],
    version,
  })}\n`
);
