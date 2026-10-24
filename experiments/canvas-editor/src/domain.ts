export type ServiceKind = "application" | "database" | "volume";
export type Service =
  | {
      id: string;
      name: string;
      kind: "application" | "database";
      image: string;
      port: number;
    }
  | { id: string; name: string; kind: "volume"; sizeGiB: number };
export type Binding = {
  source: string;
  target: string;
  kind: "database" | "storage";
};
export type Configuration = {
  version: 1;
  services: Service[];
  bindings: Binding[];
};
export type Point = { x: number; y: number };
export type Layout = {
  version: 1;
  positions: Record<string, Point>;
  viewport: Point & { zoom: number };
};
export type Fixture = "starter" | "large";

const idPattern = /^svc-[a-z0-9-]{1,75}$/;
const record = (value: unknown): value is Record<string, unknown> =>
  typeof value === "object" && value !== null && !Array.isArray(value);
const keysAre = (value: Record<string, unknown>, keys: string[]) =>
  Object.keys(value).every((key) => keys.includes(key));
const text = (value: unknown, max: number) =>
  typeof value === "string" && value.trim().length > 0 && value.length <= max;
const integer = (value: unknown, max: number) =>
  typeof value === "number" &&
  Number.isInteger(value) &&
  value >= 1 &&
  value <= max;

export function serviceProblems(
  service: Service,
  services: Service[],
): Partial<Record<"name" | "image" | "port" | "sizeGiB", string>> {
  const problems: ReturnType<typeof serviceProblems> = {};
  if (
    !text(service.name, 64) ||
    services.some(
      (other) =>
        other.id !== service.id &&
        other.name.trim().toLowerCase() === service.name.trim().toLowerCase(),
    )
  )
    problems.name = "Use a unique name of 1–64 characters.";
  if (service.kind === "volume") {
    if (!integer(service.sizeGiB, 65536))
      problems.sizeGiB = "Use a whole number from 1 to 65536 GiB.";
  } else {
    if (!text(service.image, 256))
      problems.image = "Enter an image reference of 1–256 characters.";
    if (!integer(service.port, 65535))
      problems.port = "Use a whole number from 1 to 65535.";
  }
  return problems;
}

export function connectionProblem(
  config: Configuration,
  binding: Binding,
): string | null {
  const source = config.services.find(
    (service) => service.id === binding.source,
  );
  const target = config.services.find(
    (service) => service.id === binding.target,
  );
  if (!source || !target) return "Both services must exist.";
  if (source.id === target.id) return "A service cannot connect to itself.";
  if (
    binding.kind === "database" &&
    (source.kind !== "application" || target.kind !== "database")
  ) {
    return "Database connections go from an application to a database.";
  }
  if (
    binding.kind === "storage" &&
    (source.kind === "volume" || target.kind !== "volume")
  ) {
    return "Storage connections go from an application or database to a volume.";
  }
  if (
    config.bindings.some(
      (existing) =>
        existing.source === binding.source && existing.kind === binding.kind,
    )
  ) {
    return (
      "This service already has a " +
      binding.kind +
      " connection. Remove it before choosing another."
    );
  }
  return null;
}

export function configurationProblems(value: unknown): string[] {
  if (
    !record(value) ||
    !keysAre(value, ["version", "services", "bindings"]) ||
    value.version !== 1 ||
    !Array.isArray(value.services) ||
    !Array.isArray(value.bindings) ||
    value.services.length < 1 ||
    value.services.length > 250 ||
    value.bindings.length > 500
  ) {
    return ["The draft must contain 1–250 services in the supported format."];
  }
  const ids = new Set<string>();
  for (const service of value.services) {
    if (
      !record(service) ||
      typeof service.id !== "string" ||
      !idPattern.test(service.id) ||
      ids.has(service.id)
    )
      return ["Service identities must be unique and valid."];
    ids.add(service.id);
    if (
      typeof service.name !== "string" ||
      (service.kind === "volume"
        ? !keysAre(service, ["id", "name", "kind", "sizeGiB"]) ||
          typeof service.sizeGiB !== "number"
        : (service.kind !== "application" && service.kind !== "database") ||
          !keysAre(service, ["id", "name", "kind", "image", "port"]) ||
          typeof service.image !== "string" ||
          typeof service.port !== "number")
    )
      return ["The draft contains an unsupported service."];
  }
  const config = value as Configuration;
  const problems = config.services.flatMap((service) =>
    Object.values(serviceProblems(service, config.services)).map(
      (problem) => `${service.name || "Service"}: ${problem}`,
    ),
  );
  if (problems.length) return problems;
  const accepted: Binding[] = [];
  for (const binding of value.bindings) {
    if (
      !record(binding) ||
      !keysAre(binding, ["source", "target", "kind"]) ||
      typeof binding.source !== "string" ||
      typeof binding.target !== "string" ||
      (binding.kind !== "database" && binding.kind !== "storage")
    )
      return ["The draft contains an unsupported connection."];
    const problem = connectionProblem(
      { ...config, bindings: accepted },
      binding as Binding,
    );
    if (problem) return [problem];
    accepted.push(binding as Binding);
  }
  return [];
}

