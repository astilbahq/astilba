import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useBlocker } from "@tanstack/react-router";
import { useRef, useState } from "react";
import { ApiError, client, result, type Schema } from "./api/client";
import { sessionOptions } from "./api/session";
import { Button } from "./components/button";
import { Label } from "./components/label";
import { Link } from "./components/link";
import { Select } from "./components/select";
import { Textarea } from "./components/textarea";
import { Backups } from "./features/backups";
import { ComposeEditor } from "./features/compose-editor";
import { Deployments } from "./features/deployments";
import { Pipelines } from "./features/pipelines";
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

export function Dashboard({
  selection,
  onSelectionChange,
}: {
  selection: WorkspaceSelection;
  onSelectionChange: (selection: WorkspaceSelection) => Promise<void>;
}) {
  const hosted = useQuery(hostingOptions).data?.enabled;
  const queries = useQueryClient();
  const session = useQuery(sessionOptions).data;
  const action = useAction();
  const workspace =
    session?.workspaces.find((w) => w.id === selection.workspace) ??
    session?.workspaces[0];
  const workspaceId = workspace?.id ?? "";
  const projects = useQuery({
    queryKey: ["projects", workspaceId],
    enabled: !!workspaceId,
    queryFn: () =>
      result(
        client.GET("/api/v1/workspaces/{workspaceId}/projects", {
          params: { path: { workspaceId } },
        }),
      ),
  });
  const project = projects.data?.find((p) => p.id === selection.project);
  const projectId = project?.id ?? "";
  const environments = useQuery({
    queryKey: ["environments", workspaceId, projectId],
    enabled: !!projectId,
    queryFn: () =>
      result(
        client.GET(
          "/api/v1/workspaces/{workspaceId}/projects/{projectId}/environments",
          { params: { path: { workspaceId, projectId } } },
        ),
      ),
  });
  const environment = environments.data?.find(
    (e) => e.id === selection.environment,
  );
  const write = workspace?.role !== "viewer";
  return (
    <div className={s.stack}>
      <header className={s.spread}>
        <div>
          <p className={s.muted}>YOUR WORKSPACE</p>
          <h1>{project?.name ?? "Built on your terms."}</h1>
          <p className={s.muted}>
            {project
              ? "A home for your applications and their configuration."
              : "Your projects, environments and infrastructure in one place."}
          </p>
        </div>
        {workspace && (
          <Label>
            Workspace
            <Select
              value={workspaceId}
              onChange={(e) =>
                void onSelectionChange({
                  workspace: e.target.value,
                  project: undefined,
                  environment: undefined,
                })
              }
            >
              {session?.workspaces.map((w) => (
                <option key={w.id} value={w.id}>
                  {w.name}
                </option>
              ))}
            </Select>
          </Label>
        )}
      </header>
      <ErrorNotice
        error={action.error ?? projects.error ?? environments.error}
      />
      {!project ? (
        <>
          <section className={s.panel}>
            <div className={s.spread}>
              <h2>Projects</h2>
              <span className={s.muted}>
                {projects.data?.length ?? 0} total
              </span>
            </div>
            {projects.isPending ? (
              <p role="status">Loading projects…</p>
            ) : projects.data?.length ? (
              <div className={s.list}>
                {projects.data.map((p) => (
                  <Link
                    key={p.id}
                    to="/"
                    search={{
                      workspace: workspaceId,
                      project: p.id,
                      environment: undefined,
                    }}
                  >
                    <div className={s.spread}>
                      <strong>{p.name}</strong>
                      <span>Open project →</span>
                    </div>
                  </Link>
                ))}
              </div>
            ) : (
              <p className={s.muted}>
                Start with a project, then add its first environment.
              </p>
            )}
            {write && workspace && (
              <Form
                action={action}
                label="New project"
                onSubmit={(d, form) =>
                  action.run(async () => {
                    await result(
                      client.POST("/api/v1/workspaces/{workspaceId}/projects", {
                        params: { path: { workspaceId } },
                        body: { name: text(d, "name") },
                      }),
                    );
                    form.reset();
                    // An initial list request can still contain the pre-create snapshot.
                    await queries.cancelQueries({
                      queryKey: ["projects", workspaceId],
                    });
                    await queries.invalidateQueries({
                      queryKey: ["projects", workspaceId],
                    });
                  })
                }
              >
                <Field name="name" label="Project name" />
                <BusyButton busy={action.isPending}>Create project</BusyButton>
              </Form>
            )}
          </section>
          <WorkspaceImport hosted={!!hosted} />
          <details className={s.panel}>
            <summary>Create another workspace</summary>
            <Form
              action={action}
              onSubmit={(d, form) =>
                action.run(async () => {
                  await result(
                    client.POST("/api/v1/workspaces", {
                      body: {
                        name: text(d, "name"),
                        invitation: hosted ? text(d, "invitation") : undefined,
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
        </>
      ) : (
        <>
          <Link
            to="/"
            search={{
              workspace: workspaceId,
              project: undefined,
              environment: undefined,
            }}
          >
            ← All projects
          </Link>
          <section className={s.panel}>
            <div className={s.spread}>
              <h2>Environments</h2>
              <div className={s.row}>
                {environments.data?.map((env) => (
                  <Link
                    to="/"
                    key={env.id}
                    search={{
                      workspace: workspaceId,
                      project: projectId,
                      environment: env.id,
                    }}
                    aria-current={
                      env.id === environment?.id ? "page" : undefined
                    }
                  >
                    {env.name}
                  </Link>
                ))}
              </div>
            </div>
            {write && (
              <details>
                <summary>Add an environment</summary>
                <Form
                  action={action}
                  onSubmit={(d, form) =>
                    action.run(async () => {
                      const env = await result(
                        client.POST(
                          "/api/v1/workspaces/{workspaceId}/projects/{projectId}/environments",
                          {
                            params: { path: { workspaceId, projectId } },
                            body: {
                              name: text(d, "name"),
                              configurationOwner: text(d, "owner") as
                                | "dashboard"
                                | "compose",
                            },
                          },
                        ),
                      );
                      form.reset();
                      await queries.cancelQueries({
                        queryKey: ["environments", workspaceId, projectId],
                      });
                      await queries.invalidateQueries({
                        queryKey: ["environments", workspaceId, projectId],
                      });
                      await onSelectionChange({
                        workspace: workspaceId,
                        project: projectId,
                        environment: env.id,
                      });
                    })
                  }
                >
                  <Field name="name" label="Environment name" />
                  <Label>
                    Configuration ownership
                    <Select name="owner">
                      <option value="dashboard">Visual configuration</option>
                      <option value="compose">Compose files</option>
                    </Select>
                  </Label>
                  <BusyButton busy={action.isPending}>
                    Create environment
                  </BusyButton>
                </Form>
              </details>
            )}
          </section>
          {environment ? (
            <Environment
              key={environment.id}
              workspaceId={workspaceId}
              projectId={projectId}
              environment={environment}
              write={write}
              owner={workspace?.role === "owner"}
            />
          ) : (
            <p className={s.notice}>
              Choose an environment to view its configuration.
            </p>
          )}
        </>
      )}
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
      <ErrorNotice error={draft.error} />
      {draft.data ? (
        <DraftEditor {...props} initial={draft.data} />
      ) : (
        <p role="status">Loading configuration…</p>
      )}
      <Secrets {...props} />
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
      <Pipelines
        path={path}
        revision={base}
        dirty={dirty}
        write={write}
        owner={owner}
        stage={(spec) => setDraft(JSON.stringify(spec, null, 2))}
      />
      <Deployments
        path={path}
        revision={base}
        dirty={dirty}
        write={write}
        owner={owner}
        removeVolume={(id) => {
          if (base.specification.compose) return;
          setDraft((current) => {
            try {
              const spec = JSON.parse(current) as Schema["Specification"];
              return JSON.stringify(
                { ...spec, volumes: spec.volumes?.filter((v) => v.id !== id) },
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
      {blocker.status === "blocked" && (
        <section
          className={s.notice}
          role="alertdialog"
          aria-modal="false"
          aria-label="Unsaved changes"
        >
          <p>Leave this page and discard unsaved edits?</p>
          <div className={s.row}>
            <Button type="button" onClick={() => blocker.reset()}>
              Keep editing
            </Button>
            <Button type="button" onClick={() => blocker.proceed()}>
              Discard and leave
            </Button>
          </div>
        </section>
      )}
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
