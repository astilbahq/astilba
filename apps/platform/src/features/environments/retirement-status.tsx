import { useQuery, useQueryClient } from "@tanstack/react-query";
import { type RefObject, useId, useRef } from "react";
import { Trans, useTranslation } from "react-i18next";
import { cx } from "../../../styled-system/css";
import { client, result, type Schema } from "../../api/client";
import { type EnvironmentPath, volumesOptions } from "../../api/environment";
import { environmentsOptions } from "../../api/environments";
import { membersOptions } from "../../api/members";
import { sessionOptions } from "../../api/session";
import { Button } from "../../components/button";
import * as s from "../../styles";
import type { LifecycleChange } from "./environment-lifecycle";
import * as e from "./environment.styles";

const root =
  "/api/v1/workspaces/{workspaceId}/projects/{projectId}/environments/{environmentId}";

const finished = (state: Schema["ExecutionState"]) =>
  state === "succeeded" || state === "failed" || state === "cancelled";

// Shown in place of the editing controls while an environment is not active: retiring, retired,
// having its retained data deleted (purging) or purged.
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
  const queries = useQueryClient();
  const heading = useId();
  const volumesHeading = useId();
  const volumesRegion = useRef<HTMLElement>(null);
  const state = environment.state;
  const retiring = state === "retiring";
  const purging = state === "purging";
  const purged = state === "purged";
  // The environment moves on when its server confirms the removal or the deletion.
  const environments = environmentsOptions(
    path.workspaceId,
    path.projectId,
    path.environmentId,
  );
  useQuery({
    ...environments,
    refetchInterval: retiring || purging ? 5000 : false,
  });
  const operationId = retiring
    ? environment.retirementOperationId
    : purging
      ? environment.purgeOperationId
      : null;
  const operation = useQuery({
    queryKey: ["operation", path, operationId],
    enabled: !!operationId,
    queryFn: async ({ signal }) => {
      const next = await result(
        client.GET(`${root}/operations/{operationId}`, {
          params: { path: { ...path, operationId: operationId ?? "" } },
          signal,
        }),
      );
      // The same report moved the environment on, so the page catches up now rather than at its
      // next poll.
      if (next.state === "succeeded")
        void queries.invalidateQueries({ queryKey: environments.queryKey });
      return next;
    },
    refetchInterval: ({ state: { data } }) =>
      data && finished(data.state) ? false : 5000,
  });
  const succeeded = operation.data?.state === "succeeded";
  const progress = operation.data && (
    <p>
      {purging
        ? t(($) => $.lifecycle.status.purgeOperation, {
            state: t(($) => $.lifecycle.operationState[operation.data.state]),
          })
        : t(($) => $.lifecycle.status.operation, {
            state: t(($) => $.lifecycle.operationState[operation.data.state]),
          })}
    </p>
  );
  const volumes = useQuery(volumesOptions(path));
  const kept = volumes.data?.filter((volume) => !volume.deleted);
  const retryable =
    !!operation.data &&
    (operation.data.state === "failed" || operation.data.state === "cancelled");
  // A purged environment's volumes are gone, unless its server was revoked and so never asked to
  // delete them: those are listed as what may remain there.
  const listVolumes = !purged || !!environment.purgedWithoutHost;
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
            : purging
              ? t(($) => $.lifecycle.status.purgingTitle)
              : purged
                ? environment.purgedWithoutHost
                  ? t(($) => $.lifecycle.status.purgedWithoutHostTitle)
                  : t(($) => $.lifecycle.status.purgedTitle)
                : t(($) => $.lifecycle.status.retiredTitle)}
        </h2>
        <p>
          {/* A purge has an operation only when it asked a server to delete volumes, and becomes
              purged only once that server confirms it. The volume list cannot say this: it also
              marks volumes deleted before the environment was retired. */}
          {retiring
            ? t(($) => $.lifecycle.status.retiringDescription)
            : purging
              ? t(($) => $.lifecycle.status.purgingDescription)
              : purged
                ? environment.purgedWithoutHost
                  ? t(($) => $.lifecycle.status.purgedWithoutHostDescription)
                  : environment.purgeOperationId
                    ? t(($) => $.lifecycle.status.purgedDescription)
                    : t(($) => $.lifecycle.status.purgedNoVolumesDescription)
                : t(($) => $.lifecycle.status.retiredDescription)}
        </p>
        <RequestedBy
          change="retire"
          at={environment.retiredAt}
          by={environment.retiredBy}
          workspaceId={path.workspaceId}
          owner={owner}
        />
        <RequestedBy
          change="purge"
          at={environment.purgedAt}
          by={environment.purgedBy}
          workspaceId={path.workspaceId}
          owner={owner}
        />
        {environment.retiredWithoutHost && (
          <p>{t(($) => $.lifecycle.status.withoutHost)}</p>
        )}
        {environment.purgedWithoutHost && (
          <p>{t(($) => $.lifecycle.status.purgedWithoutHost)}</p>
        )}
        {(purging || purged) && (
          <p>{t(($) => $.lifecycle.status.backupsKept)}</p>
        )}
      </div>
      {operationId && (
        <div className={e.intro}>
          {/* Progress is announced here, but not success: the environment moves on with it,
              which the page announces once. */}
          <div className={e.intro} role="status">
            {operation.data ? (
              !succeeded && (
                <>
                  {progress}
                  {operation.data.note && (
                    <p className={e.muted}>{operation.data.note}</p>
                  )}
                  {owner && retryable && (
                    <p>
                      {purging
                        ? t(($) => $.lifecycle.status.purgeRetryHint)
                        : t(($) => $.lifecycle.status.retryHint)}
                    </p>
                  )}
                </>
              )
            ) : (
              <p className={e.muted}>
                {operation.isError
                  ? purging
                    ? t(($) => $.lifecycle.status.purgeOperationFailed)
                    : t(($) => $.lifecycle.status.operationFailed)
                  : purging
                    ? t(($) => $.lifecycle.status.purgeOperationLoading)
                    : t(($) => $.lifecycle.status.operationLoading)}
              </p>
            )}
          </div>
          {succeeded && progress}
        </div>
      )}
      {listVolumes && (
        // A retry focuses this region, so what loads is read from it.
        <section
          ref={volumesRegion}
          tabIndex={-1}
          aria-labelledby={volumesHeading}
          className={cx(e.intro, e.recoveryTarget)}
        >
          <h3 id={volumesHeading} className={e.retirementSubheading}>
            {purging
              ? t(($) => $.lifecycle.status.purgingVolumesTitle)
              : purged
                ? t(($) => $.lifecycle.status.remainingVolumesTitle)
                : t(($) => $.lifecycle.status.volumesTitle)}
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
          ) : volumes.isError ? (
            // Failed reads are not retried on their own, so the owner asks again here.
            <div className={s.row}>
              <p className={e.muted}>
                {t(($) => $.lifecycle.status.volumesFailed)}
              </p>
              <Button
                size="sm"
                disabled={volumes.isFetching}
                onClick={() => {
                  volumesRegion.current?.focus();
                  void volumes.refetch();
                }}
              >
                {t(($) => $.retry)}
              </Button>
            </div>
          ) : (
            <p className={e.muted}>
              {t(($) => $.lifecycle.status.volumesLoading)}
            </p>
          )}
        </section>
      )}
      {owner && state === "retired" && (
        <div className={s.row}>
          <Button
            variant="primary"
            onClick={(event) => onChange("restore", event.currentTarget)}
          >
            {t(($) => $.lifecycle.restore)}
          </Button>
          <Button
            variant="danger"
            onClick={(event) => onChange("purge", event.currentTarget)}
          >
            {t(($) => $.lifecycle.purge)}
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
      {owner && purging && retryable && (
        <div>
          <Button
            variant="danger"
            onClick={(event) => onChange("purge", event.currentTarget)}
          >
            {t(($) => $.lifecycle.purge)}
          </Button>
        </div>
      )}
      {!owner && state === "retired" && (
        <p className={e.muted}>{t(($) => $.lifecycle.status.ownersOnly)}</p>
      )}
    </section>
  );
}