function json(raw: string): unknown {
  if (raw.length > 500_000)
    throw new Error("Saved data exceeds the fixture limit.");
  return JSON.parse(raw);
}

export function parseConfiguration(raw: string): Configuration {
  const value = json(raw);
  if (configurationProblems(value).length)
    throw new Error("Saved draft has an unsupported format.");
  return value as Configuration;
}

export function parseLayout(raw: string): Layout {
  const value = json(raw);
  const point = (p: unknown): p is Record<string, unknown> & Point =>
    record(p) &&
    [p.x, p.y].every(
      (n) =>
        typeof n === "number" && Number.isFinite(n) && Math.abs(n) <= 1_000_000,
    );
  if (
    !record(value) ||
    !keysAre(value, ["version", "positions", "viewport"]) ||
    value.version !== 1 ||
    !record(value.positions) ||
    Object.keys(value.positions).length > 250 ||
    !point(value.viewport) ||
    !record(value.viewport) ||
    typeof value.viewport.zoom !== "number" ||
    value.viewport.zoom < 0.05 ||
    value.viewport.zoom > 2
  )
    throw new Error("Saved layout has an unsupported format.");
  for (const [id, position] of Object.entries(value.positions)) {
    if (!idPattern.test(id) || !point(position))
      throw new Error("Saved position has an unsupported format.");
  }
  return value as Layout;
}

export const bindingId = (binding: Binding) =>
  `${binding.source}:${binding.kind}`;
export const kindLabel = (kind: ServiceKind) =>
  ({ application: "Application", database: "PostgreSQL", volume: "Volume" })[
    kind
  ];
export const storageKey = (fixture: Fixture, kind: "draft" | "layout") =>
  `astilba:plat1:${fixture}:${kind}`;

export function fixtureData(fixture: Fixture): {
  config: Configuration;
  layout: Layout;
} {
  const services: Service[] = [];
  const bindings: Binding[] = [];
  const positions: Record<string, Point> = {};
  const total = fixture === "large" ? 250 : 4;
  for (let i = 0; i < total; i++) {
    const group = Math.floor(i / 4);
    const slot = i % 4;
    const suffix = fixture === "large" ? ` ${group + 1}` : "";
    const id = `svc-${["web", "worker", "database", "volume"][slot]}-${group}`;
    const name = ["Web", "Worker", "Database", "Data"][slot] + suffix;
    const service: Service =
      slot === 3
        ? { id, name, kind: "volume", sizeGiB: 10 }
        : {
            id,
            name,
            kind: slot === 2 ? "database" : "application",
            image:
              slot === 2
                ? "postgres:18"
                : `ghcr.io/example/${slot ? "worker" : "web"}:1.0`,
            port: slot === 2 ? 5432 : 3000,
          };
    services.push(service);
    positions[id] = {
      x: (group % 5) * 840 + (slot >= 2 ? 430 : 80),
      y: Math.floor(group / 5) * 540 + (slot % 2 ? 340 : 100),
    };
    if (slot === 2)
      bindings.push({
        source: `svc-web-${group}`,
        target: id,
        kind: "database",
      });
    if (slot === 3)
      bindings.push({
        source: `svc-database-${group}`,
        target: id,
        kind: "storage",
      });
  }
  return {
    config: { version: 1, services, bindings },
    layout: { version: 1, positions, viewport: { x: 0, y: 0, zoom: 1 } },
  };
}
