import { createHash } from "node:crypto";

// A scheduled run's own failure notice goes to whichever account GitHub holds
// responsible for the schedule, often a bot, so scheduled checks report through an
// issue instead. Each report keeps at most one open issue, which says what is wrong
// today and closes once nothing is.

const api = "https://api.github.com";
// GitHub refuses a body or comment over 65,536 characters.
const textLimit = 60_000;
// What the issue listed, and what earlier runs found that a check could not look at
// this time. Only what it listed counts as accepted when a person closes it.
const seenMarker = /^<!-- report keys: ([0-9a-f ]*) -->$/m;
const unseenMarker = /^<!-- report unseen keys: ([0-9a-f ]*) -->$/m;

export function github(token, fetchImpl = fetch) {
  return async (path, { method = "GET", body } = {}) => {
    const response = await fetchImpl(`${api}${path}`, {
      method,
      headers: {
        accept: "application/vnd.github+json",
        authorization: `Bearer ${token}`,
      },
      body: body && JSON.stringify(body),
    });
    if (!response.ok)
      throw new Error(`${method} ${path} returned ${response.status}`);
    return response;
  };
}

// Text from a scanner, an advisory or a registry reaches a public issue, so it is shown
// as code: a mention, link, reference or markup in it stays inert.
export function inert(text, limit = 300) {
  const flat = String(text)
    .replace(/[\s\u0000-\u001f\u007f]+/g, " ")
    .replaceAll("`", "'")
    .trim();
  const cut = flat.length > limit ? `${flat.slice(0, limit - 1)}…` : flat;
  return cut ? `\`${cut}\`` : "";
}

export function clip(
  text,
  limit = textLimit,
  note = "\n\n…cut to fit the issue. The run's summary has the rest.",
) {
  if (text.length <= limit) return text;
  const line = text.lastIndexOf("\n", limit - note.length);
  return text.slice(0, line > 0 ? line : limit - note.length) + note;
}

// Eight hex digits keep thousands of keys in one body; a new key matches a known one by
// chance about once in a million reports.
const digest = (key) =>
  createHash("sha256").update(key).digest("hex").slice(0, 8);
// A problem may also be known by aliases, such as an advisory's other ids, so it stays
// the same problem when an id is added.
const digests = ({ key, aliases = [] }) =>
  [key, ...aliases.slice(0, maxAliases)].map(digest);

const read = (pattern, body) =>
  new Set(
    pattern
      .exec(body ?? "")?.[1]
      .split(" ")
      .filter(Boolean) ?? [],
  );
const line = (name, keys) => `<!-- report ${name}: ${[...keys].join(" ")} -->`;
// Each key takes 9 characters of the body, so the markers stay well inside its limit.
const maxKeys = 5000;
// Each problem's aliases are capped, and primary keys come first, so a cap can only
// drop aliases.
export const maxAliases = 10;
const keysOf = (problems) =>
  new Set(
    [
      ...problems.map(({ key }) => digest(key)),
      ...problems.flatMap(({ aliases = [] }) =>
        aliases.slice(0, maxAliases).map(digest),
      ),
    ].slice(0, maxKeys),
  );
const pageSize = 100;
const maxPages = 20;

// The newest issue this workflow opened with the title. Only this workflow's own
// issues count, as anyone can open one with the same title.
async function newest(request, repository, title, state) {
  for (let page = 1; page <= maxPages; page++) {
    const issues = await (
      await request(
        `/repos/${repository}/issues?state=${state}&creator=github-actions%5Bbot%5D&sort=created&direction=desc&per_page=${pageSize}&page=${page}`,
      )
    ).json();
    const match = issues.find(
      (issue) => issue.title === title && !issue.pull_request,
    );
    if (match || issues.length < pageSize) return match;
  }
}

