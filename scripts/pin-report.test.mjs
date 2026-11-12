import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import { platformManifests, registry } from "./image-registry.mjs";
import {
  assemble,
  findGoTools,
  findImages,
  findReleases,
  imageFindings,
  newestInLine,
  parseImageAllowlist,
  parseName,
  sections,
} from "./pin-report.mjs";
import { osvScannerVersion } from "./vulnerability-scan.mjs";

const digest = (fill) => fill.repeat(64);
const sha256 = (text) =>
  `sha256:${createHash("sha256").update(text).digest("hex")}`;

test("names follow Docker's defaults for the registry and official images", () => {
  assert.deepEqual(parseName("postgres"), {
    registry: "docker.io",
    repository: "library/postgres",
    image: "postgres",
  });
  assert.deepEqual(parseName("docker.io/library/alpine"), {
    registry: "docker.io",
    repository: "library/alpine",
    image: "alpine",
  });
  assert.deepEqual(parseName("gitea/gitea"), {
    registry: "docker.io",
    repository: "gitea/gitea",
    image: "gitea/gitea",
  });
  assert.deepEqual(parseName("localhost:5000/team/app"), {
    registry: "localhost:5000",
    repository: "team/app",
    image: "localhost:5000/team/app",
  });
});

test("finds each pinned digest once, wherever it is written, and what ships it", () => {
  const pin = `postgres:18.4-alpine3.23@sha256:${digest("a")}`;
  const images = findImages([
    {
      path: "services/platform/internal/domain/visual.go",
      text: `const PostgresImage = "${pin}"`,
    },
    {
      path: ".github/workflows/ci.yml",
      text: `      - docker.io/library/${pin}`,
    },
    { path: "docs/hosting.md", text: `ghcr.io/x/old:1@sha256:${digest("b")}` },
    {
      path: "services/platform/internal/x/fixture_test.go",
      text: `img = "ghcr.io/x/y:v1.2.3@sha256:${digest("c")}"; unpinned = "nginx:1.30"`,
    },
    // Not a reference: part of a longer word, or a digest too long.
    {
      path: "a.txt",
      text: `xpostgres:1@sha256:${digest("d")}0 or @sha256:${digest("e")}`,
    },
  ]);
  assert.deepEqual(
    images.map(({ image, tag, digest: pinned, paths, runtime }) => [
      image,
      tag,
      pinned.slice(7, 8),
      paths,
      runtime,
    ]),
    [
      [
        "postgres",
        "18.4-alpine3.23",
        "a",
        [
          ".github/workflows/ci.yml",
          "services/platform/internal/domain/visual.go",
        ],
        true,
      ],
      [
        "ghcr.io/x/y",
        "v1.2.3",
        "c",
        ["services/platform/internal/x/fixture_test.go"],
        false,
      ],
    ],
  );
});

const tracked = execFileSync("git", ["ls-files", "-z"], { encoding: "utf8" })
  .split("\0")
  .filter((path) =>
    /\.(go|mjs|sh|tmpl|ya?ml)$|(^|\/)(Dockerfile|go\.mod)$/.test(path),
  )
  .flatMap((path) => {
    try {
      return [{ path, text: readFileSync(path, "utf8") }];
    } catch {
      return [];
    }
  });

test("every image constant the server ships is found, so a new one needs no list", () => {
  const constants = tracked
    .filter(
      ({ path }) =>
        path.startsWith("services/platform/") && !path.endsWith("_test.go"),
    )
    .flatMap(({ text }) => [
      ...text.matchAll(/^const \w+ = "([^"]+@sha256:[0-9a-f]{64})"$/gm),
    ])
    .map(([, reference]) => reference.split("@")[1]);
  assert.ok(constants.length >= 10);
  const found = new Map(
    findImages(tracked).map((pin) => [pin.digest, pin.runtime]),
  );
  for (const pinned of constants) assert.equal(found.get(pinned), true, pinned);
});

