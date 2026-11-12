import assert from "node:assert/strict";
import { test } from "node:test";
import { clip, inert, syncIssue } from "./report-issue.mjs";

const fakeGitHub = (issues) => {
  const sent = [];
  return {
    sent,
    request: async (path, { method = "GET", body } = {}) => {
      if (method !== "GET") sent.push({ method, path, ...body });
      // GitHub answers an unknown creator with an empty list, not an error.
      else assert.match(path, /[?&]creator=github-actions%5Bbot%5D(&|$)/);
      return { json: async () => issues };
    },
  };
};
const title = "Scheduled check failed";
const problem = (key) => ({ key, line: `- ${key}` });

test("opens one issue for the first problem, keeping its keys", async () => {
  const none = fakeGitHub([{ number: 3, title: "Another issue" }]);
  assert.equal(
    await syncIssue(none.request, "o/r", {
      title,
      problems: [problem("a"), problem("b")],
      body: "today",
    }),
    "opened",
  );
  assert.equal(none.sent.length, 1);
  const [{ method, path, body }] = none.sent;
  assert.deepEqual([method, path], ["POST", "/repos/o/r/issues"]);
  assert.match(
    body,
    /^<!-- report keys: [0-9a-f]{16} [0-9a-f]{16} -->\ntoday$/,
  );
  assert.equal(none.sent[0].title, title);
});

test("the same problems on a later day update the issue without a comment", async () => {
  const first = fakeGitHub([]);
  await syncIssue(first.request, "o/r", {
    title,
    problems: [problem("a")],
    body: "Monday",
  });
  const open = [{ number: 7, title, body: first.sent[0].body }];
  const again = fakeGitHub(open);
  assert.equal(
    await syncIssue(again.request, "o/r", {
      title,
      problems: [problem("a")],
      body: "Tuesday",
    }),
    "updated",
  );
  assert.deepEqual(again.sent, [
    {
      method: "PATCH",
      path: "/repos/o/r/issues/7",
      body: first.sent[0].body.replace("Monday", "Tuesday"),
    },
  ]);
});

test("a new problem is announced in one comment, naming only what is new", async () => {
  const first = fakeGitHub([]);
  await syncIssue(first.request, "o/r", {
    title,
    problems: [problem("a")],
    body: "",
  });
  const open = [{ number: 7, title, body: first.sent[0].body }];
  const more = fakeGitHub(open);
  assert.equal(
    await syncIssue(more.request, "o/r", {
      title,
      problems: [problem("a"), problem("b"), problem("b")],
      body: "",
    }),
    "commented",
  );
  assert.equal(more.sent.length, 2);
  assert.deepEqual(more.sent[1], {
    method: "POST",
    path: "/repos/o/r/issues/7/comments",
    body: "New since the last report (1):\n\n- b",
  });
});

test("an issue without keys, from before they were kept, hears every problem once", async () => {
  const legacy = fakeGitHub([{ number: 7, title, body: "over budget" }]);
  assert.equal(
    await syncIssue(legacy.request, "o/r", {
      title,
      problems: [problem("a")],
      body: "",
    }),
    "commented",
  );
});

test("closes the issue once nothing is wrong, unless the report could not tell", async () => {
  const open = [{ number: 7, title, body: "" }];
  const unsure = fakeGitHub(open);
  assert.equal(
    await syncIssue(unsure.request, "o/r", {
      title,
      problems: [],
      body: "",
      undecided: true,
    }),
    "unchanged",
  );
  assert.deepEqual(unsure.sent, []);

  const recovered = fakeGitHub(open);
  assert.equal(
    await syncIssue(recovered.request, "o/r", {
      title,
      problems: [],
      body: "passed",
    }),
    "closed",
  );
  assert.deepEqual(recovered.sent, [
    {
      method: "PATCH",
      path: "/repos/o/r/issues/7",
      body: "<!-- report keys:  -->\npassed",
      state: "closed",
      state_reason: "completed",
    },
  ]);

  const nothing = fakeGitHub([]);
  await syncIssue(nothing.request, "o/r", { title, problems: [], body: "" });
  assert.deepEqual(nothing.sent, []);
});

test("ignores pull requests and other titles, and keeps the oldest of two", async () => {
  const github = fakeGitHub([
    { number: 9, title, body: "", pull_request: {} },
    { number: 8, title, body: "" },
    { number: 5, title, body: "" },
  ]);
  await syncIssue(github.request, "o/r", { title, problems: [], body: "" });
  assert.equal(github.sent[0].path, "/repos/o/r/issues/5");
});

test("scanner text cannot mention, link or break out of its code span", () => {
  assert.equal(
    inert("see @owner and [x](https://e.test)\n#1 `rm`"),
    "`see @owner and [x](https://e.test) #1 'rm'`",
  );
  assert.equal(inert("  "), "");
  assert.equal(inert("abcdef", 4), "`abc…`");
});

test("a long body is cut at a line, under GitHub's limit", () => {
  const long = Array.from({ length: 5000 }, (_, i) => `line ${i}`).join("\n");
  const cut = clip(long, 1000);
  assert.ok(cut.length <= 1000);
  assert.match(cut, /\nline \d+\n\n…cut to fit the issue/);
  assert.equal(clip("short", 1000), "short");
});
