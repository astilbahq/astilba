import { queryOptions, useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { client, result } from "../api/client";
import * as s from "../styles";
import { ErrorNotice } from "../ui";

export const crossHostRecoveryOptions = (workspaceId: string) =>
  queryOptions({
    queryKey: ["cross-host-recovery", workspaceId],
    queryFn: () =>
      result(
        client.GET("/api/v1/workspaces/{workspaceId}/cross-host-recovery", {
          params: { path: { workspaceId } },
        }),
      ),
  });

// The newest recovery checkpoint and the oldest management backup cross-host recovery can use
// with it, for owners and operators. A short window is also raised as the recovery_window alert.
export function CrossHostRecovery({ workspaceId }: { workspaceId: string }) {
  const { t, i18n } = useTranslation("workspace");
  const recovery = useQuery(crossHostRecoveryOptions(workspaceId));
  const date = (value: string) =>
    new Date(value).toLocaleString(i18n.resolvedLanguage);
  const data = recovery.data;
  return (
    <section
      className={s.panel}
      aria-label={t(($) => $.crossHostRecovery.title)}
    >
      <h2>{t(($) => $.crossHostRecovery.title)}</h2>
      <p className={s.muted}>{t(($) => $.crossHostRecovery.description)}</p>
      {recovery.isPending ? (
        <p role="status">{t(($) => $.crossHostRecovery.loading)}</p>
      ) : !data ? (
        <ErrorNotice
          error={recovery.error}
          message={t(($) => $.crossHostRecovery.loadFailed)}
        />
      ) : !data.checkpointAt ? (
        <p>{t(($) => $.crossHostRecovery.none)}</p>
      ) : (
        <>
          <p>
            {t(($) => $.crossHostRecovery.checkpoint, {
              date: date(data.checkpointAt),
            })}
          </p>
          {data.windowStart ? (
            <p>
              {t(($) => $.crossHostRecovery.window, {
                date: date(data.windowStart),
              })}
            </p>
          ) : (
            <p>{t(($) => $.crossHostRecovery.whole)}</p>
          )}
          {data.shortWindow && data.windowStart && (
            <p className={s.notice}>
              {t(($) => $.crossHostRecovery.short, {
                date: date(data.windowStart),
              })}
            </p>
          )}
        </>
      )}
    </section>
  );
}
