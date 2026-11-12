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
  createHash("sha256").update(key).digest("hex").slice(0, 16);

// Takes each problem as a stable key and a line of markdown that names it. Keys are
// kept in the issue body, so a later run comments only on problems that are new, and
// the same problem on the next day changes the body without notifying anyone. A
// report that could not tell passes `undecided` to leave an open issue alone. Only
// this workflow's own issues count, as anyone can open one with the same title.
export async function syncIssue(
  request,
  repository,
  { title, problems, body, undecided = false },
) {
  const issues = await (
    await request(
      `/repos/${repository}/issues?state=open&creator=github-actions%5Bbot%5D&per_page=100`,
    )
  ).json();
  const open = issues
    .filter((issue) => issue.title === title && !issue.pull_request)
    .toSorted((a, b) => a.number - b.number)[0];
  const keys = [...new Set(problems.map(({ key }) => digest(key)))];
  const marked = clip(`<!-- report keys: ${keys.join(" ")} -->\n${body}`);
  if (problems.length && !open) {
    await request(`/repos/${repository}/issues`, {
      method: "POST",
      body: { title, body: marked },
    });
    return "opened";
  }
  if (problems.length) {
    const known = new Set(marker.exec(open.body ?? "")?.[1].split(" ") ?? []);
    const fresh = problems.filter(
      ({ key }, index) =>
        !known.has(digest(key)) &&
        problems.findIndex((other) => other.key === key) === index,
    );
    await request(`/repos/${repository}/issues/${open.number}`, {
      method: "PATCH",
      body: { body: marked },
    });
    // An edited body notifies nobody, so what is new is said in a comment.
    if (fresh.length)
      await request(`/repos/${repository}/issues/${open.number}/comments`, {
        method: "POST",
        body: {
          body: clip(
            [
              `New since the last report (${fresh.length}):`,
              "",
              ...fresh.map(({ line }) => line),
            ].join("\n"),
          ),
        },
      });
    return fresh.length ? "commented" : "updated";
  }
  if (open && !undecided) {
    await request(`/repos/${repository}/issues/${open.number}`, {
      method: "PATCH",
      body: { body: marked, state: "closed", state_reason: "completed" },
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
