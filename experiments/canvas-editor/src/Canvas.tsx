import {
  applyNodeChanges,
  Background,
  type Connection,
  Controls,
  type Edge,
  Handle,
  type Node,
  type NodeChange,
  type NodeProps,
  Position,
  ReactFlow,
  type ReactFlowInstance,
} from "@xyflow/react";
import {
  forwardRef,
  memo,
  useCallback,
  useEffect,
  useImperativeHandle,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { css } from "../styled-system/css";
import {
  type Binding,
  bindingId,
  type Configuration,
  connectionProblem,
  kindLabel,
  type Layout,
  type Service,
} from "./domain";
import { recordCommit } from "./metrics";
import { muted } from "./styles";

type ServiceNode = Node<{ service: Service }, "service">;
const nodeTypes = { service: memo(ServiceCard) };
const edgeOptions = {
  style: { stroke: "#65756c", strokeWidth: 1.5 },
  labelStyle: { fill: "#18201e", fontSize: 11 },
  labelBgPadding: [6, 4] as [number, number],
  labelBgBorderRadius: 4,
};
const ariaLabels = {
  "node.a11yDescription.default":
    "Press Enter or Space to select a service. Use arrow keys to move it; hold Shift for larger steps. Escape clears selection.",
  "node.a11yDescription.keyboardDisabled":
    "Press Enter or Space to select a service. Escape clears selection.",
  "edge.a11yDescription.default":
    "Use the Connections section in the service properties to remove a connection.",
};

function ServiceCard({ data: { service }, selected }: NodeProps<ServiceNode>) {
  useLayoutEffect(() => recordCommit("nodes", service.id));
  const hasDatabase = service.kind === "application";
  const hasStorage = service.kind !== "volume";
  return (
    <div
      className={css({
        width: "228px",
        bg: "paper",
        border: "1px solid",
        borderColor: "control",
        rounded: "lg",
        color: "ink",
        overflow: "visible",
        "&[data-selected=true]": {
          borderColor: "accent",
          outline: "2px solid token(colors.accent)",
          outlineOffset: "2px",
        },
      })}
      data-selected={selected}
    >
      {service.kind !== "application" && (
        <Handle
          type="target"
          position={Position.Left}
          id="input"
          aria-hidden="true"
          title={`${service.name} input`}
          className={css({
            width: "12px!",
            height: "12px!",
            bg: "accent!",
            border: "2px solid white!",
          })}
        />
      )}
      <div
        className={css({
          p: "4",
          borderBottom: "1px solid",
          borderColor: "line",
        })}
      >
        <div className={muted}>{kindLabel(service.kind)}</div>
        <div
          className={css({
            fontSize: "16px",
            fontWeight: "600",
            mt: "1",
            overflowWrap: "anywhere",
          })}
        >
          {service.name || "Unnamed service"}
        </div>
      </div>
      <div
        className={css({
          p: "4",
          pt: "3",
          fontSize: "12px",
          color: "muted",
          overflowWrap: "anywhere",
        })}
      >
        {service.kind === "volume" ? (
          `${service.sizeGiB || "—"} GiB persistent storage`
        ) : (
          <>
            <div>{service.image || "Image required"}</div>
            <div className={css({ mt: "2" })}>Port {service.port || "—"}</div>
          </>
        )}
      </div>
      {hasDatabase && (
        <>
          <span
            className={css({
              position: "absolute",
              right: "3",
              top: "42px",
              fontSize: "10px",
              color: "muted",
            })}
          >
            DB
          </span>
          <Handle
            type="source"
            position={Position.Right}
            id="database"
            style={{ top: 52 }}
            aria-hidden="true"
            title={`${service.name} database output`}
            className={css({
              width: "12px!",
              height: "12px!",
              bg: "accent!",
              border: "2px solid white!",
            })}
          />
        </>
      )}
      {hasStorage && (
        <>
          <span
            className={css({
              position: "absolute",
              right: "3",
              bottom: "6px",
              fontSize: "10px",
              color: "muted",
            })}
          >
            Disk
          </span>
          <Handle
            type="source"
            position={Position.Right}
            id="storage"
            style={{ top: "auto", bottom: 8 }}
            aria-hidden="true"
            title={`${service.name} storage output`}
            className={css({
              width: "12px!",
              height: "12px!",
              bg: "accent!",
              border: "2px solid white!",
            })}
          />
        </>
      )}
    </div>
  );
}

export type CanvasHandle = {
  layout: () => Layout;
  focus: (id: string) => void;
};
type Props = {
  config: Configuration;
  initialLayout: Layout;
  hasLayout: boolean;
  selected: string;
  onSelect: (id: string) => void;
  onConnect: (binding: Binding) => void;
  onDirty: () => void;
};
function bindingFromConnection(connection: Connection | Edge): Binding | null {
  if (
    connection.targetHandle !== "input" ||
    (connection.sourceHandle !== "database" &&
      connection.sourceHandle !== "storage")
  )
    return null;
  return {
    source: connection.source,
    target: connection.target,
    kind: connection.sourceHandle,
  };
}

export const Canvas = memo(
  forwardRef<CanvasHandle, Props>(function Canvas(
    {
      config,
      initialLayout,
      hasLayout,
      selected,
      onSelect,
      onConnect,
      onDirty,
    },
    ref,
  ) {
    const [nodes, setNodes] = useState<ServiceNode[]>(() =>
      config.services.map((service, index) => ({
        id: service.id,
        type: "service",
        data: { service },
        selected: service.id === selected,
        position: initialLayout.positions[service.id] ?? {
          x: 80 + (index % 4) * 300,
          y: 100 + Math.floor(index / 4) * 220,
        },
        ariaLabel: `${service.name}, ${kindLabel(service.kind)}`,
      })),
    );
    const instance = useRef<ReactFlowInstance<ServiceNode, Edge> | null>(null);
    useLayoutEffect(() => recordCommit("canvas"));
    useEffect(() => {
      setNodes((previous) =>
        config.services.map((service, index) => {
          const node = previous.find((item) => item.id === service.id);
          if (
            node?.data.service === service &&
            node.selected === (service.id === selected)
          )
            return node;
          return {
            ...node,
            id: service.id,
            type: "service",
            data: { service },
            selected: service.id === selected,
            position: node?.position ?? {
              x: 80 + (index % 4) * 300,
              y: 100 + Math.floor(index / 4) * 220,
            },
            ariaLabel: `${service.name}, ${kindLabel(service.kind)}`,
          };
        }),
      );
    }, [config.services, selected]);
    useImperativeHandle(
      ref,
      () => ({
        layout: () => ({
          version: 1,
          positions: Object.fromEntries(
            nodes.map((node) => [node.id, node.position]),
          ),
          viewport: instance.current?.getViewport() ?? initialLayout.viewport,
        }),
        focus: (id) => {
          void instance.current?.fitView({
            nodes: [{ id }],
            maxZoom: 1,
            padding: 0.8,
            duration: 0,
          });
        },
      }),
      [nodes, initialLayout],
    );
    const edges = useMemo(
      () =>
        config.bindings.map((binding): Edge => ({
          id: bindingId(binding),
          source: binding.source,
          target: binding.target,
          sourceHandle: binding.kind,
          targetHandle: "input",
          label: binding.kind === "database" ? "DATABASE_URL" : "/data",
          ariaLabel: `${binding.kind} connection`,
          ...edgeOptions,
        })),
      [config.bindings],
    );
    const changes = useCallback(
      (changes: NodeChange<ServiceNode>[]) => {
        setNodes((previous) => applyNodeChanges(changes, previous));
        if (
          changes.some(
            (change) => change.type === "position" && change.position,
          )
        )
          onDirty();
        const selection = changes.find(
          (change) => change.type === "select" && change.selected,
        );
        if (selection?.type === "select") onSelect(selection.id);
      },
      [onDirty, onSelect],
    );
    const valid = useCallback(
      (connection: Connection | Edge) => {
        const binding = bindingFromConnection(connection);
        return binding !== null && connectionProblem(config, binding) === null;
      },
      [config],
    );
    const connect = useCallback(
      (connection: Connection) => {
        const binding = bindingFromConnection(connection);
        if (binding) onConnect(binding);
      },
      [onConnect],
    );
    return (
      <ReactFlow<ServiceNode>
        nodes={nodes}
        edges={edges}
        nodeTypes={nodeTypes}
        onNodesChange={changes}
        onConnect={connect}
        isValidConnection={valid}
        onInit={(flow) => {
          instance.current = flow;
        }}
        onMoveEnd={onDirty}
        onNodeClick={(_, node) => onSelect(node.id)}
        defaultEdgeOptions={edgeOptions}
        defaultViewport={initialLayout.viewport}
        fitView={!hasLayout}
        fitViewOptions={{ padding: 0.25, maxZoom: 1 }}
        minZoom={0.05}
        maxZoom={2}
        deleteKeyCode={null}
        nodesFocusable
        edgesFocusable
        ariaLabelConfig={ariaLabels}
        proOptions={{ hideAttribution: false }}
      >
        <Background color="#c9d2c9" gap={24} size={1} />
        <Controls showInteractive={false} />
      </ReactFlow>
    );
  }),
);
