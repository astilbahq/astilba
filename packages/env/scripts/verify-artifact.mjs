// @ts-check
/// <reference types="node" />

import { readArtifact } from "./matrix-artifact.mjs";

const { archive, manifest } = await readArtifact();
process.stdout.write(
  `${JSON.stringify({
    archive,
    entries: manifest.entries.length,
    passed: true,
    sha256: manifest.archive.sha256,
  })}\n`
);
