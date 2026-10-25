import { useEffect, useRef, useState } from "react";
import type { Schema } from "../api/client";
import * as s from "../styles";
import { Field, Form, text, useAction } from "../ui";

type Spec = Schema["Specification"];
type App = Schema["ApplicationReference"];
type Output = Schema["ServiceOutput"];

export function serviceOutputs(app: App): Output[] {
  return [
    { name: "HOST", type: "address" },
    ...(app.postgres
      ? ([
          { name: "PORT", type: "port", port: 5432 },
          { name: "URL", type: "url" },
          { name: "DATABASE", type: "variable", variable: "POSTGRES_DB" },
          { name: "USERNAME", type: "variable", variable: "POSTGRES_USER" },
          { name: "PASSWORD", type: "variable", variable: "POSTGRES_PASSWORD" },
        ] as Output[])
      : []),
    ...(app.outputs ?? []),
  ];
}

export function OutputEditor({
  app,
  spec,
  stage,
}: {
  app: App;
  spec: Spec;
  stage: (spec: Spec) => void;
}) {
  const action = useAction();
  const [type, setType] = useState<"port" | "url" | "variable">("url");
  const [error, setError] = useState("");
  const variables = [...spec.variables, ...(app.variables ?? [])];
  return (
    <details>
      <summary>Service outputs</summary>
      <p className={s.muted}>
        HOST is the private service address. Outputs describe connection values;
        they do not publish ports or set startup order.
      </p>
      {error && <p role="alert">{error}</p>}
      <ul className={s.list}>
        {serviceOutputs(app).map((o) => (
          <li key={o.name}>
            <strong>{o.name}</strong> · {o.type}
            {o.port ? ` · port ${o.port}` : ""}
            {o.variable ? ` · ${o.variable} reference` : ""}
            {app.outputs?.some((custom) => custom.name === o.name) && (
              <button
                type="button"
                onClick={() =>
                  stage({
                    ...spec,
                    applications: spec.applications.map((a) =>
                      a.id === app.id
                        ? {
                            ...a,
                            outputs: a.outputs?.filter(
                              (value) => value.name !== o.name,
                            ),
                          }
                        : a,
                    ),
                    bindings: spec.bindings?.filter(
                      (b) => b.sourceId !== app.id || b.output !== o.name,
                    ),
                  })
                }
              >
                Remove output {o.name} and its bindings
              </button>
            )}
          </li>
        ))}
      </ul>
      <Form
        action={action}
        label="Define service output"
        onSubmit={(d, form) => {
          const name = text(d, "name");
          if (serviceOutputs(app).some((output) => output.name === name)) {
            setError(
              "An output with this name already exists. Remove it before defining its replacement.",
            );
            return;
          }
          const output: Output = {
            name,
            type,
            ...(type === "variable"
              ? { variable: text(d, "variable") }
              : { port: Number(text(d, "port")) }),
            ...(type === "url"
              ? { scheme: text(d, "scheme"), path: text(d, "path") }
              : {}),
          };
          stage({
            ...spec,
            applications: spec.applications.map((a) =>
              a.id === app.id
                ? { ...a, outputs: [...(a.outputs ?? []), output] }
                : a,
            ),
          });
          setError("");
          form.reset();
        }}
      >
        <label>
          Output name
          <input
            name="name"
            pattern="[A-Z_][A-Z0-9_]*"
            maxLength={80}
            placeholder="API_URL"
            required
          />
        </label>
        <label>
          Output type
          <select
            value={type}
            onChange={(event) => setType(event.target.value as typeof type)}
          >
            <option value="url">Private URL</option>
            <option value="port">Port</option>
            <option value="variable">Source variable reference</option>
          </select>
        </label>
        {type === "variable" ? (
          <label>
            Source variable
            <select name="variable" required>
              <option value="">Choose a declared variable</option>
              {variables.map((v) => (
                <option key={v.name} value={v.name}>
                  {v.name}
                  {v.secretVersionId ? " · secret reference" : ""}
                </option>
              ))}
            </select>
          </label>
        ) : (
          <label>
            Internal port
            <input name="port" type="number" min={1} max={65535} required />
          </label>
        )}
        {type === "url" && (
          <>
            <label>
              URL scheme
              <input
                name="scheme"
                defaultValue="http"
                pattern="[a-z][a-z0-9+.\-]*"
                maxLength={32}
                required
              />
            </label>
            <Field name="path" label="URL path" required={false} />
          </>
        )}
        <button type="submit">Stage output</button>
      </Form>
    </details>
  );
}

