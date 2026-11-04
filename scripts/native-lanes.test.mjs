import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import { alternatives, nativeLaneProblems } from "./native-lanes.mjs";

const script = fileURLToPath(new URL("./native-lanes.mjs", import.meta.url));
const agent = "services/platform/internal/agent";
const hostnetwork = "services/platform/internal/hostnetwork";

const makefile = `NATIVE_GO = sudo env CGO_ENABLED=1
NATIVE_AGENT_GO = $(NATIVE_GO) ASTILBA_NATIVE_AGENT=1 /opt/go/bin/go test -race

verify-agent-host:
	$(MAKE) verify-agent-host-core
	$(MAKE) verify-agent-host-visual

verify-agent-host-core:
	cd services/platform && $(NATIVE_GO) /opt/go/bin/go test -v -run '^TestNativeManagedLinks$$' ./internal/hostnetwork
	cd services/platform && $(NATIVE_AGENT_GO) -timeout=30m -run '^TestNative((Compose|CrossHost)Deployment|Pipeline)$$' ./internal/agent

verify-agent-host-visual:
	cd services/platform && $(NATIVE_AGENT_GO) \\
		-run '^TestNativeVisualDeployment$$' ./internal/agent
`;
const workflow = "        lane: [core, visual]\n";
const tests = [
  ...[
    "TestNativeCrossHostHelper",
    "TestNativeComposeDeployment",
    "TestNativeCrossHostDeployment",
    "TestNativePipeline",
    "TestNativeVisualDeployment",
    "TestOrdinary",
  ].map((name) => ({ name, directory: agent })),
  ...["TestNativeEndpointHelper", "TestNativeManagedLinks"].map((name) => ({
    name,
    directory: hostnetwork,
  })),
];
const without = (name) => tests.filter((entry) => entry.name !== name);
const replace = (from, to) => {
  assert.ok(makefile.includes(from), from);
  return makefile.replaceAll(from, to);
};

test("alternatives distribute groups and keep sets and escapes whole", () => {
  for (const [pattern, expected] of [
    ["^a$", ["^a$"]],
    ["^a|b$", ["^a", "b$"]],
    ["^T((C|X)D|P)$", ["^TCD$", "^TXD$", "^TP$"]],
    ["^T(?:a|b)$", ["^Ta$", "^Tb$"]],
    ["^T[a|b]\\|c$", ["^T[a|b]\\|c$"]],
    ["^T[]|]$", ["^T[]|]$"]],
    ["^T(a|)$", ["^Ta$", "^T$"]],
  ])
    assert.deepEqual(alternatives(pattern), expected, pattern);
  for (const pattern of ["^T(a|b)?$", "^T(a", "^Ta)", "^T(?i)a$", "^T$/sub"])
    assert.throws(() => alternatives(pattern), Error, pattern);
});

test("a complete partition has no problems", () => {
  assert.deepEqual(nativeLaneProblems({ makefile, workflow, tests }), []);
});

test("reports each way a native test can stop running or run twice", () => {
  for (const [label, input, expected] of [
    [
      "a new test on no list",
      {
        tests: [...tests, { name: "TestNativeForgotten", directory: agent }],
      },
      /TestNativeForgotten in .*agent is selected by no verify-agent-host-\*/,
    ],
    [
      "the same name in a package the lane does not run",
      {
        tests: [
          ...tests,
          { name: "TestNativePipeline", directory: hostnetwork },
        ],
      },
      /TestNativePipeline in .*hostnetwork is selected by no/,
    ],
    [
      "a test selected by two lanes",
      {
        makefile: replace(
          "'^TestNativeVisualDeployment$$'",
          "'^TestNative(VisualDeployment|Pipeline)$$'",
        ),
      },
      /TestNativePipeline in .*agent is selected more than once: verify-agent-host-core .* and verify-agent-host-visual/,
    ],
    [
      "a test selected twice within a lane",
      {
        makefile: makefile.replace(
          "\nverify-agent-host-visual:",
          "\tcd services/platform && $(NATIVE_AGENT_GO) -run '^TestNativePipeline$$' ./internal/agent\n\nverify-agent-host-visual:",
        ),
      },
      /TestNativePipeline in .*agent is selected more than once/,
    ],
    [
      "a lane selecting a helper",
      {
        makefile: replace(
          "(Compose|CrossHost)Deployment",
          "(Compose|CrossHost)Deployment|CrossHostHelper",
        ),
      },
      /TestNativeCrossHostHelper is a helper that no lane may select/,
    ],
    [
      "an exception for a helper that is gone",
      { tests: without("TestNativeEndpointHelper") },
      /helper exception TestNativeEndpointHelper has no test in .*hostnetwork/,
    ],
    [
      "a helper name reused by an ordinary test elsewhere",
      {
        tests: [
          ...tests,
          { name: "TestNativeEndpointHelper", directory: agent },
        ],
      },
      /TestNativeEndpointHelper in .*agent is selected by no/,
    ],
    [
      "a lane the aggregate does not run",
      {
        makefile: replace("\t$(MAKE) verify-agent-host-visual\n", ""),
      },
      /verify-agent-host-visual is not run by verify-agent-host/,
    ],
    [
      "an aggregate target with no rule",
      {
        makefile: replace(
          "\t$(MAKE) verify-agent-host-visual\n",
          "\t$(MAKE) verify-agent-host-visual\n\t$(MAKE) verify-agent-host-gone\n",
        ),
      },
      /runs verify-agent-host-gone, which is not a lane rule/,
    ],
    [
      "an aggregate that runs tests itself",
      {
        makefile: replace(
          "\t$(MAKE) verify-agent-host-visual\n",
          "\t$(MAKE) verify-agent-host-visual\n\tgo test ./...\n",
        ),
      },
      /verify-agent-host may only run lanes/,
    ],
  ])
    assert.match(
      nativeLaneProblems({ makefile, workflow, tests, ...input }).join("\n"),
      expected,
      label,
    );
});

