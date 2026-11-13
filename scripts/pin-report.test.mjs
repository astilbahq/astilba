import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import { platformManifests, registry } from "./image-registry.mjs";
import { assemble, imageFindings, sections } from "./pin-report.mjs";
import {
  findGoTools,
  findImages,
  findReleases,
  newestInLine,
  parseName,
} from "./pin-sources.mjs";
import { readReport } from "./scheduled-report.mjs";
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
          // Fixed only on another branch: nothing to bump to on this one.
          {
            package: {
              name: "musl",
              version: "1.2.6-r2",
              ecosystem: "Alpine:v3.23",
            },
            groups: [{ ids: ["ALPINE-CVE-2026-9"], aliases: [] }],
            vulnerabilities: [
              {
                id: "ALPINE-CVE-2026-9",
                affected: [
                  {
                    package: { name: "musl", ecosystem: "Alpine:v3.22" },
                    ranges: [
                      { events: [{ introduced: "0" }, { fixed: "1.2.5-r9" }] },
                    ],
                  },
                ],
              },
            ],
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
    {
      id: "ALPINE-CVE-2026-9",
      cve: "CVE-2026-9",
      ids: ["ALPINE-CVE-2026-9"],
      package: "musl",
      ecosystem: "Alpine:v3.23",
      version: "1.2.6-r2",
      fixed: "",
      severity: "",
      summary: "",
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

test("a serious fixable vulnerability in a runtime image is to act on, one line per image", () => {
  const unscored = finding({
    id: "GO-2026-3",
    cve: "",
    ids: ["GO-2026-3"],
    package: "stdlib",
    version: "1.25.1",
    fixed: "1.25.2",
    severity: "",
  });
  const lower = finding({
    id: "ALPINE-CVE-2026-4",
    cve: "CVE-2026-4",
    severity: "5.3",
  });
  const unfixed = finding({
    id: "ALPINE-CVE-2026-5",
    cve: "CVE-2026-5",
    fixed: "",
  });
  const { problems, summary, info } = assemble({
    images: [
      checked({
        scans: [
          {
            platform: "linux/amd64",
            findings: [finding(), finding(), unscored, lower, unfixed],
          },
          { platform: "linux/arm64", findings: [finding()] },
        ],
      }),
    ],
    tools: [],
  });
  assert.deepEqual(info, []);
  assert.deepEqual(
    problems.map(({ section, text }) => ({ section, text })),
    [
      {
        section: sections.vulnerabilities,
        text: "[runtime] postgres:18.4-alpine3.23 (linux/amd64, linux/arm64): ALPINE-CVE-2026-1 in openssl 3.5.4-r0, fixed in 3.5.4-r1, CVSS 7.5",
      },
      {
        section: sections.vulnerabilities,
        text: "[runtime] postgres:18.4-alpine3.23 (linux/amd64): GO-2026-3 in stdlib 1.25.1, fixed in 1.25.2",
      },
    ],
  );
  // A key names the advisory in the pinned digest, not today's score or wording, so
  // the issue does not announce it again; a bumped digest is new.
  const keys = (pin) =>
    assemble({ images: [pin], tools: [] }).problems.map(({ key }) => key);
  const rescored = {
    platform: "linux/amd64",
    findings: [finding({ severity: "8.1", summary: "reworded" })],
  };
  assert.deepEqual(keys(checked({ scans: [rescored] })), keys(checked()));
  assert.notDeepEqual(
    keys(checked({ digest: `sha256:${digest("c")}` })),
    keys(checked()),
  );
  assert.notEqual(problems[0].key, problems[1].key);
  assert.ok(problems.every(({ check }) => !check));
  assert.deepEqual(summary, [
    {
      section: sections.vulnerabilities,
      text: "[runtime] postgres:18.4-alpine3.23: 2 to act on (1 high, 1 unscored), and 1 more with a fix (1 lower), 1 without a fix (1 high). Worst: ALPINE-CVE-2026-1 in openssl 3.5.4-r0, fixed in 3.5.4-r1, CVSS 7.5; GO-2026-3 in stdlib 1.25.1, fixed in 1.25.2",
    },
  ]);
});

test("a CI image's vulnerabilities, and an image with nothing to read, are for information", () => {
  const { problems, summary, info } = assemble({
    images: [
      checked({
        runtime: false,
        image: "gitea/gitea",
        tag: "28.2.0",
        scans: [
          { platform: "linux/amd64", findings: [finding({ severity: "9.8" })] },
          { platform: "linux/arm64", findings: [], empty: true },
        ],
      }),
    ],
    tools: [],
  });
  assert.deepEqual([problems, summary], [[], []]);
  assert.deepEqual(info, [
    {
      section: sections.other,
      text: "[CI] gitea/gitea:28.2.0: 1 more with a fix (1 critical). Worst: ALPINE-CVE-2026-1 in openssl 3.5.4-r0, fixed in 3.5.4-r1, CVSS 9.8",
    },
    {
      section: sections.other,
      text: "[CI] gitea/gitea:28.2.0 (linux/arm64): OSV Scanner found no packages it can read, as in an image of one static binary",
    },
  ]);
});

test("an upstream rebuild, a newer tag or tool, and anything not checked need attention", () => {
  const { problems, summary } = assemble({
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
  });
  assert.deepEqual(
    problems.map(({ section, check }) => [section, Boolean(check)]),
    [
      [sections.problems, true],
      [sections.images, false],
      [sections.images, false],
      [sections.tools, false],
      [sections.problems, true],
    ],
  );
  assert.match(
    problems[3].text,
    /o\/tool v1.2.3 has a newer release in its line: v1.2.4, published 2026-11-09 \(a.sh\)/,
  );
  assert.equal(new Set(problems.map(({ key }) => key)).size, problems.length);
  // Each of these is its own line in the issue too.
  assert.deepEqual(
    summary.map(({ text }) => text),
    problems.map(({ text }) => text),
  );
});

test("a retired pin is listed for information, except a manifest that cannot be read", () => {
  const retired = "PostgreSQL 18.4, replaced by 18.7; old databases and backups still name it.";
  const { problems, summary, info } = assemble({
    images: [
      checked({ retired }),
      checked({
        digest: `sha256:${digest("c")}`,
        retired,
        scans: [],
        errors: [{ step: "reading the pinned manifest", message: "HTTP 404" }],
      }),
    ],
    tools: [],
  });
  assert.deepEqual(
    problems.map(({ section, key }) => [section, key]),
    [
      [
        sections.problems,
        `image-error|postgres|sha256:${digest("c")}|reading the pinned manifest`,
      ],
    ],
  );
  assert.equal(summary.length, 1);
  assert.deepEqual(
    info.map(({ section }) => section),
    [sections.retired, sections.retired],
  );
  assert.match(info[0].text, /postgres:18\.4-alpine3\.23@sha256:aaaaaaaaaaaa \(runtime: .*\): PostgreSQL 18\.4, replaced/);
});

test("an image and a tool of one name are separate, and a tag's newer release is one", () => {
  const pin = (fill, runtime) =>
    checked({
      image: "nginx",
      tag: "1.30.6-alpine3.24",
      digest: `sha256:${digest(fill)}`,
      runtime,
      scans: [],
      moved: `sha256:${digest("f")}`,
      newer: "1.30.7-alpine3.24",
    });
  const buildkit = checked({
    image: "moby/buildkit",
    tag: "v0.34.1",
    scans: [],
    newer: "v0.35.0",
  });
  const { problems } = assemble({
    images: [pin("a", true), pin("b", false), buildkit],
    tools: [
      {
        name: "moby/buildkit",
        version: "v0.34.1",
        newer: "v0.35.0",
        paths: ["build-tools.sh"],
      },
    ],
  });
  const keyed = (start) =>
    problems.filter(({ key }) => key.startsWith(start)).map(({ key }) => key);
  // Each pin's rebuild is its own, but both pins wait on one release of their tag.
  assert.equal(new Set(keyed("image-moved")).size, 2);
  assert.equal(new Set(keyed("image-newer|nginx")).size, 1);
  assert.notEqual(keyed("image-newer|moby")[0], keyed("tool-newer")[0]);
});

test("a newer release stays the same problem when its tag is pinned again", () => {
  const newer = (fields) =>
    assemble({
      images: [checked({ scans: [], newer: "18.5-alpine3.23", ...fields })],
      tools: [],
    }).problems.find(({ section }) => section === sections.images).key;
  assert.equal(newer({}), newer({ digest: `sha256:${digest("e")}` }));
});

test("an advisory is known by every id of its group, so a new first id is not news", () => {
  const ghsa = finding({
    id: "GHSA-aaaa-bbbb-cccc",
    cve: "",
    ids: ["GHSA-aaaa-bbbb-cccc"],
    severity: "",
  });
  const grown = finding({
    id: "GO-2026-9",
    cve: "",
    ids: ["GO-2026-9", "GHSA-aaaa-bbbb-cccc"],
    severity: "",
  });
  const identities = (found) => {
    const [{ key, aliases = [] }] = assemble({
      images: [
        checked({ scans: [{ platform: "linux/amd64", findings: [found] }] }),
      ],
      tools: [],
    }).problems;
    return [key, ...aliases];
  };
  assert.ok(identities(grown).includes(identities(ghsa)[0]));
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

test("an advisory with many ids gives no more aliases than the report job reads", () => {
  const ids = Array.from({ length: 14 }, (_, i) => `CVE-2026-${100 + i}`);
  const big = finding({
    id: "DSA-6000-1",
    cve: ids[0],
    ids: ["DSA-6000-1", ...ids],
  });
  const report = assemble({
    images: [
      checked({ scans: [{ platform: "linux/amd64", findings: [big] }] }),
    ],
    tools: [],
  });
  assert.equal(report.problems[0].aliases.length, 10);
  assert.ok(readReport(JSON.stringify(report)));
});

test("every registry request carries a time limit", async () => {
  const signals = [];
  const client = registry(async (_url, init) => {
    signals.push(init.signal);
    return response(200, JSON.stringify({ tags: [] }));
  });
  await client.tags({
    registry: "r.test",
    repository: "a/b",
    image: "r.test/a/b",
  });
  assert.ok(signals.every((signal) => signal instanceof AbortSignal));
  assert.equal(signals.length, 1);
});

test("a registry that keeps naming the same next page of tags is cut off", async () => {
  let calls = 0;
  const client = registry(async () => {
    calls++;
    return response(200, JSON.stringify({ tags: ["1.0"] }), {
      link: '</v2/team/app/tags/list?n=1000&last=1.0>; rel="next"',
    });
  });
  const pin = {
    registry: "r.test",
    repository: "team/app",
    image: "r.test/team/app",
  };
  await assert.rejects(client.tags(pin), /lists tags past 2 pages/);
  assert.equal(calls, 2);
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