test("reads go tool directives and the requirement that provides each", () => {
  const tools = findGoTools([
    {
      dir: "m",
      text: [
        "module m",
        "require (",
        "\texample.test/tools v1.2.0 // indirect",
        "\texample.test/tools/v2 v2.0.1 // indirect",
        ")",
        "require example.test/single v0.3.0",
        "tool (",
        "\texample.test/tools/v2/cmd/gen",
        "\t// example.test/commented/cmd/x",
        ")",
        "tool example.test/single/cmd/one",
      ].join("\n"),
    },
  ]);
  assert.deepEqual(tools, [
    {
      dir: "m",
      tool: "example.test/tools/v2/cmd/gen",
      module: "example.test/tools/v2",
      version: "v2.0.1",
    },
    {
      dir: "m",
      tool: "example.test/single/cmd/one",
      module: "example.test/single",
      version: "v0.3.0",
    },
  ]);
  assert.throws(
    () => findGoTools([{ dir: "m", text: "tool example.test/missing" }]),
    /no requirement provides example.test\/missing/,
  );
  assert.deepEqual(
    findGoTools(
      tracked
        .filter(({ path }) => path === "services/platform/go.mod")
        .map(({ text }) => ({ dir: "services/platform", text })),
    ).map(({ module }) => module),
    [
      "github.com/oapi-codegen/oapi-codegen/v2",
      "github.com/sqlc-dev/sqlc",
      "golang.org/x/vuln",
      "honnef.co/go/tools",
    ],
  );
});

// Split, so the report does not find these in this file.
const download = (rest) =>
  `https://github.com/${rest.replace(" ", "/releases/download/")}`;

test("finds release binaries by URL, with a version written there or in a constant", () => {
  const releases = findReleases([
    { path: "a.sh", text: `url="${download("o/tool v1.2.3/tool_$arch")}"` },
    {
      path: "b.mjs",
      text: `const toolVersion = "4.5.6";\nfetch(\`${download("o/other v${toolVersion}/x")}\`)`,
    },
    { path: "c.md", text: download("o/ignored v1.0.0/x") },
  ]);
  assert.deepEqual(releases, [
    { repository: "o/tool", version: "v1.2.3", paths: ["a.sh"] },
    { repository: "o/other", version: "v4.5.6", paths: ["b.mjs"] },
  ]);
  assert.throws(
    () =>
      findReleases([{ path: "d.mjs", text: download("o/x v${unknown}/y") }]),
    /cannot read the version of o\/x/,
  );
  assert.ok(
    findReleases(tracked).some(
      ({ repository, version }) =>
        repository === "google/osv-scanner" &&
        version === `v${osvScannerVersion}`,
    ),
  );
});

test("a newer release keeps the line: its shape and major, unless that is a year", () => {
  const tags = [
    "18.4-alpine3.23",
    "18.5-alpine3.23",
    "18.10-alpine3.24",
    "18.11-trixie",
    "19.0-alpine3.24",
    "18-alpine",
    "18.12rc1-alpine3.24",
    "latest",
  ];
  assert.equal(newestInLine("18.4-alpine3.23", tags), "18.10-alpine3.24");
  assert.equal(newestInLine("18.10-alpine3.24", tags), undefined);
  assert.equal(
    newestInLine("v3.7.16", ["v3.7.17", "v3.8.0", "v4.0.0", "v3.9.0-rc1"]),
    "v3.8.0",
  );
  assert.equal(
    newestInLine("mise-2026.10.14", ["mise-2027.1.2", "mise-2026.11.1"]),
    "mise-2027.1.2",
  );
  assert.equal(
    newestInLine("v1.9.0", ["v1.9.0", "v1.8.9", "v1.10.0-pre"]),
    undefined,
  );
});

const imageEntry = (fields = {}) => ({
  id: "CVE-2026-1",
  image: "postgres",
  package: "openssl",
  reason: "Not reachable: the server never uses this API.",
  expires: "2026-12-01",
  ...fields,
});

