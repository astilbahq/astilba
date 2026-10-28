import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useBlocker } from "@tanstack/react-router";
import { useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { ApiError, client, result, type Schema } from "./api/client";
import {
  AlertDialogActions,
  AlertDialogClose,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogRoot,
  AlertDialogTitle,
} from "./components/alert-dialog";
import { Button } from "./components/button";
import { Label } from "./components/label";
import { Textarea } from "./components/textarea";
import { AutomationCredentials } from "./features/automation";
import { Backups } from "./features/backups";
import { ComposeEditor } from "./features/compose-editor";
import { CrossHostPlacement } from "./features/cross-host-placement";
import { Deployments } from "./features/deployments";
import { IntegrationSettings } from "./features/integrations";
import { Pipelines } from "./features/pipelines";
import { Previews } from "./features/previews";
import { ProjectDetail } from "./features/projects/project-detail";
import { ProjectsOverview } from "./features/projects/projects-overview";
import { Upgrades } from "./features/upgrades";
import { VisualEditor } from "./features/visual-editor";
import { hostingOptions } from "./hosting";
import { WorkspaceImport } from "./portability";
import * as s from "./styles";
import { BusyButton, ErrorNotice, Field, Form, text, useAction } from "./ui";
export type WorkspaceSelection = {
  workspace?: string;
  project?: string;
  environment?: string;
};

export function Dashboard({ selection }: { selection: WorkspaceSelection }) {
  if (selection.project) {
    return (
      <ProjectDetail
        key={selection.project}
        projectId={selection.project}
        environmentId={selection.environment}
        renderEnvironment={(environment, workspace) => (
          <Environment
            key={environment.id}
            workspaceId={workspace.id}
            projectId={environment.projectId}
            environment={environment}
            write={workspace.role !== "viewer"}
            owner={workspace.role === "owner"}
          />
        )}
      />
    );
  }
  return <ProjectsLanding />;
}
function ProjectsLanding() {
  const action = useAction();
  const hosted = useQuery(hostingOptions).data?.enabled;
  return (
    <div className={s.stack}>
      <ErrorNotice error={action.error} />
      <ProjectsOverview />
      <WorkspaceImport hosted={!!hosted} />
      <details className={s.workspaceUtility}>
        <summary>Create another workspace</summary>
        <Form
          action={action}
          onSubmit={(data, form) =>
            action.run(async () => {
              await result(
                client.POST("/api/v1/workspaces", {
                  body: {
                    name: text(data, "name"),
                    invitation: hosted ? text(data, "invitation") : undefined,
                  },
                }),
              );
              form.reset();
            })
          }
        >
          <Field name="name" label="Workspace name" />
          {hosted && (
            <Field
              name="invitation"
              label="Beta invitation code"
              autoComplete="off"
            />
          )}
          <BusyButton busy={action.isPending}>Create workspace</BusyButton>
        </Form>
      </details>
    </div>
  );
}
type EnvProps = {
  workspaceId: string;
  projectId: string;
  environment: Schema["Environment"];
  write: boolean;
  owner: boolean;
};
function Environment(props: EnvProps) {
  const path = {
    workspaceId: props.workspaceId,
    projectId: props.projectId,
    environmentId: props.environment.id,
  };
  const draft = useQuery({
    queryKey: ["draft", path],
    queryFn: () =>
      result(
        client.GET(
          "/api/v1/workspaces/{workspaceId}/projects/{projectId}/environments/{environmentId}/draft",
          { params: { path } },
        ),
      ),
  });
  return (
    <div className={s.stack}>
      <div className={s.spread}>
        <div>
          <h2>{props.environment.name}</h2>
          <p className={s.muted}>
            {props.environment.configurationOwner === "compose"
              ? "Compose files"
              : "Dashboard configuration"}{" "}
            · Docker deployment unit
          </p>
        </div>
        <span className={s.notice}>
          {props.environment.deploymentUnit.serverId
            ? "Server assigned"
            : "Server not assigned"}
        </span>
      </div>
      {props.environment.purpose === "preview_template" && (
        <p className={s.notice}>
          Preview template · This environment supplies separately configured
          preview resources and secrets. It cannot be deployed.
        </p>
      )}
      {props.environment.purpose === "preview" && (
        <p className={s.notice}>
          Managed pull-request preview · Configuration and cleanup are
          controlled by its template’s policy.
        </p>
      )}
      <ErrorNotice error={draft.error} />
      {draft.data ? (
        <DraftEditor
          {...props}
          write={props.write && props.environment.purpose !== "preview"}
          initial={draft.data}
        />
      ) : (
        <p role="status">Loading configuration…</p>
      )}
      <Secrets
        {...props}
        write={props.write && props.environment.purpose !== "preview"}
      />
      {props.owner && (
        <AutomationCredentials
          key={props.environment.id}
          path={path}
          readOnly={
            !!props.environment.purpose &&
            props.environment.purpose !== "standard"
          }
        />
      )}
    </div>
  );
}
function DraftEditor({
  workspaceId,
  projectId,
  environment,
  initial,
  write,
  owner,
}: EnvProps & { initial: Schema["Revision"] }) {
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
      <div className={s.columns}>
        <section className={s.panel}>
          <div className={s.spread}>
            <h2>Configuration</h2>
            <span className={s.muted}>
              {dirty ? "Unsaved changes" : "Saved revision"}
            </span>
          </div>
          <p className={s.muted}>
            Add services, data volumes and connections. Configure repository
            imports and builds below.
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
                Your edits are still in the editor above. Compare them with the
                current server revision below, then merge your changes before
                saving again.
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
        <aside className={s.stack} aria-label="Revision history and layout">
          <section className={s.panel}>
            <h2>Revision history</h2>
            <ErrorNotice error={history.error} />
            <div className={s.list}>
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
      {environment.purpose === "preview_template" && (
        <>
          <Previews path={path} revision={base} dirty={dirty} owner={owner} />
          {owner && <IntegrationSettings workspaceId={path.workspaceId} />}
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
function Secrets({ workspaceId, projectId, environment, write }: EnvProps) {
  const path = { workspaceId, projectId, environmentId: environment.id };
  const queries = useQueryClient();
  const action = useAction();
  const [message, setMessage] = useState("");
  const secrets = useQuery({
    queryKey: ["secrets", path],
    queryFn: () =>
      result(
        client.GET(
          "/api/v1/workspaces/{workspaceId}/projects/{projectId}/environments/{environmentId}/secrets",
          { params: { path } },
        ),
      ),
  });
  const latest = secrets.data?.filter(
    (v, i, all) => all.findIndex((x) => x.secretId === v.secretId) === i,
  );
  return (
    <section className={s.panel}>
      <h2>Secrets</h2>
      <p className={s.muted}>
        Values are write-only. Reference a version ID in your specification with{" "}
        <code>secretVersionId</code>.
      </p>
      <ErrorNotice error={action.error ?? secrets.error} />
      {message && (
        <p className={s.notice} role="status">
          {message}
        </p>
      )}
      <div className={s.list}>
        {latest?.map((v) => (
          <div key={v.id}>
            <strong>{v.name}</strong>
            <p className={s.code}>{v.id}</p>
            <small>
              Version created {new Date(v.createdAt).toLocaleString()}
            </small>
          </div>
        ))}
      </div>
      {write && (
        <Form
          action={action}
          onSubmit={(d, form) =>
            action.run(async () => {
              const name = text(d, "name");
              const existing = latest?.find((v) => v.name === name);
              const value = text(d, "value");
              if (existing)
                await result(
                  client.POST(
                    "/api/v1/workspaces/{workspaceId}/projects/{projectId}/environments/{environmentId}/secrets/{secretId}/versions",
                    {
                      params: {
                        path: { ...path, secretId: existing.secretId },
                      },
                      body: { value },
                    },
                  ),
                );
              else
                await result(
                  client.POST(
                    "/api/v1/workspaces/{workspaceId}/projects/{projectId}/environments/{environmentId}/secrets",
                    { params: { path }, body: { name, value } },
                  ),
                );
              form.reset();
              setMessage(
                "Secret version saved. Its value cannot be read back.",
              );
              await queries.cancelQueries({ queryKey: ["secrets", path] });
              await queries.invalidateQueries({ queryKey: ["secrets", path] });
            })
          }
        >
          <Field name="name" label="Secret name" />
          <Field
            name="value"
            label="Secret value"
            type="password"
            autoComplete="off"
          />
          <small>
            Reusing a name adds a version. Confirm your identity in Account
            security if requested.
          </small>
          <BusyButton busy={action.isPending}>Save secret version</BusyButton>
        </Form>
      )}
    </section>
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
