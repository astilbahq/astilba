// The console's shared rendering (C5-c) — two tables and a caveat.
//
// Deliberately plain: no styling framework, no charts, no colour that implies a grade. A red cell
// would be an assertion, and this page does not make assertions. Every number on it is a
// measurement sitting next to the permission it was measured against, and the reader is the one who
// decides whether the gap matters.

import { durableNote } from "./durable-note";
import type { Narration } from "./narrate";
import type { ProbeResult } from "./probe";

const CELL: React.CSSProperties = {
  borderBottom: "1px solid #ddd",
  padding: "0.4rem 0.6rem",
  textAlign: "left",
  verticalAlign: "top",
};

/** What the read actually returned — the entry form reports the evidence a plain value hides. */
export function ProbePanel({ probe }: { probe: ProbeResult }) {
  if (!probe.ok) {
    return (
      <section>
        <h2>The read</h2>
        <p>
          <strong>It threw.</strong> <code>{probe.error}</code>
        </p>
        <p>
          That is a legitimate outcome, not a demo bug: some faults surface to
          the caller rather than being absorbed. What matters is that it threw
          instead of answering with something it could not stand behind.
        </p>
      </section>
    );
  }
  const { entry } = probe;
  return (
    <section>
      <h2>The read</h2>
      <p>
        <code>{entry.value ?? "(no value)"}</code>
      </p>
      <table style={{ borderCollapse: "collapse", width: "100%" }}>
        <tbody>
          <tr>
            <th style={CELL}>tier</th>
            <td style={CELL}>{entry.tier}</td>
          </tr>
          <tr>
            <th style={CELL}>graced (served stale)</th>
            <td style={CELL}>{entry.stale ? "yes" : "no"}</td>
          </tr>
          <tr>
            <th style={CELL}>served on error</th>
            <td style={CELL}>{entry.servedOnError === true ? "yes" : "no"}</td>
          </tr>
          <tr>
            <th style={CELL}>write-back durable</th>
            {/* THREE-state (E29), not a truthiness collapse: `durable` is `true` / `false` /
                ABSENT, and absent ("cannot tell from this serve") is a different answer from
                `false` ("known not durable"). `durableNote` discriminates explicitly. */}
            <td style={CELL}>{durableNote(entry)}</td>
          </tr>
          <tr>
            <th style={CELL}>age</th>
            <td style={CELL}>{entry.age}ms</td>
          </tr>
        </tbody>
      </table>
    </section>
  );
}

/** Measured beside documented, and a caveat that is part of the content. */
export function NarrationTable({ narration }: { narration: Narration }) {
  return (
    <section>
      <h2>What it did, and what it was allowed to do</h2>
      <table style={{ borderCollapse: "collapse", width: "100%" }}>
        <thead>
          <tr>
            <th style={CELL}>&nbsp;</th>
            <th style={CELL}>measured</th>
            <th style={CELL}>documented — a permission, not a promise</th>
          </tr>
        </thead>
        <tbody>
          {narration.rows.map((row) => (
            <tr key={row.label}>
              <th style={CELL}>{row.label}</th>
              <td style={CELL}>
                <code>{row.measured}</code>
              </td>
              <td style={{ ...CELL, color: "#555" }}>{row.documented}</td>
            </tr>
          ))}
        </tbody>
      </table>
      {narration.judgments.length > 0 && (
        <>
          <h3>Judgments the demo derived</h3>
          <ul>
            {narration.judgments.map((judgment) => (
              <li key={judgment.event}>
                <code>{judgment.event}</code> — {judgment.because}
              </li>
            ))}
          </ul>
        </>
      )}
      <p
        style={{
          borderLeft: "3px solid #999",
          color: "#555",
          paddingLeft: "0.8rem",
        }}
      >
        {narration.caveat}
      </p>
      <p
        style={{
          borderLeft: "3px solid #b00",
          color: "#555",
          paddingLeft: "0.8rem",
        }}
      >
        {narration.lifetimeCaveat}
      </p>
    </section>
  );
}