test("image exceptions name an advisory, image and package, for at most 90 days", () => {
  const today = "2026-11-12";
  assert.deepEqual(parseImageAllowlist({ entries: [] }, today), []);
  assert.equal(
    parseImageAllowlist({ images: [imageEntry()] }, today).length,
    1,
  );
  for (const [entry, message] of [
    [imageEntry({ extra: 1 }), /must have exactly/],
    [imageEntry({ reason: " " }), /needs a reason/],
    [imageEntry({ expires: "2026-02-30" }), /YYYY-MM-DD/],
    [imageEntry({ expires: "2027-03-01" }), /more than 90 days/],
  ])
    assert.throws(
      () => parseImageAllowlist({ images: [entry] }, today),
      message,
    );
  assert.throws(
    () => parseImageAllowlist({ images: [imageEntry(), imageEntry()] }, today),
    /duplicated/,
  );
});

test("reads OSV Scanner's image report, taking the fix from the package's own branch", () => {
  const findings = imageFindings({
    results: [
      {
        packages: [
          {
            package: {
              name: "openssl",
              version: "3.5.4-r0",
              ecosystem: "Alpine:v3.23",
            },
            groups: [
              {
                ids: ["ALPINE-CVE-2026-1"],
                aliases: ["CVE-2026-1"],
                max_severity: "7.5",
              },
            ],
            vulnerabilities: [
              {
                id: "ALPINE-CVE-2026-1",
                summary: "",
                affected: [
                  {
                    package: { name: "openssl", ecosystem: "Alpine:v3.22" },
                    ranges: [
                      { events: [{ introduced: "0" }, { fixed: "3.3.9-r0" }] },
                    ],
                  },
                  {
                    package: { name: "openssl", ecosystem: "Alpine:v3.23" },
                    ranges: [
                      { events: [{ introduced: "0" }, { fixed: "3.5.4-r1" }] },
                    ],
                  },
                ],
              },
            ],
          },
          {
            package: { name: "stdlib", version: "1.25.1", ecosystem: "Go" },
            groups: [{ ids: ["GO-2026-2"], aliases: [] }],
            vulnerabilities: [
              { id: "GO-2026-2", summary: "net/http", affected: [] },
            ],
          },
          {
            package: { name: "zlib", version: "1", ecosystem: "Alpine:v3.23" },
          },
        ],
      },
    ],
  });
  assert.deepEqual(findings, [
    {
      id: "ALPINE-CVE-2026-1",
      cve: "CVE-2026-1",
      ids: ["ALPINE-CVE-2026-1", "CVE-2026-1"],
      package: "openssl",
      ecosystem: "Alpine:v3.23",
      version: "3.5.4-r0",
      fixed: "3.5.4-r1",
      severity: "7.5",
      summary: "",
    },
    {
      id: "GO-2026-2",
      cve: "",
      ids: ["GO-2026-2"],
      package: "stdlib",
      ecosystem: "Go",
      version: "1.25.1",
      fixed: "",
      severity: "",
      summary: "net/http",
    },
  ]);
});

const finding = (fields = {}) => ({
  id: "ALPINE-CVE-2026-1",
  cve: "CVE-2026-1",
  ids: ["ALPINE-CVE-2026-1", "CVE-2026-1"],
  package: "openssl",
  ecosystem: "Alpine:v3.23",
  version: "3.5.4-r0",
  fixed: "3.5.4-r1",
  severity: "7.5",
  summary: "",
  ...fields,
});
const checked = (fields = {}) => ({
  image: "postgres",
  tag: "18.4-alpine3.23",
  digest: `sha256:${digest("a")}`,
  paths: ["services/platform/internal/domain/visual.go"],
  runtime: true,
  errors: [],
  scans: [
    { platform: "linux/amd64", findings: [finding()] },
    { platform: "linux/arm64", findings: [finding()] },
  ],
  ...fields,
});

