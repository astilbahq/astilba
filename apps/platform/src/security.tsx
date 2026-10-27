import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useContext } from "react";
import { client, result, type Schema } from "./api/client";
import { Button } from "./components/button";
import { Checkbox } from "./components/checkbox";
import { Input } from "./components/input";
import { Label } from "./components/label";
import { Select } from "./components/select";
import { WorkspaceSubscription } from "./hosting";
import { WorkspaceExport } from "./portability";
import * as s from "./styles";
import { BusyButton, ErrorNotice, Field, Form, text, useAction } from "./ui";
import { WorkspaceContext } from "./workspace-context";
export function Members() {
  const ws = useContext(WorkspaceContext);
  const id = ws?.id ?? "";
  const action = useAction();
  const queries = useQueryClient();
  const members = useQuery({
    queryKey: ["members", id],
    enabled: !!id && ws?.role === "owner",
    queryFn: () =>
      result(
        client.GET("/api/v1/workspaces/{workspaceId}/members", {
          params: { path: { workspaceId: id } },
        }),
      ),
  });
  const invitations = useQuery({
    queryKey: ["invitations", id],
    enabled: !!id && ws?.role === "owner",
    queryFn: () =>
      result(
        client.GET("/api/v1/workspaces/{workspaceId}/invitations", {
          params: { path: { workspaceId: id } },
        }),
      ),
  });
  const audit = useQuery({
    queryKey: ["audit", id],
    enabled: !!id && ws?.role === "owner",
    queryFn: () =>
      result(
        client.GET("/api/v1/workspaces/{workspaceId}/audit", {
          params: { path: { workspaceId: id } },
        }),
      ),
  });
  const refresh = async () => {
    await queries.invalidateQueries({ queryKey: ["members", id] });
    await queries.invalidateQueries({ queryKey: ["invitations", id] });
    await queries.invalidateQueries({ queryKey: ["audit", id] });
  };
  return (
    <div className={s.stack}>
      <div>
        <p className={s.muted}>WORKSPACE</p>
        <h1>A place for your team.</h1>
      </div>
      <WorkspaceSubscription workspaceId={id} owner={ws?.role === "owner"} />
      {ws?.role === "owner" && <WorkspaceExport workspaceId={id} />}
      <ErrorNotice
        error={
          action.error ?? members.error ?? invitations.error ?? audit.error
        }
      />
      {ws?.role !== "owner" ? (
        <p className={s.notice}>
          Workspace settings and membership are managed by its owners.
        </p>
      ) : (
        <>
          <section className={s.panel} key={id}>
            <h2>Workspace settings</h2>
            <Form
              action={action}
              onSubmit={(d) =>
                action.run(async () => {
                  await result(
                    client.PUT("/api/v1/workspaces/{workspaceId}", {
                      params: { path: { workspaceId: id } },
                      body: {
                        name: text(d, "name"),
                        requireMfa: d.get("requireMfa") === "on",
                      },
                    }),
                  );
                  await refresh();
                })
              }
            >
              <Label>
                Name
                <Input
                  name="name"
                  defaultValue={ws.name}
                  required
                  maxLength={80}
                />
              </Label>
              <Label className={s.row}>
                <Checkbox name="requireMfa" defaultChecked={ws.requireMfa} />
                Require two-factor authentication for changes
              </Label>
              <small>
                Confirm your identity in Account security before changing these
                settings.
              </small>
              <BusyButton busy={action.isPending}>Save settings</BusyButton>
            </Form>
          </section>
          <div className={s.columns}>
            <section className={s.panel}>
              <h2>Members</h2>
              <div className={s.list}>
                {members.data?.map((m) => (
                  <div className={s.stack} key={m.id}>
                    <div>
                      <strong>{m.name}</strong>
                      <p className={s.muted}>
                        {m.email} ·{" "}
                        {m.mfaEnabled ? "2FA enabled" : "2FA not enabled"}
                      </p>
                    </div>
                    <div className={s.row}>
                      <Label>
                        Role for {m.name}
                        <Select
                          value={m.role}
                          disabled={action.isPending}
                          onChange={(e) =>
                            action.run(async () => {
                              await result(
                                client.PUT(
                                  "/api/v1/workspaces/{workspaceId}/members/{accountId}",
                                  {
                                    params: {
                                      path: {
                                        workspaceId: id,
                                        accountId: m.id,
                                      },
                                    },
                                    body: {
                                      role: e.target.value as Schema["Role"],
                                    },
                                  },
                                ),
                              );
                              await refresh();
                            })
                          }
                        >
                          <option value="owner">Owner</option>
                          <option value="operator">Operator</option>
                          <option value="viewer">Viewer</option>
                        </Select>
                      </Label>
                      <Button
                        type="button"
                        disabled={action.isPending}
                        onClick={() => {
                          if (
                            window.confirm(
                              `Remove ${m.name} from this workspace?`,
                            )
                          )
                            action.run(async () => {
                              await result(
                                client.DELETE(
                                  "/api/v1/workspaces/{workspaceId}/members/{accountId}",
                                  {
                                    params: {
                                      path: {
                                        workspaceId: id,
                                        accountId: m.id,
                                      },
                                    },
                                  },
                                ),
                              );
                              await refresh();
                            });
                        }}
                      >
                        Remove
                      </Button>
                    </div>
                  </div>
                ))}
              </div>
            </section>
            <section className={s.panel}>
              <h2>Invite a member</h2>
              <Form
                action={action}
                onSubmit={(d, form) =>
                  action.run(async () => {
                    await result(
                      client.POST(
                        "/api/v1/workspaces/{workspaceId}/invitations",
                        {
                          params: { path: { workspaceId: id } },
                          body: {
                            email: text(d, "email"),
                            role: text(d, "role") as Schema["Role"],
                          },
                        },
                      ),
                    );
                    form.reset();
                    await refresh();
                  })
                }
              >
                <Field name="email" label="Email" type="email" />
                <Label>
                  Role
                  <Select name="role" defaultValue="operator">
                    <option value="owner">Owner</option>
                    <option value="operator">Operator</option>
                    <option value="viewer">Viewer</option>
                  </Select>
                </Label>
                <BusyButton busy={action.isPending}>Send invitation</BusyButton>
              </Form>
              <div className={s.list}>
                {invitations.data?.map((i) => (
                  <div key={i.id}>
                    <p>{i.email}</p>
                    <small>
                      {i.role} · expires{" "}
                      {new Date(i.expiresAt).toLocaleDateString()}
                    </small>
                    <Button
                      type="button"
                      disabled={action.isPending}
                      onClick={() =>
                        action.run(async () => {
                          await result(
                            client.DELETE(
                              "/api/v1/workspaces/{workspaceId}/invitations/{invitationId}",
                              {
                                params: {
                                  path: { workspaceId: id, invitationId: i.id },
                                },
                              },
                            ),
                          );
                          await refresh();
                        })
                      }
                    >
                      Revoke
                    </Button>
                  </div>
                ))}
              </div>
            </section>
          </div>
          <section className={s.panel}>
            <h2>Recent activity</h2>
            <div className={s.list}>
              {audit.data?.map((event) => (
                <div key={event.id} className={s.spread}>
                  <span>{event.action}</span>
                  <time className={s.muted} dateTime={event.createdAt}>
                    {new Date(event.createdAt).toLocaleString()}
                  </time>
                </div>
              ))}
            </div>
          </section>
        </>
      )}
    </div>
  );
}
