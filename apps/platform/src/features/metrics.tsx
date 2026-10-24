import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useEffect, useState } from "react";
import { client, result, type Schema } from "../api/client";
import * as s from "../styles";
import { ErrorNotice } from "../ui";

type Sample = Schema["MetricSample"];
const percent = (value: number | undefined) =>
  value === undefined ? "Unavailable" : `${value.toFixed(1)}%`;
const bytes = (value: number | undefined) => {
  if (value === undefined) return "Unavailable";
  if (value >= 1073741824) return `${(value / 1073741824).toFixed(2)} GiB`;
  if (value >= 1048576) return `${(value / 1048576).toFixed(1)} MiB`;
  if (value >= 1024) return `${(value / 1024).toFixed(1)} KiB`;
  return `${value} B`;
};
const usage = (value: Schema["MetricUsage"] | undefined) =>
  value ? (100 * value.usedBytes) / value.totalBytes : undefined;

export function ServerMetrics({
  workspaceId,
  serverId,
}: {
  workspaceId: string;
  serverId: string;
}) {
  const [open, setOpen] = useState(false);
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!open) return;
    const timer = setInterval(() => setNow(Date.now()), 30000);
    return () => clearInterval(timer);
  }, [open]);
  const [hours, setHours] = useState<1 | 6 | 24>(1);
  const [resource, setResource] = useState("");
  const history = useQuery({
    queryKey: ["server-metrics", workspaceId, serverId, hours],
    enabled: open,
    queryFn: () =>
      result(
        client.GET(
          "/api/v1/workspaces/{workspaceId}/servers/{serverId}/metrics",
          { params: { path: { workspaceId, serverId }, query: { hours } } },
        ),
      ),
    refetchInterval: open ? 30000 : false,
  });
  const latest = history.data?.samples.at(-1);
  const stale = latest && now - new Date(latest.receivedAt).getTime() > 180000;
  const names = new Map(
    history.data?.resources.map((r) => [r.id, `${r.environment} / ${r.name}`]),
  );
  return (
    <details
      onToggle={(e) => {
        setNow(Date.now());
        setOpen(e.currentTarget.open);
      }}
    >
      <summary>Resource history</summary>
      {open && (
        <section className={s.stack} aria-label="Resource history">
          <p>
            Read-only samples, retained for 24 hours. Missing readings are
            unavailable, not zero. Metrics do not establish application health.
          </p>
          <label>
            History window
            <select
              value={hours}
              onChange={(e) => setHours(Number(e.target.value) as 1 | 6 | 24)}
            >
              <option value={1}>Last hour</option>
              <option value={6}>Last 6 hours</option>
              <option value={24}>Last 24 hours</option>
            </select>
          </label>
          <ErrorNotice error={history.error} />
          {history.isPending ? (
            <p role="status">Loading resource history…</p>
          ) : !latest ? (
            <p>
              No samples in this window. A connected agent with metrics support
              is required; older agents remain usable.
            </p>
          ) : (
            <>
              <p>
                Latest sample:{" "}
                <time dateTime={latest.receivedAt}>
                  {new Date(latest.receivedAt).toLocaleString()}
                </time>
                .{" "}
                {stale
                  ? "Stale — current resource usage is unknown."
                  : "Recent sample."}
              </p>
              {latest.report.partial && (
                <p className={s.notice}>
                  Partial sample: some readings failed or exceeded the
                  collection limit.
                </p>
              )}
              {usage(latest.report.host.disk) !== undefined &&
                (usage(latest.report.host.disk) ?? 0) >= 90 && (
                  <p className={s.notice}>
                    Disk pressure: at least 90% is used or reserved. Review
                    storage before starting more work.
                  </p>
                )}
              <dl className={s.metricSummary}>
                <div>
                  <dt>Host CPU</dt>
                  <dd>{percent(latest.report.host.cpuPercent)}</dd>
                </div>
                <div>
                  <dt>Host memory</dt>
                  <dd>
                    {bytes(latest.report.host.memory?.usedBytes)} /{" "}
                    {bytes(latest.report.host.memory?.totalBytes)}
                  </dd>
                </div>
                <div>
                  <dt>
                    {latest.report.host.diskSource === "docker"
                      ? "Docker filesystem"
                      : "Agent filesystem (fallback)"}
                  </dt>
                  <dd>
                    {bytes(latest.report.host.disk?.usedBytes)} /{" "}
                    {bytes(latest.report.host.disk?.totalBytes)}
                  </dd>
                </div>
              </dl>
              <History
                samples={history.data?.samples ?? []}
                hours={hours}
                now={now}
              />
              <h3>Managed services at the latest sample</h3>
              <p>
                Service CPU is measured per core and can exceed 100%. Memory
                includes the container’s accounted cache.
              </p>
              {!latest.report.services.length ? (
                <p>No managed service readings in this sample.</p>
              ) : (
                <MetricTable label="Latest service readings">
                  <table>
                    <caption>Latest managed service resource readings</caption>
                    <thead>
                      <tr>
                        <th scope="col">Service</th>
                        <th scope="col">State</th>
                        <th scope="col">CPU</th>
                        <th scope="col">Memory</th>
                      </tr>
                    </thead>
                    <tbody>
                      {latest.report.services.map((v) => (
                        <tr key={v.resourceId}>
                          <th scope="row">
                            {names.get(v.resourceId) ?? v.resourceId}
                          </th>
                          <td>{v.state}</td>
                          <td>{percent(v.cpuPercent)}</td>
                          <td>{bytes(v.memoryUsedBytes)}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </MetricTable>
              )}
              <details>
                <summary>View service history</summary>
                <label>
                  Service history target
                  <select
                    value={resource}
                    onChange={(e) => setResource(e.target.value)}
                  >
                    <option value="">Choose a service</option>
                    {history.data?.resources.map((r) => (
                      <option key={r.id} value={r.id}>
                        {r.environment} / {r.name}
                      </option>
                    ))}
                  </select>
                </label>
                {resource && (
                  <MetricTable label="Service history readings">
                    <table>
                      <caption>
                        Recorded service readings; absent entries are
                        unavailable
                      </caption>
                      <thead>
                        <tr>
                          <th scope="col">Time</th>
                          <th scope="col">State</th>
                          <th scope="col">CPU</th>
                          <th scope="col">Memory</th>
                        </tr>
                      </thead>
                      <tbody>
                        {history.data?.samples.map((v) => {
                          const service = v.report.services.find(
                            (r) => r.resourceId === resource,
                          );
                          return (
                            <tr key={v.receivedAt}>
                              <th scope="row">
                                {new Date(v.receivedAt).toLocaleString()}
                              </th>
                              <td>{service?.state ?? "Unavailable"}</td>
                              <td>{percent(service?.cpuPercent)}</td>
                              <td>{bytes(service?.memoryUsedBytes)}</td>
                            </tr>
                          );
                        })}
                      </tbody>
                    </table>
                  </MetricTable>
                )}
              </details>
              <details>
                <summary>View sample data</summary>
                <MetricTable label="Host history readings">
                  <table>
                    <caption>
                      Recorded host readings; gaps contain no inferred data
                    </caption>
                    <thead>
                      <tr>
                        <th scope="col">Time</th>
                        <th scope="col">CPU</th>
                        <th scope="col">Memory</th>
                        <th scope="col">Disk</th>
                        <th scope="col">Collection</th>
                      </tr>
                    </thead>
                    <tbody>
                      {history.data?.samples.map((v) => (
                        <tr key={v.receivedAt}>
                          <th scope="row">
                            {new Date(v.receivedAt).toLocaleString()}
                          </th>
                          <td>{percent(v.report.host.cpuPercent)}</td>
                          <td>{percent(usage(v.report.host.memory))}</td>
                          <td>{percent(usage(v.report.host.disk))}</td>
                          <td>{v.report.partial ? "Partial" : "Complete"}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </MetricTable>
              </details>
            </>
          )}
        </section>
      )}
    </details>
  );
}

function History({
  samples,
  hours,
  now,
}: {
  samples: Sample[];
  hours: number;
  now: number;
}) {
  const end = now;
  const start = end - hours * 3600000;
  const series = [
    { name: "Host CPU", get: (v: Sample) => v.report.host.cpuPercent },
    { name: "Host memory", get: (v: Sample) => usage(v.report.host.memory) },
    { name: "Filesystem usage", get: (v: Sample) => usage(v.report.host.disk) },
  ];
  return (
    <div className={s.metricCharts}>
      {series.map(({ name, get }) => {
        let previous = 0;
        let connected = false;
        const path = samples
          .map((v) => {
            const value = get(v);
            const time = new Date(v.receivedAt).getTime();
            if (value === undefined) {
              connected = false;
              return "";
            }
            const command = connected && time - previous <= 60000 ? "L" : "M";
            previous = time;
            connected = true;
            return `${command}${Math.max(0, ((time - start) / (end - start)) * 300).toFixed(1)},${(100 - value).toFixed(1)}`;
          })
          .join(" ");
        return (
          <figure key={name}>
            <figcaption>{name} · 0–100%</figcaption>
            <svg
              viewBox="0 0 300 100"
              role="img"
              aria-label={`${name} over the last ${hours} hours. Exact readings are in View sample data.`}
              className={s.metricChart}
            >
              <title>{name} history</title>
              <path
                d="M0,100 H300 M0,0 H300"
                stroke="currentColor"
                opacity="0.25"
                fill="none"
              />
              {samples.map((v) => {
                const value = get(v);
                if (value === undefined) return null;
                return (
                  <circle
                    key={v.receivedAt}
                    cx={Math.max(
                      0,
                      ((new Date(v.receivedAt).getTime() - start) /
                        (end - start)) *
                        300,
                    )}
                    cy={100 - value}
                    r="1.5"
                    fill="currentColor"
                  />
                );
              })}
              <path
                d={path}
                stroke="currentColor"
                strokeWidth="2"
                fill="none"
              />
            </svg>
          </figure>
        );
      })}
    </div>
  );
}

function MetricTable({
  label,
  children,
}: {
  label: string;
  children: ReactNode;
}) {
  return (
    // biome-ignore lint/a11y/noNoninteractiveTabindex: WebKit needs focus to keyboard-scroll a clipped table.
    <section className={s.metricTable} tabIndex={0} aria-label={label}>
      {children}
    </section>
  );
}
