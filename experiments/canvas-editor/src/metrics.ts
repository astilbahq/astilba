type Counters = {
  nodes: Record<string, number>;
  canvas: number;
  properties: number;
};
declare global {
  interface Window {
    __plat1Metrics?: Counters;
  }
}
const enabled = new URLSearchParams(window.location.search).has("profile");
if (enabled) window.__plat1Metrics = { nodes: {}, canvas: 0, properties: 0 };

// Opt-in committed-render counters for the experiment's browser measurements.
export function recordCommit(
  kind: "canvas" | "properties" | "nodes",
  id?: string,
): void {
  const metrics = window.__plat1Metrics;
  if (!metrics) return;
  if (kind === "nodes" && id) metrics.nodes[id] = (metrics.nodes[id] ?? 0) + 1;
  else if (kind !== "nodes") metrics[kind]++;
}
