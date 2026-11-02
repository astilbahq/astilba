import type { Schema } from "../../api/client";
import { Button } from "../../components/button";
import { Label } from "../../components/label";
import { Anchor } from "../../components/link";
import { Select } from "../../components/select";
import * as s from "../../styles";
import { Form, text, type useAction } from "../../ui";
import { AutomaticBuildRejections } from "../automatic-build-rejections";

export function PipelineJobs({
  applications,
  runs,
  rejections,
  rejectionsError,
  servers,
  write,
  dirty,
  action,
  onCancel,
  onPromote,
}: {
  applications: Schema["Specification"]["applications"];
  runs: Schema["PipelineRun"][] | undefined;
  rejections: Schema["BuildRejection"][] | undefined;
  rejectionsError: unknown;
  servers: Schema["Server"][] | undefined;
  write: boolean;
  dirty: boolean;
  action: ReturnType<typeof useAction>;
  onCancel: (operationId: string) => void;
  onPromote: (
    artifact: Schema["BuildArtifact"] | undefined,
    applicationId: string,
  ) => void;
}) {
  return (
    <section className={s.panel}>
      <h3>Source and build jobs</h3>
      <AutomaticBuildRejections
        rejections={rejections}
        error={rejectionsError}
        applications={applications}
      />
      {!runs?.length && <p className={s.muted}>No source or build jobs yet.</p>}
      {runs?.map((run) => {
        const report = run.result;
        return (
          <article key={run.id} className={s.stack}>
            <h4>
              {run.action} · {run.commit.slice(0, 12)} · {run.operation.state}
            </h4>
            <p>{run.operation.note}</p>
            {run.automaticDeployment && (
              <p>
                Automatic deployment: {run.automaticDeployment.state}.{" "}
                {run.automaticDeployment.note}
              </p>
            )}
            <p className={s.muted}>
              Runner:{" "}
              {servers?.find((v) => v.id === run.operation.serverId)?.name ??
                run.operation.serverId}
            </p>
            {!!run.operation.steps.length && (
              <ol>
                {run.operation.steps.map((step) => (
                  <li key={step.name}>
                    {step.name} · {step.state} {step.detail}
                  </li>
                ))}
              </ol>
            )}
            {report?.cache && <p>{report.cache}</p>}
            {report?.railpack && (
              <details>
                <summary>
                  Railpack {report.railpack.version} ·{" "}
                  {report.railpack.provider}
                </summary>
                <p className={s.muted}>
                  Frozen planner inputs and resolved tools for this build.
                </p>
                <pre
                  className={s.code}
                  style={{ whiteSpace: "pre-wrap", overflowWrap: "anywhere" }}
                >{`Frontend: ${report.railpack.frontend}\nPlan: ${report.railpack.planHash}\n${Object.entries(
                  report.railpack.packages,
                )
                  .sort(([a], [b]) => a.localeCompare(b))
                  .map(([name, version]) => `${name}: ${version}`)
                  .join("\n")}`}</pre>
              </details>
            )}
            {report?.reviewUrl && (
              <Anchor href={report.reviewUrl} target="_blank" rel="noreferrer">
                Open source review
              </Anchor>
            )}
            {report?.logs && (
              <details>
                <summary>Build output</summary>
                <pre
                  className={s.code}
                  style={{ whiteSpace: "pre-wrap", overflowWrap: "anywhere" }}
                >
                  {report.logs}
                </pre>
              </details>
            )}
            {write &&
              !["succeeded", "failed", "cancelled"].includes(
                run.operation.state,
              ) && (
                <Button
                  type="button"
                  disabled={action.isPending}
                  onClick={() => onCancel(run.id)}
                >
                  Cancel {run.action}
                </Button>
              )}
            {run.artifact && (
              <div>
                <p>
                  Artifact:{" "}
                  <code className={s.code}>{run.artifact.digest}</code> ·{" "}
                  {run.artifact.architecture}
                </p>
                {write && (
                  <Form
                    action={action}
                    label="Promote artifact"
                    onSubmit={(d) => onPromote(run.artifact, text(d, "app"))}
                  >
                    <Label>
                      Destination service
                      <Select name="app">
                        {applications
                          .filter((a) => a.kind !== "postgres")
                          .map((a) => (
                            <option key={a.id} value={a.id}>
                              {a.name}
                            </option>
                          ))}
                      </Select>
                    </Label>
                    <Button type="submit" disabled={dirty || action.isPending}>
                      Promote artifact to draft
                    </Button>
                  </Form>
                )}
              </div>
            )}
          </article>
        );
      })}
    </section>
  );
}
