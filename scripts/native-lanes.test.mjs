import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import {
  mkdirSync,
  mkdtempSync,
  renameSync,
  rmSync,
  writeFileSync,
} from "node:fs";
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
const visualLine =
  "\tcd services/platform && $(NATIVE_AGENT_GO) \\\n\t\t-run '^TestNativeVisualDeployment$$' ./internal/agent\n";
const workflow = `        lane: [core, visual]
    steps:
      - name: Verify production agent with systemd
        run: make verify-agent-host-\${{ matrix.lane }}
`;
const tests = [
  ...[
    "TestNativeCrossHostHelper",
    "TestNativeBuildNetworkHelper",
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
// A function keeps `$$` literal, as String.replaceAll would collapse it to `$`.
const replace = (from, to, source = makefile) => {
  assert.ok(source.includes(from), from);
  return source.replaceAll(from, () => to);
};
const problems = (input) =>
  nativeLaneProblems({ makefile, workflow, tests, ...input }).join("\n");

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
  assert.equal(problems({}), "");
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
        makefile: replace(
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
      { makefile: replace("\t$(MAKE) verify-agent-host-visual\n", "") },
      /verify-agent-host-visual is not run by verify-agent-host/,
    ],
    [
      "an aggregate that names lanes as prerequisites",
      {
        makefile: replace(
          "verify-agent-host:\n\t$(MAKE) verify-agent-host-core\n\t$(MAKE) verify-agent-host-visual\n",
          "verify-agent-host: verify-agent-host-core verify-agent-host-visual\n",
        ),
      },
      /cannot read: verify-agent-host: verify-agent-host-core/,
    ],
    [
      "an aggregate with a prerequisite beyond its lanes",
      {
        makefile: replace(
          "verify-agent-host:\n",
          "verify-agent-host: extra-native\n",
        ),
      },
      /cannot read: verify-agent-host: extra-native/,
    ],
    [
      "an aggregate ignoring a lane's failure",
      {
        makefile: replace(
          "\t$(MAKE) verify-agent-host-visual\n",
          "\t-$(MAKE) verify-agent-host-visual\n",
        ),
      },
      /verify-agent-host may only run lanes/,
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
    assert.match(problems(input), expected, label);
});

test("reports a -run alternative that matches no test, including nested ones", () => {
  for (const [removed, alternative] of [
    ["TestNativeComposeDeployment", "^TestNativeComposeDeployment$"],
    ["TestNativePipeline", "^TestNativePipeline$"],
    ["TestNativeVisualDeployment", "^TestNativeVisualDeployment$"],
  ])
    assert.ok(
      problems({ tests: without(removed) }).includes(
        `-run alternative ${alternative} matches no test`,
      ),
      removed,
    );
  assert.match(
    problems({
      tests: [
        ...without("TestNativePipeline"),
        { name: "TestNativePipeline", directory: hostnetwork },
      ],
    }),
    /alternative \^TestNativePipeline\$ matches no test/,
  );
});

test("a lane may select tests other than native ones", () => {
  assert.equal(
    problems({
      makefile: replace(
        "(Compose|CrossHost)Deployment|Pipeline)$$'",
        "(Compose|CrossHost)Deployment|Pipeline)$$|^TestOrdinary$$'",
      ),
    }),
    "",
  );
});

test("the workflow must run exactly the aggregate's targets on every lane", () => {
  const step = "        run: make verify-agent-host-${{ matrix.lane }}\n";
  for (const [label, source, ok] of [
    ["unchanged", workflow, true],
    [
      "reordered and commented",
      replace(
        "lane: [core, visual]",
        "lane: ['visual', core] # order is free",
        workflow,
      ),
      true,
    ],
    ["short", replace("[core, visual]", "[core]", workflow), false],
    [
      "long",
      replace("[core, visual]", "[core, visual, recovery]", workflow),
      false,
    ],
    [
      "duplicate",
      replace("[core, visual]", "[core, core, visual]", workflow),
      false,
    ],
    [
      "block list",
      replace("lane: [core, visual]", "lane:\n  - core\n  - visual", workflow),
      false,
    ],
    ["commented out", replace("lane: [core", "# lane: [core", workflow), false],
    [
      "a lane excluded",
      replace(
        "lane: [core, visual]",
        "lane: [core, visual]\n        exclude:\n          - lane: visual",
        workflow,
      ),
      false,
    ],
    [
      "a fixed target",
      replace(step, "        run: make verify-agent-host-core\n", workflow),
      false,
    ],
    [
      "a conditional step",
      replace(step, `        if: matrix.lane != 'visual'\n${step}`, workflow),
      false,
    ],
    [
      "a condition after the run line",
      replace(step, `${step}        if: matrix.lane != 'visual'\n`, workflow),
      false,
    ],
    [
      "ignored step failures",
      replace(step, `${step}        continue-on-error: true\n`, workflow),
      false,
    ],
    ["a following step", `${workflow}      - name: Next\n`, true],
    ["a following job", `${workflow}\n  verify:\n`, true],
  ]) {
    const found = problems({ workflow: source });
    assert.equal(found === "", ok, `${label}: ${found}`);
  }
});

test("refuses a recipe that could stop naming tests from running", () => {
  const line =
    "cd services/platform && $(NATIVE_AGENT_GO) -run '^TestNativeVisualDeployment$$' ./internal/agent";
  const flagged = (flag) =>
    `cd services/platform && $(NATIVE_AGENT_GO) ${flag} -run '^TestNativeVisualDeployment$$' ./internal/agent`;
  for (const [label, recipe, expected] of [
    ["a guard", `[ -n "$$CI" ] || ${line}`, /cannot read shell syntax/],
    ["a whole-line comment", `# ${line}`, /cannot read shell syntax/],
    [
      "a trailing comment",
      replace("&& $(NATIVE_AGENT_GO)", "&& true # $(NATIVE_AGENT_GO)", line),
      /cannot read shell syntax/,
    ],
    ["hidden failure", `${line} || true`, /cannot read shell syntax/],
    ["an ignored failure", `-${line}`, /a - prefix ignores go test failures/],
    [
      "flags outside the allowlist",
      replace("$(NATIVE_AGENT_GO)", "GOFLAGS=-skip=X $(NATIVE_AGENT_GO)", line),
      /expected only cd and go test/,
    ],
    ["a later command", `exit 0; ${line}`, /cannot read shell syntax/],
    [
      "echo",
      replace("&& $(", "&& echo $(", line),
      /expected only cd and go test/,
    ],
    ["-list", flagged("-list ."), /cannot read go test argument -list/],
    ["-c", flagged("-c"), /cannot read go test argument -c/],
    ["-count=0", flagged("-count=0"), /cannot read go test argument -count=0/],
    ["-skip", flagged("-skip '^X$$'"), /cannot read go test argument -skip/],
    [
      "no -run",
      replace("-run '^TestNativeVisualDeployment$$' ", "", line),
      /expected one -run/,
    ],
    [
      "no package",
      replace(" ./internal/agent", "", line),
      /expected a \.\/ package/,
    ],
    [
      "an undefined variable",
      replace("'^TestNativeVisualDeployment$$'", "'$(UNDEFINED)'", line),
      /cannot resolve .*\$\(UNDEFINED\)/,
    ],
    [
      "a single-character make variable",
      replace("Deployment$$'", "Deployment$|^TestOrdinary$$'", line),
      /cannot resolve .*\$\|/,
    ],
    [
      "a quantified group",
      replace("Native", "Native(Visual)?", line),
      /cannot split a quantified group/,
    ],
    [
      "a subtest selection",
      replace("Deployment$$", "Deployment$$/sub", line),
      /selects whole tests/,
    ],
    [
      "syntax JavaScript cannot compile",
      replace("Visual", "\\QVisual\\E", line),
      /Invalid/,
    ],
  ]) {
    const found = problems({
      makefile: replace(visualLine, `\t${recipe}\n`),
    });
    assert.match(found, expected, label);
    assert.doesNotMatch(found, /so CI never runs it/, label);
  }
  assert.match(
    problems({ makefile: replace(visualLine, "\t@echo nothing\n") }),
    /verify-agent-host-visual runs no go test/,
  );
});

test("reports Makefile syntax that could change what a lane runs", () => {
  for (const [label, source, expected] of [
    [
      "an appended variable",
      replace(
        "\nverify-agent-host:",
        "NATIVE_AGENT_GO += -skip '^TestNativePipeline$$'\n\nverify-agent-host:",
      ),
      /cannot read: NATIVE_AGENT_GO \+=/,
    ],
    [
      "a target-specific variable on a lane",
      `verify-agent-host-visual: NATIVE_AGENT_GO = true\n${makefile}`,
      /cannot read: verify-agent-host-visual: NATIVE_AGENT_GO = true/,
    ],
    [
      "a pattern-specific variable",
      `verify-%: NATIVE_AGENT_GO = true\n${makefile}`,
      /cannot read: verify-%: NATIVE_AGENT_GO = true/,
    ],
    [
      "a lane prerequisite",
      replace(
        "verify-agent-host-visual:\n",
        "verify-agent-host-visual: setup\n",
      ),
      /cannot read: verify-agent-host-visual: setup/,
    ],
    [
      "a conditional recipe",
      replace(visualLine, `ifdef NATIVE_VISUAL\n${visualLine}endif\n`),
      /cannot read: ifdef NATIVE_VISUAL/,
    ],
    [
      "a define after a lane",
      `${replace(visualLine, "\t@echo nothing\n")}\ndefine SPARE\n${visualLine}endef\n`,
      /cannot read: define SPARE/,
    ],
  ])
    assert.match(problems({ makefile: source }), expected, label);
});

test("a make variable set elsewhere is harmless outside go test arguments", () => {
  for (const source of [
    replace(visualLine, `\t@echo arch=$(PLAT4_ARCH)\n${visualLine}`),
    replace(
      "ASTILBA_NATIVE_AGENT=1",
      'ASTILBA_NATIVE_AGENT=1 PLAT4_ARCH="$(PLAT4_ARCH)"',
    ),
    replace("ASTILBA_NATIVE_AGENT=1", "ASTILBA_NATIVE_AGENT=1 ARCH=$(ARCH)"),
  ])
    assert.equal(problems({ makefile: source }), "", source);
});

test("the check asks Go which tests each Linux architecture can run", async (t) => {
  const cwd = mkdtempSync(join(tmpdir(), "astilba-native-lanes-"));
  t.after(() => rmSync(cwd, { recursive: true, force: true }));
  const write = (path, content) => {
    mkdirSync(dirname(join(cwd, path)), { recursive: true });
    writeFileSync(join(cwd, path), content);
  };
  const go = (directory, names, constraint = "") =>
    `${constraint}package ${directory}\n\nimport "testing"\n\n${names
      .map((name) => `func ${name}(t *testing.T) {\n}\n`)
      .join("\n")}`;
  const agentTest = (path, names, constraint) =>
    write(`${agent}/${path}`, go("agent", names, constraint));
  write("Makefile", makefile);
  write(".github/workflows/ci.yml", workflow);
  write("services/platform/go.mod", "module example.invalid/platform\n");
  agentTest("agent_test.go", [
    "TestNativeCrossHostHelper",
    "TestNativeBuildNetworkHelper",
    "TestNativeComposeDeployment",
    "TestNativeCrossHostDeployment",
    "TestNativeVisualDeployment",
  ]);
  agentTest("pipeline_test.go", ["TestNativePipeline"]);
  write(
    `${hostnetwork}/host_linux_test.go`,
    go(
      "hostnetwork",
      ["TestNativeEndpointHelper", "TestNativeManagedLinks"],
      "//go:build linux\n\n",
    ),
  );
  // None of these reach a lane's `go test`.
  agentTest("testdata/fixture_test.go", ["TestNativeInTestdata"]);
  agentTest("_ignored/fixture_test.go", ["TestNativeUnderscored"]);
  agentTest(
    "norace_test.go",
    ["TestNativeWithoutRace"],
    "//go:build !race\n\n",
  );
  agentTest("mac_darwin_test.go", ["TestNativeOnDarwin"]);
  write(
    `${agent}/support.go`,
    "package agent\n\nfunc TestNativeNotInATestFile() {}\n",
  );
  agentTest("comment_test.go", []);
  write(
    `${agent}/comment_test.go`,
    `${go("agent", [])}// func TestNativeCommented(t *testing.T) {}\n`,
  );
  // A stub for other platforms is the same test, not a second selection.
  agentTest(
    "pipeline_other_test.go",
    ["TestNativePipeline"],
    "//go:build !linux\n\n",
  );
  const check = () =>
    spawnSync(process.execPath, [script], { cwd, encoding: "utf8" });

  const passed = check();
  assert.equal(passed.status, 0, passed.stderr);
  assert.match(passed.stdout, /5 selected, 3 helpers/);

  agentTest("new_test.go", ["TestNativeNewlyAdded"]);
  const missing = check();
  assert.equal(missing.status, 1);
  assert.match(
    missing.stderr,
    /^TestNativeNewlyAdded in .*agent is selected by no/m,
  );
  assert.doesNotMatch(missing.stderr, / only\)/);
  rmSync(join(cwd, agent, "new_test.go"));

  // A selected test that one architecture cannot build never runs on that lane.
  renameSync(
    join(cwd, agent, "pipeline_test.go"),
    join(cwd, agent, "pipeline_amd64_test.go"),
  );
  const partial = check();
  assert.equal(partial.status, 1);
  assert.match(
    partial.stderr,
    /alternative \^TestNativePipeline\$ matches no test .*\(linux\/arm64 only\)/,
  );
});