test("a fixable vulnerability needs attention, with the image, CVE and fixed version", () => {
  const { problems, info } = assemble({
    images: [checked()],
    tools: [],
    entries: [],
    today: "2026-11-12",
  });
  assert.deepEqual(info, []);
  assert.deepEqual(problems, [
    {
      section: sections.vulnerabilities,
      key: `vulnerable|postgres|sha256:${digest("a")}|ALPINE-CVE-2026-1|openssl|3.5.4-r0`,
      text: "[runtime] postgres:18.4-alpine3.23 (linux/amd64, linux/arm64): CVE-2026-1 ALPINE-CVE-2026-1 in openssl 3.5.4-r0, fixed in 3.5.4-r1, CVSS 7.5",
    },
  ]);
});

test("one without a fix, or with a current exception, is reported for information", () => {
  const { problems, info } = assemble({
    images: [
      checked({
        scans: [
          {
            platform: "linux/amd64",
            findings: [
              finding(),
              finding({
                id: "GO-2026-2",
                cve: "",
                ids: ["GO-2026-2"],
                package: "stdlib",
                fixed: "",
              }),
            ],
          },
        ],
      }),
    ],
    tools: [],
    entries: [imageEntry()],
    today: "2026-11-12",
  });
  assert.deepEqual(problems, []);
  assert.deepEqual(
    info.map(({ section }) => section),
    [sections.allowed, sections.unfixed],
  );
  assert.match(info[0].text, /allowlisted until 2026-12-01: Not reachable/);
});

test("an expired exception, or one matching nothing, needs attention", () => {
  const expired = assemble({
    images: [checked()],
    tools: [],
    entries: [imageEntry({ expires: "2026-11-11" })],
    today: "2026-11-12",
  });
  assert.equal(expired.problems[0].section, sections.exceptions);
  assert.match(expired.problems[0].text, /expired 2026-11-11/);

  const stale = assemble({
    images: [checked({ scans: [] })],
    tools: [],
    entries: [imageEntry()],
    today: "2026-11-12",
  });
  assert.match(stale.problems[0].text, /matches no finding, remove it/);

  // An image that could not be scanned cannot show an exception is unused.
  const unscanned = assemble({
    images: [
      checked({
        scans: [],
        errors: [{ step: "scanning linux/amd64", message: "pull failed" }],
      }),
    ],
    tools: [],
    entries: [imageEntry()],
    today: "2026-11-12",
  });
  assert.deepEqual(
    unscanned.problems.map(({ section }) => section),
    [sections.problems],
  );
});

test("an upstream rebuild, a newer tag or tool, and anything not checked need attention", () => {
  const { problems } = assemble({
    images: [
      checked({
        scans: [],
        moved: `sha256:${digest("b")}`,
        newer: "18.5-alpine3.23",
        errors: [{ step: "listing tags", message: "HTTP 503" }],
      }),
    ],
    tools: [
      {
        name: "o/tool",
        version: "v1.2.3",
        newer: "v1.2.4",
        published: "2026-11-09T04:15:04Z",
        paths: ["a.sh"],
      },
      { name: "o/current", version: "v1.0.0", paths: ["b.sh"] },
      {
        name: "o/unreachable",
        version: "v1.0.0",
        paths: ["c.sh"],
        error: "checking releases failed: HTTP 404",
      },
    ],
    entries: [],
    today: "2026-11-12",
  });
  assert.deepEqual(
    problems.map(({ section, key }) => [section, key]),
    [
      [
        sections.problems,
        `error|postgres:18.4-alpine3.23|sha256:${digest("a")}|listing tags`,
      ],
      [sections.images, `moved|postgres:18.4-alpine3.23|sha256:${digest("b")}`],
      [sections.images, "newer|postgres|18.4-alpine3.23|18.5-alpine3.23"],
      [sections.tools, "newer|o/tool|v1.2.3|v1.2.4"],
      [sections.problems, "error|o/unreachable"],
    ],
  );
  assert.match(
    problems[3].text,
    /o\/tool v1.2.3 has a newer release in its line: v1.2.4, published 2026-11-09 \(a.sh\)/,
  );
});

