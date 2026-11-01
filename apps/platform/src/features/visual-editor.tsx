import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Background,
  type Connection,
  Controls,
  type Node,
  type NodeChange,
  Position,
  ReactFlow,
} from "@xyflow/react";
import { useContext, useEffect, useRef, useState } from "react";
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
import { IconButton } from "../components/icon-button";
import { Icon } from "../components/icons";
import * as s from "../styles";
import { ThemeContext } from "../theme";
import { ErrorNotice, Form, text, useAction } from "../ui";
import { GeneratedDomain } from "./generated-domain";
import { ServiceCatalog } from "./service-catalog";
import { ServiceConnections } from "./service-outputs";
import * as v from "./visual-editor.styles";
import {
  AddServiceForm,
  AddVolumeForm,
  VolumeNameField,
} from "./visual-editor/add-resource-forms";
import { RouteEditor } from "./visual-editor/route-editor";
import { ServiceProperties } from "./visual-editor/service-properties";
import { ResourceSummary } from "./visual-resource";
import "@xyflow/react/dist/style.css";

export type { EnvironmentPath } from "../api/environment";
type Spec = Schema["Specification"];
type App = Schema["ApplicationReference"];
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
  const graph = useRef<HTMLDivElement>(null);
  const closeProperties = () => {
    setSelected(undefined);
    graph.current?.focus();
  };
  const [selected, setSelected] = useState<string>();
  const [view, setView] = useState<"canvas" | "list">(() =>
    matchMedia("(width < 40rem)").matches ? "list" : "canvas",
  );
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
        x: (i % 2) * 320,
        y: Math.floor(i / 2) * 180,
      },
      data: {
        label: (
          <ResourceSummary
            name={a.name}
            kind={a.kind ?? "web"}
            instances={a.replicas}
            detail={
              a.source.kind === "git" ? a.source.repository : a.source.image
            }
          />
        ),
      },
      selected: a.id === selected,
      ariaLabel: `Configure ${a.name}`,
      className: "visual-node",
      sourcePosition: Position.Right,
      targetPosition: Position.Left,
    })),
    ...(spec.networks ?? []).map((network, i) => ({
      id: network.id,
      position: positions?.[network.id] ?? { x: i * 320, y: -180 },
      data: { label: <ResourceSummary name={network.name} kind="network" /> },
      selected: network.id === selected,
      ariaLabel: `${network.name} network`,
      className: "visual-node",
      sourcePosition: Position.Right,
      targetPosition: Position.Left,
    })),
    ...(spec.volumes ?? []).map((v, i) => ({
      id: v.id,
      position: positions?.[v.id] ?? {
        x: i * 320,
        y: Math.ceil(spec.applications.length / 2) * 180,
      },
      data: { label: <ResourceSummary name={v.name} kind="volume" /> },
      selected: v.id === selected,
      ariaLabel: `Configure ${v.name} volume`,
      className: "visual-node",
      sourcePosition: Position.Top,
      targetPosition: Position.Left,
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
      <div className={v.editor}>
        <div
          className={v.graph}
          ref={graph}
          tabIndex={-1}
          role="group"
          aria-label={t(($) => $.visualEditor.graph)}
        >
          {view === "canvas" ? (
            <div className="visual-canvas">
              {nodes.length === 0 && (
                <div className={v.emptyCanvas}>
                  <h4 className={v.heading}>
                    {configure
                      ? t(($) => $.visualEditor.emptyTitle)
                      : t(($) => $.emptyTitle)}
                  </h4>
                  <p className={s.muted}>
                    {configure
                      ? t(($) => $.visualEditor.emptyDescription)
                      : t(($) => $.emptyReadOnly)}
                  </p>
                </div>
              )}
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
                      type: "smoothstep",
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
                <Background gap={24} size={1} />
                <Controls
                  orientation="horizontal"
                  showInteractive={false}
                  fitViewOptions={{ maxZoom: 1 }}
                />
              </ReactFlow>
            </div>
          ) : (
            <ul className={v.resourceList}>
              {(spec.networks ?? []).map((network) => (
                <li key={network.id}>
                  <ResourceSummary name={network.name} kind="network" />
                </li>
              ))}
              {spec.applications.map((a) => (
                <li key={a.id}>
                  <ResourceSummary
                    name={
                      <Button
                        type="button"
                        variant="ghost"
                        className={v.resourceChoice}
                        aria-pressed={selected === a.id}
                        onClick={() => setSelected(a.id)}
                      >
                        {a.name}
                      </Button>
                    }
                    kind={a.kind ?? "web"}
                    instances={a.replicas}
                    detail={
                      a.source.kind === "git"
                        ? a.source.repository
                        : a.source.image
                    }
                  />
                </li>
              ))}
              {spec.volumes?.map((volume) => (
                <li key={volume.id}>
                  <ResourceSummary
                    name={
                      <Button
                        type="button"
                        variant="ghost"
                        className={v.resourceChoice}
                        aria-pressed={selected === volume.id}
                        onClick={() => setSelected(volume.id)}
                      >
                        {volume.name}
                      </Button>
                    }
                    kind="volume"
                  />
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
                    setNotice(
                      "Layout saved. Running services were not changed.",
                    );
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
        </div>
        {!app && !volume && (
          <section
            className={v.inspector}
            aria-label={t(($) => $.visualEditor.selectTitle)}
          >
            <h3 className={v.inspectorTitle}>
              {t(($) => $.visualEditor.selectTitle)}
            </h3>
            <p className={s.muted}>
              {t(($) => $.visualEditor.selectDescription)}
            </p>
          </section>
        )}
        {volume && (
          <section
            className={v.inspector}
            aria-label={`Properties for ${volume.name}`}
          >
            <div className={v.inspectorHeader}>
              <div>
                <h3 className={v.inspectorTitle}>{volume.name}</h3>
                <p className={s.muted}>Data volume</p>
              </div>
              <IconButton
                aria-label={t(($) => $.visualEditor.closeProperties)}
                onClick={closeProperties}
              >
                <Icon>
                  <path d="m6 6 12 12M18 6 6 18" />
                </Icon>
              </IconButton>
            </div>
            <p>
              Removing this volume from configuration retains its data.
              Permanent deletion is available in the data controls below.
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
              nodes.find((node) => node.id === app.id)?.position ?? {
                x: 0,
                y: 0,
              }
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
                  .filter((application) => application.id !== app.id)
                  .map((application) => ({
                    ...application,
                    dependsOn: application.dependsOn?.filter(
                      (id) => id !== app.id,
                    ),
                  })),
                bindings: spec.bindings?.filter(
                  (binding) =>
                    binding.sourceId !== app.id && binding.targetId !== app.id,
                ),
                routes: spec.routes?.filter(
                  (route) => route.targetId !== app.id,
                ),
              });
              setSelected(undefined);
            }}
            className={v.inspector}
            header={
              <div className={v.inspectorHeader}>
                <div>
                  <h3 className={v.inspectorTitle}>{app.name}</h3>
                  <p className={s.muted}>{app.kind ?? "web"}</p>
                </div>
                <IconButton
                  aria-label={t(($) => $.visualEditor.closeProperties)}
                  onClick={closeProperties}
                >
                  <Icon>
                    <path d="m6 6 12 12M18 6 6 18" />
                  </Icon>
                </IconButton>
              </div>
            }
          />
        )}
      </div>
      {configure && (
        <ServiceCatalog
          path={path}
          spec={spec}
          secrets={secrets.data ?? []}
          stage={stage}
        />
      )}
      {configure && (
        <AddServiceForm
          spec={spec}
          secrets={secrets.data ?? []}
          action={action}
          stage={stage}
          onSelect={setSelected}
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
            {view === "canvas" ? (
              <Button type="button" onClick={() => setSelected(volume.id)}>
                {volume.name}
              </Button>
            ) : (
              <span>{volume.name}</span>
            )}
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
      {configure && <AddVolumeForm spec={spec} action={action} stage={stage} />}
      <RouteEditor spec={spec} change={stage} write={configure} />
      {configure && <GeneratedDomain path={path} spec={spec} change={stage} />}
    </section>
  );
}