export function ServiceConnections({
  spec,
  stage,
  write,
  connection,
}: {
  spec: Spec;
  stage: (spec: Spec) => void;
  write: boolean;
  connection?: { source: string; target: string };
}) {
  const action = useAction();
  const details = useRef<HTMLDetailsElement>(null);
  const outputInput = useRef<HTMLSelectElement>(null);
  const [sourceId, setSourceId] = useState("");
  const [targetId, setTargetId] = useState("");
  const [error, setError] = useState("");
  useEffect(() => {
    if (connection) {
      setSourceId(connection.source);
      setTargetId(connection.target);
      if (details.current) details.current.open = true;
    }
  }, [connection]);
  const source =
    spec.applications.find((a) => a.id === sourceId) ?? spec.applications[0];
  const targets = spec.applications.filter(
    (a) => a.kind !== "postgres" && a.id !== source?.id,
  );
  const target = targets.find((a) => a.id === targetId) ?? targets[0];
  useEffect(() => {
    if (connection && source?.id === connection.source)
      outputInput.current?.focus();
  }, [connection, source?.id]);
  return (
    <details ref={details}>
      <summary>Connections</summary>
      <p className={s.muted}>
        Copy a named output into a receiving service variable. Connections use
        this environment’s private network. Set readiness dependencies
        separately.
      </p>
      {error && <p role="alert">{error}</p>}
      <ul className={s.list}>
        {spec.bindings?.map((b) => (
          <li key={b.id}>
            {spec.applications.find((a) => a.id === b.sourceId)?.name}.
            {b.output || "URL (legacy readiness)"} →{" "}
            {spec.applications.find((a) => a.id === b.targetId)?.name}:{" "}
            {b.variable}
            {write && (
              <button
                type="button"
                onClick={() =>
                  stage({
                    ...spec,
                    bindings: spec.bindings?.filter((x) => x.id !== b.id),
                  })
                }
              >
                Remove binding {b.variable}
              </button>
            )}
          </li>
        ))}
      </ul>
      {write && (
        <Form
          action={action}
          label="Create service binding"
          onSubmit={(d) => {
            const variable = text(d, "variable");
            if (!source || !target) return;
            if (
              [...spec.variables, ...(target.variables ?? [])].some(
                (v) => v.name === variable,
              ) ||
              spec.bindings?.some(
                (b) => b.targetId === target.id && b.variable === variable,
              )
            ) {
              setError(
                "The receiving service already has this variable. Choose another name or remove the existing value.",
              );
              return;
            }
            stage({
              ...spec,
              bindings: [
                ...(spec.bindings ?? []),
                {
                  id: crypto.randomUUID(),
                  sourceId: source.id,
                  targetId: target.id,
                  output: text(d, "output"),
                  variable,
                },
              ],
            });
            setError("");
          }}
        >
          <label>
            Source service
            <select
              value={source?.id ?? ""}
              onChange={(e) => setSourceId(e.target.value)}
              required
            >
              {spec.applications.map((a) => (
                <option key={a.id} value={a.id}>
                  {a.name}
                </option>
              ))}
            </select>
          </label>
          <label>
            Source output
            <select
              key={source?.id}
              ref={outputInput}
              name="output"
              defaultValue={source?.postgres ? "URL" : "HOST"}
              required
            >
              {source &&
                serviceOutputs(source).map((o) => (
                  <option key={o.name} value={o.name}>
                    {o.name} · {o.type}
                  </option>
                ))}
            </select>
          </label>
          <label>
            Receiving service
            <select
              value={target?.id ?? ""}
              onChange={(e) => setTargetId(e.target.value)}
              required
            >
              {targets.map((a) => (
                <option key={a.id} value={a.id}>
                  {a.name}
                </option>
              ))}
            </select>
          </label>
          <label>
            Variable name
            <input
              name="variable"
              pattern="[A-Z_][A-Z0-9_]*"
              maxLength={80}
              defaultValue="DATABASE_URL"
              required
            />
          </label>
          <button type="submit" disabled={!source || !target}>
            Stage binding
          </button>
        </Form>
      )}
    </details>
  );
}