test("reports a -run alternative that matches no test, including nested ones", () => {
  for (const [removed, alternative] of [
    ["TestNativeComposeDeployment", "^TestNativeComposeDeployment$"],
    ["TestNativePipeline", "^TestNativePipeline$"],
    ["TestNativeVisualDeployment", "^TestNativeVisualDeployment$"],
  ]) {
    const problems = nativeLaneProblems({
      makefile,
      workflow,
      tests: without(removed),
    });
    assert.ok(
      problems.some((problem) =>
        problem.includes(`-run alternative ${alternative} matches no test`),
      ),
      problems.join("\n"),
    );
  }
  const problems = nativeLaneProblems({
    makefile,
    workflow,
    tests: [
      ...without("TestNativePipeline"),
      { name: "TestNativePipeline", directory: hostnetwork },
    ],
  }).join("\n");
  assert.match(problems, /alternative \^TestNativePipeline\$ matches no test/);
});

test("a lane may select tests other than native ones", () => {
  assert.deepEqual(
    nativeLaneProblems({
      makefile: replace(
        "(Compose|CrossHost)Deployment|Pipeline)$$'",
        "(Compose|CrossHost)Deployment|Pipeline)$$|^TestOrdinary$$'",
      ),
      workflow,
      tests,
    }),
    [],
  );
});

test("the workflow lane list must equal the aggregate's targets", () => {
  for (const [lane, ok] of [
    ["lane: [core, visual]", true],
    ["lane: ['visual', core] # order is free", true],
    ["lane: [core]", false],
    ["lane: [core, visual, recovery]", false],
    ["lane: [core, core, visual]", false],
    ["lane:\n  - core\n  - visual", false],
    ["# lane: [core, visual]", false],
  ]) {
    const problems = nativeLaneProblems({
      makefile,
      workflow: `        ${lane}\n`,
      tests,
    });
    assert.equal(problems.length === 0, ok, `${lane}: ${problems.join("; ")}`);
  }
});

test("refuses selections it cannot read rather than passing them", () => {
  for (const [label, from, to, expected] of [
    ["no -run", "-run '^TestNativeManagedLinks$$' ", "", /expected one -run/],
    ["-skip", "-v -run", "-skip '^X$$' -run", /-skip hides selected tests/],
    [
      "an undefined variable",
      "'^TestNativeManagedLinks$$'",
      "'$(UNDEFINED)'",
      /cannot resolve \$\(UNDEFINED\)/,
    ],
    [
      "a quantified group",
      "'^TestNativeManagedLinks$$'",
      "'^TestNative(ManagedLinks)?$$'",
      /cannot split a quantified group/,
    ],
    [
      "a subtest selection",
      "'^TestNativeManagedLinks$$'",
      "'^TestNativeManagedLinks$$/sub'",
      /selects whole tests/,
    ],
    [
      "syntax JavaScript cannot compile",
      "'^TestNativeManagedLinks$$'",
      "'^TestNative\\QManagedLinks\\E$$'",
      /Invalid/,
    ],
    ["no package", " ./internal/hostnetwork", "", /expected a \.\/ package/],
  ])
    assert.match(
      nativeLaneProblems({
        makefile: replace(from, to),
        workflow,
        tests,
      }).join("\n"),
      expected,
      label,
    );
  assert.match(
    nativeLaneProblems({
      makefile: makefile.replace(
        /^verify-agent-host-visual:[\s\S]*$/m,
        "verify-agent-host-visual:\n\techo nothing\n",
      ),
      workflow,
      tests,
    }).join("\n"),
    /verify-agent-host-visual runs no go test/,
  );
});

test("the check reads the repository's tests and exits non-zero on a missing one", async (t) => {
  const cwd = mkdtempSync(join(tmpdir(), "astilba-native-lanes-"));
  t.after(() => rmSync(cwd, { recursive: true, force: true }));
  const write = (path, content) => {
    mkdirSync(dirname(join(cwd, path)), { recursive: true });
    writeFileSync(join(cwd, path), content);
  };
  const go = (...names) =>
    names.map((name) => `func ${name}(t *testing.T) {\n}\n`).join("\n");
  write("Makefile", makefile);
  write(".github/workflows/ci.yml", workflow);
  write(
    `${agent}/agent_test.go`,
    go(
      "TestNativeCrossHostHelper",
      "TestNativeComposeDeployment",
      "TestNativeCrossHostDeployment",
      "TestNativePipeline",
      "TestNativeVisualDeployment",
    ),
  );
  // Linux-only files count wherever the check runs.
  write(
    `${hostnetwork}/host_linux_test.go`,
    `//go:build linux\n\npackage hostnetwork\n\n${go("TestNativeEndpointHelper", "TestNativeManagedLinks")}`,
  );
  // None of these reach the Go tool as tests.
  write(`${agent}/testdata/fixture_test.go`, go("TestNativeInTestdata"));
  write(`${agent}/_ignored/fixture_test.go`, go("TestNativeUnderscored"));
  write(`${agent}/support.go`, go("TestNativeNotInATestFile"));
  write(`${agent}/comment_test.go`, `// ${go("TestNativeCommented")}`);
  const check = () =>
    spawnSync(process.execPath, [script], { cwd, encoding: "utf8" });

  const passed = check();
  assert.equal(passed.status, 0, passed.stderr);

  write(`${agent}/new_test.go`, go("TestNativeNewlyAdded"));
  const failed = check();
  assert.equal(failed.status, 1);
  assert.match(
    failed.stderr,
    /TestNativeNewlyAdded in .*agent is selected by no/,
  );
});
