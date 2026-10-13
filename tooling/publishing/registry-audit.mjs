import { readFile } from "node:fs/promises";
import { pathToFileURL } from "node:url";

import { cutover, releaseVersion } from "./identity.mjs";

const predicates = [
  "https://slsa.dev/provenance/v1",
  "https://github.com/npm/attestation/tree/main/specs/publish/v0.1",
];

export const verifyRegistryAudit = (audit, product, tag) => {
  const version = releaseVersion(product, tag);
  if (
    !Array.isArray(audit?.invalid) ||
    audit.invalid.length ||
    !Array.isArray(audit.missing) ||
    audit.missing.length ||
    !Array.isArray(audit.verified)
  ) {
    throw new Error("npm signature audit is incomplete or not fully verified.");
  }
  for (const { name } of cutover.products[product].packages) {
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
    const actual = new Set(
      entry.attestationBundles.map((bundle) => bundle?.predicateType)
    );
    if (predicates.some((predicate) => !actual.has(predicate))) {
      throw new Error(
        "Published package lacks verified provenance or publish attestations."
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
  const [file, product, tag, ...extra] = process.argv.slice(2);
  if (!file || !product || !tag || extra.length) {
    throw new Error(
      "Usage: registry-audit.mjs <audit.json> <env|ui> <namespaced-tag>"
    );
  }
  process.stdout.write(
    `${JSON.stringify(verifyRegistryAudit(JSON.parse(await readFile(file, "utf-8")), product, tag))}\n`
  );
}
