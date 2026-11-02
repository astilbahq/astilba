import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Background,
  type Connection,
  Controls,
  type Node,
  type NodeChange,
  ReactFlow,
} from "@xyflow/react";
import { useContext, useEffect, useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { cx } from "../../styled-system/css";
import { client, result, type Schema } from "../api/client";
import {
  layoutOptions,
  publishLayout,
  type EnvironmentPath,
} from "../api/environment";
import { Button } from "../components/button";
import "./visual-editor.css";
import { Input } from "../components/input";
import { Label } from "../components/label";
import { Select } from "../components/select";
import * as s from "../styles";
import { ThemeContext } from "../theme";
import { ErrorNotice, Field, Form, text, useAction } from "../ui";
import { GeneratedDomain } from "./generated-domain";
import { ServiceCatalog } from "./service-catalog";
import { ServiceConnections } from "./service-outputs";
import * as v from "./visual-editor.styles";
import { RouteEditor } from "./visual-editor/route-editor";
import { ServiceProperties } from "./visual-editor/service-properties";
import "@xyflow/react/dist/style.css";

export type { EnvironmentPath } from "../api/environment";
type Spec = Schema["Specification"];
type App = Schema["ApplicationReference"];
const postgresImage =
  "postgres:18.4-alpine3.23@sha256:996d0920e4ff9df1fc19dacb904492f3c1ec0ec1cc338f0ad7123be7731c5f5e";

const postgres17Image =
  "postgres:17.10-alpine3.23@sha256:8189a1f6e40904781fc9e2612687877791d21679866db58b1de996b31fc312e4";

function VolumeNameField({ name }: { name: string }) {
  const id = useId();
  const [input, setInput] = useState({ name, value: name });
  if (input.name !== name) setInput({ name, value: name });
  return (
    <Label htmlFor={id}>
      Volume name
      <Input
        id={id}
        name="name"
        value={input.value}
        onChange={(event) => setInput({ name, value: event.target.value })}
        required
      />
    </Label>
  );
}

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
  const { theme } = useContext(ThemeContext);
  const { t } = useTranslation("environment");
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
  const queries = useQueryClient();
  const layout = useQuery(layoutOptions(path));
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
      data: {
        label: `${a.name} · ${a.kind ?? "web"}${(a.replicas ?? 1) > 1 ? ` · ${a.replicas} instances` : ""}`,
      },
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
    <section
      className={cx(s.stack, v.workspace)}
      aria-label="Visual configuration"
    >
      <div className={v.header}>
        <h3 className={v.heading}>Services and connections</h3>
        <div role="group" aria-label="Configuration view" className={v.views}>
          <Button
            type="button"
            variant="ghost"
            size="sm"
            className={v.view}
            aria-pressed={view === "canvas"}
            onClick={() => setView("canvas")}
          >
            Canvas
          </Button>
          <Button
            type="button"
            variant="ghost"
            size="sm"
            className={v.view}
            aria-pressed={view === "list"}
            onClick={() => setView("list")}
          >
            Service list
          </Button>
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
            fitViewOptions={{ maxZoom: 1 }}
            colorMode={theme}
            ariaLabelConfig={{
              "node.a11yDescription.default":
                "Press Enter to configure this service. Use the service list and forms to add connections without dragging.",
            }}
          >
            <Background />
            <Controls showInteractive={false} fitViewOptions={{ maxZoom: 1 }} />
          </ReactFlow>
        </div>
      ) : (
        <ul className={s.list}>
          {(spec.networks ?? []).map((network) => (
            <li key={network.id}>{network.name} · network</li>
          ))}
          {spec.applications.map((a) => (
            <li key={a.id}>
              <Button
                type="button"
                aria-pressed={selected === a.id}
                onClick={() => setSelected(a.id)}
              >
                {a.name}
              </Button>{" "}
              <span>{a.kind ?? "web"}</span>
            </li>
          ))}
        </ul>
      )}
      {write && (
        <div className={v.layout}>
          <Button
            type="button"
            size="sm"
            disabled={!layoutDirty || dirty || action.isPending}
            onClick={() =>
              action.run(async () => {
                const submitted = positions ?? {};
                const { queryKey } = layoutOptions(path);
                const previous = queries.getQueryState(queryKey);
                const saved = await result(
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
                await publishLayout(queries, path, saved, previous);
                setSavedPositions(JSON.stringify(submitted));
                setNotice("Layout saved. Running services were not changed.");
              })
            }
          >
            Save canvas layout
          </Button>
          <span className={s.muted}>
            {layoutDirty
              ? t(($) => $.configurationEditor.layoutUnsaved)
              : t(($) => $.configurationEditor.layoutSeparate)}
          </span>
        </div>
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
        <details className={s.workspaceUtility}>
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
            <Label>
              Service kind
              <Select name="kind">
                <option value="web">Web application</option>
                <option value="worker">Worker</option>
                <option value="postgres">PostgreSQL</option>
              </Select>
            </Label>
            <Field
              name="image"
              label="Container image (web or worker)"
              required={false}
            />
            <Label>
              Database password secret (PostgreSQL)
              <Select name="password">
                <option value="">Select a secret version</option>
                {secrets.data?.map((v) => (
                  <option key={v.id} value={v.id}>
                    {v.name} · {v.id.slice(0, 8)}
                  </option>
                ))}
              </Select>
            </Label>
            <Label>
              PostgreSQL version for a new database
              <Select name="postgresVersion" defaultValue="18">
                <option value="18">18.4</option>
                <option value="17">17.10</option>
              </Select>
            </Label>

            <small>
              Create the password in Secrets below before adding PostgreSQL. Its
              data volume is created automatically.
            </small>
            <Button type="submit">Stage new service</Button>
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
              <VolumeNameField name={volume.name} />
              <Button type="submit">Stage volume name</Button>
            </Form>
          )}
        </section>
      )}
      {app && (
        <ServiceProperties
          app={app}
          spec={spec}
          secrets={secrets.data}
          action={action}
          write={write}
          configure={configure}
          position={
            positions?.[app.id] ??
            nodes.find((n) => n.id === app.id)?.position ?? { x: 0, y: 0 }
          }
          onPositionChange={(position) =>
            setPositions({ ...positions, [app.id]: position })
          }
          update={update}
          stage={stage}
          onRemove={() => {
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
        />
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
            <Button type="button" onClick={() => setSelected(volume.id)}>
              {volume.name}
            </Button>
            {configure &&
              !spec.applications.some((a) =>
                a.mounts?.some((m) => m.volumeId === volume.id),
              ) && (
                <Button
                  type="button"
                  onClick={() =>
                    stage({
                      ...spec,
                      volumes: spec.volumes?.filter((v) => v.id !== volume.id),
                    })
                  }
                >
                  Remove volume from configuration; retain data
                </Button>
              )}
          </li>
        ))}
      </ul>
      {configure && (
        <details className={s.workspaceUtility}>
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
            <Button type="submit">Stage volume</Button>
          </Form>
        </details>
      )}
      <RouteEditor spec={spec} change={stage} write={configure} />
      {configure && <GeneratedDomain path={path} spec={spec} change={stage} />}
    </section>
  );
}
