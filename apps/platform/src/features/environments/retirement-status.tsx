import { useQuery } from "@tanstack/react-query";
import { type RefObject, useId } from "react";
import { Trans, useTranslation } from "react-i18next";
import { client, result, type Schema } from "../../api/client";
import type { EnvironmentPath } from "../../api/environment";
import { environmentsOptions } from "../../api/environments";
import { membersOptions } from "../../api/members";
import { sessionOptions } from "../../api/session";
import { Button } from "../../components/button";
import type { LifecycleChange } from "./environment-lifecycle";
import * as e from "./environment.styles";

const root =
  "/api/v1/workspaces/{workspaceId}/projects/{projectId}/environments/{environmentId}";

const finished = (state: Schema["ExecutionState"]) =>
  state === "succeeded" || state === "failed" || state === "cancelled";

// Shown in place of the editing controls while an environment is retiring or retired.
export function RetirementStatus({
  environment,
  path,
  owner,
  headingRef,
  onChange,
}: {
  environment: Schema["Environment"];
  path: EnvironmentPath;
  owner: boolean;
  headingRef: RefObject<HTMLHeadingElement | null>;
  onChange: (change: LifecycleChange, trigger: HTMLButtonElement) => void;
}) {
  const { t } = useTranslation("environment");
  const heading = useId();
  const retiring = environment.state === "retiring";
  // The environment becomes retired when its server confirms the removal.
  useQuery({
    ...environmentsOptions(
      path.workspaceId,
      path.projectId,
      path.environmentId,
    ),
    refetchInterval: retiring ? 5000 : false,
  });
  const operationId = retiring ? environment.retirementOperationId : null;
  const operation = useQuery({
    queryKey: ["operation", path, operationId],
    enabled: !!operationId,
    queryFn: ({ signal }) =>
      result(
        client.GET(`${root}/operations/{operationId}`, {
          params: { path: { ...path, operationId: operationId ?? "" } },
          signal,
        }),
      ),
    refetchInterval: ({ state: { data } }) =>
      data && finished(data.state) ? false : 5000,
  });
  const volumes = useQuery({
    queryKey: ["volumes", path],
    queryFn: ({ signal }) =>
      result(client.GET(`${root}/volumes`, { params: { path }, signal })),
  });
  const kept = volumes.data?.filter((volume) => !volume.deleted);
  const retryable =
    !!operation.data &&
    (operation.data.state === "failed" || operation.data.state === "cancelled");
  return (
    <section aria-labelledby={heading} className={e.retirement}>
      <div className={e.intro}>
        <h2
          id={heading}
          ref={headingRef}
          tabIndex={-1}
          className={e.retirementHeading}
        >
          {retiring
            ? t(($) => $.lifecycle.status.retiringTitle)
            : t(($) => $.lifecycle.status.retiredTitle)}
        </h2>
        <p>
          {retiring
            ? t(($) => $.lifecycle.status.retiringDescription)
            : t(($) => $.lifecycle.status.retiredDescription)}
        </p>
        <RetiredBy
          environment={environment}
          workspaceId={path.workspaceId}
          owner={owner}
        />
        {environment.retiredWithoutHost && (
          <p>{t(($) => $.lifecycle.status.withoutHost)}</p>
        )}
      </div>
      {operationId && (
        <div className={e.intro} role="status">
          {operation.data ? (
            <>
              <p>
                {t(($) => $.lifecycle.status.operation, {
                  state: t(
                    ($) => $.lifecycle.operationState[operation.data.state],
                  ),
                })}
              </p>
              {operation.data.note && (
                <p className={e.muted}>{operation.data.note}</p>
              )}
              {owner && retryable && (
                <p>{t(($) => $.lifecycle.status.retryHint)}</p>
              )}
            </>
          ) : (
            <p className={e.muted}>
              {operation.isError
                ? t(($) => $.lifecycle.status.operationFailed)
                : t(($) => $.lifecycle.status.operationLoading)}
            </p>
          )}
        </div>
      )}
      <div className={e.intro}>
        <h3 className={e.retirementSubheading}>
          {t(($) => $.lifecycle.status.volumesTitle)}
        </h3>
        {kept ? (
          kept.length ? (
            <ul className={e.retainedVolumes}>
              {kept.map((volume) => (
                <li key={volume.id} className={e.code}>
                  <bdi>{volume.name}</bdi>
                </li>
              ))}
            </ul>
          ) : (
            <p className={e.muted}>
              {t(($) => $.lifecycle.status.volumesEmpty)}
            </p>
          )
        ) : (
          <p className={e.muted}>
            {volumes.isError
              ? t(($) => $.lifecycle.status.volumesFailed)
              : t(($) => $.lifecycle.status.volumesLoading)}
          </p>
        )}
      </div>
      {owner && !retiring && (
        <div>
          <Button
            variant="primary"
            onClick={(event) => onChange("restore", event.currentTarget)}
          >
            {t(($) => $.lifecycle.restore)}
          </Button>
        </div>
      )}
      {owner && retiring && retryable && (
        <div>
          <Button
            variant="danger"
            onClick={(event) => onChange("retire", event.currentTarget)}
          >
            {t(($) => $.lifecycle.retryRetirement)}
          </Button>
        </div>
      )}
      {!owner && !retiring && (
        <p className={e.muted}>{t(($) => $.lifecycle.status.ownersOnly)}</p>
      )}
    </section>
  );
}

// Members are listed only to owners, so others learn who retired it only when it was them.
function RetiredBy({
  environment,
  workspaceId,
  owner,
}: {
  environment: Schema["Environment"];
  workspaceId: string;
  owner: boolean;
}) {
  const { t, i18n } = useTranslation("environment");
  const self = useQuery(sessionOptions).data?.user?.id;
  const by = environment.retiredBy;
  const member = useQuery({
    ...membersOptions(workspaceId),
    enabled: owner && !!by && by !== self,
  }).data?.find(({ id }) => id === by);
  if (!environment.retiredAt) return null;
  const date = new Date(environment.retiredAt).toLocaleString(
    i18n.resolvedLanguage,
  );
  return (
    <p className={e.muted}>
      {by && by === self ? (
        t(($) => $.lifecycle.status.requestedByYou, { date })
      ) : member ? (
        <Trans
          t={t}
          i18nKey={($) => $.lifecycle.status.requestedBy}
          values={{ date }}
          components={{ person: <bdi>{member.name}</bdi> }}
        />
      ) : (
        t(($) => $.lifecycle.status.requestedByOwner, { date })
      )}
    </p>
  );
}
