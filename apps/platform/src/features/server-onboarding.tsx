import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { client, result, type Schema } from "../api/client";
import { Checkbox } from "../components/checkbox";
import { Input } from "../components/input";
import { Label } from "../components/label";
import { Anchor } from "../components/link";
import { Select } from "../components/select";
import * as s from "../styles";
import { BusyButton, ErrorNotice, Field, Form, text, useAction } from "../ui";

const quote = (value: string) => `'${value.replaceAll("'", "'\\''")}'`;
const guide = "https://github.com/astilbahq/astilba/blob/main/docs/agent.md";

export function ServerOnboarding({
  workspaceId,
  releases,
  onEnrollment,
}: {
  workspaceId: string;
  releases: Schema["AgentRelease"][];
  onEnrollment: (value: Schema["Enrollment"]) => void;
}) {
  const action = useAction();
  const [role, setRole] = useState<Schema["ServerRole"]>("deployment");
  const [arch, setArch] = useState("amd64");
  const [releaseSHA, setRelease] = useState("");
  const [fingerprint, setFingerprint] = useState("");
  const [contact, setContact] = useState("");
  const [runtime, setRuntime] = useState(false);
  const [backups, setBackups] = useState(false);
  const [manual, setManual] = useState(false);
  const [prepared, setPrepared] = useState(false);
  const onboarding = useQuery({
    queryKey: ["onboarding", workspaceId],
    queryFn: () =>
      result(
        client.GET("/api/v1/workspaces/{workspaceId}/onboarding", {
          params: { path: { workspaceId } },
        }),
      ),
  });
  const available = releases.filter((r) => r.arch === arch);
  const release =
    available.find((r) => r.sha256 === releaseSHA) ?? available[0];
  const ready =
    !!release &&
    /^[a-f0-9]{64}$/.test(fingerprint) &&
    (role === "build" || /^[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+$/.test(contact));
  const command = ready
    ? `sudo bash astilba-bootstrap.sh --apply --management ${quote(location.origin)} --release ${release.sha256} --key-fingerprint ${fingerprint} --role ${role}${role !== "build" && contact ? ` --certificate-contact ${quote(contact)}` : ""}${runtime ? " --install-runtime" : ""}${backups ? " --install-backups" : ""}`
    : "Choose a release, provide its trusted key fingerprint and complete the certificate contact before preparing the installation command.";
  return (
    <section className={s.panel} aria-label="Server onboarding">
      <h2>Connect a server</h2>
      <ErrorNotice error={action.error || onboarding.error} />
      <p>
        Prepare the host first. Create its ten-minute enrollment code when you
        are ready to connect.
      </p>
      <Label>
        Server role
        <Select
          value={role}
          onChange={(e) => {
            setRole(e.target.value as Schema["ServerRole"]);
            setPrepared(false);
          }}
        >
          <option value="deployment">Deployment server</option>
          <option value="build">Build runner</option>
          <option value="combined">Deployment and build</option>
        </Select>
      </Label>
      <Label>
        <Checkbox
          checked={manual}
          onChange={(e) => {
            setManual(e.target.checked);
            setPrepared(false);
          }}
        />
        I already prepared the verified agent using the manual guide
      </Label>
      <Anchor href={guide} target="_blank" rel="noreferrer">
        Agent installation and update guide
      </Anchor>
      {!manual && (
        <div className={s.stack}>
          <h3>Prepare Ubuntu 24.04</h3>
          <p>
            Download and inspect the bootstrap script on the server. Check mode
            changes no packages, services or configuration.
          </p>
          <code
            className={`${s.code} ${s.wrappedCode}`}
          >{`curl --fail --proto '=https' --output astilba-bootstrap.sh ${quote(`${location.origin}/onboarding/bootstrap.sh`)}\nless astilba-bootstrap.sh\nsudo bash astilba-bootstrap.sh --check --role ${role}`}</code>
          <Label>
            Host architecture
            <Select
              value={arch}
              onChange={(e) => {
                setArch(e.target.value);
                setPrepared(false);
              }}
            >
              <option value="amd64">amd64 (Intel / AMD)</option>
              <option value="arm64">arm64</option>
            </Select>
          </Label>
          <Label>
            Signed agent release
            <Select
              value={release?.sha256 ?? ""}
              onChange={(e) => {
                setRelease(e.target.value);
                setPrepared(false);
              }}
              disabled={!available.length}
            >
              {!available.length && (
                <option value="">No signed release available</option>
              )}
              {available.map((r) => (
                <option key={r.sha256} value={r.sha256}>
                  {r.version} · {r.arch}
                </option>
              ))}
            </Select>
          </Label>
          {!release && (
            <p className={s.notice}>
              Ask the installation operator to publish a signed release, or use
              the manual installation path.
            </p>
          )}
          <p>
            Obtain the SHA-256 signing-key fingerprint through an independent
            trusted channel from your operator. This dashboard reports{" "}
            <code className={s.code}>
              {onboarding.data?.keyFingerprint || "no configured key"}
            </code>
            ; a value from this same dashboard alone does not establish trust.
          </p>
          <Label>
            Independently trusted signing-key fingerprint
            <Input
              autoComplete="off"
              value={fingerprint}
              onChange={(e) => {
                setFingerprint(e.target.value.trim());
                setPrepared(false);
              }}
              spellCheck={false}
            />
          </Label>
          {fingerprint &&
            onboarding.data?.keyFingerprint &&
            fingerprint !== onboarding.data.keyFingerprint && (
              <p role="alert">
                The fingerprint differs from the configured key. Check with your
                operator before continuing.
              </p>
            )}
          {role !== "build" && (
            <Label>
              Certificate contact email
              <Input
                type="email"
                value={contact}
                onChange={(e) => {
                  setContact(e.target.value);
                  setPrepared(false);
                }}
                autoComplete="email"
              />
              <small>
                Prepared in the agent service. Certificates and public ports
                still require a reviewed host-permission plan.
              </small>
            </Label>
          )}
          <Label>
            <Checkbox
              checked={runtime}
              onChange={(e) => {
                setRuntime(e.target.checked);
                setPrepared(false);
              }}
            />
            Allow installing missing Docker and Compose. This can start Docker
            and create its networks and firewall rules.
          </Label>
          <Label>
            <Checkbox
              checked={backups}
              onChange={(e) => {
                setBackups(e.target.checked);
                setPrepared(false);
              }}
            />
            Install verified backup tooling for backups and supported database
            upgrades
          </Label>
          <p>
            Existing runtimes, containers, ingress and agent data are never
            replaced. An incompatible or partial installation requires manual
            inspection. SSH settings remain unchanged.
          </p>
          <code className={`${s.code} ${s.wrappedCode}`}>{command}</code>
        </div>
      )}
      <Form
        action={action}
        label="Create server enrollment"
        onSubmit={(d) =>
          action.run(async () => {
            const value = await result(
              client.POST("/api/v1/workspaces/{workspaceId}/servers", {
                params: { path: { workspaceId } },
                body: { name: text(d, "name"), role },
              }),
            );
            onEnrollment(value);
          })
        }
      >
        <Field name="name" label="Server name" />
        <Label>
          <Checkbox
            checked={prepared}
            onChange={(e) => setPrepared(e.target.checked)}
            disabled={!manual && !ready}
          />
          I have inspected the preflight report and prepared this host for its
          selected role
        </Label>
        <BusyButton busy={action.isPending} disabled={!prepared}>
          Create enrollment
        </BusyButton>
      </Form>
    </section>
  );
}
