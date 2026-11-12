import { createHash } from "node:crypto";

// A scheduled run's own failure notice goes to whichever account GitHub holds
// responsible for the schedule, often a bot, so scheduled checks report through an
// issue instead. Each report keeps at most one open issue, which says what is wrong
// today and closes once nothing is.

const api = "https://api.github.com";
// GitHub refuses a body or comment over 65,536 characters.
const textLimit = 60_000;
const marker = /^<!-- report keys: ([0-9a-f ]*) -->$/m;

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

export function clip(text, limit = textLimit) {
  if (text.length <= limit) return text;
  const note = "\n\n…cut to fit the issue. The run's summary has the rest.";
  return text.slice(0, text.lastIndexOf("\n", limit - note.length)) + note;
}

const digest = (key) =>
  createHash("sha256").update(key).digest("hex").slice(0, 12);

const keysIn = (body) =>
  new Set(
    marker
      .exec(body ?? "")?.[1]
      .split(" ")
      .filter(Boolean) ?? [],
  );

// Takes each problem as a stable key and a line of markdown that names it. Keys are
// kept in the issue body, so a later run comments only on problems that are new, and
// the same problem on the next day changes the body without notifying anyone. A
// report that could not tell passes `undecided` to leave an open issue alone. Only
// this workflow's own issues count, as anyone can open one with the same title.
//
// With `acknowledge`, a person closing the issue accepts what it listed: a later run
// opens a new issue only for a problem the closed one did not have. Closing it here
// clears its keys, so a problem that returns after a recovery is new again.
export async function syncIssue(
  request,
  repository,
  { title, problems, body, undecided = false, acknowledge = false },
) {
  const list = async (state) =>
    (
      await (
        await request(
          `/repos/${repository}/issues?state=${state}&creator=github-actions%5Bbot%5D&sort=updated&direction=desc&per_page=100`,
        )
      ).json()
    ).filter((issue) => issue.title === title && !issue.pull_request);
  const open = (await list("open")).toSorted((a, b) => a.number - b.number)[0];
  const unique = problems.filter(
    ({ key }, index) =>
      problems.findIndex((other) => other.key === key) === index,
  );
  const mark = (text) =>
    clip(
      `<!-- report keys: ${unique.map(({ key }) => digest(key)).join(" ")} -->\n${text}`,
    );
  const newSince = (known) =>
    unique.filter(({ key }) => !known.has(digest(key)));
  const announce = (heading, fresh) =>
    [
      `${heading} (${fresh.length}):`,
      "",
      ...fresh.map(({ line }) => line),
    ].join("\n");
  if (unique.length && !open) {
    let text = body;
    if (acknowledge) {
      const [closed] = (await list("closed")).toSorted((a, b) =>
        (b.closed_at ?? "").localeCompare(a.closed_at ?? ""),
      );
      const accepted = keysIn(closed?.body);
      const fresh = newSince(accepted);
      if (!fresh.length) return "acknowledged";
      if (accepted.size)
        text = `${announce(`New since #${closed.number} was closed`, fresh)}\n\n${body}`;
    }
    await request(`/repos/${repository}/issues`, {
      method: "POST",
      body: { title, body: mark(text) },
    });
    return "opened";
  }
  if (unique.length) {
    const fresh = newSince(keysIn(open.body));
    await request(`/repos/${repository}/issues/${open.number}`, {
      method: "PATCH",
      body: { body: mark(body) },
    });
    // An edited body notifies nobody, so what is new is said in a comment.
    if (fresh.length)
      await request(`/repos/${repository}/issues/${open.number}/comments`, {
        method: "POST",
        body: { body: clip(announce("New since the last report", fresh)) },
      });
    return fresh.length ? "commented" : "updated";
  }
  if (open && !undecided) {
    await request(`/repos/${repository}/issues/${open.number}`, {
      method: "PATCH",
      body: { body: mark(body), state: "closed", state_reason: "completed" },
    });
    return "closed";
  }
  return "unchanged";
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
