import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useBlocker } from "@tanstack/react-router";
import { useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { ApiError, client, result, type Schema } from "../../api/client";
import {
  AlertDialogActions,
  AlertDialogClose,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogRoot,
  AlertDialogTitle,
} from "../../components/alert-dialog";
import { Button } from "../../components/button";
import { Label } from "../../components/label";
import { TabsPanel } from "../../components/tabs";
import { Textarea } from "../../components/textarea";
import * as s from "../../styles";
import { ErrorNotice, useAction } from "../../ui";
import { Backups } from "../backups";
import { ComposeEditor } from "../compose-editor";
import { CrossHostPlacement } from "../cross-host-placement";
import { Deployments } from "../deployments";
import { Pipelines } from "../pipelines";
import { Previews } from "../previews";
import { Upgrades } from "../upgrades";
import { VisualEditor } from "../visual-editor";
import { EnvironmentOverview } from "./environment-overview";
import * as e from "./environment.styles";
import type { EnvProps, EnvironmentSection } from "./environment.types";
export function EnvironmentWorkspace({
  workspaceId,
  projectId,
  environment,
  initial,
  write,
  owner,
  visited,
  selectSection,
}: EnvProps & {
  initial: Schema["Revision"];
  visited: ReadonlySet<EnvironmentSection>;
  selectSection: (section: EnvironmentSection) => void;
}) {
  const path = { workspaceId, projectId, environmentId: environment.id };
  const [base, setBase] = useState(initial);
  const [draft, setDraft] = useState(
    JSON.stringify(initial.specification, null, 2),
  );
  const draftRef = useRef(draft);
  draftRef.current = draft;
  const [savedText, setSavedText] = useState(draft);
  const [rawChanged, setRawChanged] = useState(false);
  const [conflict, setConflict] = useState<Schema["Revision"]>();
  const [message, setMessage] = useState("");
  const rejectedDrafts = useRef(new WeakMap<Error, string>());
  const action = useAction();
  const rejectedText = action.error
    ? rejectedDrafts.current.get(action.error)
    : undefined;
  const visibleError =
    rejectedText === undefined || rejectedText === draft
      ? action.error
      : undefined;
  const queries = useQueryClient();
  const dirty = draft !== savedText;
  const [layoutDirty, setLayoutDirty] = useState(false);
  const appT = useTranslation("app").t;
  const envT = useTranslation("environment").t;
  const blocker = useBlocker({
    shouldBlockFn: () => dirty || layoutDirty,
    enableBeforeUnload: dirty || layoutDirty,
    withResolver: true,
  });
  const history = useQuery({
    queryKey: ["revisions", path],
    queryFn: () =>
      result(
        client.GET(
          "/api/v1/workspaces/{workspaceId}/projects/{projectId}/environments/{environmentId}/revisions",
          { params: { path } },
        ),
      ),
  });
  const replace = (revision: Schema["Revision"]) => {
    const next = JSON.stringify(revision.specification, null, 2);
    setBase(revision);
    setDraft(next);
    setSavedText(next);
    setConflict(undefined);
    setRawChanged(false);
    setMessage("Current revision loaded.");
  };
  const save = () =>
    action.run(async () => {
      let specification: Schema["Specification"];
      try {
        specification = JSON.parse(draft) as Schema["Specification"];
      } catch (error) {
        if (error instanceof SyntaxError)
          rejectedDrafts.current.set(error, draft);
        throw error;
      }
      try {
        const revision = await result(
          client.PUT(
            "/api/v1/workspaces/{workspaceId}/projects/{projectId}/environments/{environmentId}/draft",
            {
              params: { path },
              body: { baseRevisionId: base.id, specification },
            },
          ),
        );
        setBase(revision);
        setSavedText(JSON.stringify(revision.specification, null, 2));
        const canonical = JSON.stringify(revision.specification, null, 2);
        setDraft((current) => (current === draft ? canonical : current));
        if (draftRef.current === draft) setRawChanged(false);
        setConflict(undefined);
        setMessage("Revision saved.");
        await queries.invalidateQueries({ queryKey: ["revisions", path] });
      } catch (error) {
        if (error instanceof ApiError) {
          if (error.detail.code === "invalid_input")
            rejectedDrafts.current.set(error, draft);
          if (error.detail.currentRevision)
            setConflict(error.detail.currentRevision);
        }
        throw error;
      }
    });
  return (
    <>
      <TabsPanel value="overview" keepMounted>
        <EnvironmentOverview
          environment={environment}
          path={path}
          revision={base}
          dirty={dirty || layoutDirty}
          write={write}
          onConfigure={() => selectSection("configuration")}
        />
      </TabsPanel>
      <TabsPanel
        value="configuration"
        keepMounted={visited.has("configuration")}
      >
        <div className={s.stack}>
          <div className={e.configuration}>
            <section className={s.panel}>
              <div className={s.spread}>
                <h2>Configuration</h2>
                <span className={s.muted}>
                  {dirty ? "Unsaved changes" : "Saved revision"}
                </span>
              </div>
              <p className={s.muted}>
                {envT(($) => $.configurationDescription)}
              </p>
              <ErrorNotice error={visibleError} />
              {message && (
                <p role="status" className={s.notice}>
                  {message}
                </p>
              )}
              <div hidden={rawChanged}>
                {environment.configurationOwner === "compose" ? (
                  <ComposeEditor
                    baseFiles={base.specification.compose?.files ?? []}
                    path={path}
                    spec={
                      rawChanged
                        ? base.specification
                        : (JSON.parse(draft) as Schema["Specification"])
                    }
                    change={(spec) => {
                      setDraft(JSON.stringify(spec, null, 2));
                      setMessage("");
                    }}
                    write={write}
                    dirty={dirty}
                    onLayoutDirty={setLayoutDirty}
                  />
                ) : (
                  <VisualEditor
                    path={path}
                    spec={
                      rawChanged
                        ? base.specification
                        : (JSON.parse(draft) as Schema["Specification"])
                    }
                    change={(spec) => {
                      setDraft(JSON.stringify(spec, null, 2));
                      setMessage("");
                    }}
                    write={write}
                    dirty={dirty}
                    onLayoutDirty={setLayoutDirty}
                  />
                )}
              </div>
              {rawChanged && (
                <p className={s.notice}>
                  Save the advanced specification before returning to the visual
                  editor.
                </p>
              )}
              {environment.configurationOwner !== "compose" && (
                <details>
                  <summary>Advanced specification</summary>
                  <Label>
                    Specification (JSON)
                    <Textarea
                      className={s.editor}
                      value={draft}
                      readOnly={!write}
                      spellCheck={false}
                      onChange={(e) => {
                        setDraft(e.target.value);
                        setRawChanged(true);
                        setMessage("");
                      }}
                    />
                  </Label>
                </details>
              )}
              {write && (
                <div className={s.row}>
                  <Button
                    type="button"
                    variant="primary"
                    disabled={!dirty || action.isPending || !!conflict}
                    onClick={save}
                  >
                    {action.isPending ? "Saving…" : "Save revision"}
                  </Button>
                  <Button
                    type="button"
                    disabled={action.isPending}
                    onClick={() => {
                      if (
                        !dirty ||
                        window.confirm(
                          "Discard unsaved edits and load the current revision?",
                        )
                      )
                        action.run(async () =>
                          replace(
                            await result(
                              client.GET(
                                "/api/v1/workspaces/{workspaceId}/projects/{projectId}/environments/{environmentId}/draft",
                                { params: { path } },
                              ),
                            ),
                          ),
                        );
                    }}
                  >
                    Reload current
                  </Button>
                </div>
              )}
              {conflict && (
                <section className={s.stack} aria-label="Conflicting revision">
                  <h3>Another revision was saved</h3>
                  <p>
                    Your edits are still in the editor above. Compare them with
                    the current server revision below, then merge your changes
                    before saving again.
                  </p>
                  <pre className={s.code} style={{ whiteSpace: "pre-wrap" }}>
                    {JSON.stringify(conflict.specification, null, 2)}
                  </pre>
                  <Button
                    type="button"
                    onClick={() => {
                      setBase(conflict);
                      setConflict(undefined);
                      setMessage(
                        "Your edits are preserved and will be saved on top of the compared revision.",
                      );
                    }}
                  >
                    I have compared and merged my edits
                  </Button>
                </section>
              )}
            </section>
            <aside
              className={e.history}
              aria-label="Revision history and layout"
            >
              <h2>Revision history</h2>
              <section className={s.panel}>
                <ErrorNotice error={history.error} />
                <div className={e.revisions}>
                  {history.data?.map((revision) => (
                    <div key={revision.id}>
                      <p className={s.code}>{revision.hash.slice(0, 12)}</p>
                      <time className={s.muted} dateTime={revision.createdAt}>
                        {new Date(revision.createdAt).toLocaleString()}
                      </time>
                      <Button
                        type="button"
                        onClick={() =>
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
                            setConflict(v);
                          })
                        }
                      >
                        Compare revision
                      </Button>
                    </div>
                  ))}
                </div>
              </section>
            </aside>
          </div>
          {write &&
            !base.specification.compose &&
            (!environment.purpose || environment.purpose === "standard") &&
            !rawChanged && (
              <PlacementWorkspace
                path={path}
                spec={JSON.parse(draft) as Schema["Specification"]}
                change={(spec) => setDraft(JSON.stringify(spec, null, 2))}
              />
            )}
        </div>
      </TabsPanel>
      <TabsPanel value="deployments" keepMounted={visited.has("deployments")}>
        <div className={s.stack}>
          {environment.purpose === "preview_template" && (
            <>
              <Previews
                path={path}
                revision={base}
                dirty={dirty}
                owner={owner}
              />
            </>
          )}
          {(!environment.purpose || environment.purpose === "standard") && (
            <Pipelines
              path={path}
              revision={base}
              dirty={dirty}
              write={write}
              owner={owner}
              stage={(spec) => setDraft(JSON.stringify(spec, null, 2))}
            />
          )}
          {environment.purpose !== "preview_template" && (
            <Deployments
              loadRevision={(next) => {
                if (draftRef.current !== savedText) {
                  setConflict(next);
                  return;
                }
                replace(next);
              }}
              path={path}
              revision={base}
              dirty={dirty}
              write={write}
              owner={owner && environment.purpose !== "preview"}
              removeVolume={(id) => {
                if (base.specification.compose) return;
                setDraft((current) => {
                  try {
                    const spec = JSON.parse(current) as Schema["Specification"];
                    return JSON.stringify(
                      {
                        ...spec,
                        volumes: spec.volumes?.filter((v) => v.id !== id),
                      },
                      null,
                      2,
                    );
                  } catch {
                    return current;
                  }
                });
                setMessage(
                  "Volume removal staged. Save this revision after reviewing the deletion result.",
                );
              }}
            />
          )}
          {(!environment.purpose || environment.purpose === "standard") && (
            <>
              <Upgrades
                path={path}
                revision={base}
                owner={owner}
                dirty={dirty}
                load={(revision) => {
                  if (draftRef.current !== savedText) {
                    setConflict(revision);
                    return false;
                  }
                  replace(revision);
                  return true;
                }}
              />
              <Backups
                path={path}
                revision={base}
                owner={owner}
                dirty={dirty}
                load={(revision) => {
                  if (draftRef.current !== savedText) {
                    setConflict(revision);
                    return false;
                  }
                  replace(revision);
                  return true;
                }}
              />
            </>
          )}
        </div>
      </TabsPanel>
      <AlertDialogRoot
        open={blocker.status === "blocked"}
        onOpenChange={(open) => {
          if (!open && blocker.status === "blocked") blocker.reset();
        }}
      >
        <AlertDialogContent>
          <AlertDialogTitle>{appT(($) => $.unsaved.title)}</AlertDialogTitle>
          <AlertDialogDescription>
            {appT(($) => $.unsaved.description)}
          </AlertDialogDescription>
          <AlertDialogActions>
            <AlertDialogClose
              render={<Button>{appT(($) => $.unsaved.keep)}</Button>}
            />
            <Button
              variant="danger"
              onClick={() => {
                if (blocker.status === "blocked") blocker.proceed();
              }}
            >
              {appT(($) => $.unsaved.discard)}
            </Button>
          </AlertDialogActions>
        </AlertDialogContent>
      </AlertDialogRoot>
    </>
  );
}

function PlacementWorkspace({
  path,
  spec,
  change,
}: {
  path: { workspaceId: string };
  spec: Schema["Specification"];
  change: (spec: Schema["Specification"]) => void;
}) {
  const servers = useQuery({
    queryKey: ["servers", path.workspaceId],
    queryFn: () =>
      result(
        client.GET("/api/v1/workspaces/{workspaceId}/servers", {
          params: { path: { workspaceId: path.workspaceId } },
        }),
      ),
    refetchInterval: 15000,
  });
  return (
    <CrossHostPlacement
      workspaceId={path.workspaceId}
      servers={servers.data ?? []}
      spec={spec}
      change={change}
    />
  );
}
