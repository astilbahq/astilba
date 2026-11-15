import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import { appLaneProblems } from "./app-lanes.mjs";
import { linuxTests } from "./native-lanes.mjs";

const integration = "services/platform/internal/integration";
const agent = "services/platform/internal/agent";

const makefile = `APP_PAIR = ^(TestCrossHostOne|TestCrossHostTwo)$$
APP_OPERATIONS = ^Test(Troubleshooting|Git)

verify-app-core: verify-app-build
	$(MAKE) -j3 verify-app-go-remaining verify-app-go-operations verify-app-go-pair

verify-app-go-remaining:
	cd services/platform && go test -race -count=1 -v -timeout=15m -skip '$(APP_PAIR)|$(APP_OPERATIONS)' ./...

verify-app-go-operations:
	cd services/platform && go test -race -count=1 -v -timeout=15m -run '$(APP_OPERATIONS)' ./...

verify-app-go-pair:
	cd services/platform && go test -race -count=1 -v -timeout=15m -run '$(APP_PAIR)' ./internal/integration
`;
const workflow = `        lane: [remaining, operations, pair]
    steps:
      - run: make verify-app-go-\${{ matrix.lane }}
        env:
          PLAT_APP_DOCKER_HOST: unix:///var/run/docker.sock

  next:
`;
const tests = [
  ...["TestCrossHostOne", "TestCrossHostTwo", "TestTroubleshootingEnds"].map(
    (name) => ({ name, directory: integration }),
  ),
  { name: "TestGitImport", directory: integration },
  { name: "TestOrdinary", directory: integration },
  { name: "TestTroubleshootingFloor", directory: agent },
  { name: "TestUnit", directory: agent },
];
const without = (name) => tests.filter((entry) => entry.name !== name);
// A function keeps `$$` literal, as String.replaceAll would collapse it to `$`.
const replace = (from, to, source = makefile) => {
  assert.ok(source.includes(from), from);
  return source.replaceAll(from, () => to);
};
const problems = (input) =>
  appLaneProblems({ makefile, workflow, tests, ...input }).join("\n");

test("a complete partition has no problems", () => {
  assert.equal(problems({}), "");
});

test("reports each way a test can stop running or run twice", () => {
  for (const [label, input, expected] of [
    [
      "a name a -run lane cannot reach, skipped everywhere else",
      {
        tests: [...tests, { name: "TestCrossHostOne", directory: agent }],
      },
      /TestCrossHostOne in .*agent is selected by no verify-app-go-\* lane/,
    ],
    [
      "a lane's selection the catch-all no longer skips",
      { makefile: replace("|$(APP_OPERATIONS)' ./...", "' ./...") },
      /TestGitImport in .*integration is selected more than once: verify-app-go-remaining and verify-app-go-operations/,
    ],
    [
      "two lanes selecting one test",
      {
        makefile: replace(
          "^Test(Troubleshooting|Git)",
          "^Test(Troubleshooting|Git|CrossHostOne)",
        ),
      },
      /TestCrossHostOne in .*integration is selected more than once/,
    ],
    [
      "a skip with no lane selecting what it removes",
      { makefile: replace("-run '$(APP_OPERATIONS)'", "-run '^TestGit'") },
      /TestTroubleshootingEnds in .*integration is selected by no/,
    ],
    [
      "a selection that names a test that is gone",
      { tests: without("TestCrossHostTwo") },
      /verify-app-go-pair: -run alternative \^TestCrossHostTwo\$ matches no test/,
    ],
    [
      "a second -run the check could not interpret",
      {
        makefile: replace(
          "-run '$(APP_PAIR)' ./internal/integration",
          "-run '$(APP_PAIR)' -run '^TestOrdinary$$' ./internal/integration",
        ),
      },
      /verify-app-go-pair: expected at most one -run and -skip/,
    ],
    [
      "a second -skip, of which go test honours only the last",
      {
        makefile: replace(
          "-skip '$(APP_PAIR)|$(APP_OPERATIONS)'",
          "-skip '$(APP_PAIR)' -skip '$(APP_OPERATIONS)'",
        ),
      },
      /verify-app-go-remaining: expected at most one -run and -skip/,
    ],
    [
      "a lane that hides its tests from this check",
      {
        makefile: replace(
          "\tcd services/platform && go test -race -count=1 -v -timeout=15m -run '$(APP_PAIR)' ./internal/integration",
          "\t./hidden.sh",
        ),
      },
      /verify-app-go-pair runs no go test/,
    ],
    [
      "a lane the aggregate does not run",
      { makefile: replace(" verify-app-go-pair\n", "\n") },
      /verify-app-go-pair is not run by verify-app-core/,
    ],
    [
      "an aggregate running a lane with no rule",
      {
        makefile: replace(
          "verify-app-go-pair\n\nverify-app-go-remaining",
          "verify-app-go-pair verify-app-go-gone\n\nverify-app-go-remaining",
        ),
      },
      /runs verify-app-go-gone, which is not a lane rule/,
    ],
    [
      "an aggregate that also does something else",
      {
        makefile: replace(
          "verify-app-go-pair\n\nverify-app-go-remaining",
          "verify-app-go-pair\n\tgo test ./...\n\nverify-app-go-remaining",
        ),
      },
      /verify-app-core must run only/,
    ],
  ])
    assert.match(problems(input), expected, label);
});

