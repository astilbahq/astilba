import type { Schema } from "../api/client";
import * as s from "../styles";
import { ErrorNotice } from "../ui";

const buildRejectionMessages = {
  missing_build_secret: "A declared build-secret version is unavailable.",
  build_secret_budget: "Declared build secrets exceed the build-input limit.",
  arguments_contain_secret:
    "Public build arguments contain a known secret value.",
  runtime_secret_budget:
    "Runtime secrets exceed the build-input validation limit.",
  runner_unavailable:
    "Its runner, or its fallback, was removed or revoked, lost its credential, or can no longer build it.",
  registry_unavailable: "Its registry was removed.",
} satisfies Record<Schema["BuildRejectionReason"], string>;
export function AutomaticBuildRejections({
  rejections,
  error,
  applications,
}: {
  rejections?: Schema["BuildRejection"][];
  error: unknown;
  applications: Schema["Specification"]["applications"];
}) {
  return (
    <>
      <ErrorNotice error={error} />
      <div role="status" aria-label="Automatic build rejections">
        {!!rejections?.length && (
          <div className={s.stack}>
            <h4>Automatic builds need attention</h4>
            {rejections.map((rejection) => (
              <p key={rejection.applicationId}>
                {applications.find((app) => app.id === rejection.applicationId)
                  ?.name ?? "Service"}{" "}
                · {rejection.commit.slice(0, 12)} ·{" "}
                {buildRejectionMessages[rejection.reason]} Review the build
                configuration before retrying.
              </p>
            ))}
          </div>
        )}
      </div>
    </>
  );
}
