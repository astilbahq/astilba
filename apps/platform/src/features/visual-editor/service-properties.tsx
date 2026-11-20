import type { ReactNode } from "react";
import type { Schema } from "../../api/client";
import { Button } from "../../components/button";
import { Checkbox } from "../../components/checkbox";
import { Input } from "../../components/input";
import { Label } from "../../components/label";
import { Select } from "../../components/select";
import { Textarea } from "../../components/textarea";
import * as s from "../../styles";
import { Field, Form, text, type useAction } from "../../ui";
import { availableUpdate, type PostgresRelease } from "../postgres-releases";
import { OutputEditor } from "../service-outputs";

type Spec = Schema["Specification"];
type App = Schema["ApplicationReference"];

export function ServiceProperties({
  app,
  spec,
  secrets,
  action,
  write,
  configure,
  position,
  onPositionChange,
  update,
  stage,
  postgresReleases,
  onRemove,
  className,
  header,
}: {
  app: App;
  spec: Spec;
  secrets: Schema["SecretVersion"][] | undefined;
  action: ReturnType<typeof useAction>;
  write: boolean;
  configure: boolean;
  position: Schema["Position"];
  onPositionChange: (position: Schema["Position"]) => void;
  update: (app: App) => void;
  stage: (spec: Spec) => void;
  postgresReleases: PostgresRelease[];
  onRemove: () => void;
  className: string;
  header: ReactNode;
}) {
  return (
    <section className={className} aria-label={`Properties for ${app.name}`}>
      {header}
      {write && (
        <fieldset className={s.row}>
          <legend>Canvas position</legend>
          {(["x", "y"] as const).map((axis) => (
            <Label key={axis}>
              Position {axis}
              <Input
                type="number"
                value={position[axis]}
                onChange={(e) =>
                  onPositionChange({
                    ...position,
                    [axis]: Number(e.target.value),
                  })
                }
              />
            </Label>
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
                next.command = text(d, "command").split("\n").filter(Boolean);
                next.health = text(d, "healthPort")
                  ? {
                      port: Number(text(d, "healthPort")),
                      path: text(d, "healthPath") || "/",
                    }
                  : undefined;
                if (app.kind !== "worker") {
                  const count = Number(text(d, "replicas"));
                  next.replicas = count === 1 ? undefined : count;
                }
              }
              update(next);
            }}
          >
            <Label>
              Service name
              <Input
                name="name"
                value={app.name}
                onChange={(e) => update({ ...app, name: e.target.value })}
                required
              />
            </Label>
            {app.kind === "postgres" ? (
              <>
                <Label>
                  Database name
                  <Input
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
                </Label>
                <Label>
                  Database user
                  <Input
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
                </Label>
                <Label>
                  Password secret version
                  <Select
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
                    {secrets?.map((v) => (
                      <option key={v.id} value={v.id}>
                        {v.name} · {v.id.slice(0, 8)}
                      </option>
                    ))}
                  </Select>
                </Label>
                <small>
                  Changing database initialization credentials does not alter
                  credentials in an existing data volume.
                </small>
                <PostgresVersion
                  app={app}
                  releases={postgresReleases}
                  update={update}
                />
              </>
            ) : (
              <>
                <Label>
                  Container image
                  <Input
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
                </Label>
                <Label>
                  Command arguments (one per line)
                  <Textarea
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
                </Label>
                {app.kind !== "worker" && (
                  <>
                    <Label>
                      Replica count
                      <Select
                        name="replicas"
                        value={app.replicas ?? 1}
                        onChange={(e) => {
                          const count = Number(e.currentTarget.value);
                          update({
                            ...app,
                            replicas: count === 1 ? undefined : count,
                          });
                        }}
                      >
                        {Array.from({ length: 10 }, (_, i) => i + 1).map(
                          (count) => (
                            <option key={count} value={count}>
                              {count}
                            </option>
                          ),
                        )}
                      </Select>
                    </Label>
                    <small>
                      Multiple instances need an HTTP route and readiness check,
                      without persistent mounts or internal consumers. Keep
                      sessions and shared state outside the app. All instances
                      run on this server.
                    </small>
                    <Label>
                      Readiness HTTP port
                      <Input
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
                    </Label>
                    <Label>
                      Readiness path
                      <Input
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
                    </Label>
                  </>
                )}
              </>
            )}
            <Button type="submit">Stage service changes</Button>
          </Form>
          <OutputEditor
            key={`${app.id}-outputs`}
            app={app}
            spec={spec}
            stage={stage}
          />
          <details className={s.workspaceUtility}>
            <summary>Variables</summary>
            <ul className={s.list}>
              {app.variables?.map((v) => (
                <li key={v.name}>
                  {v.name}:{" "}
                  {v.secretVersionId ? "Secret version selected" : v.value}
                  <Button
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
                  </Button>
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
              <Label>
                Or use secret version
                <Select name="secret">
                  <option value="">Use plain value</option>
                  {secrets?.map((v) => (
                    <option key={v.id} value={v.id}>
                      {v.name} · {v.id.slice(0, 8)}
                    </option>
                  ))}
                </Select>
              </Label>
              <Button type="submit">Stage variable</Button>
            </Form>
          </details>
          {app.kind !== "postgres" && (
            <details className={s.workspaceUtility}>
              <summary>Storage and readiness</summary>
              <ul>
                {app.mounts?.map((m) => (
                  <li key={m.volumeId}>
                    {spec.volumes?.find((v) => v.id === m.volumeId)?.name}:{" "}
                    {m.target}
                    <Button
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
                    </Button>
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
                <Label>
                  Volume
                  <Select name="volume" required>
                    {spec.volumes?.map((v) => (
                      <option key={v.id} value={v.id}>
                        {v.name}
                      </option>
                    ))}
                  </Select>
                </Label>
                <Field name="target" label="Container mount path" />
                <Label className={s.row}>
                  <Checkbox name="readOnly" />
                  Read only
                </Label>
                <Button type="submit">Stage volume attachment</Button>
              </Form>
            </details>
          )}
          <details className={s.workspaceUtility}>
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
                  (a) => a.id !== app.id && (a.kind === "postgres" || a.health),
                )
                .map((a) => (
                  <Label className={s.row} key={a.id}>
                    <Checkbox
                      name="dependency"
                      value={a.id}
                      defaultChecked={app.dependsOn?.includes(a.id)}
                    />
                    {a.name}
                  </Label>
                ))}
              <Button type="submit">Stage readiness dependencies</Button>
            </Form>
          </details>
          <Button type="button" onClick={onRemove}>
            Remove service; retain data volumes
          </Button>
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
  );
}

// A database on a retired release keeps running it until its owner stages the update. The
// later release opens the same data in place; the plan review names the restart.
function PostgresVersion({
  app,
  releases,
  update,
}: {
  app: App;
  releases: PostgresRelease[];
  update: (app: App) => void;
}) {
  const running = releases.find((r) => r.image === app.source.image);
  const next = availableUpdate(releases, app.source.image);
  if (!running) return null;
  return (
    <div className={s.stack}>
      <p>PostgreSQL {running.name}</p>
      {next && (
        <>
          <p>
            PostgreSQL {running.name} is retired. Update to {next.name}: a
            deployed database restarts on its existing data, and the plan review
            lists any follow-up maintenance.
          </p>
          <Button
            type="button"
            onClick={() =>
              update({ ...app, source: { kind: "image", image: next.image } })
            }
          >
            Stage update to PostgreSQL {next.name}
          </Button>
        </>
      )}
    </div>
  );
}