test("the workflow must run exactly the lanes, unconditionally", () => {
  const bad = /differ from verify-app-core|unconditional/;
  for (const [label, source, ok] of [
    ["unchanged", workflow, true],
    [
      "a missing lane",
      replace("[remaining, operations, pair]", "[remaining, pair]", workflow),
      false,
    ],
    [
      "a condition on the step",
      replace("        env:\n", "        if: false\n        env:\n", workflow),
      false,
    ],
    [
      "an ignored failure",
      replace(
        "        env:\n",
        "        continue-on-error: true\n        env:\n",
        workflow,
      ),
      false,
    ],
    [
      "a fixed target",
      replace("${{ matrix.lane }}", "remaining", workflow),
      false,
    ],
  ])
    assert.equal(
      bad.test(problems({ workflow: source })),
      !ok,
      `${label}: ${problems({ workflow: source })}`,
    );
});

test("a lane named for letters runs only names with those letters", () => {
  // Splits the catch-all into a-to-n and o-to-z, as the name lanes do.
  const lettered = replace(
    "verify-app-go-remaining:\n\tcd services/platform && go test -race -count=1 -v -timeout=15m -skip '$(APP_PAIR)|$(APP_OPERATIONS)' ./...",
    "verify-app-go-a-to-n:\n\tcd services/platform && go test -race -count=1 -v -timeout=15m -run '^Test[A-N]' -skip '$(APP_PAIR)|$(APP_OPERATIONS)' ./...\n\nverify-app-go-o-to-z:\n\tcd services/platform && go test -race -count=1 -v -timeout=15m -skip '$(APP_PAIR)|$(APP_OPERATIONS)|^Test[A-N]' ./...",
    replace(
      "-j3 verify-app-go-remaining",
      "-j4 verify-app-go-a-to-n verify-app-go-o-to-z",
    ),
  );
  const lanes = (makefile, names) => ({
    makefile,
    workflow: replace(
      "[remaining, operations, pair]",
      `[${names}, operations, pair]`,
      workflow,
    ),
  });
  assert.equal(problems(lanes(lettered, "a-to-n, o-to-z")), "");
  // The last lane takes a name outside A to Z, as its -skip leaves it there.
  assert.equal(
    problems({
      ...lanes(lettered, "a-to-n, o-to-z"),
      tests: [...tests, { name: "Test_odd", directory: agent }],
    }),
    "",
  );
  const moved = replace("[A-N]", "[A-P]", lettered);
  assert.match(
    problems(lanes(moved, "a-to-n, o-to-z")),
    /verify-app-go-a-to-n runs TestOrdinary, whose name is outside its letters/,
  );
  const renamed = moved
    .replaceAll("a-to-n", "a-to-p")
    .replaceAll("o-to-z", "q-to-z");
  assert.equal(problems(lanes(renamed, "a-to-p, q-to-z")), "");
  // A single letter names a lane too, and only the lane ending at z takes the rest.
  assert.match(
    problems(lanes(lettered.replaceAll("o-to-z", "o"), "a-to-n, o")),
    /verify-app-go-o runs TestUnit, whose name is outside its letters/,
  );
});

test("the real lanes run any test name exactly once, in every package", () => {
  // Names no family selects, starting with every letter and with what comes after Z.
  const odd = ["Test", "Test_odd", "Test9", "Testlower"];
  const names = [
    ...[..."ABCDEFGHIJKLMNOPQRSTUVWXYZ"].map(
      (letter) => `Test${letter}nyNewBehaviour`,
    ),
    ...odd,
  ];
  const real = {
    makefile: readFileSync("Makefile", "utf8"),
    workflow: readFileSync(".github/workflows/application.yml", "utf8"),
    tests: [
      ...linuxTests(process.cwd(), "amd64").filter(
        ({ name }) => name !== "TestMain",
      ),
      ...names.flatMap((name) =>
        [integration, agent, "services/platform/cmd/cli"].map((directory) => ({
          name,
          directory,
        })),
      ),
    ],
  };
  assert.equal(appLaneProblems(real).join("\n"), "");
  // s-to-z is the lane that runs a name outside A to Z: without it, nothing does.
  const remainder = /\nverify-app-go-s-to-z:\n\t[^\n]*\n/.exec(
    real.makefile,
  )?.[0];
  assert.ok(remainder);
  const without = appLaneProblems({
    ...real,
    makefile: real.makefile.replace(remainder, "\n"),
  }).join("\n");
  for (const name of [...odd, "TestSnyNewBehaviour", "TestZnyNewBehaviour"])
    assert.match(
      without,
      new RegExp(
        `^${name} in services/platform/cmd/cli is selected by no`,
        "m",
      ),
    );
  assert.doesNotMatch(without, /TestRnyNewBehaviour in .* selected by no/);
});
