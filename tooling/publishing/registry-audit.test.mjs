import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { test } from "node:test";

import { cutover } from "./identity.mjs";
import { verifyRegistryAudit } from "./registry-audit.mjs";

const provenanceType = "https://slsa.dev/provenance/v1";
const publishType =
  "https://github.com/npm/attestation/tree/main/specs/publish/v0.1";
const expectedFor = (product) => ({
  commit: "a".repeat(40),
  packages: cutover.products[product].packages.map(({ name }) => ({
    name,
    sha512: createHash("sha512").update(name).digest("hex"),
    version: "1.2.3",
  })),
});
const attestation = (statement) => ({
  bundle: {
    dsseEnvelope: {
      payload: Buffer.from(JSON.stringify(statement)).toString("base64"),
      payloadType: "application/vnd.in-toto+json",
    },
  },
  predicateType: statement.predicateType,
});
const subjectFor = (identity) => [
  {
    digest: { sha512: identity.sha512 },
    name: `pkg:npm/${identity.name.replace("@", "%40")}@${identity.version}`,
  },
];
const provenanceFor = (identity, product) => ({
  _type: "https://in-toto.io/Statement/v1",
  predicate: {
    buildDefinition: {
      buildType:
        "https://slsa-framework.github.io/github-actions-buildtypes/workflow/v1",
      externalParameters: {
        workflow: {
          path: `.github/workflows/${cutover.products[product].workflow}`,
          ref: `refs/tags/${product}/v1.2.3`,
          repository: "https://github.com/astilbahq/astilba",
        },
      },
      resolvedDependencies: [
        {
          digest: { gitCommit: "a".repeat(40) },
          uri: `git+https://github.com/astilbahq/astilba@refs/tags/${product}/v1.2.3`,
        },
      ],
    },
    runDetails: {
      builder: { id: "https://github.com/actions/runner/github-hosted" },
    },
  },
  predicateType: provenanceType,
  subject: subjectFor(identity),
});
const publishFor = (identity) => ({
  _type: "https://in-toto.io/Statement/v0.1",
  predicate: {
    name: identity.name,
    registry: "https://registry.npmjs.org",
    version: identity.version,
  },
  predicateType: publishType,
  subject: subjectFor(identity),
});

const fixture = (product) => ({
  invalid: [],
  missing: [],
  verified: expectedFor(product).packages.map((identity) => ({
    attestationBundles: [
      attestation(provenanceFor(identity, product)),
      attestation(publishFor(identity)),
    ],
    attestations: {
      provenance: { predicateType: "https://slsa.dev/provenance/v1" },
      url: `https://registry.npmjs.org/-/npm/v1/attestations/${encodeURIComponent(identity.name)}@1.2.3`,
    },
    name: identity.name,
    version: "1.2.3",
  })),
});