// Takes each problem as a stable key and a line of markdown that names it. Keys are
// kept in the issue body, so a later run comments only on problems that are new, and
// the same problem on the next day changes the body without notifying anyone. A
// problem marked `check` is a check that did not run: earlier problems may still be
// there unseen, so their keys stay too, and they are not new when it runs again. A
// report that could not tell passes `undecided` to leave the issue alone.
//
// With `acknowledge`, a person closing the issue accepts what it listed: a later run
// opens a new issue only for a problem the closed one did not have. A check that did
// not run is never accepted, so closing the issue cannot silence the report, and a
// problem accepted and then gone is new again if it returns.
export async function syncIssue(
  request,
  repository,
  { title, problems, body, undecided = false, acknowledge = false },
) {
  const unique = problems.filter(
    ({ key }, index) =>
      problems.findIndex((other) => other.key === key) === index,
  );
  const current = keysOf(unique);
  const known = (keys) => (problem) =>
    digests(problem).some((key) => keys.has(key));
  const complete = !unique.some(({ check }) => check);
  // A run that could not check carries earlier keys it did not see, so they are not
  // new when it sees them again.
  const unseen = (earlier) =>
    complete
      ? new Set()
      : new Set(
          [...earlier.difference(current)].slice(0, maxKeys - current.size),
        );
  const write = (seen, carried, text) =>
    clip(
      [
        line("keys", seen),
        ...(carried.size ? [line("unseen keys", carried)] : []),
        text,
      ].join("\n"),
    );
  const announce = (heading, fresh) =>
    [
      `${heading} (${fresh.length}):`,
      "",
      ...fresh.map(({ line }) => line),
    ].join("\n");
  const patch = (issue, fields) =>
    request(`/repos/${repository}/issues/${issue.number}`, {
      method: "PATCH",
      body: fields,
    });
  if (undecided && !unique.length) return "unchanged";
  const open = await newest(request, repository, title, "open");
  if (open && !unique.length) {
    await patch(open, {
      body: write(new Set(), new Set(), body),
      state: "closed",
      state_reason: "completed",
    });
    return "closed";
  }
  const closed = acknowledge
    ? await newest(request, repository, title, "closed")
    : undefined;
  const accepted = read(seenMarker, closed?.body);
  // An issue opened for a check that has since run again, or for a problem now gone,
  // goes back to what a person accepted, rather than staying open over it.
  if (open && complete && unique.every(known(accepted))) {
    await patch(open, {
      body: write(current, new Set(), body),
      state: "closed",
      state_reason: "completed",
    });
    return "accepted";
  }
  if (open) {
    const earlier = read(seenMarker, open.body).union(
      read(unseenMarker, open.body),
    );
    const fresh = unique.filter((problem) => !known(earlier)(problem));
    // An edited body notifies nobody, so what is new is said in a comment, first: if
    // the comment fails, the next run still finds these new.
    if (fresh.length)
      await request(`/repos/${repository}/issues/${open.number}/comments`, {
        method: "POST",
        body: { body: clip(announce("New since the last report", fresh)) },
      });
    await patch(open, { body: write(current, unseen(earlier), body) });
    return fresh.length ? "commented" : "updated";
  }
  const fresh = unique.filter(
    (problem) => problem.check || !known(accepted)(problem),
  );
  if (!fresh.length) {
    // What is accepted follows what is still there, under the ids it has today.
    if (!current.isSubsetOf(accepted) || !accepted.isSubsetOf(current))
      await patch(closed, {
        body: closed.body.replace(seenMarker, line("keys", current)),
      });
    return unique.length ? "acknowledged" : "unchanged";
  }
  const text = accepted.size
    ? `${announce(`New since #${closed.number} was closed`, fresh)}\n\n${body}`
    : body;
  await request(`/repos/${repository}/issues`, {
    method: "POST",
    body: { title, body: write(current, unseen(accepted), text) },
  });
  return "opened";
}

// A section's title comes from the report, so anything but plain words is replaced.
const sectionTitle = (name) =>
  /^[A-Za-z][A-Za-z ,-]{0,59}$/.test(name ?? "") ? name : "Other";

function grouped(entries) {
  const groups = new Map();
  for (const { section, text } of entries) {
    const title = sectionTitle(section);
    groups.set(title, [...(groups.get(title) ?? []), `- ${inert(text, 600)}`]);
  }
  return groups;
}

// Problems as headed lists, then what is only for information folded away. Every line
// is inert, so a report's text never becomes markup.
export function renderSections({ problems = [], info = [] }) {
  const lines = [];
  for (const [title, items] of grouped(problems))
    lines.push(`### ${title} (${items.length})`, "", ...items, "");
  for (const [title, items] of grouped(info))
    lines.push(
      `<details><summary>${title} (${items.length}), for information</summary>`,
      "",
      ...items,
      "",
      "</details>",
      "",
    );
  return lines.join("\n").trim();
}
