import type { Schema } from "../api/client";
import * as s from "../styles";
import { ErrorNotice } from "../ui";

const buildRejectionMessages = {
  missing_build_secret: "A declared build-secret version is unavailable.",
  build_secret_budget: "Declared build secrets exceed the build-input limit.",
  arguments_contain_secret:
    "Public build arguments contain a known secret value.",
  runtime_secret_budget:
    "Runtime secrets exceed the build-input validation limit. An owner can retire secrets the environment no longer needs, or replace versions that the draft or the deployment still uses.",
  runner_unavailable:
    "Its runner was removed or revoked, lost its credential, or can no longer build it, or the build waited a day for it to be updated or to finish enrolling.",
  registry_unavailable: "Its registry was removed.",
} satisfies Record<Schema["BuildRejectionReason"], string>;
// A build waits for its runner until a day after its refresh began (refreshWaitLimit in
// git_refresh.go), which until says.
const buildWaitMessages = {
  runner_update: (runner: string, until: string) =>
    `Waiting for runner ${runner} to update to the release management runs. If it isn't updated by ${until}, this commit isn't built automatically.`,
  runner_enrollment: (runner: string, until: string) =>
    `Waiting for runner ${runner} to finish enrolling. If it doesn't by ${until}, this commit isn't built automatically.`,
} satisfies Record<
  Schema["BuildWaitReason"],
  (runner: string, until: string) => string
>;
export function AutomaticBuildRejections({
  rejections,
  waits,
  servers,
  error,
  applications,
}: {
  rejections?: Schema["BuildRejection"][];
  waits?: Schema["BuildWait"][];
  servers?: Schema["Server"][];
  error: unknown;
  applications: Schema["Specification"]["applications"];
}) {
  const service = (id: string) =>
    applications.find((app) => app.id === id)?.name ?? "Service";
  return (
    <>
      <ErrorNotice error={error} />
      <div role="status" aria-label="Automatic builds waiting">
        {!!waits?.length && (
          <div className={s.stack}>
            <h4>Automatic builds waiting for their runner</h4>
            {waits.map((wait) => (
              <p key={wait.applicationId}>
                {service(wait.applicationId)} · {wait.commit.slice(0, 12)} ·{" "}
                {buildWaitMessages[wait.reason](
                  servers?.find((server) => server.id === wait.runnerId)
                    ?.name ?? wait.runnerId.slice(0, 8),
                  new Date(wait.until).toLocaleString(),
                )}
              </p>
            ))}
          </div>
        )}
      </div>
      <div role="status" aria-label="Automatic build rejections">
        {!!rejections?.length && (
          <div className={s.stack}>
            <h4>Automatic builds need attention</h4>
            {rejections.map((rejection) => (
              <p key={rejection.applicationId}>
                {service(rejection.applicationId)} ·{" "}
                {rejection.commit.slice(0, 12)} ·{" "}
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