for (const product of ["env", "ui"]) {
  test(`${product} requires verified npm signatures and both attestation kinds for its exact packages`, () => {
    assert.equal(
      verifyRegistryAudit(
        fixture(product),
        product,
        `${product}/v1.2.3`,
        expectedFor(product)
      ).passed,
      true
    );
    for (const field of ["invalid", "missing"]) {
      assert.throws(() =>
        verifyRegistryAudit(
          { ...fixture(product), [field]: [{}] },
          product,
          `${product}/v1.2.3`,
          expectedFor(product)
        )
      );
    }
    for (const patch of [
      { version: "1.2.4" },
      { name: "@astilba/cache" },
      { attestationBundles: [] },
      { attestations: { url: "https://example.com/payload" } },
    ]) {
      const audit = fixture(product);
      audit.verified[0] = { ...audit.verified[0], ...patch };
      assert.throws(() =>
        verifyRegistryAudit(
          audit,
          product,
          `${product}/v1.2.3`,
          expectedFor(product)
        )
      );
    }
    const duplicate = fixture(product);
    duplicate.verified.push(duplicate.verified[0]);
    assert.throws(() =>
      verifyRegistryAudit(
        duplicate,
        product,
        `${product}/v1.2.3`,
        expectedFor(product)
      )
    );
  });

  test(`${product} rejects signed provenance from a different repository, workflow, ref, commit or runner`, () => {
    const [identity] = expectedFor(product).packages;
    const mutations = [
      (s) => {
        s.predicate.buildDefinition.externalParameters.workflow.repository = `https://github.com/astilbahq/${product}`;
      },
      (s) => {
        s.predicate.buildDefinition.externalParameters.workflow.path =
          ".github/workflows/release.yml";
      },
      (s) => {
        s.predicate.buildDefinition.externalParameters.workflow.ref =
          "refs/tags/v1.2.3";
      },
      (s) => {
        s.predicate.buildDefinition.externalParameters.workflow.ref = `refs/tags/${product === "env" ? "ui" : "env"}/v1.2.3`;
      },
      (s) => {
        s.predicate.buildDefinition.resolvedDependencies[0].digest.gitCommit =
          "b".repeat(40);
      },
      (s) => {
        s.predicate.buildDefinition.resolvedDependencies[0].uri =
          "git+https://github.com/astilbahq/env@refs/tags/v1.2.3";
      },
      (s) => {
        s.predicate.buildDefinition.resolvedDependencies.push(
          s.predicate.buildDefinition.resolvedDependencies[0]
        );
      },
      (s) => {
        s.predicate.buildDefinition.buildType = "other-builder";
      },
      (s) => {
        s.predicate.runDetails.builder.id =
          "https://github.com/actions/runner/self-hosted";
      },
    ];
    for (const mutate of mutations) {
      const statement = provenanceFor(identity, product);
      mutate(statement);
      const audit = fixture(product);
      audit.verified[0].attestationBundles[0] = attestation(statement);
      assert.throws(
        () =>
          verifyRegistryAudit(
            audit,
            product,
            `${product}/v1.2.3`,
            expectedFor(product)
          ),
        /Signed provenance/u
      );
    }
  });

  test(`${product} binds both signed attestation subjects to the exact package and SHA-512 archive bytes`, () => {
    for (const index of [0, 1]) {
      for (const mutate of [
        (s) => {
          s.subject[0].digest.sha512 = "f".repeat(128);
        },
        (s) => {
          s.subject[0].name = "pkg:npm/%40astilba/cache@1.2.3";
        },
        (s) => {
          s.subject.push(s.subject[0]);
        },
        (s) => {
          s.subject = [];
        },
      ]) {
        const [identity] = expectedFor(product).packages;
        const statement =
          index === 0 ? provenanceFor(identity, product) : publishFor(identity);
        mutate(statement);
        const audit = fixture(product);
        audit.verified[0].attestationBundles[index] = attestation(statement);
        assert.throws(
          () =>
            verifyRegistryAudit(
              audit,
              product,
              `${product}/v1.2.3`,
              expectedFor(product)
            ),
          /Signed attestation subject/u
        );
      }
    }
  });

  test(`${product} rejects missing, duplicate, malformed or mislabeled verified payloads`, () => {
    for (const mutate of [
      (a) => {
        delete a.bundle;
      },
      (a) => {
        a.bundle.dsseEnvelope.payload = "not base64!";
      },
      (a) => {
        a.bundle.dsseEnvelope.payload = Buffer.from("{").toString("base64");
      },
      (a) => {
        a.bundle.dsseEnvelope.payloadType = "application/json";
      },
      (a) => {
        a.bundle.dsseEnvelope.payload = "a".repeat(1_398_108);
      },
    ]) {
      const audit = fixture(product);
      mutate(audit.verified[0].attestationBundles[0]);
      assert.throws(() =>
        verifyRegistryAudit(
          audit,
          product,
          `${product}/v1.2.3`,
          expectedFor(product)
        )
      );
    }
    const duplicate = fixture(product);
    duplicate.verified[0].attestationBundles.push(
      duplicate.verified[0].attestationBundles[0]
    );
    assert.throws(
      () =>
        verifyRegistryAudit(
          duplicate,
          product,
          `${product}/v1.2.3`,
          expectedFor(product)
        ),
      /exactly once/u
    );
    const wrongType = fixture(product);
    const statement = provenanceFor(expectedFor(product).packages[0], product);
    statement.predicateType = publishType;
    wrongType.verified[0].attestationBundles[0] = {
      ...attestation(statement),
      predicateType: provenanceType,
    };
    assert.throws(
      () =>
        verifyRegistryAudit(
          wrongType,
          product,
          `${product}/v1.2.3`,
          expectedFor(product)
        ),
      /statement type/u
    );
    const wrongPublish = fixture(product);
    const publish = publishFor(expectedFor(product).packages[0]);
    publish.predicate.registry = "https://example.com";
    wrongPublish.verified[0].attestationBundles[1] = attestation(publish);
    assert.throws(
      () =>
        verifyRegistryAudit(
          wrongPublish,
          product,
          `${product}/v1.2.3`,
          expectedFor(product)
        ),
      /Signed publish/u
    );
  });

  test(`${product} requires independent exact artifact and commit expectations`, () => {
    for (const expected of [
      undefined,
      { ...expectedFor(product), commit: "bad" },
      { ...expectedFor(product), packages: [] },
      {
        ...expectedFor(product),
        packages: [
          { name: "@astilba/cache", sha512: "c".repeat(128), version: "1.2.3" },
        ],
      },
    ]) {
      assert.throws(
        () =>
          verifyRegistryAudit(
            fixture(product),
            product,
            `${product}/v1.2.3`,
            expected
          ),
        /Expected identity/u
      );
    }
  });
}
