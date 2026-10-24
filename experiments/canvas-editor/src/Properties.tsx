import { memo, useLayoutEffect, useState } from "react";
import { css } from "../styled-system/css";
import {
  type Binding,
  bindingId,
  type Configuration,
  kindLabel,
  type Service,
  serviceProblems,
} from "./domain";
import { recordCommit } from "./metrics";
import { button, error, input, label, muted, stack } from "./styles";

type Props = {
  service: Service;
  config: Configuration;
  showErrors: boolean;
  onChange: (service: Service) => void;
  onConnect: (binding: Binding) => void;
  onRemove: (binding: Binding) => void;
  onFocus: () => void;
};
export const Properties = memo(function Properties({
  service,
  config,
  showErrors,
  onChange,
  onConnect,
  onRemove,
  onFocus,
}: Props) {
  useLayoutEffect(() => recordCommit("properties"));
  const [kind, setKind] = useState<Binding["kind"]>(
    service.kind === "application" ? "database" : "storage",
  );
  const [target, setTarget] = useState("");
  const problems = showErrors ? serviceProblems(service, config.services) : {};
  const targets = config.services.filter(
    (item) => item.kind === (kind === "database" ? "database" : "volume"),
  );
  const selectedTarget = targets.some((item) => item.id === target)
    ? target
    : (targets[0]?.id ?? "");
  const connections = config.bindings.filter(
    (binding) => binding.source === service.id,
  );
  return (
    <aside
      aria-label="Service properties"
      className={css({
        bg: "paper",
        p: "6",
        borderLeft: { md: "1px solid token(colors.line)" },
        borderTop: { base: "1px solid token(colors.line)", md: "none" },
        overflowY: "auto",
        minWidth: "0",
      })}
    >
      <div className={muted}>
        {kindLabel(service.kind)} · Draft configuration
      </div>
      <h2
        className={css({
          fontSize: "20px",
          fontWeight: "600",
          mt: "1",
          mb: "5",
          overflowWrap: "anywhere",
        })}
      >
        {service.name || "Unnamed service"}
      </h2>
      <div className={stack}>
        <label className={label}>
          Service name
          <input
            className={input}
            value={service.name}
            onChange={(event) =>
              onChange({ ...service, name: event.target.value })
            }
            aria-invalid={!!problems.name}
            aria-describedby={problems.name ? "name-error" : undefined}
          />
          {problems.name && (
            <span id="name-error" className={error}>
              {problems.name}
            </span>
          )}
        </label>
        {service.kind === "volume" ? (
          <label className={label}>
            Size (GiB)
            <input
              className={input}
              type="number"
              value={service.sizeGiB || ""}
              onChange={(event) =>
                onChange({ ...service, sizeGiB: Number(event.target.value) })
              }
              aria-invalid={!!problems.sizeGiB}
              aria-describedby={problems.sizeGiB ? "size-error" : undefined}
            />
            {problems.sizeGiB && (
              <span id="size-error" className={error}>
                {problems.sizeGiB}
              </span>
            )}
          </label>
        ) : (
          <>
            <label className={label}>
              Container image
              <input
                className={input}
                value={service.image}
                onChange={(event) =>
                  onChange({ ...service, image: event.target.value })
                }
                aria-invalid={!!problems.image}
                aria-describedby={problems.image ? "image-error" : "image-help"}
                spellCheck={false}
              />
              <span id="image-help" className={muted}>
                A reference only. This prototype does not pull or run images.
              </span>
              {problems.image && (
                <span id="image-error" className={error}>
                  {problems.image}
                </span>
              )}
            </label>
            <label className={label}>
              Internal port
              <input
                className={input}
                type="number"
                value={service.port || ""}
                onChange={(event) =>
                  onChange({ ...service, port: Number(event.target.value) })
                }
                aria-invalid={!!problems.port}
                aria-describedby={problems.port ? "port-error" : undefined}
              />
              {problems.port && (
                <span id="port-error" className={error}>
                  {problems.port}
                </span>
              )}
            </label>
          </>
        )}
        <button type="button" className={button} onClick={onFocus}>
          Show on canvas
        </button>
      </div>
      <section
        aria-labelledby="connections-title"
        className={css({
          mt: "8",
          pt: "5",
          borderTop: "1px solid token(colors.line)",
        })}
      >
        <h3
          id="connections-title"
          className={css({ fontWeight: "600", mb: "3" })}
        >
          Connections
        </h3>
        {connections.length ? (
          <ul
            className={css({
              display: "flex",
              flexDirection: "column",
              gap: "3",
              mb: "4",
            })}
          >
            {connections.map((binding) => {
              const destination = config.services.find(
                (item) => item.id === binding.target,
              )?.name;
              return (
                <li
                  key={bindingId(binding)}
                  className={css({
                    display: "flex",
                    alignItems: "center",
                    justifyContent: "space-between",
                    gap: "2",
                  })}
                >
                  <span
                    className={css({ minWidth: "0", overflowWrap: "anywhere" })}
                  >
                    {destination}
                    <span
                      className={css({
                        display: "block",
                        color: "muted",
                        fontSize: "12px",
                      })}
                    >
                      {binding.kind === "database" ? "DATABASE_URL" : "/data"}
                    </span>
                  </span>
                  <button
                    className={button}
                    type="button"
                    aria-label={`Remove ${binding.kind} connection to ${destination}`}
                    onClick={() => onRemove(binding)}
                  >
                    Remove
                  </button>
                </li>
              );
            })}
          </ul>
        ) : (
          <p className={muted}>No outgoing connections yet.</p>
        )}
        {service.kind === "volume" ? (
          <p className={muted}>
            Select an application or database to attach this volume.
          </p>
        ) : (
          <form
            className={css({
              display: "flex",
              flexDirection: "column",
              gap: "3",
              mt: "4",
            })}
            onSubmit={(event) => {
              event.preventDefault();
              if (selectedTarget)
                onConnect({ source: service.id, target: selectedTarget, kind });
            }}
          >
            <label className={label}>
              Connection type
              <select
                className={input}
                value={kind}
                onChange={(event) => {
                  setKind(event.target.value as Binding["kind"]);
                  setTarget("");
                }}
              >
                {service.kind === "application" && (
                  <option value="database">Database · DATABASE_URL</option>
                )}
                <option value="storage">Storage · /data</option>
              </select>
            </label>
            <label className={label}>
              Connect to
              <select
                className={input}
                value={selectedTarget}
                onChange={(event) => setTarget(event.target.value)}
              >
                {targets.length ? (
                  targets.map((item) => (
                    <option key={item.id} value={item.id}>
                      {item.name || "Unnamed service"}
                    </option>
                  ))
                ) : (
                  <option value="">
                    Add a {kind === "database" ? "database" : "volume"} first
                  </option>
                )}
              </select>
            </label>
            <button type="submit" className={button} disabled={!selectedTarget}>
              Connect services
            </button>
          </form>
        )}
      </section>
    </aside>
  );
});
