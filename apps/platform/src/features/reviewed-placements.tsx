import type { Schema } from "../api/client";
import * as s from "../styles";

export function ReviewedPlacements({
  distributed,
  applications,
  servers,
}: {
  distributed: Schema["DistributedPlan"];
  applications: Schema["Specification"]["applications"];
  servers?: Schema["Server"][];
}) {
  return (
    <section className={s.stack} aria-label="Placement changes">
      <h4>Reviewed placements</h4>
      {distributed.units.map((unit) => (
        <div key={unit.plan.unitId}>
          <strong>
            {servers?.find((server) => server.id === unit.plan.serverId)
              ?.name ?? unit.plan.serverId}
            {unit.plan.local?.primary
              ? " · ingress and persistent services"
              : ""}
          </strong>
          <ul>
            {applications.map((app) => (
              <li key={app.id}>
                {app.name}:{" "}
                {unit.plan.local?.priorInstances[app.id]?.length ?? 0} →{" "}
                {unit.plan.local?.instances[app.id]?.length ?? 0} instances
                {unit.plan.images[app.id] && (
                  <code className={s.code}> · {unit.plan.images[app.id]}</code>
                )}
              </li>
            ))}
          </ul>
          <p>
            New pool: {unit.cpuMillis} millicores · {unit.memoryMiB} MiB.
            Serving pools remain reserved until publication is confirmed.
          </p>
        </div>
      ))}
    </section>
  );
}
