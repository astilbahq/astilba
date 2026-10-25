import { useQuery } from "@tanstack/react-query";
import {
  Background,
  type Connection,
  Controls,
  type Node,
  type NodeChange,
  ReactFlow,
} from "@xyflow/react";
import { useContext, useEffect, useState } from "react";
import { client, result, type Schema } from "../api/client";
import * as s from "../styles";
import { ThemeContext } from "../theme";
import { ErrorNotice, Field, Form, text, useAction } from "../ui";
import { ServiceCatalog } from "./service-catalog";
import { OutputEditor, ServiceConnections } from "./service-outputs";
import "@xyflow/react/dist/style.css";

export type EnvironmentPath = {
  workspaceId: string;
  projectId: string;
  environmentId: string;
};
type Spec = Schema["Specification"];
type App = Schema["ApplicationReference"];
const postgresImage =
  "postgres:18.4-alpine3.23@sha256:996d0920e4ff9df1fc19dacb904492f3c1ec0ec1cc338f0ad7123be7731c5f5e";

const postgres17Image =
  "postgres:17.10-alpine3.23@sha256:8189a1f6e40904781fc9e2612687877791d21679866db58b1de996b31fc312e4";

export function VisualEditor({
  path,
  spec,
  change,
  write,
  dirty,
  onLayoutDirty,
  onSelectService,
}: {
  path: EnvironmentPath;
  spec: Spec;
  change: (v: Spec) => void;
  write: boolean;
  dirty: boolean;
  onLayoutDirty: (v: boolean) => void;
  onSelectService?: (name: string) => void;
}) {
  const theme = useContext(ThemeContext);
  const configure = write && !spec.compose;
  const [selected, setSelected] = useState<string>();
  const [view, setView] = useState<"canvas" | "list">("canvas");
  const [positions, setPositions] = useState<Schema["Layout"]["positions"]>();
  const [savedPositions, setSavedPositions] = useState("");
  const [notice, setNotice] = useState("");
  const [connection, setConnection] = useState<{
    source: string;
    target: string;
  }>();
  const action = useAction();
  const layout = useQuery({
    queryKey: ["layout", path],
    queryFn: () =>
      result(
        client.GET(
          "/api/v1/workspaces/{workspaceId}/projects/{projectId}/environments/{environmentId}/layout",
          { params: { path } },
        ),
      ),
  });
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
  useEffect(() => {
    if (layout.data && positions === undefined) {
      setPositions(layout.data.positions);
      setSavedPositions(JSON.stringify(layout.data.positions));
    }
  }, [layout.data, positions]);
  const layoutDirty =
    positions !== undefined && JSON.stringify(positions) !== savedPositions;
  useEffect(() => onLayoutDirty(layoutDirty), [layoutDirty, onLayoutDirty]);
  const stage = (v: Spec) => {
    change(v);
    setNotice(
      "Changes staged. Save and review a deployment plan to apply them.",
    );
  };
  const update = (app: App) =>
    stage({
      ...spec,
      applications: spec.applications.map((a) => (a.id === app.id ? app : a)),
    });
  const app = spec.applications.find((a) => a.id === selected);
  const volume = spec.volumes?.find((v) => v.id === selected);
  useEffect(() => {
    if (app) onSelectService?.(app.name);
  }, [app, onSelectService]);
  const connect = (next: Connection) => {
    const source = spec.applications.find((a) => a.id === next.source);
    const target = spec.applications.find((a) => a.id === next.target);
    if (
      !source ||
      !target ||
      source.id === target.id ||
      target.kind === "postgres"
    ) {
      setNotice(
        "Choose a source service and a different web service or worker to receive its output.",
      );
      return;
    }
    setConnection({ source: source.id, target: target.id });
    setNotice(
      "Choose the output and receiving variable in Connections. No binding has been staged yet.",
    );
  };
  const nodes: Node[] = [
    ...spec.applications.map((a, i) => ({
      id: a.id,
      position: positions?.[a.id] ?? {
        x: (i % 3) * 250,
        y: Math.floor(i / 3) * 140,
      },
      data: { label: `${a.name} · ${a.kind ?? "web"}` },
      selected: a.id === selected,
      ariaLabel: `Configure ${a.name}`,
      className: "visual-node",
    })),
    ...(spec.networks ?? []).map((network, i) => ({
      id: network.id,
      position: positions?.[network.id] ?? { x: i * 250, y: -140 },
      data: { label: `${network.name} · network` },
      selected: network.id === selected,
      ariaLabel: `${network.name} network`,
      className: "visual-node",
    })),
    ...(spec.volumes ?? []).map((v, i) => ({
      id: v.id,
      position: positions?.[v.id] ?? {
        x: i * 250,
        y: Math.ceil(spec.applications.length / 3) * 140,
      },
      data: { label: `${v.name} · volume` },
      selected: v.id === selected,
      ariaLabel: `Configure ${v.name} volume`,
      className: "visual-node",
    })),
  ];
  const move = (changes: NodeChange[]) => {
    const next = { ...positions };
    let moved = false;
    for (const c of changes) {
      if (c.type === "position" && c.position) {
        next[c.id] = c.position;
        moved = true;
      }
      if (c.type === "select" && c.selected) setSelected(c.id);
    }
    if (moved) setPositions(next);
  };
  return (
    <section className={s.stack} aria-label="Visual configuration">
      <div className={s.spread}>
        <h3>Services and connections</h3>
        <div className={s.row}>
          <button
            type="button"
            aria-pressed={view === "canvas"}
            onClick={() => setView("canvas")}
          >
            Canvas
          </button>
          <button
            type="button"
            aria-pressed={view === "list"}
            onClick={() => setView("list")}
          >
            Service list
          </button>
        </div>
      </div>
      <p className={s.muted}>
        Moving a card only changes its layout. Configuration is staged until you
        save, review and apply a plan.
      </p>
      <ErrorNotice error={action.error ?? layout.error ?? secrets.error} />
      {notice && <p role="status">{notice}</p>}
      {view === "canvas" ? (
        <div className="visual-canvas">
          <ReactFlow
            nodes={nodes}
            edges={[
              ...spec.applications.flatMap((app) =>
                (app.networks ?? []).map((id) => ({
                  id: `network-${id}-${app.id}`,
                  source: id,
                  target: app.id,
                  label: "Network",
                  ariaLabel: `Network for ${app.name}`,
                })),
              ),
              ...(spec.bindings ?? []).map((b) => ({
                id: b.id,
                source: b.sourceId,
                target: b.targetId,
                label: b.variable,
                ariaLabel: `${b.variable} binding`,
              })),
              ...spec.applications.flatMap((a) =>
                (a.mounts ?? []).map((m) => ({
                  id: `mount-${a.id}-${m.volumeId}`,
                  source: m.volumeId,
                  target: a.id,
                  label: m.readOnly ? "Read-only volume" : "Data volume",
                  ariaLabel: `Volume attached to ${a.name}`,
                })),
              ),
            ]}
            onNodesChange={move}
            onNodeClick={(_, node) => setSelected(node.id)}
            onConnect={configure ? connect : undefined}
            nodesDraggable={write}
            nodesConnectable={configure}
            deleteKeyCode={null}
            fitView
            colorMode={theme}
            ariaLabelConfig={{
              "node.a11yDescription.default":
                "Press Enter to configure this service. Use the service list and forms to add connections without dragging.",
            }}
          >
            <Background />
            <Controls showInteractive={false} />
          </ReactFlow>
        </div>
      ) : (
        <ul className={s.list}>
          {(spec.networks ?? []).map((network) => (
            <li key={network.id}>{network.name} · network</li>
          ))}
          {spec.applications.map((a) => (
            <li key={a.id}>
              <button
                type="button"
                aria-pressed={selected === a.id}
                onClick={() => setSelected(a.id)}
              >
                {a.name}
              </button>{" "}
              <span>{a.kind ?? "web"}</span>
            </li>
          ))}
        </ul>
      )}
      {write && (
        <button
          type="button"
          disabled={!layoutDirty || dirty || action.isPending}
          onClick={() =>
            action.run(async () => {
              const submitted = positions ?? {};
              await result(
                client.PUT(
                  "/api/v1/workspaces/{workspaceId}/projects/{projectId}/environments/{environmentId}/layout",
                  {
                    params: { path },
                    body: {
                      positions: Object.fromEntries(
                        Object.entries(submitted).filter(([id]) =>
                          [
                            ...spec.applications,
                            ...(spec.volumes ?? []),
                            ...(spec.routes ?? []),
                            ...(spec.networks ?? []),
                          ].some((resource) => resource.id === id),
                        ),
                      ),
                    },
                  },
                ),
              );
              setSavedPositions(JSON.stringify(submitted));
              setNotice("Layout saved. Running services were not changed.");
            })
          }
        >
          Save canvas layout
        </button>
      )}
      {dirty && layoutDirty && (
        <small>
          Save the configuration revision before saving positions for new
          services.
        </small>
      )}
      {configure && (
        <ServiceCatalog
          path={path}
          spec={spec}
          secrets={secrets.data ?? []}
          stage={stage}
        />
      )}
      {configure && (
        <details>
          <summary>Add a service</summary>
          <Form
            action={action}
            label="Add service"
            onSubmit={(d, form) => {
              const kind = text(d, "kind") as "web" | "worker" | "postgres";
              const id = crypto.randomUUID();
              let next: Spec = {
                ...spec,
                applications: [...spec.applications],
              };
              const service: App = {
                id,
                name: text(d, "name"),
                kind,
                source: {
                  kind: "image",
                  image:
                    kind === "postgres"
                      ? text(d, "postgresVersion") === "17"
                        ? postgres17Image
                        : postgresImage
                      : text(d, "image"),
                },
              };
              if (kind === "postgres") {
                const volumeId = crypto.randomUUID();
                service.postgres = {
                  database: "app",
                  username: "app",
                  passwordSecretVersionId: text(d, "password"),
                };
                service.mounts = [
                  {
                    volumeId,
                    target:
                      text(d, "postgresVersion") === "17"
                        ? "/var/lib/postgresql/data"
                        : "/var/lib/postgresql",
                    readOnly: false,
                  },
                ];
                next = {
                  ...next,
                  volumes: [
                    ...(next.volumes ?? []),
                    { id: volumeId, name: `${service.name} data` },
                  ],
                };
              }
              next.applications.push(service);
              stage(next);
              setSelected(id);
              form.reset();
            }}
          >
            <Field name="name" label="Service name" />
            <label>
              Service kind
              <select name="kind">
                <option value="web">Web application</option>
                <option value="worker">Worker</option>
                <option value="postgres">PostgreSQL</option>
              </select>
            </label>
            <Field
              name="image"
              label="Container image (web or worker)"
              required={false}
            />
            <label>
              Database password secret (PostgreSQL)
              <select name="password">
                <option value="">Select a secret version</option>
                {secrets.data?.map((v) => (
                  <option key={v.id} value={v.id}>
                    {v.name} · {v.id.slice(0, 8)}
                  </option>
                ))}
              </select>
            </label>
            <label>
              PostgreSQL version for a new database
              <select name="postgresVersion" defaultValue="18">
                <option value="18">18.4</option>
                <option value="17">17.10</option>
              </select>
            </label>

            <small>
              Create the password in Secrets below before adding PostgreSQL. Its
              data volume is created automatically.
            </small>
            <button type="submit">Stage new service</button>
          </Form>
        </details>
      )}
      {volume && (
        <section
          className={s.panel}
          aria-label={`Properties for ${volume.name}`}
        >
          <h3>Data volume</h3>
          <p>
            Removing this volume from configuration retains its data. Permanent
            deletion is available in the data controls below.
          </p>
          {configure && (
            <Form
              key={volume.id}
              action={action}
              label="Volume properties"
              onSubmit={(d) =>
                stage({
                  ...spec,
                  volumes: spec.volumes?.map((v) =>
                    v.id === volume.id ? { ...v, name: text(d, "name") } : v,
                  ),
                })
              }
            >
              <label>
                Volume name
                <input name="name" defaultValue={volume.name} required />
              </label>
              <button type="submit">Stage volume name</button>
            </Form>
          )}
        </section>
      )}
      {app && (
        <section className={s.panel} aria-label={`Properties for ${app.name}`}>
          <h3>{app.name}</h3>
          {write && (
            <fieldset className={s.row}>
              <legend>Canvas position</legend>
              {(["x", "y"] as const).map((axis) => (
                <label key={axis}>
                  Position {axis}
                  <input
                    type="number"
                    value={
                      positions?.[app.id]?.[axis] ??
                      nodes.find((n) => n.id === app.id)?.position[axis] ??
                      0
                    }
                    onChange={(e) =>
                      setPositions({
                        ...positions,
                        [app.id]: {
                          ...(positions?.[app.id] ??
                            nodes.find((n) => n.id === app.id)?.position ?? {
                              x: 0,
                              y: 0,
                            }),
                          [axis]: Number(e.target.value),
                        },
                      })
                    }
                  />
                </label>
              ))}
            </fieldset>
          )}
          {configure ? (
            <>
              <Form
                key={app.id}
                action={action}
                label="Service properties"
                onSubmit={(d) => {
                  const next = { ...app, name: text(d, "name") };
                  if (app.kind === "postgres" && app.postgres)
                    next.postgres = {
                      ...app.postgres,
                      database: text(d, "database"),
                      username: text(d, "username"),
                      passwordSecretVersionId: text(d, "password"),
                    };
                  else {
                    if (
                      app.source.kind === "image" &&
                      text(d, "image") !== app.source.image
                    )
                      next.source = { kind: "image", image: text(d, "image") };
                    next.command = text(d, "command")
                      .split("\n")
                      .filter(Boolean);
                    next.health = text(d, "healthPort")
                      ? {
                          port: Number(text(d, "healthPort")),
                          path: text(d, "healthPath") || "/",
                        }
                      : undefined;
                  }
                  update(next);
                }}
              >
                <label>
                  Service name
                  <input
                    name="name"
                    value={app.name}
                    onChange={(e) => update({ ...app, name: e.target.value })}
                    required
                  />
                </label>
                {app.kind === "postgres" ? (
                  <>
                    <label>
                      Database name
                      <input
                        name="database"
                        value={app.postgres?.database ?? ""}
                        onChange={(e) =>
                          app.postgres &&
                          update({
                            ...app,
                            postgres: {
                              ...app.postgres,
                              database: e.target.value,
                            },
                          })
                        }
                        required
                      />
                    </label>
                    <label>
                      Database user
                      <input
                        name="username"
                        value={app.postgres?.username ?? ""}
                        onChange={(e) =>
                          app.postgres &&
                          update({
                            ...app,
                            postgres: {
                              ...app.postgres,
                              username: e.target.value,
                            },
                          })
                        }
                        required
                      />
                    </label>
                    <label>
                      Password secret version
                      <select
                        name="password"
                        value={app.postgres?.passwordSecretVersionId ?? ""}
                        onChange={(e) =>
                          app.postgres &&
                          update({
                            ...app,
                            postgres: {
                              ...app.postgres,
                              passwordSecretVersionId: e.target.value,
                            },
                          })
                        }
                        required
                      >
                        {secrets.data?.map((v) => (
                          <option key={v.id} value={v.id}>
                            {v.name} · {v.id.slice(0, 8)}
                          </option>
                        ))}
                      </select>
                    </label>
                    <small>
                      Changing database initialization credentials does not
                      alter credentials in an existing data volume.
                    </small>
                  </>
                ) : (
                  <>
                    <label>
                      Container image
                      <input
                        name="image"
                        value={app.source.image ?? ""}
                        disabled={app.source.kind === "git"}
                        onChange={(e) =>
                          update({
                            ...app,
                            source:
                              e.target.value === app.source.image
                                ? app.source
                                : { kind: "image", image: e.target.value },
                          })
                        }
                        required
                      />
                    </label>
                    <label>
                      Command arguments (one per line)
                      <textarea
                        key={app.command?.join("\n") ?? ""}
                        name="command"
                        defaultValue={app.command?.join("\n") ?? ""}
                        onBlur={(e) =>
                          update({
                            ...app,
                            command: e.target.value.split("\n").filter(Boolean),
                          })
                        }
                      />
                    </label>
                    {app.kind !== "worker" && (
                      <>
                        <label>
                          Readiness HTTP port
                          <input
                            name="healthPort"
                            type="number"
                            min={1}
                            max={65535}
                            value={app.health?.port ?? ""}
                            onChange={(e) =>
                              update({
                                ...app,
                                health: e.target.value
                                  ? {
                                      port: Number(e.target.value),
                                      path: app.health?.path ?? "/",
                                    }
                                  : undefined,
                              })
                            }
                          />
                        </label>
                        <label>
                          Readiness path
                          <input
                            name="healthPath"
                            value={app.health?.path ?? "/"}
                            onChange={(e) =>
                              update({
                                ...app,
                                health: {
                                  port: app.health?.port ?? 8080,
                                  path: e.target.value,
                                },
                              })
                            }
                          />
                        </label>
                      </>
                    )}
                  </>
                )}
                <button type="submit">Stage service changes</button>
              </Form>
              <OutputEditor
                key={`${app.id}-outputs`}
                app={app}
                spec={spec}
                stage={stage}
              />
              <details>
                <summary>Variables</summary>
                <ul className={s.list}>
                  {app.variables?.map((v) => (
                    <li key={v.name}>
                      {v.name}:{" "}
                      {v.secretVersionId ? "Secret version selected" : v.value}
                      <button
                        type="button"
                        onClick={() =>
                          update({
                            ...app,
                            variables: app.variables?.filter(
                              (x) => x.name !== v.name,
                            ),
                          })
                        }
                      >
                        Remove {v.name}
                      </button>
                    </li>
                  ))}
                </ul>
                <Form
                  action={action}
                  label="Add service variable"
                  onSubmit={(d, form) => {
                    const secret = text(d, "secret");
                    const variable = {
                      name: text(d, "name"),
                      ...(secret
                        ? { secretVersionId: secret }
                        : { value: text(d, "value") }),
                    };
                    update({
                      ...app,
                      variables: [
                        ...(app.variables ?? []).filter(
                          (v) => v.name !== variable.name,
                        ),
                        variable,
                      ],
                    });
                    form.reset();
                  }}
                >
                  <Field name="name" label="Variable name" />
                  <Field name="value" label="Plain value" required={false} />
                  <label>
                    Or use secret version
                    <select name="secret">
                      <option value="">Use plain value</option>
                      {secrets.data?.map((v) => (
                        <option key={v.id} value={v.id}>
                          {v.name} · {v.id.slice(0, 8)}
                        </option>
                      ))}
                    </select>
                  </label>
                  <button type="submit">Stage variable</button>
                </Form>
              </details>
              {app.kind !== "postgres" && (
                <details>
                  <summary>Storage and readiness</summary>
                  <ul>
                    {app.mounts?.map((m) => (
                      <li key={m.volumeId}>
                        {spec.volumes?.find((v) => v.id === m.volumeId)?.name}:{" "}
                        {m.target}
                        <button
                          type="button"
                          onClick={() =>
                            update({
                              ...app,
                              mounts: app.mounts?.filter(
                                (x) => x.volumeId !== m.volumeId,
                              ),
                            })
                          }
                        >
                          Detach volume
                        </button>
                      </li>
                    ))}
                  </ul>
                  <Form
                    action={action}
                    label="Attach volume"
                    onSubmit={(d) =>
                      update({
                        ...app,
                        mounts: [
                          ...(app.mounts ?? []),
                          {
                            volumeId: text(d, "volume"),
                            target: text(d, "target"),
                            readOnly: d.has("readOnly"),
                          },
                        ],
                      })
                    }
                  >
                    <label>
                      Volume
                      <select name="volume" required>
                        {spec.volumes?.map((v) => (
                          <option key={v.id} value={v.id}>
                            {v.name}
                          </option>
                        ))}
                      </select>
                    </label>
                    <Field name="target" label="Container mount path" />
                    <label className={s.row}>
                      <input type="checkbox" name="readOnly" />
                      Read only
                    </label>
                    <button type="submit">Stage volume attachment</button>
                  </Form>
                </details>
              )}
              <details>
                <summary>Readiness dependencies</summary>
                <Form
                  key={`${app.id}-dependencies-${app.dependsOn?.join()}`}
                  action={action}
                  label="Readiness dependencies"
                  onSubmit={(d) =>
                    update({
                      ...app,
                      dependsOn: d.getAll("dependency").map(String),
                    })
                  }
                >
                  {spec.applications
                    .filter(
                      (a) =>
                        a.id !== app.id && (a.kind === "postgres" || a.health),
                    )
                    .map((a) => (
                      <label className={s.row} key={a.id}>
                        <input
                          type="checkbox"
                          name="dependency"
                          value={a.id}
                          defaultChecked={app.dependsOn?.includes(a.id)}
                        />
                        {a.name}
                      </label>
                    ))}
                  <button type="submit">Stage readiness dependencies</button>
                </Form>
              </details>
              <button
                type="button"
                onClick={() => {
                  stage({
                    ...spec,
                    applications: spec.applications
                      .filter((a) => a.id !== app.id)
                      .map((a) => ({
                        ...a,
                        dependsOn: a.dependsOn?.filter((id) => id !== app.id),
                      })),
                    bindings: spec.bindings?.filter(
                      (b) => b.sourceId !== app.id && b.targetId !== app.id,
                    ),
                    routes: spec.routes?.filter((r) => r.targetId !== app.id),
                  });
                  setSelected(undefined);
                }}
              >
                Remove service; retain data volumes
              </button>
            </>
          ) : (
            <p className={s.muted}>
              Image: {app.source.image}.{" "}
              {spec.compose
                ? "Edit configuration in the source files or preview an image edit below."
                : "Configuration is read only."}
            </p>
          )}
        </section>
      )}
      <ServiceConnections
        spec={spec}
        stage={stage}
        write={configure}
        connection={connection}
      />
      <ul className={s.list}>
        {spec.volumes?.map((volume) => (
          <li key={volume.id}>
            <button type="button" onClick={() => setSelected(volume.id)}>
              {volume.name}
            </button>
            {configure &&
              !spec.applications.some((a) =>
                a.mounts?.some((m) => m.volumeId === volume.id),
              ) && (
                <button
                  type="button"
                  onClick={() =>
                    stage({
                      ...spec,
                      volumes: spec.volumes?.filter((v) => v.id !== volume.id),
                    })
                  }
                >
                  Remove volume from configuration; retain data
                </button>
              )}
          </li>
        ))}
      </ul>
      {configure && (
        <details>
          <summary>Add a data volume</summary>
          <Form
            action={action}
            onSubmit={(d, form) => {
              stage({
                ...spec,
                volumes: [
                  ...(spec.volumes ?? []),
                  { id: crypto.randomUUID(), name: text(d, "name") },
                ],
              });
              form.reset();
            }}
          >
            <Field name="name" label="Volume name" />
            <button type="submit">Stage volume</button>
          </Form>
        </details>
      )}
      <RouteEditor spec={spec} change={stage} write={configure} />
    </section>
  );
}
function RouteEditor({
  spec,
  change,
  write,
}: {
  spec: Spec;
  change: (v: Spec) => void;
  write: boolean;
}) {
  const action = useAction();
  const [edit, setEdit] = useState<string>();
  const route = spec.routes?.find((r) => r.id === edit);
  return (
    <details>
      <summary>HTTP and TCP routes</summary>
      <ul className={s.list}>
        {spec.routes?.map((r) => (
          <li key={r.id}>
            {r.domain} · {r.protocol.toUpperCase()} · {r.tls}
            {write && (
              <>
                <button type="button" onClick={() => setEdit(r.id)}>
                  Edit route
                </button>
                <button
                  type="button"
                  onClick={() =>
                    change({
                      ...spec,
                      routes: spec.routes?.filter((x) => x.id !== r.id),
                    })
                  }
                >
                  Remove route
                </button>
              </>
            )}
          </li>
        ))}
      </ul>
      {write && (
        <Form
          key={edit ?? "new"}
          action={action}
          label="Route properties"
          onSubmit={(d, form) => {
            const protocol = text(d, "protocol") as "http" | "tcp";
            const headers = Object.fromEntries(
              text(d, "headers")
                .split("\n")
                .filter(Boolean)
                .map((line) => {
                  const i = line.indexOf(":");
                  return [line.slice(0, i).trim(), line.slice(i + 1).trim()];
                }),
            );
            const next: Schema["Route"] = {
              id: edit ?? crypto.randomUUID(),
              targetId: text(d, "target"),
              domain: text(d, "domain"),
              port: Number(text(d, "port")),
              protocol,
              listenPort:
                protocol === "tcp" ? Number(text(d, "listenPort")) : 0,
              tls: text(d, "tls") as Schema["Route"]["tls"],
              allowCidrs: text(d, "cidrs")
                .split(/[\n,]/)
                .map((v) => v.trim())
                .filter(Boolean),
              ...(protocol === "http"
                ? {
                    advanced: {
                      responseHeaders: headers,
                      stripPrefix: text(d, "prefix"),
                      requestsPerSecond: Number(text(d, "rate") || 0),
                    },
                  }
                : {}),
            };
            change({
              ...spec,
              routes: [
                ...(spec.routes ?? []).filter((r) => r.id !== edit),
                next,
              ],
            });
            setEdit(undefined);
            form.reset();
          }}
        >
          <label>
            Target service
            <select name="target" required defaultValue={route?.targetId}>
              {spec.applications
                .filter((a) => a.kind !== "worker")
                .map((a) => (
                  <option key={a.id} value={a.id}>
                    {a.name}
                  </option>
                ))}
            </select>
          </label>
          <label>
            Protocol
            <select name="protocol" defaultValue={route?.protocol ?? "http"}>
              <option value="http">HTTP</option>
              <option value="tcp">TCP</option>
            </select>
          </label>
          <label>
            Domain
            <input name="domain" defaultValue={route?.domain} required />
          </label>
          <label>
            Container port
            <input
              name="port"
              type="number"
              min={1}
              max={65535}
              defaultValue={route?.port ?? 8080}
              required
            />
          </label>
          <label>
            Host port (TCP only)
            <input
              name="listenPort"
              type="number"
              min={1024}
              max={65535}
              defaultValue={route?.listenPort || 15432}
            />
          </label>
          <label>
            TLS policy
            <select name="tls" defaultValue={route?.tls ?? "managed"}>
              <option value="managed">Managed certificate (HTTP)</option>
              <option value="passthrough">Pass through TLS (TCP)</option>
              <option value="off">Unencrypted</option>
            </select>
          </label>
          <label>
            Allowed client networks (CIDRs, required for TCP)
            <textarea
              name="cidrs"
              defaultValue={route?.allowCidrs.join("\n") ?? ""}
            />
          </label>
          <details>
            <summary>Advanced HTTP settings</summary>
            <label>
              Strip path prefix
              <input
                name="prefix"
                defaultValue={route?.advanced?.stripPrefix ?? ""}
              />
            </label>
            <label>
              Requests per second (0 disables limit)
              <input
                name="rate"
                type="number"
                min={0}
                max={10000}
                defaultValue={route?.advanced?.requestsPerSecond ?? 0}
              />
            </label>
            <label>
              Response headers (one Name: Value per line)
              <textarea
                name="headers"
                defaultValue={Object.entries(
                  route?.advanced?.responseHeaders ?? {},
                )
                  .map(([k, v]) => `${k}: ${v}`)
                  .join("\n")}
              />
            </label>
            <small>
              Only X-* headers, Content-Security-Policy and Referrer-Policy are
              accepted. Host files, templates and provider references are
              unavailable.
            </small>
          </details>
          <button type="submit">Stage route</button>
          {edit && (
            <button type="button" onClick={() => setEdit(undefined)}>
              Cancel route edit
            </button>
          )}
        </Form>
      )}
    </details>
  );
}
