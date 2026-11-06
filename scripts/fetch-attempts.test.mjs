import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import {
  chmodSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { mock, test } from "node:test";
import {
  downloadVerified,
  fetchingCommand,
  pullImages,
  runLogged,
  withFetchAttempts,
} from "./fetch-attempts.mjs";

mock.method(console, "warn", () => {});
mock.method(process.stderr, "write", () => true);

const failing = (count, value = "built") => {
  const limits = [];
  const step = (limit) => {
    limits.push(limit);
    if (limits.length <= count) throw new Error(`failure ${limits.length}`);
    return value;
  };
  return { limits, step };
};
const clock = () => {
  let elapsed = 0;
  return {
    now: () => elapsed,
    sleep: async (ms) => {
      elapsed += ms;
    },
  };
};

test("one dropped connection does not fail the step", async () => {
  const { limits, step } = failing(1);
  assert.equal(
    await withFetchAttempts("Step", step, { limitMs: 600_000, ...clock() }),
    "built",
  );
  assert.equal(limits.length, 2);
});

test("a step that keeps failing stops after three attempts", async () => {
  const { limits, step } = failing(5);
  await assert.rejects(
    withFetchAttempts("Step", step, { limitMs: 600_000, ...clock() }),
    /Step failed: failure 3/,
  );
  assert.equal(limits.length, 3);
});

test("the attempts share the time limit rather than each having their own", async () => {
  const { limits, step } = failing(2);
  await withFetchAttempts("Step", step, { limitMs: 600_000, ...clock() });
  assert.deepEqual(limits, [600_000, 580_000, 540_000]);
});

test("no attempt starts once the limit would be spent", async () => {
  const { limits, step } = failing(5);
  await assert.rejects(
    withFetchAttempts("Step", step, { limitMs: 15_000, ...clock() }),
    /failure 1/,
  );
  assert.equal(limits.length, 1);
});

test("a command that used up the limit is not started again", async () => {
  let attempts = 0;
  await assert.rejects(
    withFetchAttempts(
      "Step",
      (timeoutMs) => {
        attempts++;
        return runLogged("sleep", ["5"], {
          timeoutMs: Math.min(timeoutMs, 50),
        });
      },
      { limitMs: 60_000, ...clock() },
    ),
    { code: "ETIMEDOUT" },
  );
  assert.equal(attempts, 1);
});

test("a command that could not start is not started again", async () => {
  let attempts = 0;
  await assert.rejects(
    withFetchAttempts(
      "Step",
      () => {
        attempts++;
        return runLogged("/nonexistent/command", []);
      },
      clock(),
    ),
    /Step failed/,
  );
  assert.equal(attempts, 1);
});

// A real process in docker's place, printing what a real pull printed.
function fakeCommand(t, script) {
  const directory = mkdtempSync(join(tmpdir(), "astilba-fetch-"));
  t.after(() => rmSync(directory, { recursive: true, force: true }));
  const path = join(directory, "command");
  const calls = join(directory, "calls");
  writeFileSync(
    path,
    `#!/bin/sh\nprintf '%s\\n' "$*" >> "${calls}"\nattempt=$(wc -l < "${calls}")\n${script}\n`,
  );
  chmodSync(path, 0o755);
  return {
    path,
    calls: () => readFileSync(calls, "utf8").trim().split("\n"),
  };
}
const failFirst = (message) =>
  `if [ "$attempt" -eq 1 ]; then echo '${message}' >&2; exit 1; fi`;
const digest = "a".repeat(64);
const image = `busybox:1.37.0@sha256:${digest}`;

for (const [store, message] of [
  [
    "graph driver",
    `Error response from daemon: Get "https://registry-1.docker.io/v2/": read: connection reset by peer`,
  ],
  [
    "containerd",
    `Error response from daemon: failed to resolve reference "${image}": ${image}: not found`,
  ],
  [
    "either",
    "Error response from daemon: received unexpected HTTP status: 502 Bad Gateway",
  ],
])
  test(`a pull recovers from a dropped manifest request (${store})`, async (t) => {
    const docker = fakeCommand(t, failFirst(message));
    await pullImages([image], [docker.path, "--host", "unix:///x"], clock());
    assert.deepEqual(docker.calls(), [
      `--host unix:///x pull ${image}`,
      `--host unix:///x pull ${image}`,
    ]);
  });

// Measured with a fake registry changing bytes, on both Docker image stores.
for (const message of [
  `failed commit on ref "manifest-sha256:${digest}": commit failed: unexpected commit digest sha256:${digest}, expected sha256:${digest}: failed precondition`,
  `manifest verification failed for digest sha256:${digest}`,
  `filesystem layer verification failed for digest sha256:${digest}`,
  `error pulling image configuration: image config verification failed for digest sha256:${digest}`,
])
  test(`a pull stops at once on ${message.slice(0, 40)}`, async (t) => {
    const docker = fakeCommand(t, `echo '${message}' >&2; exit 1`);
    await assert.rejects(
      pullImages([image], [docker.path], clock()),
      (error) => {
        assert.ok(error.message.includes(image), error.message);
        return true;
      },
    );
    assert.equal(docker.calls().length, 1);
  });

test("a pull outlasts four minutes of outage, then names the image", async (t) => {
  const docker = fakeCommand(t, `echo 'read: connection reset' >&2; exit 1`);
  const time = clock();
  await assert.rejects(pullImages([image], [docker.path], time), (error) => {
    assert.ok(error.message.includes(image), error.message);
    assert.match(error.message, /connection reset/);
    return true;
  });
  assert.ok(time.now() >= 240_000, `gave up after ${time.now()} ms`);
});

test("a pull needs a digest", async (t) => {
  const docker = fakeCommand(t, "exit 0");
  await assert.rejects(
    pullImages(["busybox:1.37.0"], [docker.path], clock()),
    /busybox:1\.37\.0 needs a digest/,
  );
});

test("a command that ran to completion is not repeated, whatever it reported", async (t) => {
  const command = fakeCommand(t, `echo '{"finding":{}}'; exit 0`);
  assert.equal(
    await fetchingCommand("Scanning", command.path, ["scan"], {}, clock()),
    '{"finding":{}}\n',
  );
  assert.equal(command.calls().length, 1);
});

test("a command recovers from a reset and returns the later output", async (t) => {
  const command = fakeCommand(
    t,
    `${failFirst('fetching vulnerabilities: Get "https://vuln.go.dev/index": EOF')}\necho result`,
  );
  assert.equal(
    await fetchingCommand("Scanning", command.path, ["scan"], {}, clock()),
    "result\n",
  );
  assert.equal(command.calls().length, 2);
});

test("a command that cannot complete fails under its name, after four minutes", async (t) => {
  const command = fakeCommand(
    t,
    `echo 'creating client: unrecognized vulndb format' >&2; exit 1`,
  );
  const time = clock();
  await assert.rejects(
    fetchingCommand(
      "Scanning against the database",
      command.path,
      ["scan"],
      {},
      time,
    ),
    /Scanning against the database failed: creating client/,
  );
  assert.ok(time.now() >= 240_000, `gave up after ${time.now()} ms`);
});

for (const message of [
  "verifying golang.org/x/vuln@v1.3.0: checksum mismatch",
  "SECURITY ERROR\nThis download does NOT match an earlier download recorded in go.sum.",
])
  test(`a Go fetch stops at once on ${message.slice(0, 30)}`, async (t) => {
    const command = fakeCommand(t, `echo '${message}' >&2; exit 1`);
    await assert.rejects(
      fetchingCommand("Fetching", command.path, ["scan"], {}, clock()),
    );
    assert.equal(command.calls().length, 1);
  });

// A real server, so these exercise fetch's own failures, on a clock that only
// the pauses between attempts advance.
async function download(t, attempts, { expected = pinned, ...options } = {}) {
  const requests = [];
  const server = createServer((request, response) => {
    requests.push(request.url);
    attempts[Math.min(requests.length, attempts.length) - 1](response);
  });
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  t.after(() => {
    server.closeAllConnections();
    server.close();
  });
  let elapsed = 0;
  const url = `http://127.0.0.1:${server.address().port}/file`;
  const result = downloadVerified(url, "sha256", expected, {
    now: () => elapsed,
    sleep: async (ms) => {
      elapsed += ms;
    },
    ...options,
  });
  return { url, requests, result, elapsed: () => elapsed };
}
const file = Buffer.from("scanner");
const pinned = createHash("sha256").update(file).digest("hex");
const serve = (response) => response.end(file);
const status = (code) => (response) => {
  response.statusCode = code;
  response.end();
};
const dropBeforeResponding = (response) => response.socket.destroy();
const dropMidBody = (response) => {
  response.writeHead(200, { "Content-Length": file.length * 2 });
  response.write(file, () => response.socket.destroy());
};

test("a failed download is asked for again until it arrives", async (t) => {
  const { requests, result } = await download(t, [
    status(504),
    dropBeforeResponding,
    dropMidBody,
    serve,
  ]);
  assert.deepEqual(await result, file);
  assert.equal(requests.length, 4);
});

test("a stalled download gives way to the next attempt", async (t) => {
  for (const stall of [
    () => {},
    (response) => {
      response.writeHead(200, { "Content-Length": file.length * 2 });
      response.write(file);
    },
  ]) {
    // On a loaded machine the next attempt can look stalled too.
    const { requests, result } = await download(t, [stall, serve], {
      idleMs: 100,
    });
    assert.deepEqual(await result, file);
    assert.ok(requests.length >= 2);
  }
});

test("a slow download that keeps progressing finishes", async (t) => {
  const trickle = (response) => {
    response.writeHead(200, { "Content-Length": file.length });
    let sent = 0;
    const timer = setInterval(() => {
      response.write(file.subarray(sent, ++sent));
      if (sent === file.length) {
        clearInterval(timer);
        response.end();
      }
    }, 250);
  };
  const { requests, result } = await download(t, [trickle], { idleMs: 1000 });
  assert.deepEqual(await result, file);
  assert.equal(requests.length, 1);
});

test("an outage is outlasted for four minutes, then names the URL", async (t) => {
  for (const attempt of [status(504), dropBeforeResponding]) {
    const { url, result, elapsed } = await download(t, [attempt]);
    await assert.rejects(result, (error) => {
      assert.ok(error.message.includes(url), error.message);
      return true;
    });
    assert.ok(elapsed() >= 240_000, `gave up after ${elapsed()} ms`);
  }
});

test("a file failing its hash stops at the first attempt", async (t) => {
  const { url, requests, result } = await download(t, [serve], {
    expected: "0".repeat(64),
  });
  await assert.rejects(result, (error) => {
    assert.match(error.message, /pinned hash/);
    assert.ok(error.message.includes(url), error.message);
    return true;
  });
  assert.equal(requests.length, 1);
});