const response = (status, body = "", headers = {}) =>
  new Response(status === 204 ? null : body, { status, headers });

test("takes the registry's anonymous token and checks every manifest against its digest", async () => {
  const index = JSON.stringify({ manifests: [] });
  const calls = [];
  const client = registry(async (url, init = {}) => {
    calls.push([
      String(url),
      init.method ?? "GET",
      init.headers?.authorization,
    ]);
    const href = String(url);
    if (href.startsWith("https://auth.example.test/token"))
      return response(200, JSON.stringify({ token: "t" }));
    if (!init.headers?.authorization)
      return response(401, "", {
        "www-authenticate":
          'Bearer realm="https://auth.example.test/token",service="registry.example.test",scope="repository:team/app:pull"',
      });
    if (href.endsWith(`/manifests/${sha256(index)}`))
      return response(200, index);
    if (href.endsWith("/manifests/sha256:" + digest("f")))
      return response(200, index);
    if (href.endsWith("/manifests/1.0"))
      return response(200, "", { "docker-content-digest": sha256(index) });
    if (href.endsWith("/tags/list?n=1000"))
      return response(200, JSON.stringify({ tags: ["1.0"] }), {
        link: '</v2/team/app/tags/list?n=1000&last=1.0>; rel="next"',
      });
    if (href.endsWith("/tags/list?n=1000&last=1.0"))
      return response(200, JSON.stringify({ tags: ["1.1"] }));
    return response(404);
  });
  const pin = {
    registry: "registry.example.test",
    repository: "team/app",
    image: "registry.example.test/team/app",
    tag: "1.0",
  };
  assert.deepEqual(await client.manifest(pin, sha256(index)), {
    manifests: [],
  });
  assert.deepEqual(
    calls.slice(0, 3).map(([url, , auth]) => [url.split("?")[0], auth]),
    [
      [
        `https://registry.example.test/v2/team/app/manifests/${sha256(index)}`,
        undefined,
      ],
      ["https://auth.example.test/token", undefined],
      [
        `https://registry.example.test/v2/team/app/manifests/${sha256(index)}`,
        "Bearer t",
      ],
    ],
  );
  await assert.rejects(
    client.manifest(pin, `sha256:${digest("f")}`),
    /returned other bytes/,
  );
  assert.equal(await client.tagDigest(pin), sha256(index));
  assert.deepEqual(await client.tags(pin), ["1.0", "1.1"]);
  const docker = registry(async () =>
    response(401, "", {
      "www-authenticate": 'Bearer realm="http://plain.test/token"',
    }),
  );
  await assert.rejects(
    docker.manifest(pin, sha256(index)),
    /unexpected registry challenge/,
  );
});

test("scans the amd64 and arm64 images an index holds, or the one platform pinned", async () => {
  const pin = { image: "x", digest: `sha256:${digest("a")}` };
  const index = {
    manifests: [
      {
        digest: "sha256:amd",
        platform: { os: "linux", architecture: "amd64" },
      },
      {
        digest: "sha256:v7",
        platform: { os: "linux", architecture: "arm", variant: "v7" },
      },
      {
        digest: "sha256:arm",
        platform: { os: "linux", architecture: "arm64", variant: "v8" },
      },
      {
        digest: "sha256:att",
        platform: { os: "unknown", architecture: "unknown" },
      },
    ],
  };
  assert.deepEqual(await platformManifests({}, pin, index), [
    { platform: "linux/amd64", digest: "sha256:amd" },
    { platform: "linux/arm64", digest: "sha256:arm" },
  ]);
  const client = { blob: async () => ({ os: "linux", architecture: "arm64" }) };
  assert.deepEqual(
    await platformManifests(client, pin, { config: { digest: "sha256:c" } }),
    [{ platform: "linux/arm64", digest: pin.digest }],
  );
});
