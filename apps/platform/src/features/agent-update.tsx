import { type ComponentProps, useId, useState } from "react";
import type { Schema } from "../api/client";
import { Label } from "../components/label";
import { Select } from "../components/select";
import * as s from "../styles";
import { BusyButton, Form, text } from "../ui";

/**
 * Offers the catalog releases management sends this server's agent. An agent refuses a release
 * whose minimum version is below that of the release it runs, so management sends only releases
 * at or above that floor. The others are listed beside them, disabled, with the reason; when none
 * qualifies, the note says so. Releases are listed in management's order, newest first.
 */
export function AgentUpdate({
  server,
  releases,
  action,
  busy,
  onUpdate,
}: {
  server: Schema["Server"];
  releases: Schema["AgentRelease"][];
  action: ComponentProps<typeof Form>["action"];
  busy: boolean;
  onUpdate: (version: string) => void;
}) {
  const note = useId();
  const [choice, setChoice] = useState<string>();
  const capabilities = server.capabilities;
  const updates = server.agentUpdates;
  if (!capabilities || !updates) return null;
  const others = releases.filter(
    (r) => r.arch === capabilities.arch && r.version !== capabilities.version,
  );
  if (!others.length) return null;
  const sent = new Set(updates.versions);
  const offered = others.filter((r) => sent.has(r.version));
  const withheld = offered.length < others.length;
  const floor = updates.minimumVersion;
  const none = offered.length
    ? ""
    : " No other release in the catalog has a minimum version that high, so management offers none.";
  const reason =
    updates.basis === "unknown"
      ? "This agent reports no release version, so management can't tell which releases it installs and offers none. Update it with the local procedure."
      : updates.basis === "catalog"
        ? `This agent installs only releases whose minimum version is at least ${floor}, that of the release it runs.${none}`
        : `Release ${capabilities.version}, which this agent runs, isn't in the release catalog, so management can't read its minimum version. Management offers only releases whose minimum version is at least ${floor}, which the agent installs whatever its own minimum version.${none} Add this release's signed manifest to the catalog so management can read its minimum version, or update the agent with the local procedure.`;
  // Management lists the catalog newest first, so the first release it sends is the newest. The
  // select starts there when that is newer than the release the agent runs, so accepting what it
  // shows never downgrades the agent. When management sends only older releases, for example once
  // the agent runs the newest, the select starts on no release, so a rollback is always one the
  // owner picked. An agent whose release isn't in the catalog (basis version) is sent only other
  // releases whose minimum version, and so version, is at least its own, so every one is newer.
  // With basis catalog the order tells, as long as this page's release list holds the agent's
  // release. The list loads less often than the server, so after management restarts with a new
  // catalog it may not: then the select starts on no release rather than guess.
  const running = releases.findIndex(
    (r) => r.arch === capabilities.arch && r.version === capabilities.version,
  );
  const newest = offered[0];
  const newer =
    updates.basis === "version" ||
    (running >= 0 && !!newest && releases.indexOf(newest) < running);
  const start = newest && newer ? newest.version : "";
  // The select keeps showing a release while management sends it, whether the owner chose it or
  // the select started there. Once management stops sending it, for example after an update
  // installed it or raised the floor, the select starts again as above and keeps that release the
  // same way, so a release it moved away from never comes back by itself.
  const value = choice !== undefined && sent.has(choice) ? choice : start;
  if (value !== choice) setChoice(value);
  return (
    <>
      {withheld && (
        <p id={note} className={s.muted}>
          {reason}
        </p>
      )}
      {offered.length > 0 && (
        <Form
          action={action}
          onSubmit={(data) => onUpdate(text(data, "version"))}
        >
          <Label>
            Agent release
            <Select
              name="version"
              value={value}
              required
              onChange={(e) => setChoice(e.target.value)}
              aria-describedby={withheld ? note : undefined}
            >
              {start === "" && (
                <option value="" disabled>
                  {running >= 0
                    ? `Choose a release older than ${capabilities.version}, which this agent runs`
                    : "Choose a release"}
                </option>
              )}
              {others.map((r) =>
                sent.has(r.version) ? (
                  <option key={r.sha256} value={r.version}>
                    {r.version}
                  </option>
                ) : (
                  <option key={r.sha256} value={r.version} disabled>
                    {`${r.version} (minimum version ${r.minimumVersion}, below ${floor})`}
                  </option>
                ),
              )}
            </Select>
          </Label>
          <BusyButton busy={busy}>Update agent</BusyButton>
        </Form>
      )}
    </>
  );
}
