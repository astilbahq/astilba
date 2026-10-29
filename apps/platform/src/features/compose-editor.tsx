import { useQuery } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { client, result, type Schema } from "../api/client";
import { Button } from "../components/button";
import { Label } from "../components/label";
import { Select } from "../components/select";
import { Textarea } from "../components/textarea";
import * as s from "../styles";
import { ErrorNotice, Field, Form, text, useAction } from "../ui";
import { CodeEditor } from "./code-editor";
import { type EnvironmentPath, VisualEditor } from "./visual-editor";

export function ComposeEditor({
  path,
  spec,
  change,
  write,
  dirty,
  onLayoutDirty,
  baseFiles,
}: {
  path: EnvironmentPath;
  spec: Schema["Specification"];
  change: (spec: Schema["Specification"]) => void;
  write: boolean;
  dirty: boolean;
  onLayoutDirty: (value: boolean) => void;
  baseFiles: Schema["ComposeFile"][];
}) {
  const source = spec.compose;
  const [file, setFile] = useState(source?.entrypoints[0] ?? "");
  const [line, setLine] = useState<{ line: number }>();
  const [graph, setGraph] = useState(spec);
  const [diagnostics, setDiagnostics] = useState<Schema["SourceDiagnostic"][]>(
    [],
  );
  const [permissions, setPermissions] = useState<string[]>([]);
  const [validating, setValidating] = useState(false),
    [failure, setFailure] = useState<unknown>();
  const [proposal, setProposal] = useState<Schema["ComposePreview"]>();
  const [selectedService, setService] = useState("");
  const savedFiles = baseFiles;
  const [proposalBase, setProposalBase] = useState("");
  const action = useAction();
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
  const serialized = JSON.stringify(spec);
  const serializedRef = useRef(serialized);
  serializedRef.current = serialized;
  const previewSequence = useRef(0);
  const { workspaceId, projectId, environmentId } = path;
  useEffect(() => {
    if (!write) {
      setGraph(JSON.parse(serialized) as Schema["Specification"]);
      setValidating(false);
      return;
    }
    let active = true;
    const controller = new AbortController();
    setValidating(true);
    const timer = setTimeout(() => {
      void result(
        client.POST(
          "/api/v1/workspaces/{workspaceId}/projects/{projectId}/environments/{environmentId}/compose/preview",
          {
            params: { path: { workspaceId, projectId, environmentId } },
            body: {
              specification: JSON.parse(serialized) as Schema["Specification"],
            },
            signal: controller.signal,
          },
        ),
      )
        .then((value) => {
          if (active) {
            setDiagnostics(value.diagnostics);
            setPermissions(value.permissions);
            if (value.specification) setGraph(value.specification);
            setFailure(undefined);
          }
        })
        .catch((error) => {
          if (active) setFailure(error);
        })
        .finally(() => {
          if (active) setValidating(false);
        });
    }, 300);
    return () => {
      active = false;
      clearTimeout(timer);
      controller.abort();
    };
  }, [serialized, workspaceId, projectId, environmentId, write]);
  if (!source) return null;
  const current =
    source.files.find((value) => value.path === file) ?? source.files[0];
  const updateFile = (content: string) => {
    change({
      ...spec,
      compose: {
        ...source,
        files: source.files.map((value) =>
          value.path === current?.path ? { ...value, content } : value,
        ),
      },
    });
    setProposal(undefined);
  };
  const changed = source.files.filter(
    (value) =>
      savedFiles.find((old) => old.path === value.path)?.content !==
      value.content,
  );
  const removed = savedFiles.filter(
    (old) => !source.files.some((value) => value.path === old.path),
  );
  return (
    <section className={s.stack} aria-label="Compose configuration">
      <p>
        Compose files are authoritative. Saving a revision does not deploy it.
        Canvas movement only changes layout.
      </p>
      {source.origin && (
        <p>
          Source commit: <code>{source.origin.commit}</code>. File changes stay
          in this draft; provider submission is available in the Git workflow.
        </p>
      )}
      <ErrorNotice error={failure ?? action.error} />
      <fieldset className={s.row} aria-label="Source files">
        {source.files.map((value) => (
          <Button
            type="button"
            key={value.path}
            aria-pressed={current?.path === value.path}
            onClick={() => {
              setFile(value.path);
              setLine(undefined);
            }}
          >
            {value.path}
          </Button>
        ))}
      </fieldset>
      {current && (
        <>
          <h3>{current.path}</h3>
          <CodeEditor
            key={current.path}
            label={`Source ${current.path}`}
            value={current.content}
            change={updateFile}
            readOnly={!write}
            line={line}
          />
          {write && source.files.length > 1 && (
            <Button
              type="button"
              onClick={() => {
                if (
                  window.confirm(
                    `Remove ${current.path} from the source bundle?`,
                  )
                ) {
                  change({
                    ...spec,
                    compose: {
                      ...source,
                      files: source.files.filter(
                        (value) => value.path !== current.path,
                      ),
                      entrypoints: source.entrypoints.filter(
                        (value) => value !== current.path,
                      ),
                    },
                  });
                  setFile(source.entrypoints[0] ?? "");
                }
              }}
            >
              Remove selected file
            </Button>
          )}
        </>
      )}
      {write && (
        <details>
          <summary>Bundle files and entrypoints</summary>
          <Label>
            Compose entrypoints (one per line)
            <Textarea
              value={source.entrypoints.join("\n")}
              onChange={(event) =>
                change({
                  ...spec,
                  compose: {
                    ...source,
                    entrypoints: event.target.value.split("\n"),
                  },
                })
              }
            />
          </Label>
          <Form
            action={action}
            label="Add source file"
            onSubmit={(data, form) =>
              action.run(async () => {
                const name = text(data, "path");
                if (source.files.some((value) => value.path === name))
                  throw new Error("This file already exists.");
                change({
                  ...spec,
                  compose: {
                    ...source,
                    files: [...source.files, { path: name, content: "" }],
                  },
                });
                setFile(name);
                form.reset();
              })
            }
          >
            <Field name="path" label="New relative file path" />
            <Button type="submit">Add file</Button>
          </Form>
        </details>
      )}
      <details>
        <summary>Interpolation values</summary>
        <ul>
          {spec.variables.map((v) => (
            <li key={v.name}>
              {v.name}:{" "}
              {v.secretVersionId ? "Secret version selected" : v.value}
              {write && (
                <Button
                  type="button"
                  onClick={() =>
                    change({
                      ...spec,
                      variables: spec.variables.filter(
                        (x) => x.name !== v.name,
                      ),
                    })
                  }
                >
                  Remove {v.name}
                </Button>
              )}
            </li>
          ))}
        </ul>
        {write && (
          <Form
            action={action}
            label="Add interpolation value"
            onSubmit={(data, form) =>
              action.run(async () => {
                const name = text(data, "name"),
                  secret = text(data, "secret");
                change({
                  ...spec,
                  variables: [
                    ...spec.variables.filter((v) => v.name !== name),
                    {
                      name,
                      ...(secret
                        ? { secretVersionId: secret }
                        : { value: text(data, "value") }),
                    },
                  ],
                });
                form.reset();
              })
            }
          >
            <Field name="name" label="Interpolation name" />
            <Field
              name="value"
              label="Plain interpolation value"
              required={false}
            />
            <Label>
              Or use secret version
              <Select name="secret">
                <option value="">Use plain value</option>
                {secrets.data?.map((secret) => (
                  <option key={secret.id} value={secret.id}>
                    {secret.name} · {secret.id.slice(0, 8)}
                  </option>
                ))}
              </Select>
            </Label>
            <Button type="submit">Stage interpolation value</Button>
          </Form>
        )}
      </details>
      <div role="status">
        {validating
          ? "Validating source…"
          : diagnostics.length
            ? "Source needs attention; graph shows the last valid source."
            : failure
              ? "Source validation is unavailable."
              : "Source validated."}
      </div>
      {diagnostics.length > 0 && (
        <ul className={s.list}>
          {diagnostics.map((item) => (
            <li
              key={`${item.path}-${item.line}-${item.column}-${item.message}`}
            >
              <Button
                type="button"
                onClick={() => {
                  setFile(item.path);
                  setLine({ line: item.line });
                }}
              >
                {item.path}:{item.line}:{item.column}
              </Button>{" "}
              {item.message}
            </li>
          ))}
        </ul>
      )}
      {permissions.length > 0 && (
        <section aria-label="Required host permissions">
          <h3>Owner review required</h3>
          <ul>
            {permissions.map((value) => (
              <li key={value}>{value}</li>
            ))}
          </ul>
        </section>
      )}
      <details>
        <summary>Staged file diff ({changed.length + removed.length})</summary>
        {changed.map((value) => (
          <section key={value.path}>
            <h3>{value.path}</h3>
            <div className={s.columns}>
              <div>
                <strong>Saved source</strong>
                <pre className={s.log}>
                  {savedFiles.find((old) => old.path === value.path)?.content ??
                    "New file"}
                </pre>
              </div>
              <div>
                <strong>Draft source</strong>
                <pre className={s.log}>{value.content}</pre>
              </div>
            </div>
          </section>
        ))}
        {removed.map((value) => (
          <p key={value.path}>Remove {value.path}</p>
        ))}
      </details>
      <p>
        Removing or renaming services and volumes changes their runtime
        identities. Existing data is retained; backup policies must keep
        pointing at the intended volume.
      </p>
      <VisualEditor
        path={path}
        spec={graph}
        change={() => {}}
        write={write}
        dirty={dirty || validating || diagnostics.length > 0}
        onLayoutDirty={onLayoutDirty}
        onSelectService={setService}
      />
      {write && (
        <section className={s.panel} aria-label="Edit image in source">
          <h3>Edit an image from the graph</h3>
          <p>
            Select a service on the canvas or list, then preview its source
            edit. Anchors and ambiguous ownership must be edited in the file.
          </p>
          <Form
            action={action}
            label="Preview source image edit"
            onSubmit={(data) => {
              const sequence = ++previewSequence.current;
              setProposal(undefined);
              action.run(async () => {
                const value = await result(
                  client.POST(
                    "/api/v1/workspaces/{workspaceId}/projects/{projectId}/environments/{environmentId}/compose/preview",
                    {
                      params: { path },
                      body: {
                        specification: spec,
                        edit: {
                          service: selectedService,
                          image: text(data, "image"),
                        },
                      },
                    },
                  ),
                );
                if (
                  sequence === previewSequence.current &&
                  serialized === serializedRef.current
                ) {
                  setProposal(value);
                  setProposalBase(serialized);
                }
              });
            }}
          >
            <Label>
              Source service
              <Select
                required
                value={selectedService}
                onChange={(event) => setService(event.target.value)}
              >
                <option value="">Choose service</option>
                {graph.applications.map((app) => (
                  <option key={app.id} value={app.name}>
                    {app.name}
                  </option>
                ))}
              </Select>
            </Label>
            <Field name="image" label="New image reference" />
            <Button
              type="submit"
              disabled={validating || diagnostics.length > 0}
            >
              Preview image edit
            </Button>
          </Form>
          {proposal && (
            <>
              <ul>
                {proposal.diagnostics.map((item) => (
                  <li key={`${item.path}-${item.line}-${item.message}`}>
                    {item.path}:{item.line}: {item.message}
                  </li>
                ))}
              </ul>
              {proposal.changes.map((value) => (
                <section key={value.path}>
                  <h4>{value.path}</h4>
                  <div className={s.columns}>
                    <pre className={s.log}>{value.before}</pre>
                    <pre className={s.log}>{value.after}</pre>
                  </div>
                </section>
              ))}
              {proposal.specification && proposal.diagnostics.length === 0 && (
                <Button
                  type="button"
                  disabled={proposalBase !== serialized}
                  onClick={() => {
                    if (proposal.specification && proposalBase === serialized)
                      change(proposal.specification);
                    setProposal(undefined);
                  }}
                >
                  Stage this source edit
                </Button>
              )}
            </>
          )}
        </section>
      )}
    </section>
  );
}
