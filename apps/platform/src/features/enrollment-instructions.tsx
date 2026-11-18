import type { Schema } from "../api/client";
import { Button } from "../components/button";
import { Input } from "../components/input";
import { Label } from "../components/label";
import { Anchor } from "../components/link";
import * as s from "../styles";

export function EnrollmentInstructions({
  enrollment,
  releases,
  onHide,
}: {
  enrollment: Schema["Enrollment"];
  releases: Schema["AgentRelease"][];
  onHide: () => void;
}) {
  return (
    <section className={s.panel} aria-label="Enrollment instructions">
      <h2>Connect {enrollment.server.name}</h2>
      <p>
        One-use code, expires{" "}
        {new Date(enrollment.expiresAt).toLocaleTimeString()}. Paste it into the
        agent’s stdin prompt; keep it out of shell history.
      </p>
      <Label>
        Enrollment code
        <Input readOnly value={enrollment.token} autoComplete="off" />
      </Label>
      <code className={s.code}>
        sudo astilba-agent enroll --management {location.origin} --server{" "}
        {enrollment.server.id} --release-key /etc/astilba/release-public.pem
      </code>
      <code className={s.code}>sudo systemctl enable --now astilba-agent</code>
      <p>
        Enrolling does not start the service. Enable it explicitly, then inspect
        the connection and role capabilities below.
      </p>
      <p>
        Use the signed download and systemd installation steps in the
        repository’s agent guide before running this command.
      </p>
      {releases.length ? (
        <ul>
          {releases.map((release) => (
            <li key={release.sha256}>
              {release.version} · Linux {release.arch}:{" "}
              <Anchor
                href={`/agent-artifacts/${release.sha256}`}
                download="astilba-agent"
              >
                Download agent
              </Anchor>
              {" · "}
              <Anchor
                href={`/agent-artifacts/${release.sha256}/manifest.json`}
                download="release.json"
              >
                Signed manifest
              </Anchor>
            </li>
          ))}
        </ul>
      ) : (
        <p>
          No signed downloads are configured. The installation operator must
          provide the agent artifact and its signed manifest.
        </p>
      )}
      <Anchor
        href="https://github.com/astilbahq/astilba/blob/main/docs/agent.md"
        target="_blank"
        rel="noreferrer"
      >
        Agent installation guide
      </Anchor>
      <Button type="button" onClick={onHide}>
        Hide enrollment code
      </Button>
    </section>
  );
}
