import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useBlocker } from "@tanstack/react-router";
import { useEffect, useId, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { ApiError, client, result, type Schema } from "../../api/client";
import { draftOptions, publishDraft } from "../../api/environment";
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
import { RevisionComparison } from "./revision-comparison";
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
  const [comparison, setComparison] = useState<Schema["Revision"]>();
  const [reloadOpen, setReloadOpen] = useState(false);
  const [operation, setOperation] = useState<"save" | "reload" | "compare">();
  const reloadButton = useRef<HTMLButtonElement>(null);
  const configurationHeading = useRef<HTMLHeadingElement>(null);
  const advancedDetails = useRef<HTMLDetailsElement>(null);
  const jsonEditor = useRef<HTMLTextAreaElement>(null);
  const focusTarget = (button: HTMLButtonElement | null) =>
    button && !button.disabled ? button : configurationHeading.current;
  const comparisonHeading = useRef<HTMLHeadingElement>(null);
  const conflictHeading = useRef<HTMLHeadingElement>(null);
  const comparisonTrigger = useRef<HTMLButtonElement>(null);
  const jsonId = useId();
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
  useEffect(() => {
    if (visibleError instanceof SyntaxError) {
      if (advancedDetails.current) advancedDetails.current.open = true;
      jsonEditor.current?.focus();
    }
  }, [visibleError]);
  const appT = useTranslation("app").t;
  const envT = useTranslation("environment").t;
  useEffect(() => {
    if (comparison) comparisonHeading.current?.focus();
  }, [comparison]);
  useEffect(() => {
    if (conflict) conflictHeading.current?.focus();
  }, [conflict]);
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
    setMessage(envT(($) => $.configurationEditor.loaded));
  };
  const reload = () => {
    const submitted = draftRef.current;
    setOperation("reload");
    action.run(async () => {
      const revision = await queries.fetchQuery({
        ...draftOptions(path),
        staleTime: 0,
      });
      if (draftRef.current !== submitted) {
        setMessage(envT(($) => $.configurationEditor.reloadNotApplied));
        return;
      }
      replace(revision);
    });
  };
  const save = () => {
    setOperation("save");
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
        await publishDraft(queries, path, revision);
        setBase(revision);
        setSavedText(JSON.stringify(revision.specification, null, 2));
        const canonical = JSON.stringify(revision.specification, null, 2);
        setDraft((current) => (current === draft ? canonical : current));
        if (draftRef.current === draft) setRawChanged(false);
        setConflict(undefined);
        setMessage(envT(($) => $.configurationEditor.saved));
        await queries.invalidateQueries({ queryKey: ["revisions", path] });
      } catch (error) {
        if (error instanceof ApiError) {
          if (error.detail.code === "invalid_input")
            rejectedDrafts.current.set(error, draft);
          if (error.detail.currentRevision) {
            await publishDraft(queries, path, error.detail.currentRevision);
            setConflict(error.detail.currentRevision);
          }
        }
        throw error;
      }
    });
  };
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
              <div className={e.configurationHeader}>
                <div className={e.intro}>
                  <h2
                    ref={configurationHeading}
                    tabIndex={-1}
                    className={e.configurationHeading}
                  >
                    {envT(($) => $.configuration)}
                  </h2>
                  <div className={e.metadata}>
                    <span>
                      {envT(($) => $.configurationEditor.revision, {
                        hash: base.hash.slice(0, 12),
                      })}
                    </span>
                    <span>
                      {dirty
                        ? envT(($) => $.configurationEditor.unsaved)
                        : envT(($) => $.savedRevision)}
                    </span>
                    {layoutDirty && (
                      <span>
                        {envT(($) => $.configurationEditor.layoutUnsaved)}
                      </span>
                    )}
                  </div>
                </div>
                {write && (
                  <div className={e.configurationActions}>
                    <Button
                      type="button"
                      size="sm"
                      variant="primary"
                      disabled={!dirty || action.isPending || !!conflict}
                      onClick={save}
                    >
                      {action.isPending && operation === "save"
                        ? envT(($) => $.configurationEditor.saving)
                        : envT(($) => $.configurationEditor.save)}
                    </Button>
                    <Button
                      ref={reloadButton}
                      type="button"
                      size="sm"
                      disabled={action.isPending}
                      onClick={() => (dirty ? setReloadOpen(true) : reload())}
                    >
                      {action.isPending && operation === "reload"
                        ? envT(($) => $.configurationEditor.reloading)
                        : envT(($) => $.configurationEditor.reload)}
                    </Button>
                  </div>
                )}
              </div>
              <p className={e.muted}>
                {envT(($) => $.configurationDescription)}
              </p>
              {!(visibleError instanceof SyntaxError) && (
                <ErrorNotice error={visibleError} />
              )}
              {message && (
                <p role="status" className={e.configurationNotice}>
                  {message}
                </p>
              )}
              {conflict && (
                <RevisionComparison
                  id={`${jsonId}-conflict`}
                  label={envT(($) => $.configurationEditor.conflictRegion)}
                  title={envT(($) => $.configurationEditor.conflictTitle)}
                  description={envT(
                    ($) => $.configurationEditor.conflictDescription,
                  )}
                  draft={draft}
                  revision={conflict}
                  revisionLabel={envT(
                    ($) => $.configurationEditor.serverRevision,
                    { hash: conflict.hash.slice(0, 12) },
                  )}
                  headingRef={conflictHeading}
                >
                  {write && (
                    <div>
                      <Button
                        type="button"
                        onClick={() => {
                          setBase(conflict);
                          setConflict(undefined);
                          setMessage(envT(($) => $.configurationEditor.merged));
                          focusTarget(reloadButton.current)?.focus();
                        }}
                      >
                        {envT(($) => $.configurationEditor.acknowledge)}
                      </Button>
                    </div>
                  )}
                </RevisionComparison>
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
                <p className={e.configurationNotice}>
                  {envT(($) => $.configurationEditor.advancedNotice)}
                </p>
              )}
              {environment.configurationOwner !== "compose" && (
                <details ref={advancedDetails} className={s.workspaceUtility}>
                  <summary>
                    {envT(($) => $.configurationEditor.advanced)}
                  </summary>
                  <div className={e.intro}>
                    <Label htmlFor={jsonId}>
                      {envT(($) => $.configurationEditor.json)}
                    </Label>
                    <Textarea
                      ref={jsonEditor}
                      id={jsonId}
                      className={s.editor}
                      value={draft}
                      readOnly={!write}
                      spellCheck={false}
                      aria-invalid={
                        visibleError instanceof SyntaxError || undefined
                      }
                      aria-describedby={
                        visibleError instanceof SyntaxError
                          ? `${jsonId}-error`
                          : undefined
                      }
                      onChange={(event) => {
                        setDraft(event.target.value);
                        setRawChanged(true);
                        setMessage("");
                      }}
                    />
                    {visibleError instanceof SyntaxError && (
                      <p
                        id={`${jsonId}-error`}
                        role="alert"
                        className={e.fieldError}
                      >
                        {envT(($) => $.configurationEditor.invalidJson)}
                      </p>
                    )}
                  </div>
                </details>
              )}
            </section>
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
                          comparisonTrigger.current = event.currentTarget;
                          setOperation("compare");
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
                            setComparison(v);
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
                  description={envT(
                    ($) => $.configurationEditor.comparisonDescription,
                  )}
                  draft={draft}
                  revision={comparison}
                  revisionLabel={envT(
                    ($) => $.configurationEditor.comparedRevision,
                    { hash: comparison.hash.slice(0, 12) },
                  )}
                  headingRef={comparisonHeading}
                  headerAction={
                    <Button
                      type="button"
                      size="sm"
                      onClick={() => {
                        setComparison(undefined);
                        focusTarget(comparisonTrigger.current)?.focus();
                      }}
                    >
                      {envT(($) => $.configurationEditor.closeComparison)}
                    </Button>
                  }
                />
              )}
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
      <AlertDialogRoot open={reloadOpen} onOpenChange={setReloadOpen}>
        <AlertDialogContent
          finalFocus={() => focusTarget(reloadButton.current)}
        >
          <AlertDialogTitle>
            {envT(($) => $.configurationEditor.reloadTitle)}
          </AlertDialogTitle>
          <AlertDialogDescription>
            {envT(($) => $.configurationEditor.reloadDescription)}
          </AlertDialogDescription>
          <AlertDialogActions>
            <AlertDialogClose
              render={
                <Button>
                  {envT(($) => $.configurationEditor.keepEditing)}
                </Button>
              }
            />
            <Button
              variant="danger"
              onClick={() => {
                setReloadOpen(false);
                reload();
              }}
            >
              {envT(($) => $.configurationEditor.discardReload)}
            </Button>
          </AlertDialogActions>
        </AlertDialogContent>
      </AlertDialogRoot>
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
