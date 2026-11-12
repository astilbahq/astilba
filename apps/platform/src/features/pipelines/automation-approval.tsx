import { useQuery } from "@tanstack/react-query";
import { ApiError, client, result, type Schema } from "../../api/client";
import type { EnvironmentPath } from "../../api/environment";
import { Button } from "../../components/button";
import * as s from "../../styles";
import { ErrorNotice, type useAction } from "../../ui";

const root =
  "/api/v1/workspaces/{workspaceId}/projects/{projectId}/environments/{environmentId}" as const;

// Why automatic work is paused. Each pause waits for an approval given again by someone who holds
// the role it needs; nothing is moved to another person by itself.
const pauseReasons = {
  approvalRequired: "It needs to be approved again.",
  approverRemoved: "The person who approved it has left this workspace.",
  approverRole: "The person who approved it no longer has the role it needs.",
  approverCredentials:
    "The person who approved it changed their password, two-factor settings or recovery codes. That pauses what they approved, in case someone else made the change. They, or someone else with the role it needs, can approve it again.",
} satisfies Record<Exclude<Schema["PolicyApproval"], "current">, string>;

export function pauseReason(approval: Schema["PolicyApproval"]) {
  return approval === "current" ? "" : pauseReasons[approval];
}

type Action = ReturnType<typeof useAction>;

// approved runs after an approval succeeds, to say so and to move focus off the control that
// disappears with it.
type Approved = (message: string) => Promise<void>;

// An approval names the configuration and approver it was shown. If either changed, most often
// because someone else approved it first, the server refuses it: reload what is shown and say so,
// rather than report an error about saving.
async function approve(
  approved: Approved,
  refresh: () => Promise<void>,
  request: () => Promise<unknown>,
  message: string,
) {
  try {
    await request();
  } catch (e) {
    if (!(e instanceof ApiError) || e.status !== 409) throw e;
    await refresh();
    await approved(
      "This changed since it was shown, for example because someone else approved it. It now shows as it is.",
    );
    return;
  }
  await approved(message);
}

export function PausedSource({
  path,
  source,
  owner,
  action,
  approved,
  refresh,
}: {
  path: EnvironmentPath;
  source: Schema["GitSourceStatus"];
  owner: boolean;
  action: Action;
  approved: Approved;
  refresh: () => Promise<void>;
}) {
  if (!source.approval || source.approval === "current") return null;
  const deploys = source.configuration.autoDeploy;
  return (
    <div className={s.notice}>
      <p>
        Automatic imports from this branch
        {deploys ? " and their automatic deployments are" : " are"} paused.{" "}
        {pauseReason(source.approval)}
      </p>
      {owner ? (
        <Button
          type="button"
          disabled={action.isPending}
          onClick={() =>
            action.run(async () => {
              await approve(
                approved,
                refresh,
                () =>
                  result(
                    client.POST(`${root}/source/approval`, {
                      params: { path },
                      body: { sourceHash: source.sourceHash },
                    }),
                  ),
                "Source approved as yours. Automatic imports resume from the next push.",
              );
            })
          }
        >
          Approve as mine and resume
        </Button>
      ) : (
        <p className={s.muted}>An owner can approve it again.</p>
      )}
    </div>
  );
}

function approveTarget(
  path: EnvironmentPath,
  target: Schema["ConfiguredBuildTarget"],
) {
  return result(
    client.POST(`${root}/build-targets/{applicationId}/approval`, {
      params: { path: { ...path, applicationId: target.applicationId } },
      body: { targetHash: target.targetHash },
    }),
  );
}

// BuildTargetApprovals loads the environment's build targets and shows those waiting for
// approval. Its query is the one refresh invalidates as ["build-targets", path].
export function BuildTargetApprovals(
  props: Omit<Parameters<typeof PausedBuildTargets>[0], "targets">,
) {
  const targets = useQuery({
    queryKey: ["build-targets", props.path],
    queryFn: () =>
      result(
        client.GET(`${root}/build-targets`, { params: { path: props.path } }),
      ),
    refetchInterval: 15000,
  });
  return (
    <>
      <ErrorNotice error={targets.error} />
      {targets.data && <PausedBuildTargets {...props} targets={targets.data} />}
    </>
  );
}

export function PausedBuildTargets({
  path,
  targets,
  write,
  owner,
  action,
  approved,
  refresh,
}: {
  path: EnvironmentPath;
  targets: Schema["ConfiguredBuildTarget"][];
  write: boolean;
  owner: boolean;
  action: Action;
  approved: Approved;
  refresh: () => Promise<void>;
}) {
  const paused = targets.filter(
    (t) =>
      (t.automaticBuild || t.automaticDeploy) &&
      t.approval &&
      t.approval !== "current",
  );
  // Running a build on demand with an automation credential needs a current owner's approval of
  // its target, which only an owner can give, so only owners are told.
  const unrunnable = owner
    ? targets.filter((t) => t.runnable === false && !paused.includes(t))
    : [];
  if (!paused.length && !unrunnable.length) return null;
  return (
    <section className={s.stack} aria-labelledby="paused-automation">
      <h3 id="paused-automation">Automation waiting for approval</h3>
      {paused.map((target) => {
        const allowed = target.automaticDeploy ? owner : write;
        return (
          <div key={target.applicationId} className={s.notice}>
            <p>
              {target.name}: automatic builds
              {target.automaticDeploy
                ? " and deployments are"
                : " are"} paused.{" "}
              {pauseReason(target.approval ?? "approvalRequired")} Approving
              resumes them from the next push.
            </p>
            {allowed ? (
              <Button
                type="button"
                disabled={action.isPending}
                onClick={() =>
                  action.run(async () => {
                    await approve(
                      approved,
                      refresh,
                      () => approveTarget(path, target),
                      `${target.name} approved as yours. Automatic builds resume from the next push.`,
                    );
                  })
                }
              >
                Approve {target.name} as mine and resume
              </Button>
            ) : (
              <p className={s.muted}>
                {target.automaticDeploy
                  ? "An owner can approve it again."
                  : "An owner or operator can approve it again."}
              </p>
            )}
          </div>
        );
      })}
      {unrunnable.map((target) => (
        <div key={target.applicationId} className={s.notice}>
          <p>
            {target.name}: automation credentials can’t run this build until an
            owner approves its configuration.
          </p>
          <Button
            type="button"
            disabled={action.isPending}
            onClick={() =>
              action.run(async () => {
                await approve(
                  approved,
                  refresh,
                  () => approveTarget(path, target),
                  `${target.name} approved as yours. Automation credentials can run it.`,
                );
              })
            }
          >
            Approve {target.name} as mine
          </Button>
        </div>
      ))}
    </section>
  );
}
