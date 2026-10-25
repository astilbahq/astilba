import { useQuery, useQueryClient } from "@tanstack/react-query";
import { QRCodeSVG } from "qrcode.react";
import { useState } from "react";
import { client, result, type Schema, setSession } from "./api/client";
import { sessionOptions } from "./api/session";
import { Button } from "./components/button";
import { Checkbox } from "./components/checkbox";
import { Input } from "./components/input";
import { Label } from "./components/label";
import { Anchor } from "./components/link";
import { Select } from "./components/select";
import { EmailVerification, WorkspaceSubscription } from "./hosting";
import { WorkspaceExport } from "./portability";
import * as s from "./styles";
import { BusyButton, ErrorNotice, Field, Form, text, useAction } from "./ui";
export function Account() {
  const session = useQuery(sessionOptions).data;
  const action = useAction();
  const [enrollment, setEnrollment] = useState<Schema["MFAEnrollment"]>();
  const [codes, setCodes] = useState<string[]>();
  const [message, setMessage] = useState("");
  const saveCodes = (v: Schema["RecoveryCodes"]) => {
    setSession(v.session);
    setCodes(v.codes);
    setEnrollment(undefined);
  };
  return (
    <div className={s.stack}>
      <div>
        <p className={s.muted}>ACCOUNT</p>
        <h1>Security, under your control.</h1>
        <p className={s.muted}>{session?.user?.email}</p>
      </div>
      <ErrorNotice error={action.error} />
      {message && (
        <p role="status" className={s.notice}>
          {message}
        </p>
      )}
      <EmailVerification />
      <div className={s.columns}>
        <section className={s.panel}>
          <h2>Confirm your identity</h2>
          <p className={s.muted}>
            Confirmation lasts five minutes and is required for credential and
            secret changes.
          </p>
          <Form
            action={action}
            onSubmit={(d, form) =>
              action.run(async () => {
                setSession(
                  await result(
                    client.POST("/api/v1/auth/reauthenticate", {
                      body: {
                        password: text(d, "password"),
                        code: text(d, "code") || undefined,
                      },
                    }),
                  ),
                );
                form.reset();
                setMessage("Identity confirmed for five minutes.");
              })
            }
          >
            <Field
              name="password"
              label="Current password"
              type="password"
              autoComplete="current-password"
            />
            {session?.user?.mfaEnabled && (
              <Field
                name="code"
                label="Authenticator or recovery code"
                autoComplete="one-time-code"
              />
            )}
            <BusyButton busy={action.isPending}>Confirm identity</BusyButton>
          </Form>
        </section>
        <section className={s.panel}>
          <h2>Two-factor authentication</h2>
          <p>{session?.user?.mfaEnabled ? "Enabled" : "Not enabled"}</p>
          {session?.user?.mfaEnabled ? (
            <>
              <p className={s.muted}>
                {session.user.recoveryCodesRemaining} recovery codes remaining.
              </p>
              <Button
                type="button"
                disabled={action.isPending}
                onClick={() =>
                  action.run(async () =>
                    saveCodes(
                      await result(
                        client.POST("/api/v1/auth/mfa/recovery-codes"),
                      ),
                    ),
                  )
                }
              >
                Replace recovery codes
              </Button>
              <Button
                type="button"
                disabled={action.isPending}
                onClick={() => {
                  if (
                    window.confirm(
                      "Disable two-factor authentication? Workspaces that require it will block changes until you enable it again.",
                    )
                  )
                    action.run(async () => {
                      setSession(
                        await result(client.POST("/api/v1/auth/mfa/disable")),
                      );
                      setCodes(undefined);
                    });
                }}
              >
                Disable two-factor authentication
              </Button>
            </>
          ) : (
            <Button
              type="button"
              disabled={action.isPending}
              onClick={() =>
                action.run(async () =>
                  setEnrollment(
                    await result(client.POST("/api/v1/auth/mfa/enroll")),
                  ),
                )
              }
            >
              Set up authenticator
            </Button>
          )}
          {enrollment && (
            <>
              <p>
                Scan this QR code with your authenticator, or add the key
                manually. Then enter its six-digit code.
              </p>
              <QRCodeSVG
                value={enrollment.uri}
                title="Authenticator setup QR code"
                size={224}
                marginSize={4}
                className={s.authenticatorQR}
              />
              <code className={s.code}>{enrollment.secret}</code>
              <Anchor href={enrollment.uri}>Open authenticator app</Anchor>
              <Form
                action={action}
                onSubmit={(d) =>
                  action.run(async () =>
                    saveCodes(
                      await result(
                        client.POST("/api/v1/auth/mfa/confirm", {
                          body: { code: text(d, "code") },
                        }),
                      ),
                    ),
                  )
                }
              >
                <Field
                  name="code"
                  label="Six-digit code"
                  autoComplete="one-time-code"
                />
                <BusyButton busy={action.isPending}>
                  Enable two-factor authentication
                </BusyButton>
              </Form>
            </>
          )}
          {codes && (
            <div className={s.stack}>
              <p role="status">
                Save these recovery codes somewhere safe. They are shown only
                now; each works once.
              </p>
              <pre className={s.code}>{codes.join("\n")}</pre>
              <Button type="button" onClick={() => setCodes(undefined)}>
                I have saved my codes
              </Button>
            </div>
          )}
        </section>
      </div>
      <section className={s.panel}>
        <h2>Change password</h2>
        <Form
          action={action}
          onSubmit={(d, form) =>
            action.run(async () => {
              setSession(
                await result(
                  client.POST("/api/v1/auth/password", {
                    body: { password: text(d, "password") },
                  }),
                ),
              );
              form.reset();
              setMessage(
                "Password changed. Other sessions have been signed out.",
              );
            })
          }
        >
          <Field
            name="password"
            label="New password (15–128 characters)"
            type="password"
            autoComplete="new-password"
            minLength={15}
          />
          <BusyButton busy={action.isPending}>Change password</BusyButton>
        </Form>
      </section>
    </div>
  );
}
export function Members() {
  const session = useQuery(sessionOptions).data;
  const [selected, setSelected] = useState(
    () => new URLSearchParams(location.search).get("workspace") ?? "",
  );
  const ws =
    session?.workspaces.find((w) => w.id === selected) ??
    session?.workspaces[0];
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
      <Label>
        Workspace
        <Select value={id} onChange={(e) => setSelected(e.target.value)}>
          {session?.workspaces.map((w) => (
            <option key={w.id} value={w.id}>
              {w.name}
            </option>
          ))}
        </Select>
      </Label>
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