// Members are listed only to owners, so others learn who asked for a change only when it was
// them.
function RequestedBy({
  change,
  at,
  by,
  workspaceId,
  owner,
}: {
  change: "retire" | "purge";
  at: string | null | undefined;
  by: string | null | undefined;
  workspaceId: string;
  owner: boolean;
}) {
  const { t, i18n } = useTranslation("environment");
  const self = useQuery(sessionOptions).data?.user?.id;
  const member = useQuery({
    ...membersOptions(workspaceId),
    enabled: owner && !!at && !!by && by !== self,
  }).data?.find(({ id }) => id === by);
  if (!at) return null;
  const date = new Date(at).toLocaleString(i18n.resolvedLanguage);
  const person = member && { person: <bdi>{member.name}</bdi> };
  const retire = change === "retire";
  return (
    <p className={e.muted}>
      {by && by === self ? (
        retire ? (
          t(($) => $.lifecycle.status.requestedByYou, { date })
        ) : (
          t(($) => $.lifecycle.status.purgeRequestedByYou, { date })
        )
      ) : person ? (
        retire ? (
          <Trans
            t={t}
            i18nKey={($) => $.lifecycle.status.requestedBy}
            values={{ date }}
            components={person}
          />
        ) : (
          <Trans
            t={t}
            i18nKey={($) => $.lifecycle.status.purgeRequestedBy}
            values={{ date }}
            components={person}
          />
        )
      ) : retire ? (
        t(($) => $.lifecycle.status.requestedByOwner, { date })
      ) : (
        t(($) => $.lifecycle.status.purgeRequestedByOwner, { date })
      )}
    </p>
  );
}
