import type { UseQueryResult } from "@tanstack/react-query";
import type { Ref } from "react";
import { useTranslation } from "react-i18next";
import { client, result, type Schema } from "../../api/client";
import type { EnvironmentPath } from "../../api/environment";
import { Button } from "../../components/button";
import * as s from "../../styles";
import { ErrorNotice, type useAction } from "../../ui";
import * as e from "./environment.styles";
import { RevisionComparison } from "./revision-comparison";

export function RevisionHistory({
  history,
  path,
  action,
  comparison,
  draft,
  jsonId,
  headingRef,
  onCompareStart,
  onComparison,
  onClose,
}: {
  history: Pick<
    UseQueryResult<Schema["RevisionSummary"][]>,
    "data" | "error" | "isPending"
  >;
  path: EnvironmentPath;
  action: ReturnType<typeof useAction>;
  comparison?: Schema["Revision"];
  draft: string;
  jsonId: string;
  headingRef: Ref<HTMLHeadingElement>;
  onCompareStart: (trigger: HTMLButtonElement) => void;
  onComparison: (revision: Schema["Revision"]) => void;
  onClose: () => void;
}) {
  const envT = useTranslation("environment").t;
  return (
    <aside
      className={e.history}
      aria-label={envT(($) => $.configurationEditor.history)}
    >
      <h2 className={e.heading}>
        {envT(($) => $.configurationEditor.history)}
      </h2>
      <section className={s.panel}>
        <ErrorNotice error={history.error} />
        {history.isPending && (
          <p className={e.muted}>
            {envT(($) => $.configurationEditor.historyLoading)}
          </p>
        )}
        {history.data?.length === 0 && (
          <p className={e.muted}>
            {envT(($) => $.configurationEditor.historyEmpty)}
          </p>
        )}
        <div className={e.revisions}>
          {history.data?.map((revision) => (
            <div key={revision.id}>
              <p className={s.code}>{revision.hash.slice(0, 12)}</p>
              <time className={s.muted} dateTime={revision.createdAt}>
                {new Date(revision.createdAt).toLocaleString()}
              </time>
              <Button
                type="button"
                size="sm"
                disabled={action.isPending}
                aria-expanded={comparison?.id === revision.id}
                onClick={(event) => {
                  onCompareStart(event.currentTarget);
                  action.run(async () => {
                    const v = await result(
                      client.GET(
                        "/api/v1/workspaces/{workspaceId}/projects/{projectId}/environments/{environmentId}/revisions/{revisionId}",
                        {
                          params: {
                            path: { ...path, revisionId: revision.id },
                          },
                        },
                      ),
                    );
                    onComparison(v);
                  });
                }}
              >
                {envT(($) => $.configurationEditor.compare)}
              </Button>
            </div>
          ))}
        </div>
      </section>
      {comparison && (
        <RevisionComparison
          id={`${jsonId}-comparison`}
          title={envT(($) => $.configurationEditor.comparison)}
          description={envT(($) => $.configurationEditor.comparisonDescription)}
          draft={draft}
          revision={comparison}
          revisionLabel={envT(($) => $.configurationEditor.comparedRevision, {
            hash: comparison.hash.slice(0, 12),
          })}
          headingRef={headingRef}
          headerAction={
            <Button type="button" size="sm" onClick={onClose}>
              {envT(($) => $.configurationEditor.closeComparison)}
            </Button>
          }
        />
      )}
    </aside>
  );
}
